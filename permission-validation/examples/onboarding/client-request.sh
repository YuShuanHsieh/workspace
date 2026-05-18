#!/usr/bin/env bash
# Phase 1 onboarding: example client calls through the sidecar.
# Trust model: the client is trusted to declare a truthful (objectId, objectType).
# PCS still decides whether the SSO user holds the requested permission on that object.
# A client substituting an objectId they don't have permission on is harmless (PCS denies).
# A client substituting an objectId they DO have permission on while the app backend
# operates on a different object referenced in the URL is the Phase 1 residual risk.
set -euo pipefail

ENVOY=${ENVOY:-http://127.0.0.1:8000}
SSO=${SSO:-"Bearer your-sso-token"}

echo "# granted: user-1 viewing doc-1"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "Authorization: $SSO" \
  -H "X-Auth-Context: doc-1:document:view"

echo
echo "# denied: user-1 trying admin-delete on doc-1 (PCS will reject if they lack the perm)"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "Authorization: $SSO" \
  -H "X-Auth-Context: doc-1:document:admin-delete"

echo
echo "# rejected at sidecar (malformed: too few segments)"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "Authorization: $SSO" \
  -H "X-Auth-Context: doc-1:document"

echo
echo "# rejected at sidecar (missing Authorization)"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "X-Auth-Context: doc-1:document:view"
