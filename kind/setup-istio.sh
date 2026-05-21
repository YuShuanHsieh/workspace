#!/usr/bin/env bash
# kind/setup-istio.sh
# Idempotent bring-up for the Option A ext_proc demo. Run from repo root:
#   ./kind/setup-istio.sh
set -euo pipefail

CLUSTER_NAME="ext-proc-istio-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHARTS="${KIND_DIR}/charts"
CHART_DIR="${KIND_DIR}/demo-ext-proc-istio"
NAMESPACE="demo-istio"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# Note: we avoid touching /etc/hosts — all verification curls use
# `-H "Host: app.local"` with a 127.0.0.1 URL.

ISTIO_HUB="docker.io/istio"
ISTIO_TAG="1.24.2"

# -- Phase A: cluster ---------------------------------------------------------
if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi

# -- Phase A: Istio -----------------------------------------------------------
log "Installing Istio (base + istiod + ingressgateway)"
helm upgrade --install istio-base   "${CHARTS}/base-1.24.2.tgz" \
  -n istio-system --create-namespace --wait
helm upgrade --install istiod       "${CHARTS}/istiod-1.24.2.tgz" \
  -n istio-system \
  --set "global.hub=${ISTIO_HUB}" --set "global.tag=${ISTIO_TAG}" \
  --wait
helm upgrade --install ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n istio-system \
  --set "service.type=NodePort" \
  --set "service.ports[0].name=http2" \
  --set "service.ports[0].port=80" \
  --set "service.ports[0].targetPort=80" \
  --set "service.ports[0].nodePort=30080" \
  --wait

# -- Phase A: images ----------------------------------------------------------
log "Building images"
docker build -t workspace/echo-server:dev          -f "${ROOT}/sample-apps/echo-server/deploy/Dockerfile"         "${ROOT}/sample-apps/echo-server/"
docker build -t workspace/pcs-ext-proc:dev         -f "${ROOT}/sample-apps/pcs-ext-proc/deploy/Dockerfile"        "${ROOT}/sample-apps/pcs-ext-proc/"
docker build -t workspace/permission-validation:dev -f "${ROOT}/permission-validation/test/e2e/Dockerfile.sidecar" "${ROOT}/permission-validation/"

log "Loading images into kind"
for img in workspace/echo-server:dev workspace/pcs-ext-proc:dev workspace/permission-validation:dev; do
  kind load docker-image "${img}" --name "${CLUSTER_NAME}"
done

echo "Phase A done."
