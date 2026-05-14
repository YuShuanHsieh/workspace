#!/usr/bin/env bash
# One-shot bring-up for the ext-authz kind demo. Idempotent.
# Run from the repo root: ./kind/setup.sh
set -euo pipefail

CLUSTER_NAME="ext-authz-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHARTS="${KIND_DIR}/charts"
MANIFESTS="${KIND_DIR}/manifests"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# ─────────────────────────────────────────────────────────────────────────────
# Phase A — Cluster bootstrap (cluster admin actions)
# ─────────────────────────────────────────────────────────────────────────────

# 1. kind cluster
if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi
kubectl config use-context "kind-${CLUSTER_NAME}"

# 2. Istio control plane from vendored charts
log "Installing istio-base from ${CHARTS}/base-1.24.2.tgz"
kubectl create namespace istio-system --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install istio-base "${CHARTS}/base-1.24.2.tgz" -n istio-system --wait \
  -f "${KIND_DIR}/chart-values/istio-base-values.yaml"

log "Installing istiod from ${CHARTS}/istiod-1.24.2.tgz"
helm upgrade --install istiod "${CHARTS}/istiod-1.24.2.tgz" -n istio-system --wait \
  -f "${KIND_DIR}/chart-values/istiod-values.yaml"

# 3. Build and load local images
log "Building local images (echo-server, pcs, dashboard-client)"
(cd "${ROOT}/sample-apps/echo-server"      && docker build -t workspace/echo-server:dev      -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/pcs"              && docker build -t workspace/pcs:dev              -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/dashboard-client" && docker build -t workspace/dashboard-client:dev -f deploy/Dockerfile .)

log "Loading images into kind"
kind load docker-image workspace/echo-server:dev      --name "${CLUSTER_NAME}"
kind load docker-image workspace/pcs:dev              --name "${CLUSTER_NAME}"
kind load docker-image workspace/dashboard-client:dev --name "${CLUSTER_NAME}"

# ─────────────────────────────────────────────────────────────────────────────
# Phase B — Documents product-team actions
# ─────────────────────────────────────────────────────────────────────────────

# 4. documents namespace
log "Applying documents namespace (istio-injection: enabled)"
kubectl apply -f "${MANIFESTS}/documents/namespace-documents.yaml"

# 5. documents ingressgateway (Helm release in documents ns)
log "Installing documents-ingressgateway (chart: gateway-1.24.2.tgz)"
helm upgrade --install documents-ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n documents --wait --skip-schema-validation \
  -f "${KIND_DIR}/chart-values/documents-ingressgateway-values.yaml"

# 6. PCS (owned by documents team)
log "Deploying PCS in documents namespace"
kubectl apply -f "${MANIFESTS}/documents/pcs-deployment.yaml"
kubectl apply -f "${MANIFESTS}/documents/pcs-service.yaml"
kubectl -n documents wait --for=condition=Available deploy/pcs --timeout=120s

# 7. documents-api and documents-search
log "Deploying documents-api and documents-search"
kubectl apply -f "${MANIFESTS}/documents/documents-api-deployment.yaml"
kubectl apply -f "${MANIFESTS}/documents/documents-api-service.yaml"
kubectl apply -f "${MANIFESTS}/documents/documents-search-deployment.yaml"
kubectl apply -f "${MANIFESTS}/documents/documents-search-service.yaml"
kubectl -n documents wait --for=condition=Available deploy/documents-api    --timeout=180s
kubectl -n documents wait --for=condition=Available deploy/documents-search --timeout=180s

# 8. documents EnvoyFilter
log "Applying documents-ext-authz EnvoyFilter"
kubectl apply -f "${MANIFESTS}/documents/documents-ext-authz.yaml"

# 9. documents Gateway + VirtualService
log "Applying documents Gateway + VirtualService"
kubectl apply -f "${MANIFESTS}/documents/documents-gateway.yaml"
kubectl apply -f "${MANIFESTS}/documents/documents-virtualservice.yaml"

# 10. dashboard-client
log "Deploying dashboard-client"
kubectl apply -f "${MANIFESTS}/documents/dashboard-client-deployment.yaml"

# ─────────────────────────────────────────────────────────────────────────────
# Phase C — Wiki team actions (cross-namespace pattern)
# ─────────────────────────────────────────────────────────────────────────────

# 11. wiki namespace
log "Applying wiki namespace (istio-injection: enabled)"
kubectl apply -f "${MANIFESTS}/wiki/namespace-wiki.yaml"

# 12. wiki ingressgateway
log "Installing wiki-ingressgateway (chart: gateway-1.24.2.tgz)"
helm upgrade --install wiki-ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n wiki --wait --skip-schema-validation \
  -f "${KIND_DIR}/chart-values/wiki-ingressgateway-values.yaml"

# 13. wiki EnvoyFilter (cross-ns copy)
log "Applying wiki-ext-authz EnvoyFilter (cross-ns copy)"
kubectl apply -f "${MANIFESTS}/wiki/wiki-ext-authz.yaml"

# 14. wiki-api
log "Deploying wiki-api"
kubectl apply -f "${MANIFESTS}/wiki/wiki-api-deployment.yaml"
kubectl apply -f "${MANIFESTS}/wiki/wiki-api-service.yaml"
kubectl -n wiki wait --for=condition=Available deploy/wiki-api --timeout=180s

# 15. wiki Gateway + VirtualService
log "Applying wiki Gateway + VirtualService"
kubectl apply -f "${MANIFESTS}/wiki/wiki-gateway.yaml"
kubectl apply -f "${MANIFESTS}/wiki/wiki-virtualservice.yaml"

# ─────────────────────────────────────────────────────────────────────────────
# Verification banner
# ─────────────────────────────────────────────────────────────────────────────

cat <<EOF

─────────────────────────────────────────────────────────────
✓ kind ext-authz demo is up

Add to /etc/hosts (one line):
  127.0.0.1  documents.local wiki.local

Watch the dashboard-client cycle:
  kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f

Watch PCS decisions:
  kubectl -n documents logs deploy/pcs -c pcs -f

Curl from host (after /etc/hosts):
  curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local/hello       # 200
  curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local/hello       # 403
  curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello       # 200
  curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello       # 403

EnvoyFilters in app namespaces (none in istio-system):
  kubectl get envoyfilter -A

Teardown:
  ${KIND_DIR}/teardown.sh
EOF
