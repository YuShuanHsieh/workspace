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

# -- Phase B: validate routes + helm install ----------------------------------
log "Validating routes.yaml (lint only — Option A does not generate envoy.yaml)"
( cd "${ROOT}/permission-validation" && go run ./cmd/validate-routes validate "${KIND_DIR}/routes.yaml" )

log "Helm install"
helm upgrade --install istio "${CHART_DIR}" --namespace default --wait --timeout 180s

# -- Phase C: verification ----------------------------------------------------
log "Waiting for echo-app and pcs pods to be Ready"
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=echo-app --timeout=180s
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=pcs       --timeout=180s

log "Verifying canonical curls (via ingressgateway @ localhost:8080, Host: app.local)"
fail=0
expect_status() {
  local want="$1"; shift
  local got
  got="$(curl -sS -o /dev/null -w '%{http_code}' "$@")"
  if [[ "${got}" != "${want}" ]]; then
    printf "  FAIL  expected %s, got %s    curl %s\n" "${want}" "${got}" "$*"
    fail=1
  else
    printf "  ok    %s    curl %s\n" "${got}" "$*"
  fi
}

# Tiny wait for the EnvoyFilter to propagate to istio-proxy via xDS
sleep 5

# 1) ALLOW
expect_status 200 "http://127.0.0.1:8080/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"

# 2) DENY
expect_status 403 "http://127.0.0.1:8080/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-2:document:edit"

# 3) MISSING CONTEXT
expect_status 403 "http://127.0.0.1:8080/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test"

# 4) ECHO HEALTHZ — Option A does not honour the routes.yaml skipped list
#    (no translate target); the request still goes through ext_proc, where
#    the sidecar rejects missing X-Auth-Context with 403. We assert 403 here
#    intentionally and call it out in DEMO.md.
expect_status 403 "http://127.0.0.1:8080/healthz" -H "Host: app.local"

if [[ ${fail} -eq 0 ]]; then
  printf "\n\033[1;32mAll four canonical curls returned expected status codes.\033[0m\n"
else
  printf "\n\033[1;31mAt least one curl returned an unexpected status.\033[0m\n"
  exit 1
fi
