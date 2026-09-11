# 🚀 TalosDeck — Руководство по развёртыванию (Deployment Guide)

> **GitHub**: [https://github.com/etosheartem/TalosDeck](https://github.com/etosheartem/TalosDeck)

В этом документе описаны все способы запуска **TalosDeck**: от локального запуска на рабочей станции до полноценного контейнерного деплоя внутри кластера Kubernetes.

---

## 1. Overview & Architecture

TalosDeck is a lightweight web UI and control plane for managing and monitoring Talos Linux clusters.

- **Frontend**: Vue 3 + Tailwind CSS + Lucide icons.
- **Backend**: Go (Golang) leveraging the official Talos Machinery SDK (`github.com/siderolabs/talos/pkg/machinery`).
- **Packaging**: Single static binary with embedded frontend assets (`go:embed`), containerized in a minimal Alpine Linux image.
- **Connectivity**: Back-end communicates with Talos nodes over mTLS gRPC on port `50000` using certificates provided via `talosconfig`.

```
                  Browser (Client)
                         |
                 HTTP / WebSocket
                         v
      +-------------------------------------+
      |      TalosDeck Pod (:8080)          |
      |                                     |
      |  Mounted Secret:                    |
      |  /etc/talos/talosconfig             |
      +------------------+------------------+
                         |
                         | gRPC mTLS (:50000)
         +---------------+---------------+
         |                               |
         v                               v
  Control Plane Node              Worker Node(s)
  10.42.0.110:50000               10.42.0.111:50000
```

---

## 2. Prerequisites

1. **Talos Linux Cluster**: An active cluster (e.g. `10.42.0.110`).
2. **talosconfig**: Valid administrative credentials located at `cluster-config/talosconfig` or `~/.talos/config`.
3. **kubectl**: Configured with cluster credentials (`kubeconfig`).
4. **Docker**: (Optional for local development, required for container image build).
5. **Bun** or **Node.js**: (For local frontend compilation).
6. **Go 1.23+**: (For local backend compilation).

---

## 3. Local Development & Quick Start

You can run TalosDeck directly on your workstation connecting to your remote Talos cluster.

### Step 1: Build the Project
```bash
make build
```
This target:
1. Installs frontend dependencies and builds production assets into `web/dist`.
2. Compiles the Go binary into `bin/talosdeck` with the frontend embedded.

### Step 2: Run Locally
```bash
make run
```
By default, `make run` auto-detects `../cluster-config/talosconfig` or `/home/artem/laba-kuber/cluster-config/talosconfig`.
To explicitly specify a custom config:
```bash
TALOSCONFIG=/path/to/talosconfig make run
```
Open your browser at [http://localhost:8080](http://localhost:8080).

---

## 4. Containerization (Docker)

TalosDeck uses a 3-stage `Dockerfile`:
- **Stage 1 (`frontend-builder`)**: Builds the Vue 3 application into `web/dist` using Bun/Node.
- **Stage 2 (`backend-builder`)**: Compiles a static Go binary embedding `web/dist`.
- **Stage 3 (Runtime)**: Minimal `alpine:latest` image containing only `ca-certificates`, `tzdata`, non-root user, and the binary.

### Build Docker Image
```bash
make docker-build
```
Or directly with Docker:
```bash
docker build -t talosdeck:latest .
```

### Test Container Locally
Run the container locally, mounting your host's `talosconfig`:
```bash
make docker-run
```
Or manually:
```bash
docker run --rm -it \
  -p 8080:8080 \
  -v "/home/artem/laba-kuber/cluster-config/talosconfig:/etc/talos/talosconfig:ro" \
  -e TALOSCONFIG=/etc/talos/talosconfig \
  talosdeck:latest
```

---

## 5. Kubernetes Deployment

The manifests in `deploy/` deploy TalosDeck to your Kubernetes cluster.

### Step 1: Create the Kubernetes Secret
TalosDeck requires access to `talosconfig` to authenticate against the node API. The helper script creates an idempotent Secret:

```bash
# Using the Makefile target:
make k8s-secret

# Or directly executing the script:
./deploy/secret-create.sh /home/artem/laba-kuber/cluster-config/talosconfig default
```

Verify the secret:
```bash
kubectl get secret talosdeck-config -o yaml
```

### Step 2: Deploy Application & Service
Deploy the Deployment and NodePort Service:
```bash
# Single command via Makefile:
make k8s-deploy

# Or using kubectl directly:
kubectl apply -f deploy/deployment.yaml
kubectl apply -f deploy/service.yaml
```

### Step 3: Verify Deployment Status
Check Pod status and logs:
```bash
kubectl rollout status deployment/talosdeck
kubectl get pods -l app=talosdeck -o wide
kubectl logs -l app=talosdeck -f
```

---

## 6. Accessing TalosDeck UI

### Option A: Via NodePort (Recommended for LAN / Lab)
The service exposes port `8080` externally on NodePort `32000`:
- [http://10.42.0.110:32000](http://10.42.0.110:32000) (Control Plane)
- [http://10.42.0.111:32000](http://10.42.0.111:32000) (Worker 1)
- [http://10.42.0.112:32000](http://10.42.0.112:32000) (Worker 2)

### Option B: Port Forwarding
```bash
kubectl port-forward svc/talosdeck 8080:8080
```
Then navigate to [http://localhost:8080](http://localhost:8080).

---

## 7. Configuration Reference

| Environment Variable | Default Value | Description |
| :--- | :--- | :--- |
| `TALOSCONFIG` | `/etc/talos/talosconfig` | Path to Talos administrative config file |
| `PORT` | `:8080` | HTTP listen address and port |

### Probes & Resources
- **Liveness Probe**: HTTP GET on `/` (initial delay: 10s, period: 15s).
- **Readiness Probe**: HTTP GET on `/` (initial delay: 5s, period: 10s).
- **Security Context**: Runs as non-root UID 1000 (`talosdeck`).
- **Resource Requests**: 50m CPU, 64Mi RAM.
- **Resource Limits**: 500m CPU, 256Mi RAM.

---

## 8. Troubleshooting

### 1. `talosconfig` Not Found or Permission Denied
If the pod fails to start, verify the secret mount:
```bash
kubectl describe pod -l app=talosdeck
kubectl exec -it deployment/talosdeck -- ls -la /etc/talos/
```

### 2. Cannot Connect to Nodes on Port 50000
Verify network connectivity from within cluster pods to node IPs:
- Talos nodes listen for `apid` on port `50000`.
- If CNI (e.g. Flannel / Cilium) blocks Pod-to-Node communication, verify network policies or host routing.

---

## 9. Cleanup / Undeploy

To remove TalosDeck and associated secrets:
```bash
make k8s-undeploy
```
Or manually:
```bash
kubectl delete -f deploy/service.yaml --ignore-not-found
kubectl delete -f deploy/deployment.yaml --ignore-not-found
kubectl delete secret talosdeck-config --ignore-not-found
```
