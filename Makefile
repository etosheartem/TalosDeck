# TalosDeck Makefile
SHELL := /bin/bash

APP_NAME ?= talosdeck
IMAGE_NAME ?= talosdeck
IMAGE_TAG ?= 0.1.0
PORT ?= 8080
NAMESPACE ?= talosdeck

# Auto-detect kubeconfig location
KUBECONFIG ?= $(shell \
	if [ -f "../kubeconfig" ]; then echo "$$(pwd)/../kubeconfig"; \
	elif [ -f "./kubeconfig" ]; then echo "$$(pwd)/kubeconfig"; \
	else echo "$${HOME}/.kube/config"; fi)
export KUBECONFIG

# Auto-detect talosconfig location
TALOSCONFIG ?= $(shell \
	if [ -f "../cluster-config/talosconfig" ]; then echo "$$(pwd)/../cluster-config/talosconfig"; \
	elif [ -f "./cluster-config/talosconfig" ]; then echo "$$(pwd)/cluster-config/talosconfig"; \
	elif [ -f "$${HOME}/.talos/config" ]; then echo "$${HOME}/.talos/config"; \
	else echo ""; fi)
export TALOSCONFIG

# Detect package manager for frontend (bun or npm)
PM ?= $(shell if command -v bun >/dev/null 2>&1; then echo "bun"; else echo "npm"; fi)

.PHONY: all help build build-web build-go run docker-build docker-run k8s-namespace k8s-secret k8s-deploy k8s-undeploy clean

all: build

help:
	@echo "TalosDeck Management Commands:"
	@echo "  make build         - Build frontend and Go backend binary"
	@echo "  make run           - Run application locally with discovered talosconfig"
	@echo "  make docker-build  - Build multi-stage production Docker image"
	@echo "  make docker-run    - Run container locally with host talosconfig mounted"
	@echo "  make k8s-secret    - Create/update Kubernetes secret 'talosdeck-config'"
	@echo "  make k8s-deploy    - Deploy TalosDeck (secret, deployment, service) to Kubernetes"
	@echo "  make k8s-undeploy  - Remove TalosDeck resources from Kubernetes"
	@echo "  make clean         - Clean build artifacts (bin/ and web/dist)"

build-web:
	@echo "==> Building frontend using $(PM)..."
	@cd web && $(PM) install && $(PM) run build

build-go:
	@echo "==> Building Go binary..."
	@mkdir -p bin
	CGO_ENABLED=0 go build -ldflags="-s -w" -o bin/$(APP_NAME) ./cmd/talosdeck

build: build-web build-go
	@echo "==> Build complete: bin/$(APP_NAME)"

run: build
	@if [ -z "$(TALOSCONFIG)" ]; then \
		echo "ERROR: talosconfig not found. Specify via TALOSCONFIG=/path/to/talosconfig make run"; \
		exit 1; \
	fi
	@echo "==> Running $(APP_NAME) locally with talosconfig: $(TALOSCONFIG)..."
	TALOSCONFIG="$(TALOSCONFIG)" PORT=":$(PORT)" ./bin/$(APP_NAME)

docker-build:
	@echo "==> Building Docker image: $(IMAGE_NAME):$(IMAGE_TAG)..."
	docker build -t $(IMAGE_NAME):$(IMAGE_TAG) .

docker-run:
	@if [ -z "$(TALOSCONFIG)" ]; then \
		echo "ERROR: talosconfig not found. Specify TALOSCONFIG=/path/to/talosconfig"; \
		exit 1; \
	fi
	@mkdir -p ./data
	@echo "==> Running Docker container on port $(PORT)..."
	docker run --rm -it \
		-p $(PORT):8080 \
		-v "$(TALOSCONFIG)":/etc/talos/talosconfig:ro \
		-v "$$(pwd)/data":/app/data \
		-e TALOSCONFIG=/etc/talos/talosconfig \
		$(IMAGE_NAME):$(IMAGE_TAG)

k8s-namespace:
	@echo "==> Ensuring Kubernetes namespace '$(NAMESPACE)' exists..."
	@kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -

k8s-secret: k8s-namespace
	@echo "==> Creating/updating Kubernetes secret 'talosdeck-config' in namespace '$(NAMESPACE)'..."
	@./deploy/secret-create.sh "$(TALOSCONFIG)" "$(NAMESPACE)"

k8s-deploy: k8s-secret
	@echo "==> Deploying TalosDeck manifests to Kubernetes namespace '$(NAMESPACE)'..."
	kubectl apply -n $(NAMESPACE) -f deploy/
	kubectl set image deployment/$(APP_NAME) $(APP_NAME)=$(IMAGE_NAME):$(IMAGE_TAG) -n $(NAMESPACE)
	@echo "==> Deployment initiated. Checking rollout status..."
	kubectl rollout status deployment/talosdeck -n $(NAMESPACE) --timeout=60s
	@echo "==> Service info:"
	kubectl get svc talosdeck -n $(NAMESPACE)

k8s-undeploy:
	@echo "==> Undeploying TalosDeck from namespace '$(NAMESPACE)'..."
	kubectl delete -n $(NAMESPACE) -f deploy/ --ignore-not-found
	kubectl delete secret talosdeck-config -n $(NAMESPACE) --ignore-not-found
	@echo "==> Undeploy complete."

clean:
	@echo "==> Cleaning build artifacts..."
	rm -rf bin
	rm -rf web/dist
