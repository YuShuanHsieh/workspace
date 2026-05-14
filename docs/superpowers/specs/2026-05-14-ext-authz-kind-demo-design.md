# Ext-Authz Kind Demo — Minimal Harness Design

> **Status:** Draft for review
> **Audience:** Platform team members learning the Envoy `ext_authz` pattern; app teams onboarding to the Permission Checking Service.
> **Owner:** Ashwini (platform)
> **Related documents:**
> - [`2026-05-13-workspace-kind-harness-design.md`](2026-05-13-workspace-kind-harness-design.md) — broader workspace platform kind harness (NATS-based; this demo is intentionally narrower and unrelated to that NATS flow).
> - [`2026-05-13-permission-validation-phase1-sidecar-design.md`](2026-05-13-permission-validation-phase1-sidecar-design.md) — Phase 1 Permission Validation sidecar implementation (Envoy `ext_authz` is the deployment shape this demo exercises end-to-end).
> **Reference implementation pattern:** The ECK Elasticsearch kind harness on the `claude/eck-elasticsearch-setup-VCAsc` branch of the `hmchangw/chat` repository at `charts/elasticsearch/kind/` — structural model (vendored chart layout, single `setup.sh`, plain manifests, per-namespace ingressgateway).

---

## 1. Background and Motivation

The Permission Validation Phase 1 design (sibling spec) describes a per-request authorization gate that sits in front of application services. Workloads onboard their services to that gate, and the gate calls a **Permission Checking Service (PCS)** for an allow/deny decision on every inbound request. In this team's architecture the **main product team (documents) owns and operates PCS as part of their product offering** — it is deployed in the `documents` namespace alongside the documents product's own workloads, not in a separate platform-owned namespace. Other product teams (e.g. wiki) that want to plug into the same authorization decision call the documents-owned PCS across namespace boundaries via cluster DNS. Before this flow is wired into production app charts, Ashwini needs a local, single-file-to-run demo that proves the wiring end-to-end against the team's current production cluster conventions.

The demo is split into two **stages** that match a realistic rollout:

- **Stage 1 (this demo's scope).** Each app namespace that wants the gate carries **its own copy** of the `EnvoyFilter` resource. The filter inside each namespace uses a `workloadSelector` keyed on an opt-in Pod label (`workspace.io/ext-authz: enabled`). This matches the team's existing pattern of per-namespace Istio filters such as the `gateway-connection-limit` EnvoyFilter in the `chat` namespace. **No writes to `istio-system` are required**, which is important because the product team may not have access to that namespace in production.
- **Stage 2 (future, documented in §8).** After the platform team agrees to host a shared filter, the per-namespace `EnvoyFilter` copies collapse into one resource in `istio-system`. The opt-in label stays exactly the same. App teams' Deployment YAML does not change. The migration is YAML-only.

Both stages share the same PCS contract, the same per-namespace ingressgateway pattern, the same opt-in label, and the same `kind/setup.sh` skeleton. Stage 2 is a contained YAML migration, not a redesign.

The Stage 1 demo exercises **three onboarding cases** end-to-end so the pattern is provable for every realistic team setup:

1. **The product's own workload** — `documents-api` in the product's namespace, with the opt-in label.
2. **A sister workload in the same namespace** — `documents-search` in the same `documents` namespace, also carrying the opt-in label. Same EnvoyFilter (in `documents` ns) matches it via the label, no copy needed within the namespace.
3. **A workload in a different namespace** — `wiki-api` in `wiki` namespace. The wiki team **copies** the `documents-ext-authz` EnvoyFilter shape into their own namespace as `wiki-ext-authz`, with the same `workloadSelector` label, and `wiki-api` carries the opt-in label.

These three cases are what Stage 2 would unify under a single `istio-system` EnvoyFilter. Demonstrating them in Stage 1 makes the upgrade story concrete.

The ECK Elasticsearch kind harness is the structural template — vendored Istio charts, a single idempotent `setup.sh`, plain-YAML manifests, a clear verification path.

## 2. Goal and Non-Goals

### 2.1 Goal (Stage 1)

Stand up a single-node `kind` cluster that demonstrates per-request authorization via Envoy's `ext_authz` HTTP filter, with the filter wired via **per-namespace `EnvoyFilter` resources scoped by an opt-in label** (`workspace.io/ext-authz: enabled`). The cluster mirrors the production deployment posture: every namespace involved has Istio sidecar injection enabled, and external traffic enters through a per-namespace ingressgateway in the product namespace.

Concretely the harness must produce these observable outcomes on a fresh checkout, in any order:

1. `documents-api` runs in namespace `documents` with sidecar injection enabled and label `workspace.io/ext-authz: enabled` on its Pod template. The `documents-ext-authz` EnvoyFilter in the same `documents` namespace matches this label and patches the Pod's sidecar with the `ext_authz` filter.
2. `documents-search` runs in the same `documents` namespace, also carrying `workspace.io/ext-authz: enabled`. The same `documents-ext-authz` EnvoyFilter (no second filter needed) patches it via the shared label.
3. `wiki-api` runs in namespace `wiki` with sidecar injection enabled and the same opt-in label. A **copied** EnvoyFilter (`wiki-ext-authz`) in the `wiki` namespace matches this label and patches its sidecar. The copy is identical to `documents-ext-authz` except for `metadata.namespace` and `metadata.name`.
4. `pcs` runs in the **same `documents` namespace** as the documents product, owned by the documents team. It is sidecar-injected and returns `200` for users in its allow-list, `403` otherwise. Both `documents-ext-authz` and `wiki-ext-authz` point at `pcs.documents.svc.cluster.local:8080` — the same PCS service.
5. `dashboard-client` runs in the `documents` namespace and issues calls in a loop to all three protected workloads (`documents-api`, `documents-search`, `wiki-api`) with alternating `x-workspace-user-id` headers (`alice@workspace.test` and `mallory@workspace.test`). Its log shows the pattern of `200`s and `403`s.
6. Each protected workload's app container sees **only `alice`'s traffic**; `mallory`'s requests never reach the app container — the deny decision is enforced in the workload's own Envoy sidecar.
7. `pcs` logs show three allow + three deny decisions per dashboard-client iteration cycle.
8. The same allow/deny behaviour is observable from outside the cluster via the per-namespace ingressgateway: `curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local/hello` returns `200`; the same call with `mallory@workspace.test` returns `403`.
9. Scaling `pcs` to zero causes all gated traffic to receive `403` (fail-closed); restoring the replica brings allow decisions back.
10. `kubectl get envoyfilter -A` shows exactly **two** EnvoyFilters — `documents-ext-authz` in `documents` namespace and `wiki-ext-authz` in `wiki` namespace. **Nothing in `istio-system`** (proving the Stage 1 no-istio-system-writes property).

### 2.2 Non-Goals (Stage 1)

| Capability | Status | Reason |
|---|---|---|
| Mesh-wide `EnvoyFilter` in `istio-system` with label opt-in | **Out — moved to Stage 2 (§8)** | Requires platform-team approval and `istio-system` write access. Documented as the target evolution but not implemented in this demo. |
| `extensionProvider` + `AuthorizationPolicy` (action: CUSTOM) wiring | Out | Documented as a future migration (§10.7). |
| HTTPS / TLS termination on the ingressgateway | Out | Plain HTTP keeps the curl-from-host verification simple. TLS is a small follow-up (§10.5). |
| Real auth tokens (JWT, OIDC) | Out | Header-based allow/deny is sufficient. |
| Dynamic policy registry / database for `pcs` | Out | A hard-coded in-process allow-list of usernames is sufficient. |
| Cluster-level baseline `AuthorizationPolicy` resources | Out | Orthogonal concern; would dilute the headline claim. |
| Performance / latency / load testing | Out | Correctness harness, not a benchmark. |
| Explicit mTLS verification | Out | All namespaces are injected, so mesh-mTLS happens by default; not separately asserted. |
| Multi-cluster / cross-cluster federation | Out | Single kind cluster only. |
| Helm chart for the demo workloads | Out | Few resources, plain YAML stays. |
| Mutating admission webhook for label auto-injection | Out | Post-Stage-2; tracked under §10.6. |
| Explicit opt-out negative example | Out | Opt-out is implicit (a Pod without the label is unpatched). A dedicated opted-out workload would add a namespace and a Deployment that the headline claims do not depend on. Cross-namespace copy pattern (case 3 above) is enough for readers to see the boundary. |

## 3. Architecture

### 3.1 Cluster Layout

A single-node `kind` cluster with `extraPortMappings` exposing per-namespace ingressgateways on host ports. Two app-relevant namespaces (`documents`, `wiki`) plus the standard `istio-system`. **No separate `pcs` namespace** — PCS is part of the documents product and lives in `documents` ns.

```text
                       ┌──────────────────────────────────────────────────────────┐
                       │                kind cluster (single node)                │
   curl from host ─80──┤                                                          │
                       │  ns: istio-system            (Istio control plane)       │
                       │    istio-base                                            │
                       │    istiod                                                │
                       │    (NO EnvoyFilter — Stage 1 does not write here)        │
                       │                                                          │
                       │  ns: documents               (injection: enabled)        │
                       │    documents-ingressgateway Pod                          │
                       │      └─ istio-proxy (NodePort 30080 → host 80)           │
                       │                                                          │
                       │    documents-api Pod   labels: {workspace.io/ext-authz=  │
                       │                                  enabled, app=docs-api}  │
                       │      ├─ documents-api (Go echo server)                   │
                       │      └─ istio-proxy ← patched by documents-ext-authz     │
                       │                                                          │
                       │    documents-search Pod   labels: {workspace.io/ext-     │
                       │                                  authz=enabled, app=docs-search}│
                       │      ├─ documents-search (same Go echo server image)     │
                       │      └─ istio-proxy ← patched by documents-ext-authz     │
                       │                                                          │
                       │    dashboard-client Pod  (no opt-in label — caller only) │
                       │      ├─ dashboard-client (Go HTTP loop)                  │
                       │      └─ istio-proxy (sidecar)                            │
                       │                                                          │
                       │    pcs Pod   (no opt-in label — it IS the decisioner)    │
                       │      ├─ pcs (Go decision server)                         │
                       │      └─ istio-proxy (sidecar)                            │
                       │                                                          │
                       │    EnvoyFilter "documents-ext-authz"                     │
                       │      workloadSelector: { workspace.io/ext-authz: enabled}│
                       │      → matches both docs-api and docs-search             │
                       │      → calls pcs.documents.svc:8080                      │
                       │                                                          │
                       │    Gateway "documents-gateway" (host: documents.local)   │
                       │    VirtualService "documents-vs"                         │
                       │    Services: "documents-api", "documents-search", "pcs"  │
                       │                                                          │
                       │  ns: wiki                    (injection: enabled)        │
                       │    wiki-ingressgateway Pod                               │
                       │      └─ istio-proxy (NodePort 30081 → host 8081)         │
                       │                                                          │
                       │    wiki-api Pod   labels: {workspace.io/ext-authz=       │
                       │                            enabled, app=wiki-api}        │
                       │      ├─ wiki-api (same Go echo server image)             │
                       │      └─ istio-proxy ← patched by wiki-ext-authz          │
                       │                                                          │
                       │    EnvoyFilter "wiki-ext-authz"                          │
                       │      workloadSelector: { workspace.io/ext-authz: enabled}│
                       │      → matches wiki-api                                  │
                       │      → calls pcs.documents.svc:8080 (cross-namespace,    │
                       │        the documents team's PCS)                         │
                       │      (identical config to documents-ext-authz except     │
                       │       for metadata.namespace and metadata.name —         │
                       │       this is the Stage 1 cross-namespace copy)          │
                       │                                                          │
                       │    Gateway "wiki-gateway" (host: wiki.local)             │
                       │    VirtualService "wiki-vs"                              │
                       │    Service "wiki-api"                                    │
   curl from host ─8081┤                                                          │
                       └──────────────────────────────────────────────────────────┘
```

**Host port mappings** (`extraPortMappings` in `kind-config.yaml`):

| Host port | Container port | Purpose |
|---|---|---|
| 80 | 30080 | Plain HTTP to `documents-ingressgateway` (main product) |
| 8081 | 30081 | Plain HTTP to `wiki-ingressgateway` (cross-namespace example) |
| 443 | 30443 | HTTPS reserved for future TLS upgrade (see §10.5) |

**Required `/etc/hosts` additions** (printed at the end of `setup.sh`):

```text
127.0.0.1  documents.local wiki.local
```

Each app namespace has its own ingressgateway with a dedicated NodePort, mirroring the production convention where every app namespace operates its own ingress.

### 3.2 Ownership Split (Stage 1)

| Resource | Lives in | Production owner |
|---|---|---|
| `documents-api`, `documents-search`, `dashboard-client`, `documents-ingressgateway`, `documents-gateway`, `documents-vs`, `documents-ext-authz` EnvoyFilter, **`pcs` Deployment + Service**, all Services in `documents` ns | `documents` | **Documents product team** — owns everything in their own namespace, including the per-namespace ingressgateway, the EnvoyFilter that wires ext_authz into their workloads, **and PCS itself** (PCS is part of the documents product offering). |
| `wiki-api`, `wiki-ingressgateway`, `wiki-gateway`, `wiki-vs`, `wiki-ext-authz` EnvoyFilter, `wiki-api` Service | `wiki` | **Wiki team** — owns everything in their own namespace, including their own ingressgateway and a copied EnvoyFilter that follows the same shape as `documents-ext-authz`. The wiki team's `wiki-ext-authz` calls into the documents team's PCS via cluster DNS. |

The documents team does **not** write into `istio-system`. The wiki team does **not** write into `documents` or `istio-system`. Each team owns their own namespace end-to-end. **There is no separate platform-owned namespace in Stage 1** — the documents team plays the role of PCS host because PCS is part of their product.

`setup.sh` applies all of the above for demo convenience, but the manifests are organised by owner (see §7) so the boundary is visible.

### 3.3 Request Flow

`dashboard-client` exercises three protected workloads in a loop. Each gated path goes through the same shape:

```text
┌──────────────────┐  HTTP GET /hello                       ┌────────────────────────┐
│ dashboard-client │ ── x-workspace-user-id: alice@... ────►│  Workload's Envoy      │
│  (caller, in     │                                        │  sidecar — SIDECAR_    │
│   documents ns)  │                                        │  INBOUND ext_authz     │
└──────────────────┘                                        └─────────┬──────────────┘
                                                                      │ ext_authz HTTP
                                                                      ▼ POST /check
                                                            ┌────────────────────────┐
                                                            │ pcs.documents.svc:8080 │
                                                            │   alice → 200          │
                                                            │   mallory → 403        │
                                                            └─────────┬──────────────┘
                                                                      ▼
                                                            ┌────────────────────────┐
                                                            │ Workload's Envoy:      │
                                                            │   200 → forward        │
                                                            │   403 → reply 403      │
                                                            └─────────┬──────────────┘
                                                                      │ (allow case)
                                                                      ▼
                                                            ┌────────────────────────┐
                                                            │ Workload's app cont.   │
                                                            │  returns "hello from   │
                                                            │  $POD_NAME"            │
                                                            └────────────────────────┘
```

Per dashboard-client iteration:

| Step | Target | Header | Expected status |
|---|---|---|---|
| 1 | `documents-api.documents.svc.cluster.local:8080/hello` | `alice@workspace.test` | `200` |
| 2 | `documents-api.documents.svc.cluster.local:8080/hello` | `mallory@workspace.test` | `403` |
| 3 | `documents-search.documents.svc.cluster.local:8080/hello` | `alice@workspace.test` | `200` |
| 4 | `documents-search.documents.svc.cluster.local:8080/hello` | `mallory@workspace.test` | `403` |
| 5 | `wiki-api.wiki.svc.cluster.local:8080/hello` | `alice@workspace.test` | `200` |
| 6 | `wiki-api.wiki.svc.cluster.local:8080/hello` | `mallory@workspace.test` | `403` |

A 2-second sleep separates each call, giving a 12-second cycle.

**External paths** exist for both `documents-api` (via `documents-ingressgateway` on host port 80, hostname `documents.local`) and `wiki-api` (via `wiki-ingressgateway` on host port 8081, hostname `wiki.local`). `documents-search` stays internal-only (no Gateway/VirtualService) — it represents a sibling service that's only reachable from inside the cluster. In every case the ext_authz filter on the workload's own sidecar fires identically, regardless of whether the request came in via cluster DNS or via that namespace's ingressgateway.

### 3.4 ext_authz Wiring: per-namespace EnvoyFilter with label opt-in (Stage 1)

Each namespace that hosts opt-in workloads carries its own `EnvoyFilter` resource. The filter's `workloadSelector` targets the same label (`workspace.io/ext-authz: enabled`) — but because the filter lives in a non-root namespace, the match is restricted to Pods in that same namespace. Adding a new opt-in workload to a namespace that already has its EnvoyFilter is a one-line change (add the label); onboarding a new namespace requires copying the EnvoyFilter into it.

Key configuration decisions, common to both `documents-ext-authz` and `wiki-ext-authz`:

- **Resource lives in the team's own namespace** — never in `istio-system` for Stage 1. Cross-namespace reach is achieved by copying the file, not by relying on root-namespace mechanics.
- **`workloadSelector.labels: { workspace.io/ext-authz: enabled }`** — opt-in marker. Workloads inside the namespace either carry the label and get gated, or don't and are unaffected. The same label name is used across all namespaces for consistency with Stage 2.
- **`context: SIDECAR_INBOUND`** — the filter intercepts traffic entering each matched Pod, not outbound traffic.
- **`applyTo: HTTP_FILTER` + `operation: INSERT_BEFORE`** — the ext_authz filter is inserted into the HTTP filter chain ahead of the router filter. A `subFilter.name: envoy.filters.http.router` inside the `match.listener.filterChain.filter` block anchors the insertion point; without it, `INSERT_BEFORE` would place ext_authz at the front of the filter chain (before routing context is available), causing `path_prefix` forwarding to PCS to misbehave.
- **`http_service` (not gRPC)** — Envoy talks to `pcs` via plain HTTP `POST /check`. Both EnvoyFilter copies point at the same FQDN `pcs.documents.svc.cluster.local:8080`; the FQDN resolves identically from any namespace.
- **`allowed_headers`** — forwards `x-workspace-user-id` and `authorization` to `pcs`.
- **`failure_mode_allow: false`** — fail-closed.

The complete EnvoyFilter resources are in §4.6 (documents) and §4.7 (wiki). They are byte-for-byte identical except for `metadata.namespace` and `metadata.name`.

### 3.5 All-namespace Istio Injection

Both app namespaces (`documents`, `wiki`) carry the `istio-injection: enabled` label. This mirrors the production cluster's posture (every namespace defaults to sidecar-injected). Concrete consequences:

- Traffic between any two pods is mesh-mTLS (Istio `PERMISSIVE` mode). Falls out for free; not separately asserted.
- The `ext_authz` check happens **on the receiver side** regardless of the caller's identity. The decision input is the `x-workspace-user-id` header.
- The `pcs` Pod (in `documents` ns) has a sidecar; outbound calls from both `documents-ext-authz` (same-ns call) and `wiki-ext-authz` (cross-ns call) to `pcs.documents.svc.cluster.local:8080` are mesh-managed.

## 4. Component Specifications

### 4.1 `dashboard-client` — request driver (caller, no opt-in)

| Field | Value |
|---|---|
| Path on disk | `sample-apps/dashboard-client/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/dashboard-client:dev` |
| Listen port | None (client) |
| Behaviour | A `for` loop iterating over the 6 calls in §3.3 with 2-second sleeps; each call logs target URL + header + HTTP status via `log/slog` JSON. |
| Namespace | `documents` |
| Sidecar | Envoy (auto-injected) |
| Opt-in label | **Not set** — caller, not a receiver. |
| Pod container name | `dashboard-client` |
| Estimated LOC | ~60 |

### 4.2 `documents-api` — main product workload (opted in)

| Field | Value |
|---|---|
| Path on disk | `sample-apps/echo-server/` (shared image — see §4.4) |
| Image | `workspace/echo-server:dev` |
| Listen port | 8080 |
| Endpoints | `GET /hello` → `200 "hello from $POD_NAME"` |
| Logging | `log/slog` JSON; one line per request including `x-workspace-user-id` header |
| Namespace | `documents` |
| Sidecar | Envoy (auto-injected); patched by `documents-ext-authz` |
| Pod labels | `app: documents-api`, **`workspace.io/ext-authz: enabled`** |
| Pod container name | `documents-api` |
| `POD_NAME` env | Set from the downward API `metadata.name` so the response identifies the pod |

### 4.3 `documents-search` — sibling workload in same namespace (opted in)

| Field | Value |
|---|---|
| Image | `workspace/echo-server:dev` (same image as `documents-api`) |
| Listen port | 8080 |
| Endpoints | `GET /hello` → `200 "hello from $POD_NAME"` |
| Namespace | `documents` |
| Sidecar | Envoy (auto-injected); patched by `documents-ext-authz` via the shared label |
| Pod labels | `app: documents-search`, **`workspace.io/ext-authz: enabled`** |
| Pod container name | `documents-search` |

`documents-search` demonstrates that adding a second opted-in workload **inside the same namespace** requires only the opt-in label on the new Deployment — no new EnvoyFilter is needed.

### 4.4 `echo-server` (shared image for `documents-api`, `documents-search`, `wiki-api`)

| Field | Value |
|---|---|
| Path on disk | `sample-apps/echo-server/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/echo-server:dev` |
| Source code | Single Gin handler `GET /hello` → `200 "hello from $POD_NAME"`. Reads `POD_NAME` from env at startup. |
| Estimated LOC | ~30 |

One binary, three Deployments (`documents-api`, `documents-search`, `wiki-api`). Each Deployment names its container after itself; `POD_NAME` is set per Pod via the downward API.

### 4.5 `wiki-api` — workload in a different namespace (opted in)

| Field | Value |
|---|---|
| Image | `workspace/echo-server:dev` (same image as documents-*) |
| Listen port | 8080 |
| Endpoints | `GET /hello` → `200 "hello from $POD_NAME"` |
| Namespace | `wiki` |
| Sidecar | Envoy (auto-injected); patched by `wiki-ext-authz` (the copied EnvoyFilter in `wiki` namespace) |
| Pod labels | `app: wiki-api`, **`workspace.io/ext-authz: enabled`** |
| Pod container name | `wiki-api` |

`wiki-api` demonstrates the Stage 1 cross-namespace pattern: a team in a separate namespace copies the EnvoyFilter shape into their own namespace and applies the opt-in label to their workload.

### 4.6 EnvoyFilter `documents-ext-authz` (in `documents` namespace)

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: documents-ext-authz
  namespace: documents                              # ← product team's own namespace
spec:
  workloadSelector:
    labels:
      workspace.io/ext-authz: enabled               # ← opt-in label for documents ns
  configPatches:
  - applyTo: HTTP_FILTER
    match:
      context: SIDECAR_INBOUND
      listener:
        filterChain:
          filter:
            name: envoy.filters.network.http_connection_manager
            subFilter:
              name: envoy.filters.http.router
    patch:
      operation: INSERT_BEFORE
      value:
        name: envoy.filters.http.ext_authz
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
          transport_api_version: V3
          http_service:
            server_uri:
              uri: http://pcs.documents.svc.cluster.local:8080
              cluster: outbound|8080||pcs.documents.svc.cluster.local
              timeout: 1s
            path_prefix: /check
            authorization_request:
              allowed_headers:
                patterns:
                - exact: x-workspace-user-id
                - exact: authorization
          failure_mode_allow: false
```

Matches both `documents-api` and `documents-search` Pods via the shared `workspace.io/ext-authz: enabled` label.

### 4.7 EnvoyFilter `wiki-ext-authz` (in `wiki` namespace — the cross-namespace copy)

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: wiki-ext-authz
  namespace: wiki                                   # ← wiki team's own namespace
spec:
  workloadSelector:
    labels:
      workspace.io/ext-authz: enabled
  configPatches:
  - applyTo: HTTP_FILTER
    match:
      context: SIDECAR_INBOUND
      listener:
        filterChain:
          filter:
            name: envoy.filters.network.http_connection_manager
            subFilter:
              name: envoy.filters.http.router
    patch:
      operation: INSERT_BEFORE
      value:
        name: envoy.filters.http.ext_authz
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
          transport_api_version: V3
          http_service:
            server_uri:
              uri: http://pcs.documents.svc.cluster.local:8080      # same PCS as documents
              cluster: outbound|8080||pcs.documents.svc.cluster.local
              timeout: 1s
            path_prefix: /check
            authorization_request:
              allowed_headers:
                patterns:
                - exact: x-workspace-user-id
                - exact: authorization
          failure_mode_allow: false
```

This file is the documents-ext-authz copy with two field changes: `metadata.namespace: wiki` and `metadata.name: wiki-ext-authz`. Everything else is identical, including the PCS service URL. **This duplication is the Stage 1 cost — Stage 2 (§8) eliminates it by collapsing both copies into a single resource in `istio-system`.**

### 4.8 `pcs` — Permission Checking Service (owned by documents team)

| Field | Value |
|---|---|
| Path on disk | `sample-apps/pcs/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/pcs:dev` |
| Listen port | 8080 |
| Endpoints | `POST /check` (exact) + Gin `NoRoute` catch-all |
| Decision policy | Hard-coded allow-list `{"alice@workspace.test", "bob@workspace.test"}`. Header value in list → `200`. Otherwise → `403`. Missing header → `403`. Envoy's `path_prefix: /check` prepends `/check` to the original request path (e.g. a request for `/hello` becomes `POST /check/hello`), so PCS registers both an exact `POST /check` route and a `NoRoute` handler that routes all unmatched paths through the same `checkHandler` function. |
| Logging | `log/slog` JSON; one line per decision (`{"user":"...","decision":"allow|deny","ts":"..."}`) |
| Namespace | **`documents`** (PCS is part of the documents product offering — owned by the documents team, deployed in their namespace, reached from other namespaces via cluster DNS) |
| Sidecar | Envoy (auto-injected) |
| Opt-in label | **Not set** — PCS is the decision service itself; gating it would create a loop. |
| Pod container name | `pcs` |
| Service name + FQDN | `pcs` (in `documents` ns) → `pcs.documents.svc.cluster.local:8080` |
| Estimated LOC | ~40 |

Response body is intentionally empty — `ext_authz` looks only at the HTTP status code.

### 4.9 Per-namespace Ingressgateways

Every app namespace runs its own ingressgateway Pod (deployed via the `istio/gateway` Helm chart vendored under `kind/charts/`). Same shape per namespace; only the namespace, release name, pod label, and NodePort differ.

**`documents-ingressgateway`** (in `documents` namespace):

| Field | Value |
|---|---|
| Chart | `kind/charts/gateway-1.24.2.tgz` |
| Release name | `documents-ingressgateway` |
| Namespace | `documents` |
| Pod labels | `istio: documents-ingressgateway` |
| Service type | `NodePort` |
| Ports | `80 → nodePort 30080` |
| Resources | 50m / 128Mi CPU/memory request |

**`wiki-ingressgateway`** (in `wiki` namespace):

| Field | Value |
|---|---|
| Chart | `kind/charts/gateway-1.24.2.tgz` (same vendored file) |
| Release name | `wiki-ingressgateway` |
| Namespace | `wiki` |
| Pod labels | `istio: wiki-ingressgateway` |
| Service type | `NodePort` |
| Ports | `80 → nodePort 30081` |
| Resources | 50m / 128Mi CPU/memory request |

Neither gateway pod carries the `workspace.io/ext-authz: enabled` label, so the corresponding EnvoyFilters do **not** patch them. The ext_authz check fires one hop later on the actual receiver workload's sidecar. PCS itself (the `pcs` Pod in `documents` ns) is reached only via cluster DNS from the protected workloads' EnvoyFilters; it is not exposed via any gateway.

### 4.10 Gateway + VirtualService pairs

Each app namespace exposes its main protected workload via its own Gateway + VirtualService pair, attached to that namespace's ingressgateway.

**`documents` namespace — exposes `documents-api` on `documents.local`:**

```yaml
apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: documents-gateway
  namespace: documents
spec:
  selector:
    istio: documents-ingressgateway
  servers:
  - port: { number: 80, name: http, protocol: HTTP }
    hosts: [ documents.local ]
---
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: documents-vs
  namespace: documents
spec:
  hosts: [ documents.local ]
  gateways: [ documents-gateway ]
  http:
  - route:
    - destination:
        host: documents-api.documents.svc.cluster.local
        port: { number: 8080 }
```

**`wiki` namespace — exposes `wiki-api` on `wiki.local`:**

```yaml
apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: wiki-gateway
  namespace: wiki
spec:
  selector:
    istio: wiki-ingressgateway
  servers:
  - port: { number: 80, name: http, protocol: HTTP }
    hosts: [ wiki.local ]
---
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: wiki-vs
  namespace: wiki
spec:
  hosts: [ wiki.local ]
  gateways: [ wiki-gateway ]
  http:
  - route:
    - destination:
        host: wiki-api.wiki.svc.cluster.local
        port: { number: 8080 }
```

`documents-search` is **not** exposed externally — it represents a sibling internal-only service. A team could add a third Gateway/VirtualService for it inside `documents` namespace if they wanted, but it's not required to prove the headline claim.

### 4.11 Istio Install Surface

All Istio charts are vendored as `.tgz` under `kind/charts/`. `setup.sh` references the local files; nothing is pulled from the internet at runtime.

| Component | Vendored file | Helm release name | Namespace |
|---|---|---|---|
| istio-base | `kind/charts/base-1.24.2.tgz` | `istio-base` | `istio-system` |
| istiod | `kind/charts/istiod-1.24.2.tgz` | `istiod` | `istio-system` |
| Documents ingressgateway | `kind/charts/gateway-1.24.2.tgz` | `documents-ingressgateway` | `documents` |
| Wiki ingressgateway | `kind/charts/gateway-1.24.2.tgz` (same file, second release) | `wiki-ingressgateway` | `wiki` |

The `.tgz` files are downloaded once during initial repo setup (`helm pull --repo https://istio-release.storage.googleapis.com/charts --version 1.24.2 base istiod gateway --destination kind/charts/`) and committed to the repo.

### 4.12 Umbrella chart (`kind/demo/`)

After the initial implementation (Tasks 1-32 in the plan), all 12 app-side Kubernetes resources were consolidated into a thin umbrella Helm chart at `kind/demo/`. The chart owns everything except Istio itself.

**Single registry/tag override point.** Edit `kind/demo/values.yaml` — the `images.*` block controls the repository and tag for `echo-server`, `pcs`, and `dashboard-client`. For a company-internal private registry, change these three entries and re-run `./kind/setup.sh`; no other files need editing.

**No subchart dependencies.** The chart's `Chart.yaml` declares no `dependencies:` block. Istio (`istio-base`, `istiod`) and the two per-namespace ingressgateways are installed by `setup.sh` as separate top-level Helm releases. This is the shape Istio's own install docs recommend; bundling them as subcharts causes resource-ownership conflicts between `istio-base` and `istiod` and prevents per-namespace gateway installs from working. See `kind/demo/README.md` for the full rationale.

**Other tunables in `values.yaml`:** resource sizing for app pods and sidecars, the opt-in label key/value, the PCS service FQDN/port/path, and gateway NodePort/hostname settings.

## 5. Bring-up Flow (`kind/setup.sh`)

Idempotent — re-runs pick up where they left off. Steps grouped by owner:

> **Post-implementation note:** After Tasks 1-32, all app k8s resources were consolidated into the umbrella chart at `kind/demo/` (see §4.12). The current `setup.sh` installs Istio as separate releases and then runs `helm upgrade --install demo kind/demo/ -n istio-system` for the app side. The phase groupings below reflect the logical ownership split; the actual commands are in `kind/setup.sh`.

### Phase A — Cluster bootstrap + image build/load

1. **Create kind cluster.** If `kind get clusters` already contains `ext-authz-demo`, reuse; else `kind create cluster --name ext-authz-demo --config kind-config.yaml`. Switch kubectl context to `kind-ext-authz-demo`.
2. **Build local images.** From the repo root:
   - `(cd sample-apps/echo-server && docker build -t workspace/echo-server:dev -f deploy/Dockerfile .)`
   - `(cd sample-apps/pcs && docker build -t workspace/pcs:dev -f deploy/Dockerfile .)`
   - `(cd sample-apps/dashboard-client && docker build -t workspace/dashboard-client:dev -f deploy/Dockerfile .)`
3. **Load images into kind.**
   - `kind load docker-image workspace/echo-server:dev --name ext-authz-demo`
   - `kind load docker-image workspace/pcs:dev --name ext-authz-demo`
   - `kind load docker-image workspace/dashboard-client:dev --name ext-authz-demo`

### Phase B — Istio control plane + umbrella chart for app resources

4. **Install Istio control plane from vendored charts (separate releases — see §4.12 for why).**
   - `helm upgrade --install istio-base kind/charts/base-1.24.2.tgz -n istio-system --create-namespace --wait`
   - `helm upgrade --install istiod kind/charts/istiod-1.24.2.tgz -n istio-system --wait`
5. **Install the umbrella chart for all app resources.** The chart creates both namespaces, deploys `pcs`, `documents-api`, `documents-search`, `dashboard-client`, `wiki-api`, both EnvoyFilters, both Gateway+VirtualService pairs, and all Services.
   - `helm upgrade --install demo kind/demo/ -n istio-system --wait`
6. **Wait for app Deployments to be `Available`** (pcs, documents-api, documents-search, dashboard-client, wiki-api).

### Phase C — Per-namespace ingressgateways (separate Helm releases)

7. **Install the per-namespace ingressgateways from the vendored chart.** Installed as separate releases because a single Helm release cannot span namespaces.
   - `helm upgrade --install documents-ingressgateway kind/charts/gateway-1.24.2.tgz -n documents --skip-schema-validation --wait` (with NodePort 30080 settings)
   - `helm upgrade --install wiki-ingressgateway kind/charts/gateway-1.24.2.tgz -n wiki --skip-schema-validation --wait` (with NodePort 30081 settings)
8. **Print verification commands and `/etc/hosts` lines** (see §6).

Total wall-clock on a warm Docker cache should be ≤ 3 minutes on a 16 GB MacBook.

`kind/teardown.sh` is one line: `kind delete cluster --name ext-authz-demo`.

## 6. Verification

The setup script prints these commands at the end, plus a reminder to add `127.0.0.1 documents.local` to `/etc/hosts`.

**Internal-path demo — dashboard-client cycling through all three workloads:**

```bash
kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f
# Expected (rolling, 2s between calls):
#   documents-api    alice@workspace.test   → 200  "hello from documents-api-..."
#   documents-api    mallory@workspace.test → 403
#   documents-search alice@workspace.test   → 200  "hello from documents-search-..."
#   documents-search mallory@workspace.test → 403
#   wiki-api         alice@workspace.test   → 200  "hello from wiki-api-..."
#   wiki-api         mallory@workspace.test → 403
```

**External-path demo — curl from host through each namespace's own ingressgateway:**

```bash
# Via documents-ingressgateway (host port 80)
curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local/hello       # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local/hello       # 403

# Via wiki-ingressgateway (host port 8081)
curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello       # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello       # 403
```

**PCS (in `documents` ns) sees all three workloads' decisions:**

```bash
kubectl -n documents logs deploy/pcs -c pcs -f
# Expected (rolling, 3 allow + 3 deny per dashboard-client cycle —
# one pair from documents-api, one pair from documents-search, one pair from wiki-api):
#   {"user":"alice@workspace.test","decision":"allow",...}
#   {"user":"mallory@workspace.test","decision":"deny",...}
```

**EnvoyFilters are in app namespaces, NOT in `istio-system`:**

```bash
kubectl get envoyfilter -A
# Expected: exactly two rows —
#   documents   documents-ext-authz   ...
#   wiki        wiki-ext-authz        ...
# istio-system: nothing.
```

**Both EnvoyFilters share the same `workloadSelector` label:**

```bash
kubectl -n documents get envoyfilter documents-ext-authz -o jsonpath='{.spec.workloadSelector.labels}'
# Expected: {"workspace.io/ext-authz":"enabled"}
kubectl -n wiki get envoyfilter wiki-ext-authz -o jsonpath='{.spec.workloadSelector.labels}'
# Expected: {"workspace.io/ext-authz":"enabled"}
```

**Headline observable property — denials happen at Envoy, not in app containers:**

```bash
for tuple in "documents documents-api" "documents documents-search" "wiki wiki-api"; do
  ns=$(echo $tuple | awk '{print $1}'); app=$(echo $tuple | awk '{print $2}')
  echo "=== $app (ns=$ns) ==="
  echo "  mallory hits: $(kubectl -n $ns logs deploy/$app -c $app | grep -c mallory)"
  echo "  alice hits:   $(kubectl -n $ns logs deploy/$app -c $app | grep -c alice)"
done
# Expected: every workload reports mallory=0, alice>0
```

**Envoy sidecars are present everywhere they should be:**

```bash
for ns in documents wiki; do
  echo "=== $ns ==="
  kubectl -n $ns get pod -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.containers[*].name}{"\n"}{end}'
done
# Expected: every pod shows two containers (the app + istio-proxy)
```

**Fail-closed proof:**

```bash
kubectl -n documents scale deploy/pcs --replicas=0
sleep 5
kubectl -n documents logs deploy/dashboard-client --tail=20
# Expected: every status code in the last 20 lines is 403 — across all three target workloads,
# including wiki-api (cross-ns call to documents' PCS fails when PCS is down).

curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local/hello   # 403
curl -H "x-workspace-user-id: alice@workspace.test" http://wiki.local:8081/hello   # 403

kubectl -n documents scale deploy/pcs --replicas=1
# Wait for rollout; allow decisions return.
```

## 7. Directory Layout

```text
~/ashwini-repos/workspace/
├── docs/superpowers/specs/
│   ├── 2026-05-13-workspace-kind-harness-design.md             (existing — unchanged)
│   ├── 2026-05-13-permission-validation-phase1-sidecar-design.md (existing — unchanged)
│   └── 2026-05-14-ext-authz-kind-demo-design.md                (this document)
├── sample-apps/
│   ├── echo-server/                       (shared by documents-api, documents-search, wiki-api)
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── deploy/Dockerfile
│   ├── pcs/
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── deploy/Dockerfile
│   └── dashboard-client/
│       ├── main.go
│       ├── go.mod
│       └── deploy/Dockerfile
└── kind/
    ├── README.md
    ├── kind-config.yaml
    ├── setup.sh
    ├── teardown.sh
    ├── charts/                                  ← vendored Istio Helm charts
    │   ├── base-1.24.2.tgz
    │   ├── istiod-1.24.2.tgz
    │   └── gateway-1.24.2.tgz
    ├── chart-values/                            ← explicit Helm values (one file per install)
    │   ├── istio-base-values.yaml               (CRDs only — no images; documents that)
    │   ├── istiod-values.yaml                   (docker.io/istio/pilot:1.24.2 + MacBook sizing)
    │   ├── documents-ingressgateway-values.yaml (proxyv2:1.24.2 noted; NodePort 30080/30443)
    │   └── wiki-ingressgateway-values.yaml      (proxyv2:1.24.2 noted; NodePort 30081)
    ├── demo/                                    ← umbrella Helm chart (app k8s manifests)
    │   ├── Chart.yaml                           (no subchart deps — see kind/demo/README.md)
    │   ├── values.yaml                          ← single swap point for registry/tag/resources
    │   └── templates/
    │       ├── _helpers.tpl
    │       ├── namespace-documents.yaml
    │       ├── namespace-wiki.yaml
    │       ├── pcs.yaml
    │       ├── documents-api.yaml
    │       ├── documents-search.yaml
    │       ├── documents-ext-authz.yaml
    │       ├── documents-gateway.yaml
    │       ├── dashboard-client.yaml
    │       ├── wiki-api.yaml
    │       ├── wiki-ext-authz.yaml
    │       └── wiki-gateway.yaml
    └── manifests/
        ├── documents/                           ← documents product-team-owned (includes PCS)
        │   ├── namespace-documents.yaml
        │   ├── pcs-deployment.yaml              ← PCS owned by documents team
        │   ├── pcs-service.yaml                 ← PCS Service in documents ns
        │   ├── documents-api-deployment.yaml    (carries opt-in label)
        │   ├── documents-api-service.yaml
        │   ├── documents-search-deployment.yaml (carries opt-in label)
        │   ├── documents-search-service.yaml
        │   ├── documents-ext-authz.yaml         ← per-ns EnvoyFilter (label-keyed)
        │   ├── documents-gateway.yaml
        │   ├── documents-virtualservice.yaml
        │   └── dashboard-client-deployment.yaml
        └── wiki/                                ← wiki team-owned (cross-ns copy pattern)
            ├── namespace-wiki.yaml
            ├── wiki-api-deployment.yaml         (carries opt-in label)
            ├── wiki-api-service.yaml
            ├── wiki-ext-authz.yaml              ← copied EnvoyFilter; calls pcs.documents.svc
            ├── wiki-gateway.yaml
            └── wiki-virtualservice.yaml
```

The `manifests/platform/`, `manifests/documents/`, and `manifests/wiki/` split mirrors the ownership boundary documented in §3.2.

> **Note:** `kind/manifests/` is the original per-resource manifest set and is retained as a reference. The active bring-up path (`setup.sh`) uses the umbrella chart at `kind/demo/` — see §4.12 and §5.

## 8. Stage 2 Evolution — Move EnvoyFilter to `istio-system` with label opt-in

The intended evolution after the platform team approves hosting a shared filter. Two things change:

**Change 1 — Collapse both per-namespace EnvoyFilters into one in `istio-system`:**

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: ext-authz-pcs
  namespace: istio-system                       # ← root namespace = cross-namespace reach
spec:
  workloadSelector:
    labels:
      workspace.io/ext-authz: enabled           # ← SAME opt-in label as Stage 1
  configPatches:
  # ... (byte-for-byte identical to the Stage 1 EnvoyFilter configPatches —
  #      same PCS URL, same allowed_headers, same fail-closed setting)
```

The platform team writes and owns this file. App teams cannot edit it.

**Change 2 — App teams delete their per-namespace EnvoyFilter copies:**

- Documents team deletes `documents-ext-authz` from `documents` namespace.
- Wiki team deletes `wiki-ext-authz` from `wiki` namespace.

**App teams' Deployments do not change.** The opt-in label is the same; the new `istio-system` EnvoyFilter matches the same label across all namespaces.

**Migration ordering (zero-downtime):**

1. Platform team applies the new `EnvoyFilter` in `istio-system`. Workloads are now patched **twice** (once by the per-namespace filter, once by the istio-system filter) — but both filters insert identical ext_authz config, so each request still goes through one effective check per round-trip (Envoy de-duplicates the filter chain when patches collide on identical names). Verify no behaviour change via the same verification commands.
2. Each app team, when ready, deletes their per-namespace EnvoyFilter. After this, only the `istio-system` filter is in effect.
3. Verification: `kubectl get envoyfilter -A` shows only the single resource in `istio-system`.

**Why this is the better long-term shape:** single source of truth; teams onboard by adding one label, not by copying YAML; platform owns the PCS URL and version, so an upgrade is one PR.

**Why it's not Stage 1:** it requires `istio-system` write access and platform-team approval.

## 9. Trade-offs and Risks

### 9.1 Per-namespace EnvoyFilter duplication (Stage 1)

Every namespace that opts in carries its own EnvoyFilter copy. If the PCS URL, allowed-headers list, or fail-mode flag changes, every namespace has to update its copy. Mitigation: ship a templated YAML in a shared platform repo so teams pull from a single canonical source. Stage 2 eliminates the duplication.

### 9.2 EnvoyFilter is the "old" pattern

Istio docs recommend `AuthorizationPolicy` with `action: CUSTOM` and a registered `extensionProvider` for new clusters. `EnvoyFilter` patches raw Envoy config and is brittle to Envoy version changes. The demo chooses `EnvoyFilter` because that is what the team's existing cluster uses (per the `gateway-connection-limit` example). Migration tracked in §10.7.

### 9.3 Hard-coded allow-list in `pcs`

The decision policy lives in Go code. Fine for a demo with two allow-listed users; a real policy service would consult a registry.

### 9.4 Header-based identity, ignoring Istio mesh identity

The decision input is `x-workspace-user-id`, not mesh SPIFFE identity. JWT-derived identity tracked in §10.4.

### 9.5 Single-replica everything

Every Deployment runs one replica. Multi-replica routing, EnvoyFilter propagation under churn, pod-restart behavior — not exercised.

### 9.6 Plain HTTP through the ingressgateway

TLS termination tracked in §10.5.

### 9.7 No explicit opt-out negative example deployed

A team that does not carry the opt-in label is implicitly opted out. The demo does not deploy a fourth, opt-out workload to make this explicit — the absence-of-label semantics are clear from the EnvoyFilter selector. Adding such a workload would clarify for new readers but adds a namespace + Deployment that the headline claim does not depend on.

## 10. Future Enhancements

### 10.1 Move EnvoyFilter to `istio-system` (Stage 2)

See §8. The full migration plan is documented there.

### 10.2 gRPC ext_authz protocol

Switch from HTTP `POST /check` to Envoy's gRPC `CheckRequest`. Production deployments commonly prefer gRPC for typed contracts and lower per-request overhead.

### 10.3 Cluster-level baseline policies

Layer `allow-ip`, `allow-metrics`, and `allow-specific-namespace` `AuthorizationPolicy` resources at the mesh layer to demonstrate defense-in-depth alongside ext_authz.

### 10.4 Real auth (JWT) in front of `x-workspace-user-id`

Add a `RequestAuthentication` resource targeting the gated Pods, requiring a signed JWT; derive identity from JWT claims instead of a raw header.

### 10.5 TLS termination on the ingressgateway

Add a self-signed cert generated at setup time, mount it as a TLS Secret in `documents` namespace, switch the Gateway's `port.protocol` to `HTTPS` with `tls.mode: SIMPLE`.

### 10.6 Mutating webhook for label auto-injection (post-Stage 2)

Replace the manual opt-in label with a mutating admission webhook that adds the label automatically based on namespace classification.

### 10.7 Migrate from `EnvoyFilter` to `AuthorizationPolicy` (action: CUSTOM) + `extensionProvider`

The Istio-recommended path. After Stage 2: add an `extensionProvider` entry in the `istio` ConfigMap's `meshConfig`; replace the `EnvoyFilter` in `istio-system` with an `AuthorizationPolicy` (also in `istio-system`) with `action: CUSTOM`. PCS contract unchanged.

## 11. Open Questions

1. **Opt-in label key.** The demo uses `workspace.io/ext-authz: enabled`. The same string appears in five places (three Deployment Pod templates, two EnvoyFilter `workloadSelector` blocks, plus verification commands). Align with the team's standard label vocabulary before implementation; the Stage 2 migration will reuse this label.
2. **Demo cluster name.** Default `ext-authz-demo`. If another demo already uses that name on the machine, `setup.sh` reuses the existing cluster; `teardown.sh` removes it cleanly.
3. **Hostnames for external curl.** Defaults `documents.local`, `wiki.local`. If either collides with anything in `/etc/hosts`, swap for less-common names. Note that `wiki.local` is reached on host port `8081` (not 80) because two distinct ingressgateways cannot share the same host port.
4. **PCS namespace ownership confirmed for the demo.** PCS lives in `documents` ns, owned by the documents (main product) team — not in a separate platform-owned namespace. Stage 2's `istio-system` EnvoyFilter would still call `pcs.documents.svc.cluster.local:8080`; PCS itself does not move.
5. **Whether to also expose `documents-search` via its own Gateway/VirtualService.** Current spec keeps `documents-search` internal-only. It can be added with a third Gateway/VirtualService inside `documents` namespace if the demo audience wants to curl all three workloads from the host.

## 12. Success Criteria

The harness is considered successful when, on a fresh checkout of `~/ashwini-repos/workspace/`:

1. `./kind/setup.sh` runs to completion in ≤ 3 minutes on a 16 GB MacBook with Docker Desktop allocated ≥ 6 GB.
2. After adding `127.0.0.1 documents.local wiki.local` to `/etc/hosts`:
   - `curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local/hello` returns `200`.
   - `curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local/hello` returns `403`.
   - `curl -H "x-workspace-user-id: alice@workspace.test" http://wiki.local:8081/hello` returns `200`.
   - `curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello` returns `403`.
3. `kubectl -n documents logs deploy/dashboard-client -f` shows the 6-call cycle from §3.3 with the expected status codes.
4. `kubectl -n documents logs deploy/pcs -c pcs -f` shows 3 allow + 3 deny decision lines per dashboard-client cycle.
5. For each of `documents-api`, `documents-search`, `wiki-api`: `kubectl logs ... | grep -c mallory` returns `0`; the equivalent grep for `alice` returns a positive number.
6. `kubectl get envoyfilter -A` shows exactly two EnvoyFilters — `documents-ext-authz` in `documents` namespace and `wiki-ext-authz` in `wiki` namespace. Nothing in `istio-system`.
7. `kubectl -n documents scale deploy/pcs --replicas=0` causes all calls (internal and external, all three workloads — including the wiki-api cross-ns path) to return `403`; restoring the replica brings allow decisions back.
8. `./kind/teardown.sh` removes the cluster cleanly.

The harness deliberately does not target throughput, latency, or any production-grade SLO. It is a correctness and teaching harness for the Stage 1 per-namespace EnvoyFilter pattern, with all three onboarding cases (main product, sibling-in-same-ns, cross-namespace) exercised end-to-end.

## 13. References

- Envoy `ext_authz` HTTP filter reference: <https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_authz_filter>
- Istio `EnvoyFilter` reference (including root-namespace scoping rules): <https://istio.io/latest/docs/reference/config/networking/envoy-filter/>
- Istio `AuthorizationPolicy` reference (for §10.7 future migration): <https://istio.io/latest/docs/reference/config/security/authorization-policy/>
- Istio `Gateway` + `VirtualService` reference: <https://istio.io/latest/docs/reference/config/networking/gateway/>
- ECK Elasticsearch kind harness — structural template: `charts/elasticsearch/kind/` on branch `claude/eck-elasticsearch-setup-VCAsc` of `hmchangw/chat`.
- Sibling spec: `2026-05-13-permission-validation-phase1-sidecar-design.md` — the broader Phase 1 sidecar design this demo exercises.
