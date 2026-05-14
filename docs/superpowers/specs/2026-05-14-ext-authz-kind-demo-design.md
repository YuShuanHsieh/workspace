# Ext-Authz Kind Demo — Minimal Harness Design

> **Status:** Draft for review
> **Audience:** Platform team members learning the Envoy `ext_authz` pattern; engineers evaluating ext_authz wiring options before integrating into production manifests.
> **Owner:** Ashwini (platform)
> **Related documents:**
> - [`2026-05-13-workspace-kind-harness-design.md`](2026-05-13-workspace-kind-harness-design.md) — broader workspace platform kind harness (NATS-based; this demo is intentionally narrower and unrelated to that NATS flow).
> - [`2026-05-13-permission-validation-phase1-sidecar-design.md`](2026-05-13-permission-validation-phase1-sidecar-design.md) — Phase 1 Permission Validation sidecar implementation (Envoy `ext_authz` is the deployment shape this demo exercises end-to-end).
> **Reference implementation pattern:** The ECK Elasticsearch kind harness on the `claude/eck-elasticsearch-setup-VCAsc` branch of the `hmchangw/chat` repository at `charts/elasticsearch/kind/` — structural model only (vendored chart layout, single `setup.sh`, plain manifests).

---

## 1. Background and Motivation

The Permission Validation Phase 1 design (sibling spec) calls for two deployment shells over a shared core: an Envoy `ext_authz` gRPC sidecar for Istio-enabled namespaces and a standalone HTTP proxy for non-Istio namespaces. Before that core is wired into production app charts, the platform engineer (Ashwini) needs a **local, single-file-to-run demo** that proves the Envoy ext_authz path end-to-end against a dummy permission checking service.

The demo also serves as a teaching artifact: it pins down concretely "where does the ext_authz YAML live, who owns it, and what does the request flow look like" — questions that are otherwise abstract.

The ECK Elasticsearch kind harness is the structural template — vendored or pulled charts, a single idempotent `setup.sh`, plain-YAML manifests, a clear verification path. This demo is deliberately narrower: no Vault, no NATS, no ingress to host, no Helm chart for app components.

## 2. Goal and Non-Goals

### 2.1 Goal

Stand up a single-node `kind` cluster that demonstrates per-request authorization via Envoy's `ext_authz` HTTP filter, with the filter wired **purely through a namespace-scoped `EnvoyFilter` resource** (no mesh-config / `extensionProvider` registration).

Concretely the harness must produce these observable outcomes on a fresh checkout, in any order:

1. `app1` (a plain pod with no sidecar) running in namespace `app1` calls `app2.app2.svc.cluster.local:8080/hello` over HTTP, alternating two values of the `x-user` request header (`alice` and `mallory`).
2. `app2` runs in namespace `app2` with Istio sidecar injection enabled. The injected Envoy sidecar carries an `ext_authz` HTTP filter that calls a `perm-check` service on every inbound request.
3. The `perm-check` service in namespace `perm-check` returns `200` for `alice` and `403` for `mallory`.
4. `app1`'s log shows alternating `200 hello from app2` and `403` responses.
5. The `app2` container log shows requests **only from `alice`** — `mallory`'s requests are rejected by the Envoy sidecar before reaching the `app2` container.
6. Scaling `perm-check` to zero causes **all** requests (both `alice` and `mallory`) to receive `403`, demonstrating fail-closed behavior.

### 2.2 Non-Goals

| Capability | Status | Reason for deferral |
|---|---|---|
| Mesh-level `extensionProvider` + `AuthorizationPolicy` (action: CUSTOM) wiring | Out | Demo is scoped to the `EnvoyFilter`-only approach (Tareeka 2 from brainstorming). The mesh-level pattern is documented in §9 as a future enhancement. |
| HTTPS / TLS termination | Out | Cluster-internal demo. Plain HTTP keeps the observable flow simple. |
| Ingress gateway exposed to host | Out | No browser-side path in this demo. All verification is via `kubectl logs` / `kubectl exec`. |
| Real auth tokens (JWT, OIDC) | Out | Header-based allow/deny is sufficient to demonstrate ext_authz decision-flow. |
| Dynamic policy registry / database for `perm-check` | Out | A hard-coded in-process allow-list of usernames is sufficient. |
| Cluster-level baseline `AuthorizationPolicy` resources (`allow-ip`, `allow-metrics`, `allow-specific-namespace`) | Out | Orthogonal concern — those operate at Istio L4/L7 mesh layer, not at the ext_authz call. Adding them now dilutes the headline claim. |
| Performance / latency / load testing | Out | This is a correctness harness, not a benchmark. |
| mTLS between `app1` and `app2` | Out | `app1` has no sidecar by design; mesh-mTLS would require sidecars on both ends. |
| Vendored `.tgz` Istio charts | Out (default) | `setup.sh` pulls Istio charts from the public Helm repo. Vendoring is an easy follow-up if offline operation is required (see §9). |
| Multi-cluster / cross-cluster federation | Out | Single kind cluster only. |
| Helm chart for the demo workloads | Out | Three Deployments and one EnvoyFilter — not enough variation to justify templating. Plain YAML stays. |

## 3. Architecture

### 3.1 Cluster Layout

A single-node `kind` cluster hosts every component. Four namespaces:

```text
┌────────────────────────────────────────────────────────────────────┐
│                  kind cluster (single node)                        │
│                                                                    │
│  ns: istio-system           (Istio install target)                 │
│    istio-base   (CRDs + cluster RBAC)                              │
│    istiod       (control plane)                                    │
│                                                                    │
│  ns: app1                   (istio-injection: disabled)            │
│    app1 Pod (plain)                                                │
│      └─ container: curl-loop (curlimages/curl:8.10)                │
│                                                                    │
│  ns: app2                   (istio-injection: enabled)             │
│    app2 Pod (auto-injected)                                        │
│      ├─ container: app2  (locally built Go echo server)            │
│      └─ container: istio-proxy  (Envoy sidecar — auto-injected)    │
│    EnvoyFilter "app2-ext-authz"                                    │
│      → patches istio-proxy's HTTP filter chain to add ext_authz    │
│      → points at perm-check.perm-check.svc.cluster.local:8080      │
│    Service "app2" (ClusterIP, port 8080)                           │
│                                                                    │
│  ns: perm-check             (istio-injection: disabled)            │
│    perm-check Pod (plain)                                          │
│      └─ container: perm-check (locally built Go decision server)   │
│    Service "perm-check" (ClusterIP, port 8080)                     │
└────────────────────────────────────────────────────────────────────┘
```

No host port mappings, no `/etc/hosts` edits, no ingress gateway. The entire flow stays inside the cluster and is observed via `kubectl logs`.

### 3.2 Request Flow

```text
┌──────────┐  HTTP GET /hello                ┌────────────────────┐
│ app1 pod │ ─── x-user: alice ─────────────►│ Envoy sidecar      │
│  (curl)  │                                 │  ext_authz filter  │
└──────────┘                                 └─────────┬──────────┘
                                                       │ POST /check
                                                       │ (forwards x-user header)
                                                       ▼
                                            ┌──────────────────────┐
                                            │ perm-check service   │
                                            │   user=alice → 200   │
                                            │   user=mallory → 403 │
                                            └─────────┬────────────┘
                                                      │
                                                      ▼
                                            ┌────────────────────┐
                                            │ Envoy:             │
                                            │   200 → forward    │
                                            │   403 → reply 403  │
                                            └─────────┬──────────┘
                                                      │ (allow case only)
                                                      ▼
                                            ┌────────────────────┐
                                            │ app2 container     │
                                            │  returns "hello"   │
                                            └────────────────────┘
```

The headline observable property: the **deny decision is enforced at the Envoy sidecar layer**, not by any application code in `app2`. The `app2` container only sees requests for `alice`.

### 3.3 ext_authz Wiring: EnvoyFilter (no mesh config touched)

The ext_authz HTTP filter is added to the `app2` sidecar's HTTP filter chain via an `EnvoyFilter` resource scoped to the `app2` namespace and selected by Pod labels.

Key configuration decisions, encoded in the EnvoyFilter:

- **`workloadSelector.labels.app: app2`** — the patch applies only to pods labeled `app: app2`. Other workloads in `app2` namespace (if any are added later) are not affected.
- **`context: SIDECAR_INBOUND`** — the filter intercepts traffic entering `app2`, not traffic `app2` makes outbound.
- **`applyTo: HTTP_FILTER` + `operation: INSERT_BEFORE`** — the ext_authz filter is inserted into the HTTP filter chain ahead of the router filter. (Inserting after the router has no effect because the router has already dispatched the request to the upstream.)
- **`http_service` (not gRPC)** — Envoy talks to `perm-check` via plain HTTP `POST /check`. Simpler to debug than the gRPC `CheckRequest` protocol and sufficient for the demo.
- **`allowed_headers`** — Envoy is told explicitly which inbound headers to forward to `perm-check`. The demo forwards `x-user` and `authorization`. Without an allow-list, Envoy forwards none.
- **`failure_mode_allow: false`** — if `perm-check` is unreachable or returns a 5xx, Envoy fails the request (returns 403 to the client). This is the production-correct default and lets the demo verify fail-closed behavior by scaling `perm-check` to zero.

The complete EnvoyFilter resource is included in §4.4.

**Why pure EnvoyFilter, not mesh-level `extensionProvider`:** the demo is intentionally scoped to the path that does **not** require platform-team access to `istio-system` or the `istio` ConfigMap. The full file lives in the `app2` namespace and is owned end-to-end by the app team. The mesh-level pattern is the production recommendation for new clusters (see §9 — Future Enhancements), but adding it here would double the moving parts without changing what the demo verifies.

### 3.4 Why `app1` has no Istio sidecar

`app1` exists only to send HTTP requests with controllable headers. It does not need to authorize anything, present an identity, or participate in the mesh. A plain pod running `curl` in a `while` loop is the smallest possible test driver. Omitting the sidecar from `app1`:

- Saves a moving part (no Envoy in `app1`).
- Makes it unambiguous that the ext_authz check happens **at the receiving side** (`app2`'s sidecar), which is the actual production model — the source's identity is asserted in headers, not in mesh-mTLS, in this demo.
- Means Istio identity-based authorization rules (`from.source.namespaces`, `principals`) cannot be exercised. This is acceptable because the demo's only authorization input is the `x-user` header carried by the request.

## 4. Component Specifications

### 4.1 `app1` — request driver

| Field | Value |
|---|---|
| Path on disk | None — defined entirely in `kind/manifests/app1-deployment.yaml` |
| Image | `curlimages/curl:8.10` (off-the-shelf, pulled at runtime) |
| Listen port | None (pod is a client, not a server) |
| Container command | `sh -c 'while true; do curl -s -o /dev/stdout -w "%{http_code}\n" -H "x-user: alice" http://app2.app2.svc.cluster.local:8080/hello; sleep 3; curl -s -o /dev/stdout -w "%{http_code}\n" -H "x-user: mallory" http://app2.app2.svc.cluster.local:8080/hello; sleep 3; done'` |
| Sidecar | None (`app1` namespace has no `istio-injection` label) |
| Estimated LOC | 0 |

### 4.2 `app2` — echo HTTP server

| Field | Value |
|---|---|
| Path on disk | `sample-apps/app2/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/app2:dev` (locally built; loaded via `kind load docker-image`) |
| Listen port | 8080 |
| Endpoints | `GET /hello` → `200 "hello from app2"` |
| Logging | `log/slog` JSON; one line per request including `x-user` header |
| Sidecar | Envoy (auto-injected by Istio because `app2` namespace has `istio-injection=enabled`) |
| Pod container name | `app2` (referenced explicitly by verification commands; set in `app2-deployment.yaml` `containers[0].name`) |
| Estimated LOC | ~25 |

The Go binary is intentionally minimal — a single Gin handler. `app2` has no knowledge of `ext_authz` and no awareness that some incoming requests are being denied upstream by its own sidecar.

### 4.3 `perm-check` — decision service

| Field | Value |
|---|---|
| Path on disk | `sample-apps/perm-check/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/perm-check:dev` (locally built; loaded via `kind load docker-image`) |
| Listen port | 8080 |
| Endpoints | `POST /check` |
| Decision policy | Hard-coded allow-list `{"alice", "bob"}`. If the request's `x-user` header value is in the list → `200 OK`. Otherwise → `403 Forbidden`. If header is missing → `403`. |
| Logging | `log/slog` JSON; one line per decision (`{"user":"...","decision":"allow|deny","ts":"..."}`) |
| Sidecar | None (`perm-check` namespace has no `istio-injection`) |
| Estimated LOC | ~40 |

Response body is intentionally empty — `ext_authz` looks only at the HTTP status code by default. Envoy maps `200..299` → allow; everything else → deny.

### 4.4 EnvoyFilter

The full resource, applied verbatim to `app2` namespace:

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: app2-ext-authz
  namespace: app2
spec:
  workloadSelector:
    labels:
      app: app2
  configPatches:
  - applyTo: HTTP_FILTER
    match:
      context: SIDECAR_INBOUND
      listener:
        filterChain:
          filter:
            name: envoy.filters.network.http_connection_manager
    patch:
      operation: INSERT_BEFORE
      value:
        name: envoy.filters.http.ext_authz
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
          transport_api_version: V3
          http_service:
            server_uri:
              uri: http://perm-check.perm-check.svc.cluster.local:8080
              cluster: outbound|8080||perm-check.perm-check.svc.cluster.local
              timeout: 1s
            path_prefix: /check
            authorization_request:
              allowed_headers:
                patterns:
                - exact: x-user
                - exact: authorization
          failure_mode_allow: false
```

### 4.5 Istio Install Surface

| Component | Source | Namespace |
|---|---|---|
| istio-base | `helm install --repo https://istio-release.storage.googleapis.com/charts istio-base` (chart `base`, version `1.24.2`) | `istio-system` |
| istiod | same repo, chart `istiod`, version `1.24.2` | `istio-system` |
| Per-namespace ingressgateway | Not installed | — |

If offline operation is required later, the vendored-`.tgz` approach from the ECK harness can be adopted (see §9).

## 5. Bring-up Flow (`kind/setup.sh`)

The script is idempotent — re-runs pick up where they left off. Steps in order:

1. **Create kind cluster.** If `kind get clusters` already contains `ext-authz-demo`, reuse; else `kind create cluster --name ext-authz-demo --config kind-config.yaml`. Switch kubectl context to `kind-ext-authz-demo`.
2. **Install Istio control plane.**
   - `helm upgrade --install istio-base istio/base --repo https://istio-release.storage.googleapis.com/charts --version 1.24.2 -n istio-system --create-namespace --wait`
   - `helm upgrade --install istiod istio/istiod --repo https://istio-release.storage.googleapis.com/charts --version 1.24.2 -n istio-system --wait`
3. **Create namespaces.** Apply `kind/manifests/namespace-app1.yaml`, `namespace-app2.yaml`, `namespace-perm-check.yaml`. The `app2` namespace carries `istio-injection: enabled`; the others do not.
4. **Build local images.** For each of `sample-apps/app2` and `sample-apps/perm-check`, run `(cd sample-apps/<name> && docker build -t workspace/<name>:dev -f deploy/Dockerfile .)`. The build context is the app's own directory because each app carries its own `go.mod`.
5. **Load images into kind.** `kind load docker-image workspace/app2:dev --name ext-authz-demo` and the same for `perm-check`.
6. **Deploy `perm-check`.** Apply Deployment + Service in `perm-check` namespace. Wait for `Available` condition.
7. **Deploy `app2`.** Apply Deployment + Service in `app2` namespace. Wait for `Available` condition.
8. **Apply EnvoyFilter.** Apply `kind/manifests/app2-envoyfilter.yaml` in `app2` namespace. (Order: must come after `app2` Deployment so the patch lands on the live sidecar at next config push; the order does not matter functionally because istiod re-applies the filter list whenever pods restart.)
9. **Deploy `app1`.** Apply Deployment in `app1` namespace.
10. **Print verification commands** (see §6).

Total wall-clock on a warm Docker cache should be ≤ 3 minutes on a 16 GB MacBook.

`kind/teardown.sh` is one line: `kind delete cluster --name ext-authz-demo`.

## 6. Verification

The setup script prints these commands at the end.

**The headline demo — alternating allow/deny:**

```bash
kubectl -n app1 logs deploy/app1 -f
# Expected (3-second interval, alternating):
#   200
#   hello from app2
#   403
#   200
#   hello from app2
#   403
```

**perm-check sees both decisions:**

```bash
kubectl -n perm-check logs deploy/perm-check -f
# Expected:
#   {"user":"alice","decision":"allow","ts":"..."}
#   {"user":"mallory","decision":"deny","ts":"..."}
```

**Envoy sidecar is present and EnvoyFilter is applied:**

```bash
kubectl -n app2 get pod -l app=app2 -o jsonpath='{.items[0].spec.containers[*].name}'
# Expected: "app2 istio-proxy"   (two containers in one pod)

kubectl -n app2 get envoyfilter app2-ext-authz
# Expected: returns a row showing the resource
```

**The headline observable property — denials happen at Envoy, not in `app2`:**

```bash
kubectl -n app2 logs deploy/app2 -c app2 | grep -c mallory
# Expected: 0

kubectl -n app2 logs deploy/app2 -c app2 | grep -c alice
# Expected: > 0
```

The `app2` container never sees `mallory`'s requests. The deny decision is enforced by the sidecar.

**Fail-closed proof:**

```bash
kubectl -n perm-check scale deploy/perm-check --replicas=0
sleep 5
kubectl -n app1 logs deploy/app1 --tail=10
# Expected: every status code in the last 10 lines is 403, regardless of user

kubectl -n perm-check scale deploy/perm-check --replicas=1
# Wait for rollout; after that, alice's requests return 200 again
```

## 7. Directory Layout

```text
~/ashwini-repos/workspace/
├── docs/superpowers/specs/
│   ├── 2026-05-13-workspace-kind-harness-design.md          (existing — unchanged)
│   ├── 2026-05-13-permission-validation-phase1-sidecar-design.md  (existing — unchanged)
│   └── 2026-05-14-ext-authz-kind-demo-design.md             (this document)
├── sample-apps/
│   ├── app2/
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── deploy/Dockerfile
│   └── perm-check/
│       ├── main.go
│       ├── go.mod
│       └── deploy/Dockerfile
└── kind/
    ├── README.md
    ├── kind-config.yaml
    ├── setup.sh
    ├── teardown.sh
    └── manifests/
        ├── namespace-app1.yaml
        ├── namespace-app2.yaml
        ├── namespace-perm-check.yaml
        ├── app1-deployment.yaml
        ├── app2-deployment.yaml
        ├── app2-service.yaml
        ├── app2-envoyfilter.yaml
        ├── perm-check-deployment.yaml
        └── perm-check-service.yaml
```

The directory layout mirrors the ECK harness's `charts/elasticsearch/kind/` skeleton at a smaller scale — plain-YAML manifests under `manifests/`, a single shell entry point, and a sibling `sample-apps/` tree for the locally built images.

## 8. Trade-offs and Risks

### 8.1 EnvoyFilter is the "old" pattern

Istio documentation recommends `AuthorizationPolicy` with `action: CUSTOM` and a registered `extensionProvider` for new clusters. `EnvoyFilter` patches raw Envoy config and is brittle to Envoy version changes. The demo chooses `EnvoyFilter` deliberately to satisfy the constraint "no mesh-config touch" — the trade-off is accepted because the demo is small, easy to revisit, and serves the teaching goal of showing the lowest-level wiring path. Migration to the mesh-level pattern is straightforward (see §9.1).

### 8.2 Hard-coded allow-list in `perm-check`

The decision policy lives in Go code, not in a config file or database. This is fine for a demo where the policy is two usernames. A real policy service would consult a registry. The demo's `POST /check` contract is identical to what a production service would expose, so the dummy is a drop-in replacement target.

### 8.3 No identity for `app1`

`app1` has no Istio sidecar and no mTLS identity. The demo's authorization input is the `x-user` request header, trusted at face value. In production this header would either be set by an authenticated upstream (an API gateway, a frontend after OIDC) or derived from a verified token. The demo's `perm-check` accepts the header as a stand-in for "who the caller claims to be."

### 8.4 Single-replica everything

`app2` and `perm-check` each run one replica. Multi-replica routing, EnvoyFilter propagation under churn, and pod-restart behavior are not exercised. This is acceptable for a correctness demo; load and HA characteristics are out of scope per §2.2.

### 8.5 Headers, not body, drive the decision

Envoy `ext_authz` forwards request **headers** to the authz service by default. The `authorization_request.allowed_headers` allow-list determines what `perm-check` sees. Body-based decisions are possible (`with_request_body`) but add buffering, latency, and configuration weight. The demo deliberately restricts the decision input to headers — sufficient to demonstrate the flow.

## 9. Future Enhancements (explicitly not in scope)

These are listed so the design surface stays clear and a follow-up spec can pick any of them up.

### 9.1 Migrate to mesh-level `extensionProvider` + `AuthorizationPolicy`

Replace the per-namespace `EnvoyFilter` with:

- An `extensionProvider` entry in `meshConfig` (registered at Istio install time via Helm values or by editing the `istio` ConfigMap in `istio-system`).
- An `AuthorizationPolicy` in `app2` namespace with `action: CUSTOM`, `provider.name` matching the registered provider, and a workload `selector` matching `app: app2`.

The `perm-check` service contract is unchanged. The migration is a YAML swap with no app code changes.

### 9.2 gRPC ext_authz protocol

Switch from HTTP `POST /check` to Envoy's gRPC `CheckRequest`. Production deployments commonly prefer gRPC for typed contracts and lower per-request overhead. The `perm-check` service would gain a gRPC server and be tested with a separate verification path.

### 9.3 Vendored Istio charts

Adopt the ECK harness's pattern: download `.tgz`s of `istio-base` and `istiod` into `kind/charts/` and reference them locally from `setup.sh`. Removes the runtime dependency on `istio-release.storage.googleapis.com`. Useful when working offline or behind restrictive proxies.

### 9.4 Cluster-level baseline policies

Layer `allow-ip`, `allow-metrics`, and `allow-specific-namespace` `AuthorizationPolicy` resources at the mesh layer to demonstrate defense-in-depth alongside ext_authz. Useful when teaching how Istio's L4/L7 mesh authorization composes with delegated ext_authz checks.

### 9.5 Real auth (JWT) in front of `x-user`

Add a `RequestAuthentication` resource on `app2` requiring a signed JWT, and have the `ext_authz` decision derive identity from the JWT claims instead of a raw `x-user` header.

## 10. Open Questions

1. **Vendored vs pulled Istio charts.** Default is internet pull. Switching to vendored is a small `setup.sh` change plus committing two `.tgz`s. If offline operation matters during the demo, vendor.
2. **`x-user` header naming.** The demo uses `x-user` (lowercase). Production may prefer `X-Workspace-User-Id` or similar. The constant lives in three places (app1's curl command, perm-check's handler, EnvoyFilter's `allowed_headers`); changing it is a sed-job but worth confirming before implementation.
3. **Demo cluster name.** The script defaults to `ext-authz-demo`. If another demo already uses that name on the same machine, `setup.sh` will reuse the existing cluster rather than recreating. Acceptable; `teardown.sh` removes it cleanly.

## 11. Success Criteria

The harness is considered successful when, on a fresh checkout of `~/ashwini-repos/workspace/`:

1. `./kind/setup.sh` runs to completion in ≤ 3 minutes on a 16 GB MacBook with Docker Desktop allocated ≥ 6 GB.
2. `kubectl -n app1 logs deploy/app1 -f` shows alternating `200 hello from app2` (alice) and `403` (mallory) within 30 seconds of setup completion.
3. `kubectl -n perm-check logs deploy/perm-check -f` shows one allow line per alice request and one deny line per mallory request, JSON-formatted.
4. `kubectl -n app2 logs deploy/app2 -c app2 | grep -c mallory` returns `0`; the equivalent grep for `alice` returns a positive number.
5. `kubectl -n perm-check scale deploy/perm-check --replicas=0` causes all subsequent app1 calls to return `403`; restoring the replica brings allow decisions back.
6. `./kind/teardown.sh` removes the cluster cleanly.

The harness deliberately does not target throughput, latency, or any production-grade SLO. It is a correctness and teaching harness for the Envoy ext_authz wiring.

## 12. References

- Envoy `ext_authz` HTTP filter reference: <https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_authz_filter>
- Istio `EnvoyFilter` reference: <https://istio.io/latest/docs/reference/config/networking/envoy-filter/>
- Istio `AuthorizationPolicy` reference (for the §9.1 future migration): <https://istio.io/latest/docs/reference/config/security/authorization-policy/>
- ECK Elasticsearch kind harness — structural template: `charts/elasticsearch/kind/` on branch `claude/eck-elasticsearch-setup-VCAsc` of `hmchangw/chat`.
- Sibling spec: `2026-05-13-permission-validation-phase1-sidecar-design.md` — the broader Phase 1 sidecar design this demo exercises.
