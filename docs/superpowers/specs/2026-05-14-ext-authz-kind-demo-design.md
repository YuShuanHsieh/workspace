# Ext-Authz Kind Demo — Minimal Harness Design

> **Status:** Draft for review
> **Audience:** Platform team members learning the Envoy `ext_authz` pattern; app teams onboarding to the Permission Checking Service via label-based opt-in.
> **Owner:** Ashwini (platform)
> **Related documents:**
> - [`2026-05-13-workspace-kind-harness-design.md`](2026-05-13-workspace-kind-harness-design.md) — broader workspace platform kind harness (NATS-based; this demo is intentionally narrower and unrelated to that NATS flow).
> - [`2026-05-13-permission-validation-phase1-sidecar-design.md`](2026-05-13-permission-validation-phase1-sidecar-design.md) — Phase 1 Permission Validation sidecar implementation (Envoy `ext_authz` is the deployment shape this demo exercises end-to-end).
> **Reference implementation pattern:** The ECK Elasticsearch kind harness on the `claude/eck-elasticsearch-setup-VCAsc` branch of the `hmchangw/chat` repository at `charts/elasticsearch/kind/` — structural model (vendored chart layout, single `setup.sh`, plain manifests, per-namespace ingressgateway).

---

## 1. Background and Motivation

The Permission Validation Phase 1 design (sibling spec) describes a per-request authorization gate that sits in front of application services. App teams onboard their services to that gate, and the gate calls a central **Permission Checking Service (PCS)** for an allow/deny decision on every inbound request. Before that flow is wired into production app charts, the platform engineer (Ashwini) needs a local, single-file-to-run demo that proves the wiring end-to-end in a realistic deployment pattern.

The demo also serves as a teaching artifact for the **platform-vs-app-team ownership split** that this pattern depends on:

- The platform team owns one `EnvoyFilter` resource in the Istio root namespace (`istio-system`). That filter is mesh-wide-reachable; it lives in one place; it is configured once.
- App teams opt in to the ext_authz check by adding a single label to their Deployment's Pod template. No app team writes or copies `EnvoyFilter` YAML. Teams that do not opt in are unaffected.

The ECK Elasticsearch kind harness is the structural template — pulled-from-internet (or vendored) Istio charts, a single idempotent `setup.sh`, plain-YAML manifests, a clear verification path, and a per-namespace ingressgateway that mirrors the production cluster's ingress posture. This demo is deliberately narrower than the ECK harness (no Vault, no NATS, no Elasticsearch) but uses the same kind cluster shape so the demo "feels like" the production cluster.

## 2. Goal and Non-Goals

### 2.1 Goal

Stand up a single-node `kind` cluster that demonstrates per-request authorization via Envoy's `ext_authz` HTTP filter, with the filter wired via **one mesh-wide `EnvoyFilter` in `istio-system` that targets a workload-selector label**. App teams opt in by adding that label to their Deployment. The cluster mirrors the production deployment posture: every app namespace has Istio sidecar injection enabled, and external traffic enters through a per-namespace ingressgateway.

Concretely the harness must produce these observable outcomes on a fresh checkout, in any order:

1. `documents-api` runs in namespace `documents` with sidecar injection enabled and the opt-in label `workspace.io/ext-authz: enabled` on its Pod template. The injected Envoy sidecar carries an `ext_authz` HTTP filter (patched in via the mesh-wide `EnvoyFilter` in `istio-system`) that calls the PCS on every inbound request.
2. `wiki-api` runs in namespace `wiki` with sidecar injection enabled but **no opt-in label**. The same `EnvoyFilter` does not match it; its sidecar is unpatched. Requests reach `wiki-api` without any authz check.
3. `pcs` (the Permission Checking Service) runs in namespace `pcs` with sidecar injection enabled. It returns `200` for users in its allow-list, `403` otherwise.
4. `dashboard-client` runs in namespace `dashboard` with sidecar injection enabled. A `curl` loop alternates calls between `documents-api` and `wiki-api`, alternating the `x-workspace-user-id` header between an allow-listed user (`alice@workspace.test`) and a not-allow-listed user (`mallory@workspace.test`).
5. `dashboard-client`'s log shows: every call to `documents-api` is gated (`alice` returns `200`, `mallory` returns `403`); every call to `wiki-api` returns `200` regardless of which user header was sent.
6. `documents-api`'s container log shows only `alice`'s requests; `mallory`'s never reach the app container.
7. `wiki-api`'s container log shows **both** `alice`'s and `mallory`'s requests — no authz gate fires for the opted-out workload.
8. Scaling `pcs` to zero causes all `documents-api`-bound traffic to receive `403` (fail-closed). `wiki-api` traffic is unaffected.
9. The same allow/deny behaviour is observable from outside the cluster via the per-namespace ingressgateway in `documents` ns: `curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local/hello` returns `200`; the same call with `mallory@workspace.test` returns `403`.

### 2.2 Non-Goals

| Capability | Status | Reason for deferral |
|---|---|---|
| Mesh-level `extensionProvider` + `AuthorizationPolicy` (action: CUSTOM) wiring | Out | Demo is scoped to the `EnvoyFilter`-only approach. The mesh-level provider pattern is documented in §9 as a future enhancement. |
| HTTPS / TLS termination on the ingressgateway | Out | Plain HTTP keeps the curl-from-host verification simple. TLS is a small follow-up if needed (see §9.5). |
| Real auth tokens (JWT, OIDC) | Out | Header-based allow/deny is sufficient to demonstrate the ext_authz decision flow. |
| Dynamic policy registry / database for `pcs` | Out | A hard-coded in-process allow-list of usernames is sufficient. |
| Cluster-level baseline `AuthorizationPolicy` resources (`allow-ip`, `allow-metrics`, `allow-specific-namespace`) | Out | Orthogonal concern — those operate at Istio L4/L7 mesh layer, not at the ext_authz call. Adding them now dilutes the headline claim. |
| Performance / latency / load testing | Out | This is a correctness harness, not a benchmark. |
| Explicit mTLS verification | Out | All app namespaces are injected, so mesh-mTLS happens by default. The demo does not separately assert this. |
| Vendored `.tgz` Istio charts | Out (default) | `setup.sh` pulls Istio charts from the public Helm repo. Vendoring is an easy follow-up if offline operation is required (see §9). |
| Multi-cluster / cross-cluster federation | Out | Single kind cluster only. |
| Helm chart for the demo workloads | Out | Four Deployments, one EnvoyFilter, one Gateway/VirtualService pair — not enough variation to justify templating. Plain YAML stays. |
| Mutating admission webhook for label auto-injection | Out | Apps add the opt-in label explicitly in their Deployment YAML. Auto-injection is a Phase 2 ergonomics improvement (see §9.6). |

## 3. Architecture

### 3.1 Cluster Layout

A single-node `kind` cluster with `extraPortMappings` exposing the per-namespace ingressgateway on host ports 80/443. Five namespaces:

```text
                         ┌────────────────────────────────────────────────────────┐
                         │                kind cluster (single node)              │
   curl from host ──80───┤                                                        │
                         │  ns: istio-system            (Istio control plane)     │
                         │    istio-base                                          │
                         │    istiod                                              │
                         │    EnvoyFilter "ext-authz-pcs"                         │
                         │      workloadSelector: workspace.io/ext-authz=enabled  │
                         │      → patches every matching sidecar in the cluster   │
                         │                                                        │
                         │  ns: pcs                     (injection: enabled)      │
                         │    pcs Pod                                             │
                         │      ├─ pcs (Go decision server)                       │
                         │      └─ istio-proxy (sidecar)                          │
                         │    Service "pcs" (ClusterIP, port 8080)                │
                         │                                                        │
                         │  ns: documents               (injection: enabled)      │
                         │    documents-ingressgateway Pod                        │
                         │      └─ istio-proxy (NodePort 30080 → host 80)         │
                         │    documents-api Pod                                   │
                         │      labels: { workspace.io/ext-authz: enabled }       │
                         │      ├─ documents-api (Go echo server)                 │
                         │      └─ istio-proxy (sidecar) ← patched by EnvoyFilter │
                         │    Gateway "documents-gateway" (host: documents.local) │
                         │    VirtualService "documents-vs"                       │
                         │    Service "documents-api" (ClusterIP, port 8080)      │
                         │                                                        │
                         │  ns: wiki                    (injection: enabled)      │
                         │    wiki-api Pod                                        │
                         │      (NO opt-in label)                                 │
                         │      ├─ wiki-api (same Go echo server image)           │
                         │      └─ istio-proxy (sidecar) ← UNPATCHED              │
                         │    Service "wiki-api" (ClusterIP, port 8080)           │
                         │                                                        │
                         │  ns: dashboard               (injection: enabled)      │
                         │    dashboard-client Pod                                │
                         │      ├─ dashboard-client (Go HTTP loop)                │
                         │      └─ istio-proxy (sidecar)                          │
                         └────────────────────────────────────────────────────────┘
```

**Host port mappings** (`extraPortMappings` in `kind-config.yaml`):

| Host port | Container port | Purpose |
|---|---|---|
| 80 | 30080 | Plain HTTP to `documents-ingressgateway` (curl path) |
| 443 | 30443 | HTTPS reserved for future TLS upgrade (see §9.5) |

**Required `/etc/hosts` addition** (printed at the end of `setup.sh`):

```text
127.0.0.1  documents.local
```

### 3.2 Ownership Split

The harness preserves the production ownership split inside `setup.sh`. One operator runs the script, but the resources are grouped by which team would own each one in production:

| Resource | Lives in | Production owner |
|---|---|---|
| `EnvoyFilter ext-authz-pcs` | `istio-system` | **Platform team** — only platform has write access to `istio-system` in production. |
| `pcs` Deployment + Service | `pcs` namespace | **Platform team** — PCS is shared infrastructure across all app teams. |
| `documents-api` Deployment + Service + opt-in label + Gateway + VirtualService | `documents` namespace | **Documents team** — owns its workload, its ingress, and the choice to opt in. |
| `documents-ingressgateway` (Istio gateway chart) | `documents` namespace | Documents team (or platform, depending on org). |
| `wiki-api` Deployment + Service (no opt-in label) | `wiki` namespace | **Wiki team** — chose not to opt in; no other change required. |
| `dashboard-client` Deployment | `dashboard` namespace | Dashboard team — represents any upstream caller. |

`setup.sh` applies all of the above for demo convenience, but the manifests are organised in `kind/manifests/` so the platform-team subset and the per-app-team subsets are visually distinct (see §7).

### 3.3 Request Flow

Two entry paths reach `documents-api`'s sidecar; both go through the same `ext_authz` filter that the platform-team `EnvoyFilter` patched in:

**Internal path (in-cluster):**

```text
┌──────────────────┐  HTTP GET /hello                    ┌────────────────────────┐
│ dashboard-client │ ── x-workspace-user-id: alice@... ─►│ documents-api sidecar  │
│  (curl loop)     │   via cluster DNS                   │ SIDECAR_INBOUND filter │
└──────────────────┘   documents-api.documents.svc:8080  │ → ext_authz fires      │
                                                         └─────────┬──────────────┘
                                                                   │ ext_authz HTTP
                                                                   ▼ POST /check
                                                         ┌────────────────────────┐
                                                         │ pcs service            │
                                                         │   alice → 200          │
                                                         │   mallory → 403        │
                                                         └─────────┬──────────────┘
                                                                   ▼
                                                         ┌────────────────────────┐
                                                         │ documents-api sidecar  │
                                                         │   200 → forward        │
                                                         │   403 → reply 403      │
                                                         └─────────┬──────────────┘
                                                                   │ (allow case)
                                                                   ▼
                                                         ┌────────────────────────┐
                                                         │ documents-api container│
                                                         │  returns "hello"       │
                                                         └────────────────────────┘
```

**External path (from host):**

```text
┌────────────┐  HTTP GET /hello                ┌──────────────────────────┐
│ host curl  │ ── x-workspace-user-id: alice ─►│ documents-ingressgateway │
│documents.  │  via host:80 → NodePort 30080   │ (Envoy on gateway pod)   │
│local       │                                 └─────────┬────────────────┘
└────────────┘                                           │ Gateway → VirtualService
                                                         │ routes to documents-api Svc
                                                         ▼
                                            ┌────────────────────────┐
                                            │ documents-api sidecar  │
                                            │ (same SIDECAR_INBOUND  │
                                            │  filter — ext_authz)   │
                                            └─────────┬──────────────┘
                                                      │ (continues identically
                                                      │  to the internal path)
                                                      ▼
                                                  pcs decision, then forward/reject
```

**Opt-out path (wiki-api):**

```text
┌──────────────────┐  HTTP GET /hello                    ┌────────────────────────┐
│ dashboard-client │ ── x-workspace-user-id: anyone ────►│ wiki-api sidecar       │
│  (curl loop)     │                                     │ SIDECAR_INBOUND filter │
└──────────────────┘                                     │ ← UNPATCHED            │
                                                         │ (EnvoyFilter selector  │
                                                         │  did not match this pod│
                                                         │  — no opt-in label)    │
                                                         └─────────┬──────────────┘
                                                                   │
                                                                   ▼
                                                         ┌────────────────────────┐
                                                         │ wiki-api container     │
                                                         │  returns "hello"       │
                                                         └────────────────────────┘
```

The headline observable property holds along both gated paths: the **deny decision is enforced at the `documents-api` Envoy sidecar layer**, not by any application code. The `documents-api` container only sees requests for `alice`. The `wiki-api` container is unaffected — the same request payload reaches it with no authz check, because it never opted in.

### 3.4 ext_authz Wiring: Mesh-wide EnvoyFilter with Label Opt-in

The ext_authz HTTP filter is added to every opted-in workload's sidecar by **one** `EnvoyFilter` resource in `istio-system`. The resource's `workloadSelector` matches a single label, `workspace.io/ext-authz: enabled`. Any Pod across any namespace that carries that label gets the filter patched into its sidecar; everything else is untouched.

Key configuration decisions:

- **Resource lives in `istio-system`** — Istio's "root namespace." Only an EnvoyFilter in the root namespace has cross-namespace reach. An EnvoyFilter in any other namespace would match only Pods inside that same namespace, defeating the central-config / opt-in pattern.
- **`workloadSelector.labels: { workspace.io/ext-authz: enabled }`** — the opt-in marker. App teams add this label to their Deployment's Pod template to receive the filter. No label → no filter.
- **`context: SIDECAR_INBOUND`** — the filter intercepts traffic entering each matched Pod, not traffic the Pod makes outbound.
- **`applyTo: HTTP_FILTER` + `operation: INSERT_BEFORE`** — the ext_authz filter is inserted into the HTTP filter chain ahead of the router filter. (Inserting after the router has no effect because the router has already dispatched the request to the upstream.)
- **`http_service` (not gRPC)** — Envoy talks to `pcs` via plain HTTP `POST /check`. Simpler to debug than the gRPC `CheckRequest` protocol and sufficient for the demo.
- **`allowed_headers`** — Envoy is told explicitly which inbound headers to forward to `pcs`. The demo forwards `x-workspace-user-id` and `authorization`. Without an allow-list, Envoy forwards none.
- **`failure_mode_allow: false`** — if `pcs` is unreachable or returns a 5xx, Envoy fails the request (returns 403 to the client). Production-correct default; lets the demo verify fail-closed behavior by scaling `pcs` to zero.

The complete `EnvoyFilter` resource is in §4.4.

**Why pure EnvoyFilter, not mesh-level `extensionProvider`:** the demo is intentionally scoped to the `EnvoyFilter` path. `EnvoyFilter` is verbose but does the job in a single declarative file. The `extensionProvider` + `AuthorizationPolicy (action: CUSTOM)` pattern is the production-recommended migration target and is documented in §9.1 as a future enhancement.

### 3.5 All-namespace Istio Injection

All four app namespaces (`pcs`, `documents`, `wiki`, `dashboard`) carry the `istio-injection: enabled` label. This mirrors the production cluster's posture (every namespace defaults to sidecar-injected) and avoids the artificial split where some namespaces speak mesh-mTLS and others do not.

Concrete consequences:

- Traffic between any two pods is **mesh-mTLS** by default (Istio's `PERMISSIVE` mTLS mode handles bootstrap). The demo does not separately assert this; it falls out for free.
- The `ext_authz` check happens **on the receiver side** (`documents-api`'s sidecar) regardless of whether the caller is sidecar-injected. Even with `dashboard-client`'s sidecar in the picture, the authorization input remains the `x-workspace-user-id` header, not Istio's per-workload identity. The demo's authorization decision is intentionally identity-blind at the mesh layer — that responsibility lives in `pcs`.
- The `pcs` pod has a sidecar too. Outbound calls from `documents-api`'s ext_authz filter to `pcs.pcs.svc.cluster.local:8080` are mesh-managed (mTLS, retries via Envoy defaults). This is the production-realistic shape.
- The `wiki-api` pod has a sidecar too — but no opt-in label, so the sidecar is unpatched. This is the key teaching point of §3.4.

### 3.6 Opt-in via Pod-template label

App teams onboard to the ext_authz gate by adding one label to their Deployment's `.spec.template.metadata.labels`:

```yaml
spec:
  template:
    metadata:
      labels:
        app: documents-api
        workspace.io/ext-authz: enabled    # ← single line is the entire onboarding step
```

The label lives on the **Pod template**, not on the Deployment's outer `metadata.labels`. This is the field that controls Pod labels at creation time; `workloadSelector` in Istio matches Pod labels, not Deployment labels.

Apps that do not want the gate (e.g. `wiki-api` in this demo, or any internal-only service in production) simply omit the label. There is no opt-out flag and no separate "exclude" list — the absence of the label is the opt-out.

## 4. Component Specifications

### 4.1 `dashboard-client` — request driver

| Field | Value |
|---|---|
| Path on disk | `sample-apps/dashboard-client/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/dashboard-client:dev` (locally built; loaded via `kind load docker-image`) |
| Listen port | None (pod is a client, not a server) |
| Behaviour | A `for` loop that issues, in sequence per iteration: <br>1. `GET http://documents-api.documents.svc.cluster.local:8080/hello` with header `x-workspace-user-id: alice@workspace.test` <br>2. Same URL with `x-workspace-user-id: mallory@workspace.test` <br>3. `GET http://wiki-api.wiki.svc.cluster.local:8080/hello` with `x-workspace-user-id: alice@workspace.test` <br>4. Same URL with `x-workspace-user-id: mallory@workspace.test` <br>Sleeps 3s between calls. Each call logs (`log/slog` JSON) the target URL, the header value, and the response HTTP status code. |
| Sidecar | Envoy (auto-injected; `dashboard` namespace has `istio-injection: enabled`) |
| Opt-in label | **Not set** — `dashboard-client` is a caller, never a receiver of authz-gated traffic. |
| Pod container name | `dashboard-client` |
| Estimated LOC | ~50 (single `main.go` using `net/http`) |

### 4.2 `documents-api` — protected echo HTTP server

| Field | Value |
|---|---|
| Path on disk | `sample-apps/echo-server/` (shared with `wiki-api` — same image, different Deployment) |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/echo-server:dev` (locally built; loaded via `kind load docker-image`) |
| Listen port | 8080 |
| Endpoints | `GET /hello` → `200 "hello from $POD_NAME"` (uses the `POD_NAME` env var so the response identifies which workload responded) |
| Logging | `log/slog` JSON; one line per request including `x-workspace-user-id` header |
| Sidecar | Envoy (auto-injected; `documents` namespace has `istio-injection: enabled`) |
| Opt-in label | **`workspace.io/ext-authz: enabled`** on the Pod template — the entire onboarding step. |
| Pod container name | `documents-api` (referenced explicitly by verification commands; set in `documents-api-deployment.yaml` `containers[0].name`) |
| Estimated LOC | ~30 |

The Go binary is intentionally minimal — a single Gin handler. `documents-api` has no knowledge of `ext_authz` and no awareness that some incoming requests are being denied upstream by its own sidecar.

### 4.3 `wiki-api` — opt-out negative example

| Field | Value |
|---|---|
| Path on disk | Reuses the `sample-apps/echo-server/` image. |
| Image | `workspace/echo-server:dev` (same image as `documents-api`) |
| Sidecar | Envoy (auto-injected; `wiki` namespace has `istio-injection: enabled`) |
| Opt-in label | **Not set** — `wiki-api` deliberately stays opted out. Its sidecar receives no ext_authz patch from the mesh-wide EnvoyFilter. |
| Pod container name | `wiki-api` |

`wiki-api` exists solely to prove that opt-in is truly opt-in: the same `EnvoyFilter` that gates `documents-api` does not touch `wiki-api`, because the latter does not carry the trigger label. This makes the boundary of the design self-evident in `kubectl` output rather than buried in documentation.

### 4.4 `pcs` — Permission Checking Service

| Field | Value |
|---|---|
| Path on disk | `sample-apps/pcs/` |
| Language | Go 1.25 |
| Files | `main.go`, `go.mod`, `deploy/Dockerfile` |
| Image | `workspace/pcs:dev` (locally built; loaded via `kind load docker-image`) |
| Listen port | 8080 |
| Endpoints | `POST /check` |
| Decision policy | Hard-coded allow-list `{"alice@workspace.test", "bob@workspace.test"}`. If the request's `x-workspace-user-id` header value is in the list → `200 OK`. Otherwise → `403 Forbidden`. If header is missing → `403`. |
| Logging | `log/slog` JSON; one line per decision (`{"user":"...","decision":"allow|deny","ts":"..."}`) |
| Sidecar | Envoy (auto-injected; `pcs` namespace has `istio-injection: enabled`) |
| Estimated LOC | ~40 |

Response body is intentionally empty — `ext_authz` looks only at the HTTP status code by default. Envoy maps `200..299` → allow; everything else → deny.

### 4.5 EnvoyFilter (mesh-wide, in `istio-system`)

The full resource, applied to the Istio root namespace:

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: ext-authz-pcs
  namespace: istio-system                    # ← root namespace = cross-namespace reach
spec:
  workloadSelector:
    labels:
      workspace.io/ext-authz: enabled        # ← opt-in marker for every onboarded workload
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
              uri: http://pcs.pcs.svc.cluster.local:8080
              cluster: outbound|8080||pcs.pcs.svc.cluster.local
              timeout: 1s
            path_prefix: /check
            authorization_request:
              allowed_headers:
                patterns:
                - exact: x-workspace-user-id
                - exact: authorization
          failure_mode_allow: false
```

### 4.6 Per-namespace Ingressgateway (`documents-ingressgateway`)

A dedicated Istio ingressgateway pod lives in the `documents` namespace, deployed via the upstream `istio/gateway` Helm chart (version `1.24.2`). This mirrors the production cluster's pattern of per-team / per-namespace ingress gateways rather than a single shared `istio-ingressgateway` in `istio-system`.

| Field | Value |
|---|---|
| Chart | `gateway` from `https://istio-release.storage.googleapis.com/charts`, version `1.24.2` |
| Release name | `documents-ingressgateway` |
| Namespace | `documents` |
| Pod labels | `istio: documents-ingressgateway` (consumed by the `Gateway` resource selector) |
| Service type | `NodePort` |
| Ports | `80 → nodePort 30080`, `443 → nodePort 30443` (matching `kind-config.yaml` `extraPortMappings`) |
| Resources | 50m / 128Mi CPU/memory request (sized for kind) |
| Opt-in label | **Not set** — the gateway pod routes; the ext_authz check fires one hop later on `documents-api`'s sidecar. Patching the gateway too would either double-check the request or break gateway routing. |

### 4.7 Gateway + VirtualService

Two Istio CRDs in the `documents` namespace expose `documents-api` externally on `documents.local`:

```yaml
apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: documents-gateway
  namespace: documents
spec:
  selector:
    istio: documents-ingressgateway        # matches the per-namespace gateway pod
  servers:
  - port:
      number: 80
      name: http
      protocol: HTTP
    hosts:
    - documents.local
---
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: documents-vs
  namespace: documents
spec:
  hosts:
  - documents.local
  gateways:
  - documents-gateway
  http:
  - route:
    - destination:
        host: documents-api.documents.svc.cluster.local
        port:
          number: 8080
```

The route sends gateway-terminated traffic to the `documents-api` Service. The Service in turn routes to the `documents-api` Pod, where the workload sidecar's ext_authz filter intercepts the request.

### 4.8 Istio Install Surface

All Istio charts are **vendored as `.tgz` files** committed to `kind/charts/` (the ECK harness pattern). `setup.sh` references the local files; nothing is pulled from the internet at runtime.

| Component | Vendored file | Namespace |
|---|---|---|
| istio-base | `kind/charts/base-1.24.2.tgz` | `istio-system` |
| istiod | `kind/charts/istiod-1.24.2.tgz` | `istio-system` |
| Per-namespace ingressgateway | `kind/charts/gateway-1.24.2.tgz` (release name `documents-ingressgateway`) | `documents` |

The shared `istio-ingressgateway` (the cluster-scoped default gateway in `istio-system`) is **not** installed — each app namespace owns its own gateway in this model. The `.tgz` files are downloaded once during initial repo setup (e.g. with `helm pull --repo https://istio-release.storage.googleapis.com/charts --version 1.24.2 base istiod gateway --destination kind/charts/`) and committed to the repo. Subsequent `./kind/setup.sh` runs are fully offline-capable for the Istio install phase.

## 5. Bring-up Flow (`kind/setup.sh`)

The script is idempotent — re-runs pick up where they left off. Steps in order, grouped by ownership to show how the same flow maps to platform vs app teams in production:

### Phase 1 — Platform-team actions

1. **Create kind cluster.** If `kind get clusters` already contains `ext-authz-demo`, reuse; else `kind create cluster --name ext-authz-demo --config kind-config.yaml`. The config carries `extraPortMappings` (host 80/443 → container 30080/30443). Switch kubectl context to `kind-ext-authz-demo`.
2. **Install Istio control plane from vendored charts.**
   - `helm upgrade --install istio-base kind/charts/base-1.24.2.tgz -n istio-system --create-namespace --wait`
   - `helm upgrade --install istiod kind/charts/istiod-1.24.2.tgz -n istio-system --wait`
3. **Apply the mesh-wide EnvoyFilter.** Apply `kind/manifests/platform/envoyfilter-ext-authz-pcs.yaml` into `istio-system`. In production, this step is owned by the platform team's GitOps pipeline; here it is one `kubectl apply`.
4. **Deploy PCS.** Apply `kind/manifests/platform/namespace-pcs.yaml`, then `pcs-deployment.yaml` and `pcs-service.yaml` into `pcs` namespace. Wait for `Available` condition.

### Phase 2 — App-team actions (per team)

5. **Create app namespaces.** Apply `kind/manifests/apps/namespace-documents.yaml`, `namespace-wiki.yaml`, `namespace-dashboard.yaml`. All three carry `istio-injection: enabled`.
6. **Install the per-namespace ingressgateway in `documents` from the vendored chart.**
   - `helm upgrade --install documents-ingressgateway kind/charts/gateway-1.24.2.tgz -n documents -f kind/manifests/apps/documents/istio-gateway-values.yaml --skip-schema-validation --wait`
7. **Build local images.** From the repo root:
   - `(cd sample-apps/echo-server && docker build -t workspace/echo-server:dev -f deploy/Dockerfile .)`
   - `(cd sample-apps/pcs && docker build -t workspace/pcs:dev -f deploy/Dockerfile .)`
   - `(cd sample-apps/dashboard-client && docker build -t workspace/dashboard-client:dev -f deploy/Dockerfile .)`
8. **Load images into kind.**
   - `kind load docker-image workspace/echo-server:dev --name ext-authz-demo`
   - `kind load docker-image workspace/pcs:dev --name ext-authz-demo`
   - `kind load docker-image workspace/dashboard-client:dev --name ext-authz-demo`
9. **Deploy `documents-api` (with opt-in label) and its routing.** Apply `documents-api-deployment.yaml` (carrying `workspace.io/ext-authz: enabled`), `documents-api-service.yaml`, `documents-gateway.yaml`, and `documents-virtualservice.yaml`. Wait for `Available`.
10. **Deploy `wiki-api` (without opt-in label).** Apply `wiki-api-deployment.yaml` and `wiki-api-service.yaml` in `wiki` namespace. Wait for `Available`.
11. **Deploy `dashboard-client`.** Apply `dashboard-client-deployment.yaml` in `dashboard` namespace.
12. **Print verification commands and `/etc/hosts` line** (see §6).

Total wall-clock on a warm Docker cache should be ≤ 3 minutes on a 16 GB MacBook.

`kind/teardown.sh` is one line: `kind delete cluster --name ext-authz-demo`.

## 6. Verification

The setup script prints these commands at the end, plus a reminder to add `127.0.0.1 documents.local` to `/etc/hosts`.

**Internal-path demo — dashboard-client alternates targets and users:**

```bash
kubectl -n dashboard logs deploy/dashboard-client -c dashboard-client -f
# Expected (rolling, every 3s):
#   documents-api alice@workspace.test → 200
#   documents-api mallory@workspace.test → 403
#   wiki-api alice@workspace.test → 200
#   wiki-api mallory@workspace.test → 200
```

Note that `wiki-api` returns `200` for **both** users — the opt-out workload is reachable without authz, exactly as designed.

**External-path demo — curl from the host through the ingressgateway:**

```bash
curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local/hello   # expect: 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local/hello   # expect: 403
```

**PCS sees only the gated traffic (alice + mallory headers), never wiki traffic:**

```bash
kubectl -n pcs logs deploy/pcs -c pcs -f
# Expected (per dashboard-client iteration):
#   {"user":"alice@workspace.test","decision":"allow","ts":"..."}
#   {"user":"mallory@workspace.test","decision":"deny","ts":"..."}
# (No log lines for wiki-api traffic — it never calls PCS.)
```

**Envoy sidecars are present everywhere they should be:**

```bash
for ns in documents wiki dashboard pcs; do
  echo "=== $ns ==="
  kubectl -n $ns get pod -o jsonpath='{range .items[*]}{.metadata.name}{": "}{.spec.containers[*].name}{"\n"}{end}'
done
# Expected: every pod shows two containers (the app + istio-proxy)
```

**EnvoyFilter is in `istio-system`, not in the app namespace:**

```bash
kubectl -n istio-system get envoyfilter ext-authz-pcs
# Expected: returns a row showing the resource

kubectl -n documents get envoyfilter
# Expected: No resources found in documents namespace.
```

**Opt-in label is present on `documents-api`, absent on `wiki-api`:**

```bash
kubectl -n documents get deploy documents-api -o jsonpath='{.spec.template.metadata.labels.workspace\.io/ext-authz}'
# Expected: enabled

kubectl -n wiki get deploy wiki-api -o jsonpath='{.spec.template.metadata.labels.workspace\.io/ext-authz}'
# Expected: (empty)
```

**Headline observable property — denials happen at Envoy, not in `documents-api`:**

```bash
kubectl -n documents logs deploy/documents-api -c documents-api | grep -c mallory
# Expected: 0

kubectl -n documents logs deploy/documents-api -c documents-api | grep -c alice
# Expected: > 0
```

**Opt-out observable property — `wiki-api` sees both users:**

```bash
kubectl -n wiki logs deploy/wiki-api -c wiki-api | grep -c mallory
# Expected: > 0   (mallory reached wiki-api because no ext_authz check fired)

kubectl -n wiki logs deploy/wiki-api -c wiki-api | grep -c alice
# Expected: > 0
```

**Fail-closed proof:**

```bash
kubectl -n pcs scale deploy/pcs --replicas=0
sleep 5
kubectl -n dashboard logs deploy/dashboard-client --tail=20
# Expected: every documents-api call returns 403 (regardless of user);
# every wiki-api call still returns 200 (opt-out unaffected by PCS health).

# External path under fail-closed:
curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local/hello   # expect: 403

kubectl -n pcs scale deploy/pcs --replicas=1
# Wait for rollout; afterwards alice's documents-api requests return 200 again.
```

## 7. Directory Layout

```text
~/ashwini-repos/workspace/
├── docs/superpowers/specs/
│   ├── 2026-05-13-workspace-kind-harness-design.md             (existing — unchanged)
│   ├── 2026-05-13-permission-validation-phase1-sidecar-design.md (existing — unchanged)
│   └── 2026-05-14-ext-authz-kind-demo-design.md                (this document)
├── sample-apps/
│   ├── echo-server/                       (shared by documents-api and wiki-api)
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
    └── manifests/
        ├── platform/                                ← platform-team-owned in production
        │   ├── namespace-pcs.yaml
        │   ├── envoyfilter-ext-authz-pcs.yaml       (lives in istio-system)
        │   ├── pcs-deployment.yaml
        │   └── pcs-service.yaml
        └── apps/                                    ← app-team-owned in production
            ├── namespace-documents.yaml
            ├── namespace-wiki.yaml
            ├── namespace-dashboard.yaml
            ├── documents/
            │   ├── istio-gateway-values.yaml        (Helm values for documents-ingressgateway)
            │   ├── documents-api-deployment.yaml    (carries opt-in label)
            │   ├── documents-api-service.yaml
            │   ├── documents-gateway.yaml
            │   └── documents-virtualservice.yaml
            ├── wiki/
            │   ├── wiki-api-deployment.yaml         (no opt-in label)
            │   └── wiki-api-service.yaml
            └── dashboard/
                └── dashboard-client-deployment.yaml
```

The `manifests/platform/` vs `manifests/apps/` split mirrors the ownership boundary documented in §3.2. `setup.sh` applies both for demo convenience; production reality is that the two halves are deployed by different pipelines owned by different teams.

## 8. Trade-offs and Risks

### 8.1 EnvoyFilter is the "old" pattern

Istio documentation recommends `AuthorizationPolicy` with `action: CUSTOM` and a registered `extensionProvider` for new clusters. `EnvoyFilter` patches raw Envoy config and is brittle to Envoy version changes. The demo chooses `EnvoyFilter` deliberately to fit the demo's scope (a single declarative file that platform owns; no `MeshConfig` editing required). Migration to the mesh-level provider pattern is straightforward (see §9.1).

### 8.2 Blast radius of the mesh-wide EnvoyFilter

A misconfigured EnvoyFilter in `istio-system` affects every opted-in workload in the cluster simultaneously. This is a deliberate trade-off — single source of truth versus single point of failure. Mitigations in production: a staged rollout (apply to a canary workload first via a narrowing label, e.g. `workspace.io/ext-authz: canary`, then flip), and standard mesh-level change control (the EnvoyFilter goes through the platform team's GitOps PR review). The demo does not exercise these controls; in the kind harness it is one operator with one `kubectl apply`.

### 8.3 Hard-coded allow-list in `pcs`

The decision policy lives in Go code, not in a config file or database. This is fine for a demo where the policy is two usernames. A real policy service would consult a registry. The demo's `POST /check` contract is identical to what a production service would expose, so the dummy is a drop-in replacement target.

### 8.4 Header-based identity, ignoring Istio mesh identity

All app namespaces are sidecar-injected, so each pod has a SPIFFE identity and traffic is mesh-mTLS by default. The demo's `ext_authz` decision **ignores** this identity and uses only the `x-workspace-user-id` request header. This is intentional: it keeps the authorization input under the test driver's control (just set the header) and reflects the production model where the authorization service trusts upstream-set headers rather than mesh peer identity. A future enhancement could derive identity from a verified JWT instead (see §9.5).

### 8.5 Single-replica everything

Every Deployment (`documents-api`, `wiki-api`, `pcs`, `dashboard-client`, `documents-ingressgateway`) runs one replica. Multi-replica routing, EnvoyFilter propagation under churn, and pod-restart behavior are not exercised. Acceptable for a correctness demo; load and HA characteristics are out of scope per §2.2.

### 8.6 Headers, not body, drive the decision

Envoy `ext_authz` forwards request **headers** to the authz service by default. The `authorization_request.allowed_headers` allow-list determines what `pcs` sees. Body-based decisions are possible (`with_request_body`) but add buffering, latency, and configuration weight. The demo deliberately restricts the decision input to headers.

### 8.7 Plain HTTP through the ingressgateway

The ingressgateway listens on port 80 with plain HTTP. Adding TLS termination requires a TLS Secret, a `tls.mode: SIMPLE` on the Gateway, and self-signed certs at setup time — the ECK harness's pattern. Out of scope for the MVP demo; a follow-up can layer TLS without touching the ext_authz logic.

### 8.8 Opt-in label is convention, not enforced

A team can forget to add `workspace.io/ext-authz: enabled` to their Deployment and silently run unprotected. The mesh-wide EnvoyFilter does nothing for them. Mitigations in production: a CI check that validates platform-tier Deployments carry the label, or a Phase 2 mutating webhook that adds the label based on namespace classification (see §9.6).

## 9. Future Enhancements (explicitly not in scope)

These are listed so the design surface stays clear and a follow-up spec can pick any of them up.

### 9.1 Migrate to mesh-level `extensionProvider` + `AuthorizationPolicy`

Replace the `EnvoyFilter` with:

- An `extensionProvider` entry in `meshConfig` (registered at Istio install time via Helm values or by editing the `istio` ConfigMap in `istio-system`).
- An `AuthorizationPolicy` in `istio-system` with `action: CUSTOM`, `provider.name` matching the registered provider, and a `selector` matching `workspace.io/ext-authz: enabled`.

The `pcs` service contract is unchanged. The migration is a YAML swap with no app code changes.

### 9.2 gRPC ext_authz protocol

Switch from HTTP `POST /check` to Envoy's gRPC `CheckRequest`. Production deployments commonly prefer gRPC for typed contracts and lower per-request overhead. The `pcs` service would gain a gRPC server and be tested with a separate verification path.

### 9.3 Cluster-level baseline policies

Layer `allow-ip`, `allow-metrics`, and `allow-specific-namespace` `AuthorizationPolicy` resources at the mesh layer to demonstrate defense-in-depth alongside ext_authz. Useful when teaching how Istio's L4/L7 mesh authorization composes with delegated ext_authz checks.

### 9.4 Real auth (JWT) in front of `x-workspace-user-id`

Add a `RequestAuthentication` resource targeting `workspace.io/ext-authz: enabled` Pods, requiring a signed JWT, and have the `ext_authz` decision derive identity from the JWT claims instead of a raw header.

### 9.5 TLS termination on the ingressgateway

Add a self-signed cert generated at setup time, mount it as a TLS Secret in `documents` namespace, and switch the Gateway's `port.protocol` to `HTTPS` with `tls.mode: SIMPLE`. The host port 443 is already mapped in `kind-config.yaml` for this purpose.

### 9.6 Mutating webhook for label auto-injection

Replace the manual `workspace.io/ext-authz: enabled` label step with a mutating admission webhook that adds the label automatically based on namespace classification (e.g. all Pods in tier-1 namespaces get the label unless explicitly excluded). Improves onboarding ergonomics but adds operational moving parts (TLS cert for the webhook, CA bundle, webhook server HA). Tracked in the sibling Phase 1 sidecar design under "auto-injection."

## 10. Open Questions

1. **Opt-in label key.** The demo uses `workspace.io/ext-authz: enabled`. If the workspace platform prefers a different convention (e.g. `platform.workspace.io/authz: required`), align before implementation since the same string appears in three places (Deployment Pod templates, the EnvoyFilter `workloadSelector`, and verification commands).
2. **Demo cluster name.** The script defaults to `ext-authz-demo`. If another demo already uses that name on the same machine, `setup.sh` will reuse the existing cluster rather than recreating. Acceptable; `teardown.sh` removes it cleanly.
3. **Hostname for external curl.** Default is `documents.local`. If `documents.local` collides with anything the user has configured locally, swap for a less-common name.

## 11. Success Criteria

The harness is considered successful when, on a fresh checkout of `~/ashwini-repos/workspace/`:

1. `./kind/setup.sh` runs to completion in ≤ 3 minutes on a 16 GB MacBook with Docker Desktop allocated ≥ 6 GB.
2. After adding `127.0.0.1 documents.local` to `/etc/hosts`:
   - `curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local/hello` returns `200`.
   - `curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local/hello` returns `403`.
3. `kubectl -n dashboard logs deploy/dashboard-client -f` shows, every 3 seconds: `documents-api alice → 200`, `documents-api mallory → 403`, `wiki-api alice → 200`, `wiki-api mallory → 200` — confirming both the gate (documents-api) and the opt-out (wiki-api).
4. `kubectl -n pcs logs deploy/pcs -c pcs -f` shows allow/deny lines only for `documents-api` traffic; no lines for `wiki-api` traffic.
5. `kubectl -n documents logs deploy/documents-api -c documents-api | grep -c mallory` returns `0`; the equivalent grep for `alice` returns a positive number.
6. `kubectl -n wiki logs deploy/wiki-api -c wiki-api | grep -c mallory` returns a positive number — proof that opt-out workloads are not gated.
7. `kubectl -n istio-system get envoyfilter ext-authz-pcs` returns the single EnvoyFilter, in the root namespace.
8. `kubectl -n pcs scale deploy/pcs --replicas=0` causes all `documents-api` calls (internal and external) to return `403`; `wiki-api` calls continue to return `200`. Restoring the replica brings allow decisions back.
9. `./kind/teardown.sh` removes the cluster cleanly.

The harness deliberately does not target throughput, latency, or any production-grade SLO. It is a correctness and teaching harness for the Envoy ext_authz wiring and the platform-vs-app-team ownership pattern.

## 12. References

- Envoy `ext_authz` HTTP filter reference: <https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_authz_filter>
- Istio `EnvoyFilter` reference (including root-namespace scoping rules): <https://istio.io/latest/docs/reference/config/networking/envoy-filter/>
- Istio `AuthorizationPolicy` reference (for the §9.1 future migration): <https://istio.io/latest/docs/reference/config/security/authorization-policy/>
- Istio `Gateway` + `VirtualService` reference: <https://istio.io/latest/docs/reference/config/networking/gateway/>
- ECK Elasticsearch kind harness — structural template: `charts/elasticsearch/kind/` on branch `claude/eck-elasticsearch-setup-VCAsc` of `hmchangw/chat`.
- Sibling spec: `2026-05-13-permission-validation-phase1-sidecar-design.md` — the broader Phase 1 sidecar design this demo exercises.
