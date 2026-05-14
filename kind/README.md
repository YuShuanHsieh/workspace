# Ext-Authz Kind Demo

A local kind cluster that demonstrates Envoy `ext_authz` wiring via per-namespace `EnvoyFilter` resources.

**Full design:** see [`../docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md`](../docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md).

## What this builds

| Namespace | What's inside |
|---|---|
| `istio-system` | istio-base + istiod (Stage 1 writes nothing else here) |
| `documents` | documents-api + documents-search + dashboard-client + pcs (owned by documents team) + documents-ext-authz EnvoyFilter + documents-ingressgateway |
| `wiki` | wiki-api + wiki-ext-authz (cross-ns copy) + wiki-ingressgateway |

The `documents` and `wiki` namespaces each demonstrate a distinct onboarding pattern. `dashboard-client` cycles through all three protected workloads with alternating `x-workspace-user-id` headers.

## Prerequisites

- Docker Desktop running, ≥ 6 GB RAM allocated
- `kind`, `kubectl`, `helm`, `docker`, `go` (≥ 1.25) installed

## Run

From the repo root:

```bash
./kind/setup.sh
```

Idempotent — re-running picks up where the previous run left off. Total wall-clock on a warm Docker cache is ≤ 3 minutes.

Add to `/etc/hosts`:

```
127.0.0.1  documents.local wiki.local
```

## Verify

```bash
# Watch the request cycle
kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f

# Watch authorization decisions
kubectl -n documents logs deploy/pcs -c pcs -f

# Curl from host (after /etc/hosts)
curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local/hello       # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local/hello       # 403
curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello       # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello       # 403
```

## Teardown

```bash
./kind/teardown.sh
```
