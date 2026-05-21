#!/usr/bin/env bash
# kind/teardown.sh <cluster-name>
# Removes the named kind cluster. Cluster names:
#   ext-proc-plain-demo   (created by setup-plain.sh)
#   ext-proc-istio-demo   (created by setup-istio.sh)
set -euo pipefail

CLUSTER_NAME="${1:-}"
if [[ -z "${CLUSTER_NAME}" ]]; then
  echo "Usage: $0 <cluster-name>"
  echo "Available clusters:"
  kind get clusters | sed 's/^/  /'
  exit 1
fi

if ! kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  echo "kind cluster '${CLUSTER_NAME}' not found, nothing to do"
  exit 0
fi

kind delete cluster --name "${CLUSTER_NAME}"
