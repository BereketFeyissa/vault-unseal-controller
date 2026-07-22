# Vault Unseal Controller

A Kubernetes controller that automatically unseals HashiCorp Vault pods using Shamir unseal keys stored in a Kubernetes Secret.

## Overview

This controller watches Vault pod events in a Kubernetes cluster and automatically unseals any sealed Vault pods. It eliminates the need for manual unsealing when pods restart by reading unseal keys from a Kubernetes Secret and submitting them to the Vault API.

### How It Works

1. Watches pods with label `app.kubernetes.io/name=vault` in the configured namespace
2. Detects pods that are Running but not Ready (sealed Vault pods)
3. Checks Vault initialization status via `/v1/sys/init`
4. Checks seal status via `/v1/sys/seal-status`
5. Reads unseal keys from the configured Kubernetes Secret
6. Submits keys to `/v1/sys/unseal` until the pod is unsealed or all keys are used
7. Polls periodically as a safety net for missed events

## Prerequisites

- Kubernetes cluster with Vault deployed using Raft storage backend
- Vault pods labeled with `app.kubernetes.io/name=vault`
- A Kubernetes Secret containing the unseal keys
- Docker or a container runtime to build the image

## Installation

### 1. Build the Docker Image

```bash
make build
```

### 2. Push to Your Registry (if using private registry)

```bash
make push REGISTRY=your-registry.com IMAGE_TAG=latest
```

### 3. Create the Unseal Keys Secret

```bash
kubectl create secret generic vault-unseal-keys \
  --namespace=vault \
  --from-literal=unseal-keys="$(cat unseal-keys.txt)"
```

The `unseal-keys.txt` file should contain your 3 (or configured threshold) unseal keys, one per line.

### 4. Create the Image Pull Secret (if using private registry)

```bash
kubectl create secret docker-registry regcred \
  --namespace=vault \
  --docker-server=YOUR_REGISTRY \
  --docker-username=YOUR_USERNAME \
  --docker-password=YOUR_PASSWORD
```

### 5. Deploy the Controller

```bash
make deploy
```

Or apply manifests individually:

```bash
kubectl apply -f deploy/serviceaccount.yaml
kubectl apply -f deploy/clusterrole.yaml
kubectl apply -f deploy/clusterrolebinding.yaml
kubectl apply -f deploy/imagepullsecret.yaml
kubectl apply -f deploy/secret-template.yaml
kubectl apply -f deploy/deployment.yaml
```

## Configuration

The controller accepts the following command-line flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--kubeconfig` | (empty) | Path to kubeconfig file (for out-of-cluster use) |
| `--namespace` | `vault` | Namespace where Vault pods run |
| `--secret-name` | `vault-unseal-keys` | Name of K8s secret containing unseal keys |
| `--secret-key` | `unseal-keys` | Key in the secret holding newline-separated unseal keys |
| `--threshold` | `3` | Number of unseal keys required to unseal Vault |
| `--protocol` | `http` | Protocol to use for Vault API (`http` or `https`) |
| `--poll-interval` | `30s` | Polling interval for checking pod status |

## Example: Changing Protocol to HTTPS

If your Vault pods use TLS, update the deployment:

```yaml
args:
  - --namespace=vault
  - --secret-name=vault-unseal-keys
  - --secret-key=unseal-keys
  - --threshold=3
  - --protocol=https
  - --poll-interval=30s
```

## RBAC Permissions

The controller requires the following permissions:

- **pods**: get, list, watch (in the vault namespace)
- **secrets**: get, list (in the vault namespace)

These are defined in `deploy/clusterrole.yaml` and bound via `deploy/clusterrolebinding.yaml`.

## Troubleshooting

### Check Controller Logs

```bash
kubectl logs -n vault -l app.kubernetes.io/name=vault-unseal-controller -f
```

### Common Issues

| Symptom | Cause | Solution |
|---------|-------|----------|
| `Failed to check init status` | Vault not reachable or wrong protocol | Verify pod IP and protocol flag |
| `Failed to submit unseal key` | Invalid key or wrong threshold | Verify unseal keys in Secret |
| `Not enough unseal keys` | Secret has fewer keys than threshold | Update Secret with required number of keys |
| `Pod not initialized` | Vault not yet initialized | Initialize Vault manually first |

### Verify Vault Status

```bash
# Check if Vault is sealed
kubectl exec -n vault vault-0 -- vault status

# Check unseal keys secret
kubectl get secret -n vault vault-unseal-keys -o yaml
```

## Cleanup

```bash
make clean
```

## Project Structure

```
vault-unseal-controller/
├── Dockerfile
├── Makefile
├── go.mod
├── cmd/vault-unseal-controller/
│   └── main.go
├── internal/controller/
│   └── controller.go
└── deploy/
    ├── serviceaccount.yaml
    ├── clusterrole.yaml
    ├── clusterrolebinding.yaml
    ├── imagepullsecret.yaml
    ├── secret-template.yaml
    └── deployment.yaml
```
