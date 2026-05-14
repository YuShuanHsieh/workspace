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
helm upgrade --install istio-base "${CHARTS}/base-1.24.2.tgz" -n istio-system --wait

log "Installing istiod from ${CHARTS}/istiod-1.24.2.tgz"
helm upgrade --install istiod "${CHARTS}/istiod-1.24.2.tgz" -n istio-system --wait

# 3. Build and load local images
log "Building local images (echo-server, pcs, dashboard-client)"
(cd "${ROOT}/sample-apps/echo-server"      && docker build -t workspace/echo-server:dev      -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/pcs"              && docker build -t workspace/pcs:dev              -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/dashboard-client" && docker build -t workspace/dashboard-client:dev -f deploy/Dockerfile .)

log "Loading images into kind"
kind load docker-image workspace/echo-server:dev      --name "${CLUSTER_NAME}"
kind load docker-image workspace/pcs:dev              --name "${CLUSTER_NAME}"
kind load docker-image workspace/dashboard-client:dev --name "${CLUSTER_NAME}"
