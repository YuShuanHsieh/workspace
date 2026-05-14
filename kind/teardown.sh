#!/usr/bin/env bash
# Teardown for the ext-authz demo kind cluster.
set -euo pipefail
CLUSTER_NAME="ext-authz-demo"

if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  echo "▶ Deleting kind cluster '${CLUSTER_NAME}'"
  kind delete cluster --name "${CLUSTER_NAME}"
else
  echo "kind cluster '${CLUSTER_NAME}' not found — nothing to delete."
fi
