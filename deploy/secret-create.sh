#!/usr/bin/env bash
set -euo pipefail

# Helper script to create or update the talosdeck-config secret from talosconfig

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

# Auto-detect kubeconfig if not configured or if current config is broken
if [[ -z "${KUBECONFIG:-}" ]]; then
  KUBECONFIG_CANDIDATES=(
    "${REPO_DIR}/../kubeconfig"
    "${REPO_DIR}/kubeconfig"
    "/home/artem/laba-kuber/kubeconfig"
    "${HOME}/.kube/config"
  )
  for k in "${KUBECONFIG_CANDIDATES[@]}"; do
    if [[ -f "${k}" ]] && kubectl --kubeconfig="${k}" version --client >/dev/null 2>&1; then
      export KUBECONFIG="${k}"
      break
    fi
  done
fi

# Search locations for talosconfig if not passed as argument
TALOSCONFIG_PATH="${1:-}"

if [[ -z "${TALOSCONFIG_PATH}" ]]; then
  CANDIDATES=(
    "${REPO_DIR}/../cluster-config/talosconfig"
    "${REPO_DIR}/cluster-config/talosconfig"
    "/home/artem/laba-kuber/cluster-config/talosconfig"
    "${HOME}/.talos/config"
  )
  for c in "${CANDIDATES[@]}"; do
    if [[ -f "${c}" ]]; then
      TALOSCONFIG_PATH="${c}"
      break
    fi
  done
fi

if [[ -z "${TALOSCONFIG_PATH}" || ! -f "${TALOSCONFIG_PATH}" ]]; then
  echo "Error: talosconfig not found!" >&2
  echo "Usage: $0 [path/to/talosconfig] [namespace]" >&2
  exit 1
fi

NAMESPACE="${2:-${NAMESPACE:-default}}"
SECRET_NAME="talosdeck-config"

echo "==> Creating/updating secret '${SECRET_NAME}' in namespace '${NAMESPACE}'"
echo "    Source talosconfig: ${TALOSCONFIG_PATH}"

# Ensure namespace exists if custom
if ! kubectl get namespace "${NAMESPACE}" >/dev/null 2>&1; then
  echo "==> Namespace '${NAMESPACE}' does not exist. Creating..."
  kubectl create namespace "${NAMESPACE}"
fi

kubectl create secret generic "${SECRET_NAME}" \
  --from-file=talosconfig="${TALOSCONFIG_PATH}" \
  --namespace="${NAMESPACE}" \
  --dry-run=client -o yaml | kubectl apply -f -

echo "==> Secret '${SECRET_NAME}' successfully applied in namespace '${NAMESPACE}'."
