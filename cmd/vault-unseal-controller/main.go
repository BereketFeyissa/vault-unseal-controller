package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vault-unseal-controller/internal/controller"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

func main() {
	var (
		kubeconfig   string
		namespace    string
		secretName   string
		secretKey    string
		threshold    int
		protocol     string
		pollInterval time.Duration
	)

	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig file (for out-of-cluster use)")
	flag.StringVar(&namespace, "namespace", "vault", "Namespace where Vault pods run")
	flag.StringVar(&secretName, "secret-name", "vault-unseal-keys", "Name of K8s secret containing unseal keys")
	flag.StringVar(&secretKey, "secret-key", "unseal-keys", "Key in the secret holding newline-separated unseal keys")
	flag.IntVar(&threshold, "threshold", 3, "Number of unseal keys required")
	flag.StringVar(&protocol, "protocol", "http", "Protocol to use for Vault API (http or https)")
	flag.DurationVar(&pollInterval, "poll-interval", 30*time.Second, "Polling interval for checking pod status")
	flag.Parse()

	log.SetPrefix("[vault-unseal-controller] ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lmsgprefix)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Received shutdown signal, exiting...")
		cancel()
	}()

	var restConfig *rest.Config
	var err error

	if kubeconfig != "" {
		restConfig, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
	} else {
		restConfig, err = rest.InClusterConfig()
	}
	if err != nil {
		log.Fatalf("Failed to build kubeconfig: %v", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		log.Fatalf("Failed to create kubernetes client: %v", err)
	}

	ctrl, err := controller.New(controller.ControllerConfig{
		Clientset:    clientset,
		Namespace:    namespace,
		SecretName:   secretName,
		SecretKey:    secretKey,
		Threshold:    threshold,
		Protocol:     protocol,
		PollInterval: pollInterval,
	})
	if err != nil {
		log.Fatalf("Failed to create controller: %v", err)
	}

	log.Printf("Starting vault-unseal-controller (namespace=%s, secret=%s, threshold=%d)", namespace, secretName, threshold)
	if err := ctrl.Run(ctx); err != nil {
		log.Fatalf("Controller exited with error: %v", err)
	}
}
