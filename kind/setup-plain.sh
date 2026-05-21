#!/usr/bin/env bash
# kind/setup-plain.sh
# Idempotent bring-up for the Option B ext_proc demo. Run from repo root:
#   ./kind/setup-plain.sh
set -euo pipefail

CLUSTER_NAME="ext-proc-plain-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHART_DIR="${KIND_DIR}/demo-ext-proc-plain"
NAMESPACE="demo-plain"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# Note: we avoid touching /etc/hosts — all verification curls use
# `-H "Host: app.local"` with a 127.0.0.1 URL.

# -- Phase A: cluster + images ------------------------------------------------
if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi

log "Building images"
docker build -t workspace/echo-server:dev   -f "${ROOT}/sample-apps/echo-server/deploy/Dockerfile"   "${ROOT}/sample-apps/echo-server/"
docker build -t workspace/pcs-ext-proc:dev  -f "${ROOT}/sample-apps/pcs-ext-proc/deploy/Dockerfile"  "${ROOT}/sample-apps/pcs-ext-proc/"
docker build -t workspace/permission-validation:dev \
  -f "${ROOT}/permission-validation/test/e2e/Dockerfile.sidecar" \
  "${ROOT}/permission-validation/"

log "Loading images into kind"
for img in workspace/echo-server:dev workspace/pcs-ext-proc:dev workspace/permission-validation:dev; do
  kind load docker-image "${img}" --name "${CLUSTER_NAME}"
done

echo
echo "Phase A done. Phase B (generate envoy.yaml + install) is the next task."
