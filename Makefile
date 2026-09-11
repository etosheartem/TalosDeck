# TalosDeck Makefile
SHELL := /bin/bash

APP_NAME ?= talosdeck
IMAGE_NAME ?= talosdeck
IMAGE_TAG ?= latest
PORT ?= 8080

# Auto-detect kubeconfig location
KUBECONFIG ?= $(shell \
	if [ -f "../kubeconfig" ]; then echo "$$(pwd)/../kubeconfig"; \
	elif [ -f "./kubeconfig" ]; then echo "$$(pwd)/kubeconfig"; \
	elif [ -f "/home/artem/laba-kuber/kubeconfig" ]; then echo "/home/artem/laba-kuber/kubeconfig"; \
	else echo "$${HOME}/.kube/config"; fi)
export KUBECONFIG

# Auto-detect talosconfig location
TALOSCONFIG ?= $(shell \
	if [ -f "../cluster-config/talosconfig" ]; then echo "$$(pwd)/../cluster-config/talosconfig"; \
	elif [ -f "./cluster-config/talosconfig" ]; then echo "$$(pwd)/cluster-config/talosconfig"; \
	elif [ -f "/home/artem/laba-kuber/cluster-config/talosconfig" ]; then echo "/home/artem/laba-kuber/cluster-config/talosconfig"; \
	elif [ -f "$${HOME}/.talos/config" ]; then echo "$${HOME}/.talos/config"; \
	else echo ""; fi)
export TALOSCONFIG

# Detect package manager for frontend (bun or npm)
PM ?= $(shell if command -v bun >/dev/null 2>&1; then echo "bun"; else echo "npm"; fi)

.PHONY: all help build build-web build-go run docker-build docker-run k8s-secret k8s-deploy k8s-undeploy clean

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
	@echo "==> Running Docker container on port $(PORT)..."
	docker run --rm -it \
		-p $(PORT):8080 \
		-v "$(TALOSCONFIG)":/etc/talos/talosconfig:ro \
		-e TALOSCONFIG=/etc/talos/talosconfig \
		$(IMAGE_NAME):$(IMAGE_TAG)

k8s-secret:
	@echo "==> Creating/updating Kubernetes secret 'talosdeck-config'..."
	@./deploy/secret-create.sh "$(TALOSCONFIG)"

k8s-deploy: k8s-secret
	@echo "==> Deploying TalosDeck manifests to Kubernetes..."
	kubectl apply -f deploy/deployment.yaml
	kubectl apply -f deploy/service.yaml
	@echo "==> Deployment initiated. Checking rollout status..."
	kubectl rollout status deployment/talosdeck --timeout=60s || true
	@echo "==> Service info:"
	kubectl get svc talosdeck

k8s-undeploy:
	@echo "==> Undeploying TalosDeck..."
	kubectl delete -f deploy/service.yaml --ignore-not-found
	kubectl delete -f deploy/deployment.yaml --ignore-not-found
	kubectl delete secret talosdeck-config --ignore-not-found
	@echo "==> Undeploy complete."

clean:
	@echo "==> Cleaning build artifacts..."
	rm -rf bin
	rm -rf web/dist
