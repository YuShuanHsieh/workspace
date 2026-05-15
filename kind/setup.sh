#!/usr/bin/env bash
# One-shot bring-up for the ext-authz kind demo. Idempotent.
# Run from the repo root: ./kind/setup.sh
#
# This version uses the umbrella Helm chart at kind/demo/. To swap container
# registries (e.g. for company-internal use), edit kind/demo/values.yaml.
set -euo pipefail

CLUSTER_NAME="ext-authz-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHARTS="${KIND_DIR}/charts"
DEMO="${KIND_DIR}/demo"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# ─────────────────────────────────────────────────────────────────────────────
# Single source of truth for the Istio container image: kind/demo/values.yaml
# under images.istio (hub:tag format, e.g. docker.io/istio:1.24.2). We split it
# here for the Istio Helm charts, which want global.hub and global.tag as
# separate fields. To redirect at a company-internal mirror, edit values.yaml —
# nothing else in this script needs to change.
# ─────────────────────────────────────────────────────────────────────────────
ISTIO_IMAGE=$(awk '$1=="istio:" {print $2; exit}' "${DEMO}/values.yaml")
if [[ -z "${ISTIO_IMAGE}" || "${ISTIO_IMAGE}" != *:* ]]; then
  echo "ERROR: could not read images.istio (hub:tag) from ${DEMO}/values.yaml" >&2
  exit 1
fi
ISTIO_HUB="${ISTIO_IMAGE%:*}"
ISTIO_TAG="${ISTIO_IMAGE##*:}"

# ─────────────────────────────────────────────────────────────────────────────
# Phase A — Cluster bootstrap + image build/load
# ─────────────────────────────────────────────────────────────────────────────

if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi
kubectl config use-context "kind-${CLUSTER_NAME}"

# Local image build+load. The tags below MUST match kind/demo/values.yaml →
# images.{echoServer,pcs,dashboardClient}. For company use (private registry),
# skip this build/load section and just override the three lines in values.yaml
# to point at your pre-built, pre-published images.
log "Building local images (echo-server, pcs, dashboard-client)"
(cd "${ROOT}/sample-apps/echo-server"      && docker build -t workspace/echo-server:dev      -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/pcs"              && docker build -t workspace/pcs:dev              -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/dashboard-client" && docker build -t workspace/dashboard-client:dev -f deploy/Dockerfile .)

log "Loading images into kind"
kind load docker-image workspace/echo-server:dev      --name "${CLUSTER_NAME}"
kind load docker-image workspace/pcs:dev              --name "${CLUSTER_NAME}"
kind load docker-image workspace/dashboard-client:dev --name "${CLUSTER_NAME}"

# ─────────────────────────────────────────────────────────────────────────────
# Phase B — Istio control plane (separate Helm releases) + umbrella chart
#          for the app k8s manifests
# ─────────────────────────────────────────────────────────────────────────────
#
# istio-base + istiod are installed as their OWN Helm releases (not as
# subcharts of the umbrella) because the istiod chart redeclares some of
# the resources istio-base creates (ServiceAccounts, ClusterRoles), so
# bundling them into one umbrella triggers Helm ownership conflicts.
# Istio's own install docs recommend this same separated pattern.
#
# The umbrella chart (kind/demo/) then carries ONLY the app k8s manifests
# (Deployments, Services, EnvoyFilters, Gateway, VirtualService) — and the
# single values.yaml that drives image-registry overrides everywhere.

log "Installing istio-base (provides Istio CRDs; no container image)"
kubectl create namespace istio-system --dry-run=client -o yaml | kubectl apply -f -
helm upgrade --install istio-base "${CHARTS}/base-1.24.2.tgz" -n istio-system --wait

# Image overrides come from kind/demo/values.yaml → images.istio (split above).
log "Installing istiod (image: ${ISTIO_HUB}/pilot:${ISTIO_TAG})"
helm upgrade --install istiod "${CHARTS}/istiod-1.24.2.tgz" -n istio-system --wait \
  --set "global.hub=${ISTIO_HUB}" \
  --set "global.tag=${ISTIO_TAG}"

log "Installing umbrella chart 'demo' (app k8s manifests in documents + wiki)"
helm upgrade --install demo "${DEMO}" -n istio-system --wait

# Wait for the app workloads applied by the umbrella to be Available
log "Waiting for documents-team Deployments to be Available"
kubectl -n documents wait --for=condition=Available deploy/pcs              --timeout=120s
kubectl -n documents wait --for=condition=Available deploy/documents-api    --timeout=180s
kubectl -n documents wait --for=condition=Available deploy/documents-search --timeout=180s
kubectl -n documents wait --for=condition=Available deploy/dashboard-client --timeout=120s

log "Waiting for wiki-team Deployment to be Available"
kubectl -n wiki wait --for=condition=Available deploy/wiki-api --timeout=180s

# ─────────────────────────────────────────────────────────────────────────────
# Phase C — Per-namespace ingressgateways (separate Helm releases because
# subchart deps can't span namespaces; values match the umbrella's
# values.yaml)
# ─────────────────────────────────────────────────────────────────────────────

# Image overrides for both gateways come from kind/demo/values.yaml → images.istio
# (split into ISTIO_HUB / ISTIO_TAG at the top of this script). The gateway chart
# uses ${ISTIO_HUB}/proxyv2:${ISTIO_TAG} as its container image.
log "Installing documents-ingressgateway (image: ${ISTIO_HUB}/proxyv2:${ISTIO_TAG})"
helm upgrade --install documents-ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n documents --wait --skip-schema-validation \
  --set name=documents-ingressgateway \
  --set labels.istio=documents-ingressgateway \
  --set "global.hub=${ISTIO_HUB}" \
  --set "global.tag=${ISTIO_TAG}" \
  --set service.type=NodePort \
  --set 'service.ports[0].name=status-port,service.ports[0].port=15021,service.ports[0].targetPort=15021' \
  --set 'service.ports[1].name=http2,service.ports[1].port=80,service.ports[1].targetPort=80,service.ports[1].nodePort=30080' \
  --set 'service.ports[2].name=https,service.ports[2].port=443,service.ports[2].targetPort=443,service.ports[2].nodePort=30443' \
  --set autoscaling.enabled=false \
  --set 'resources.requests.cpu=20m,resources.requests.memory=64Mi,resources.limits.cpu=200m,resources.limits.memory=128Mi'

log "Installing wiki-ingressgateway (image: ${ISTIO_HUB}/proxyv2:${ISTIO_TAG})"
helm upgrade --install wiki-ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n wiki --wait --skip-schema-validation \
  --set name=wiki-ingressgateway \
  --set labels.istio=wiki-ingressgateway \
  --set "global.hub=${ISTIO_HUB}" \
  --set "global.tag=${ISTIO_TAG}" \
  --set service.type=NodePort \
  --set 'service.ports[0].name=status-port,service.ports[0].port=15021,service.ports[0].targetPort=15021' \
  --set 'service.ports[1].name=http2,service.ports[1].port=80,service.ports[1].targetPort=80,service.ports[1].nodePort=30081' \
  --set 'service.ports[2].name=https,service.ports[2].port=443,service.ports[2].targetPort=443' \
  --set autoscaling.enabled=false \
  --set 'resources.requests.cpu=20m,resources.requests.memory=64Mi,resources.limits.cpu=200m,resources.limits.memory=128Mi'

# ─────────────────────────────────────────────────────────────────────────────
# Phase D — Wait for gateway pods to be Ready.
# Helm's --wait sometimes returns before the gateway pod has fully connected
# to istiod; the pod then restarts once and takes ~50s to flip to Ready. Block
# here so the banner below is not shown until external curls would succeed.
# ─────────────────────────────────────────────────────────────────────────────

log "Waiting for ingressgateway pods to be Ready"
kubectl -n documents wait --for=condition=ready pod -l istio=documents-ingressgateway --timeout=180s
kubectl -n wiki      wait --for=condition=ready pod -l istio=wiki-ingressgateway      --timeout=180s

# ─────────────────────────────────────────────────────────────────────────────
# Verification banner
# ─────────────────────────────────────────────────────────────────────────────

cat <<EOF

─────────────────────────────────────────────────────────────
✓ kind ext-authz demo is up (via umbrella chart kind/demo/)

One-time /etc/hosts setup (primary path — clean URLs):
  echo '127.0.0.1  documents.local wiki.local' | sudo tee -a /etc/hosts

Then curl from host:
  curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local:8080/hello       # 200
  curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello       # 403
  curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello            # 200
  curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello            # 403

Alternative (no sudo / CI-friendly) — pass the host as a header, Istio routes on it:
  curl -H "Host: documents.local" -H "x-workspace-user-id: alice@workspace.test" http://127.0.0.1:8080/hello
  curl -H "Host: wiki.local"      -H "x-workspace-user-id: alice@workspace.test" http://127.0.0.1:8081/hello

Watch the dashboard-client cycle:
  kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f

Watch PCS decisions:
  kubectl -n documents logs deploy/pcs -c pcs -f

EnvoyFilters in app namespaces (none in istio-system):
  kubectl get envoyfilter -A

Swap container registry for company use:
  App images:       edit kind/demo/values.yaml under images.*
  Kind node image:  edit kind/kind-config.yaml (node image is pinned there by digest)

Teardown:
  ${KIND_DIR}/teardown.sh
EOF
