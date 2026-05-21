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

if [[ -z "${SKIP_LOCAL_BUILD:-}" ]]; then
  log "Building images (set SKIP_LOCAL_BUILD=1 to reuse already-built images)"
  docker build -t workspace/echo-server:dev   -f "${ROOT}/sample-apps/echo-server/deploy/Dockerfile"   "${ROOT}/sample-apps/echo-server/"
  docker build -t workspace/pcs-ext-proc:dev  -f "${ROOT}/sample-apps/pcs-ext-proc/deploy/Dockerfile"  "${ROOT}/sample-apps/pcs-ext-proc/"
  docker build -t workspace/permission-validation:dev \
    -f "${ROOT}/permission-validation/test/e2e/Dockerfile.sidecar" \
    "${ROOT}/permission-validation/"
else
  log "SKIP_LOCAL_BUILD set — assuming workspace/echo-server:dev, workspace/pcs-ext-proc:dev, workspace/permission-validation:dev are already built locally"
fi

log "Loading images into kind"
for img in workspace/echo-server:dev workspace/pcs-ext-proc:dev workspace/permission-validation:dev; do
  kind load docker-image "${img}" --name "${CLUSTER_NAME}"
done

# -- Phase B: generate envoy.yaml + helm install ------------------------------
log "Validating routes.yaml"
( cd "${ROOT}/permission-validation" && go run ./cmd/validate-routes validate "${KIND_DIR}/routes.yaml" )

ENVOY_OUT="$(mktemp -t envoy.XXXXX.yaml)"
trap 'rm -f "${ENVOY_OUT}"' EXIT

log "Translating routes.yaml -> envoy.yaml at ${ENVOY_OUT}"
( cd "${ROOT}/permission-validation" && go run ./cmd/validate-routes translate \
    "${KIND_DIR}/routes.yaml" \
    -o "${ENVOY_OUT}" \
    --sidecar-host 127.0.0.1 --sidecar-port 50051 \
    --backend-host 127.0.0.1 --backend-port 8080 \
    --admin-host 127.0.0.1 \
    --access-log )

log "Helm install (with --set-file envoyBootstrap=${ENVOY_OUT})"
helm upgrade --install plain "${CHART_DIR}" \
  --namespace default \
  --set-file envoyBootstrap="${ENVOY_OUT}" \
  --wait --timeout 180s

# -- Phase C: verification ----------------------------------------------------
log "Waiting for echo-app and pcs pods to be Ready"
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=echo-app --timeout=120s
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=pcs       --timeout=120s

log "Verifying canonical curls"
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

BASE="http://127.0.0.1:8090"

# 1) ALLOW
expect_status 200 "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"

# 2) DENY
expect_status 403 "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-2:document:edit"

# 3) MISSING CONTEXT
expect_status 403 "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test"

# 4) SKIPPED ROUTE
expect_status 200 "${BASE}/healthz" -H "Host: app.local"

if [[ ${fail} -eq 0 ]]; then
  printf "\n\033[1;32mAll four canonical curls returned expected status codes.\033[0m\n"
else
  printf "\n\033[1;31mAt least one curl returned an unexpected status. Inspect pod logs.\033[0m\n"
  exit 1
fi
