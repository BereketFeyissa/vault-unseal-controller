package controller

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

type ControllerConfig struct {
	Clientset    kubernetes.Interface
	Namespace    string
	SecretName   string
	SecretKey    string
	Threshold    int
	Protocol     string
	PollInterval time.Duration
}

type Controller struct {
	clientset    kubernetes.Interface
	namespace    string
	secretName   string
	secretKey    string
	threshold    int
	protocol     string
	pollInterval time.Duration
	httpClient   *http.Client
}

type unsealRequest struct {
	Key string `json:"key"`
}

type unsealResponse struct {
	Sealed   bool   `json:"sealed"`
	Version  string `json:"version"`
	Nonce    string `json:"nonce"`
}

type initResponse struct {
	Initialized bool     `json:"initialized"`
	Sealed      bool     `json:"sealed"`
	Keys        []string `json:"keys,omitempty"`
	KeysBase64  []string `json:"keys_base64,omitempty"`
	RootToken   string   `json:"root_token,omitempty"`
}

type sealStatusResponse struct {
	Initialized bool   `json:"initialized"`
	Sealed      bool  `json:"sealed"`
	Threshold   int   `json:"threshold"`
	Nonce       string `json:"nonce"`
}

func New(cfg ControllerConfig) (*Controller, error) {
	if cfg.Clientset == nil {
		return nil, fmt.Errorf("clientset is required")
	}
	if cfg.Namespace == "" {
		return nil, fmt.Errorf("namespace is required")
	}
	if cfg.Threshold <= 0 {
		return nil, fmt.Errorf("threshold must be positive")
	}
	if cfg.Protocol == "" {
		cfg.Protocol = "http"
	}
	if cfg.Protocol != "http" && cfg.Protocol != "https" {
		return nil, fmt.Errorf("protocol must be 'http' or 'https', got '%s'", cfg.Protocol)
	}

	var httpClient *http.Client
	if cfg.Protocol == "https" {
		httpClient = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			},
		}
	} else {
		httpClient = &http.Client{
			Timeout: 10 * time.Second,
		}
	}

	return &Controller{
		clientset:    cfg.Clientset,
		namespace:    cfg.Namespace,
		secretName:   cfg.SecretName,
		secretKey:    cfg.SecretKey,
		threshold:    cfg.Threshold,
		protocol:     cfg.Protocol,
		pollInterval: cfg.PollInterval,
		httpClient:   httpClient,
	}, nil
}

func (c *Controller) Run(ctx context.Context) error {
	watcher, err := c.clientset.CoreV1().Pods(c.namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vault",
	})
	if err != nil {
		return fmt.Errorf("failed to start pod watcher: %w", err)
	}
	defer watcher.Stop()

	log.Println("Watching for vault pod events...")

	for {
		select {
		case <-ctx.Done():
			log.Println("Controller shutting down")
			return nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				log.Println("Watch channel closed, restarting watcher...")
				time.Sleep(5 * time.Second)
				watcher, err = c.clientset.CoreV1().Pods(c.namespace).Watch(ctx, metav1.ListOptions{
					LabelSelector: "app.kubernetes.io/name=vault",
				})
				if err != nil {
					return fmt.Errorf("failed to restart pod watcher: %w", err)
				}
				continue
			}
			c.handleEvent(ctx, event)
		case <-time.After(c.pollInterval):
			c.checkAllPods(ctx)
		}
	}
}

func (c *Controller) handleEvent(ctx context.Context, event watch.Event) {
	pod, ok := event.Object.(*corev1.Pod)
	if !ok {
		return
	}

	log.Printf("Event %s: pod/%s phase=%s ready=%v", event.Type, pod.Name, pod.Status.Phase, isPodReady(pod))

	if event.Type == watch.Added || event.Type == watch.Modified {
		if needsUnseal(pod) {
			c.attemptUnseal(ctx, pod)
		}
	}
}

func (c *Controller) checkAllPods(ctx context.Context) {
	pods, err := c.clientset.CoreV1().Pods(c.namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/name=vault",
	})
	if err != nil {
		log.Printf("Failed to list pods: %v", err)
		return
	}

	for i := range pods.Items {
		pod := &pods.Items[i]
		if needsUnseal(pod) {
			c.attemptUnseal(ctx, pod)
		}
	}
}

func needsUnseal(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	if isPodReady(pod) {
		return false
	}

	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionFalse {
			return true
		}
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "vault" && cs.State.Waiting != nil {
			return true
		}
	}

	return false
}

func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func (c *Controller) attemptUnseal(ctx context.Context, pod *corev1.Pod) {
	podIP := pod.Status.PodIP
	if podIP == "" {
		log.Printf("Pod %s has no IP yet, skipping", pod.Name)
		return
	}

	vaultAddr := fmt.Sprintf("%s://%s:8200", c.protocol, podIP)

	initStatus, err := c.checkInitStatus(vaultAddr)
	if err != nil {
		log.Printf("Failed to check init status for %s: %v", pod.Name, err)
		return
	}

	if !initStatus.Initialized {
		log.Printf("Pod %s is not initialized, skipping (requires manual init or auto-init)", pod.Name)
		return
	}

	sealStatus, err := c.checkSealStatus(vaultAddr)
	if err != nil {
		log.Printf("Failed to check seal status for %s: %v", pod.Name, err)
		return
	}

	if !sealStatus.Sealed {
		log.Printf("Pod %s is already unsealed", pod.Name)
		return
	}

	keys, err := c.getUnsealKeys(ctx)
	if err != nil {
		log.Printf("Failed to get unseal keys: %v", err)
		return
	}

	if len(keys) < c.threshold {
		log.Printf("Not enough unseal keys (%d available, %d required)", len(keys), c.threshold)
		return
	}

	log.Printf("Attempting to unseal pod %s at %s", pod.Name, vaultAddr)

	for i := 0; i < c.threshold; i++ {
		status, err := c.unsealKey(vaultAddr, keys[i])
		if err != nil {
			log.Printf("Failed to submit unseal key %d for %s: %v", i+1, pod.Name, err)
			return
		}

		if !status.Sealed {
			log.Printf("Successfully unsealed pod %s", pod.Name)
			return
		}

		log.Printf("Key %d/%d accepted for %s (still sealed: %v)", i+1, c.threshold, pod.Name, status.Sealed)
	}

	finalSealStatus, err := c.checkSealStatus(vaultAddr)
	if err != nil {
		log.Printf("Failed to verify unseal status for %s: %v", pod.Name, err)
		return
	}

	if finalSealStatus.Sealed {
		log.Printf("WARNING: Pod %s still sealed after submitting all %d keys", pod.Name, c.threshold)
	} else {
		log.Printf("Pod %s successfully unsealed", pod.Name)
	}
}

func (c *Controller) checkInitStatus(vaultAddr string) (*initResponse, error) {
	resp, err := c.httpClient.Get(vaultAddr + "/v1/sys/init")
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result initResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

func (c *Controller) checkSealStatus(vaultAddr string) (*sealStatusResponse, error) {
	resp, err := c.httpClient.Get(vaultAddr + "/v1/sys/seal-status")
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result sealStatusResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

func (c *Controller) unsealKey(vaultAddr, key string) (*unsealResponse, error) {
	payload := unsealRequest{Key: key}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	resp, err := c.httpClient.Post(
		vaultAddr+"/v1/sys/unseal",
		"application/json",
		strings.NewReader(string(data)),
	)
	if err != nil {
		return nil, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	var result unsealResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &result, nil
}

func (c *Controller) getUnsealKeys(ctx context.Context) ([]string, error) {
	secret, err := c.clientset.CoreV1().Secrets(c.namespace).Get(ctx, c.secretName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to get secret %s/%s: %w", c.namespace, c.secretName, err)
	}

	keyData, ok := secret.Data[c.secretKey]
	if !ok {
		return nil, fmt.Errorf("key %q not found in secret %s", c.secretKey, c.secretName)
	}

	keys := strings.Split(strings.TrimSpace(string(keyData)), "\n")
	var validKeys []string
	for _, k := range keys {
		k = strings.TrimSpace(k)
		if k != "" {
			validKeys = append(validKeys, k)
		}
	}

	return validKeys, nil
}
