# Permission Validation Phase 1 — Sidecar Implementation Design

> **Status:** Draft for review
> **Audience:** Platform team implementing Phase 1 of the Permission Validation Flow
> **Owner:** Ashwini (platform — sidecar component)
> **Related documents:**
> - [`prd/permission-validation/PRD.md`](../../../prd/permission-validation/PRD.md) — full PRD with SLOs and constraints
> - [`prd/permission-validation/phase-1-user-stories.md`](../../../prd/permission-validation/phase-1-user-stories.md) — PV1-001 through PV1-012 user stories
> - [`prd/permission-validation/phase-1-architecture.md`](../../../prd/permission-validation/phase-1-architecture.md) — component diagram and data flow
> - [`prd/permission-validation/user-stories.md`](../../../prd/permission-validation/user-stories.md) — full backlog (Phase 2+ stories are out of scope here)

---

## 1. Background and Scope

The Permission Validation Flow PRD describes a per-request authorization gate that sits in front of application services. Phase 1 (per `phase-1-user-stories.md`) delivers the minimum production-grade gate: a sidecar that intercepts HTTP traffic to an app, decrypts a sealed authorization context the client carries in headers, calls the existing **Permission Checking Service** (PCS) to obtain an allow/deny decision, and either forwards the request to the app backend or rejects with `403`.

This spec covers **points 4 through 7 of the Phase 1 Scope** — the sidecar implementation itself. Points 1–3 (Access Management API issuing the encrypted context and plain permission list to the client; the client carrying those headers on the action request) are owned by other teams and are pre-conditions for this work.

PV1-001 — the *Envoy `ext_authz` vs. custom sidecar* decision — is resolved in this spec by **supporting both deployment shapes off a single shared core**. App teams running on Istio-enabled namespaces use the Envoy `ext_authz` shell; teams without Istio use the standalone HTTP-proxy shell. The authorization logic is identical in both modes.

## 2. Goals and Non-Goals

### 2.1 Goals

- Build a Go-implemented sidecar that intercepts HTTP requests, validates them through the Phase 1 flow, and forwards or rejects per Permission Checking Service's decision.
- Meet the PRD's SLOs: **5,000 RPS** per app cluster, **P95 added latency ≤ 10 ms**, **99.99% availability**.
- Support deployment **with or without Istio** so adoption is not blocked on mesh rollout.
- Implement PV1-004 (route config), PV1-005 (route match), PV1-006 (header extract), PV1-007 (decrypt + validate context), PV1-008 (PCS request build), PV1-009 (decision enforce), PV1-010 (SRE metrics), PV1-011 (integration tests), PV1-012 (onboarding example).
- Fail-closed by default; explicit `403` for any rejection (header missing, decryption failed, PCS error, etc.).

### 2.2 Non-Goals (out of Phase 1 — listed in `phase-1-user-stories.md` §Out of Scope)

| Out of scope item | Deferred to |
|---|---|
| Decision caching | Phase 2 (PV-021–PV-025) |
| Event-driven cache invalidation | Phase 2 (PV-024) |
| Body / query / path-parameter context extraction | Phase 2/3 (PV-009–PV-011) |
| Fail-open behavior for low-risk routes | Phase 3 (PV-027) |
| Distributed tracing | Phase 2 (PV-030) |
| Detailed audit logging beyond metrics | Phase 2 (PV-031) |
| Per-route cache behavior | Phase 2 (PV-023) |
| Automatic key rotation | Phase 2/3 |
| Cross-checking decrypted `objectId` vs URL path | Phase 2+ |

These are listed not to undersell the eventual platform but to keep Phase 1 review focused on the minimum-viable gate.

## 3. Architecture

The system is **one shared core library** packaged into **two thin binary shells**, each appropriate to a different deployment environment.

```text
                    ┌────────────────────────────────────────────────┐
                    │           SHARED CORE (Go package)             │
                    │           pkg/authzcore                        │
                    │                                                │
                    │   RouteMatcher        (PV1-004, PV1-005)       │
                    │   HeaderExtractor     (PV1-006)                │
                    │   ContextDecryptor    (PV1-007)                │
                    │   PCSClient           (PV1-008)                │
                    │   DecisionEnforcer    (PV1-009)                │
                    │   MetricsEmitter      (PV1-010)                │
                    │                                                │
                    │   Single function signature consumed by shells:│
                    │     Check(ctx, AuthZReq) → AuthZDecision       │
                    └──────────┬──────────────────────┬──────────────┘
                               │                      │
                  ┌────────────▼─────────┐  ┌─────────▼─────────────┐
                  │  authz-grpc          │  │  authz-http           │
                  │  (binary shell #1)   │  │  (binary shell #2)    │
                  │                      │  │                       │
                  │  Implements Envoy    │  │  httputil.ReverseProxy│
                  │  ext_authz v3 gRPC   │  │  intercepts all HTTP  │
                  │  service             │  │                       │
                  │  Bound to 127.0.0.1  │  │  Forwards on Allow    │
                  │                      │  │  Returns 403 on Deny  │
                  │                      │  │                       │
                  │  For Istio clusters  │  │  For non-Istio        │
                  └──────────────────────┘  └───────────────────────┘
```

### 3.1 Deployment in Istio-enabled namespace

App pod contains three containers: the existing Istio Envoy sidecar (`istio-proxy`), our `authz-grpc` sidecar, and the app. Istio's Envoy is configured (via a namespace-level `AuthorizationPolicy` plus an `EnvoyFilter` or the `extensionProviders` mechanism in Istio 1.20+) to call `authz-grpc` over gRPC on `127.0.0.1:8181` for every protected route.

```text
  ┌─────────────── App Pod ────────────────┐
  │  istio-proxy (Envoy sidecar)           │
  │     │                                  │
  │     │ ext_authz gRPC → 127.0.0.1:8181  │
  │     ▼                                  │
  │  authz-grpc                            │
  │     │ uses pkg/authzcore               │
  │     │ calls PCS over the network       │
  │     │                                  │
  │     │ Allow → Envoy forwards           │
  │     │ Deny  → Envoy returns 403        │
  │     ▼                                  │
  │  app (listens on its usual port)       │
  └────────────────────────────────────────┘
```

### 3.2 Deployment in non-Istio namespace

App pod contains two containers: `authz-http` and the app. Traffic from the K8s `Service` is routed to `authz-http`'s port (e.g., 8080); the app listens on a different port (e.g., `127.0.0.1:8088`) reachable only from the sidecar. `authz-http` reverse-proxies allowed requests to the app and short-circuits denied ones with `403`.

```text
  ┌─────────────── App Pod ────────────────┐
  │                                        │
  │  authz-http  (listens 0.0.0.0:8080)    │
  │     │                                  │
  │     │ uses pkg/authzcore               │
  │     │ calls PCS over the network       │
  │     │                                  │
  │     │ Allow → reverse proxy            │
  │     │ Deny  → 403                      │
  │     ▼                                  │
  │  app (listens 127.0.0.1:8088)          │
  └────────────────────────────────────────┘
```

The K8s `Service`'s `targetPort` points to `authz-http`'s port. The app's port stays internal to the pod.

## 4. Shared Core (`pkg/authzcore`)

This is where all the actual authorization logic lives. Both shells delegate to it.

### 4.1 Public surface

```go
package authzcore

type Config struct {
    RouteConfigPath    string         // path to YAML route config (PV1-004)
    AppCredentialsDir  string         // dir containing keyId → 32-byte AES key files
    PCSEndpoint        string         // base URL of Permission Checking Service
    PCSTimeout         time.Duration  // per-request timeout, default 500ms
    Headers            HeaderConfig   // configurable header names
    Logger             *slog.Logger
    Registry           prometheus.Registerer
}

type Engine struct { /* … */ }

// Construct an Engine. Reads route config, loads app credentials, builds
// the PCS HTTP client. Returns an error if any precondition fails — both
// shells exit non-zero on construction failure (config error, not runtime).
func NewEngine(cfg Config) (*Engine, error)

// Check is the single entry point both shells call.
// req carries the HTTP method, path, and the three required headers
// (or the gRPC-form thereof). Returns a Decision the shell renders into
// either an HTTP response (authz-http) or an Envoy CheckResponse (authz-grpc).
func (e *Engine) Check(ctx context.Context, req AuthZRequest) AuthZDecision

type AuthZRequest struct {
    Method            string
    Path              string
    SSOToken          string
    EncryptedContext  string
    RequestedAction   string
    RequestID         string  // for log/metric correlation
}

type AuthZDecision struct {
    Outcome           Outcome  // Allow | Deny | Skip
    HTTPStatus        int      // 200 (Allow/Skip) or 403 (Deny)
    DecisionID        string   // uuid for correlation
    RejectionReason   string   // internal-only; never sent to client
}
```

### 4.2 Route Matcher (PV1-004, PV1-005)

**Config schema** (YAML, mounted via ConfigMap):

```yaml
# /etc/authz/routes.yaml
defaultBehavior: deny     # unmatched routes → deny (PV1-006-compatible)

skipped:
  - method: GET
    pathPattern: /healthz
  - method: GET
    pathPattern: /metrics
  - method: GET
    pathPattern: /static/*       # static assets

protected:
  - method: POST
    pathPattern: /api/v1/documents
  - method: PUT
    pathPattern: /api/v1/documents/*
  - method: DELETE
    pathPattern: /api/v1/documents/*
```

- **Matching order:** `skipped` rules checked first, then `protected`. First match wins. No-match falls through to `defaultBehavior`.
- **Pattern syntax:** glob with `*` (single path segment) and `**` (multiple segments). Regex explicitly avoided — too easy to write a catastrophic backtrack.
- **Method:** case-insensitive exact match. `*` allowed to mean any method.
- **Hot reload:** Engine watches the config file with `fsnotify`; on change, builds a new matcher atomically and swaps the pointer. Existing in-flight checks finish on the old matcher (no blocking on reload).

### 4.3 Header Extractor (PV1-006)

Three required headers for protected routes. Names are configurable for flexibility but have sane defaults:

| Purpose | Default header name | Required for protected routes? |
|---|---|---|
| SSO token | `Authorization` (with `Bearer ` prefix) | Yes |
| Encrypted authorization context | `X-Auth-Context` | Yes |
| Requested action | `X-Requested-Action` | Yes |

Missing or malformed (empty after trim) header → `Decision{Outcome: Deny, RejectionReason: "header_missing"}`, metric increment, return early.

The SSO token is **forwarded** to PCS as a header (PV1-008); it is **never decoded or validated** by the sidecar. Identity is PCS's concern.

### 4.4 Context Decryptor (PV1-007)

**Encryption format:** **AES-256-GCM**, version-prefixed binary, then base64-URL-encoded for header transport. AEAD construction satisfies the "authenticated encryption — detect tampering" requirement in `phase-1-user-stories.md` §PV1-003.

**Wire format:**

```text
Base64URL-encoded bytes:
┌──┬────────────┬──────────────────┬──────────────┐
│v1│ keyId (16B)│ nonce (12B GCM)  │ ciphertext+tag│
└──┴────────────┴──────────────────┴──────────────┘
 │
 └ version byte = 0x01 (so we can rev the scheme later)
```

The plaintext is JSON:

```json
{
  "appId": "documents-app",
  "objectId": "doc-789",
  "objectType": "document",
  "issuedAt": "2026-05-13T10:00:00Z",
  "expiresAt": "2026-05-13T10:05:00Z"
}
```

**Validation steps after decrypt (PV1-007 acceptance criteria):**

1. JSON-parse plaintext. Parse failure → `Deny`, `reason: "context_malformed"`.
2. Required fields present? (`appId`, `objectId`, `objectType`, `issuedAt`, `expiresAt`.) Missing → `Deny`, `reason: "context_incomplete"`.
3. `appId` matches the locally-configured `APP_ID` env var (audience check). Mismatch → `Deny`, `reason: "context_audience"`.
4. `expiresAt` > now (with 30 s clock-skew tolerance). Expired → `Deny`, `reason: "context_expired"`.
5. `issuedAt` ≤ now + 30 s. Future-dated → `Deny`, `reason: "context_future"`.

**Key lookup:**

- The `keyId` field in the header (the 16 bytes after the version byte) selects which key file to use.
- Keys live at `${AppCredentialsDir}/<hex-keyId>.key` — each file is exactly 32 bytes (raw AES-256 key, no PEM).
- Keys are loaded into memory at startup; the directory is `fsnotify`-watched so adding a new key file picks it up without restart. (For Phase 1, normally only one key per app exists; rotation is Phase 2 work but the format already supports it.)

**Decryption failure modes:**

| Cause | Reason code | Decision |
|---|---|---|
| Base64 decode fails | `context_encoding` | Deny |
| Unknown version byte | `context_version` | Deny |
| Unknown `keyId` | `context_unknown_key` | Deny |
| GCM auth tag mismatch (tampered or wrong key) | `context_tampered` | Deny |
| Plaintext not valid JSON | `context_malformed` | Deny |
| Audience / expiry checks fail | as above | Deny |

All Deny outcomes return the **same** `403` to the caller. The reason code goes only to logs and metrics — never to the response body — to avoid leaking validation oracles.

### 4.5 PCS Client (PV1-008)

**Outbound request:**

```http
POST /check HTTP/1.1
Host: permission-checking.platform.svc.cluster.local
Authorization: Bearer <SSO token forwarded verbatim>
Content-Type: application/json
X-Request-ID: <propagated from inbound request or generated>

{
  "objectId":    "doc-789",
  "objectType": "document",
  "permission":  "edit"
}
```

`objectId` and `objectType` come from **decrypted context** (never from URL/body/query — that's the Phase 1 invariant). `permission` comes from the `X-Requested-Action` header verbatim.

**Client config:**

- `net/http` with `Transport.MaxIdleConnsPerHost = 100`, keep-alive on, HTTP/2 on (Go default).
- Per-request timeout: **500 ms** (PRD targets 10 ms P95 added latency; 500 ms is the hard cap to prevent infinite hang).
- **No retries** in Phase 1. The chat repo uses Resty with retries elsewhere; for an auth gate, retrying a denied/error request risks subtle race conditions and doubles load on PCS. Phase 2 may add a single retry with a 50 ms budget when we have data showing it helps.
- Circuit breaker: deferred to Phase 2 (PV-018).

**Response handling:**

| PCS response | Decision | Reason |
|---|---|---|
| `200 OK` with `{"allowed": true}` | Allow | `granted` |
| `200 OK` with `{"allowed": false}` | Deny | `pcs_denied` |
| `4xx` (client error) | Deny | `pcs_bad_request` (logs the full body once; alert-worthy — implies request shape is broken) |
| `5xx` or timeout | Deny | `pcs_unavailable` (fail-closed per PRD §5.5) |
| Connection refused / DNS failure | Deny | `pcs_unreachable` |

### 4.6 Decision Enforcer (PV1-009)

Trivial in the core — it's just the final `AuthZDecision` returned to the shell. The shell renders the decision:

- `authz-grpc`: maps Allow → `OK` (Envoy proceeds), Deny → `PERMISSION_DENIED` (Envoy returns 403)
- `authz-http`: maps Allow → reverse-proxy to upstream app, Deny → write `403` directly with body `{"error":"permission_denied","decisionId":"<uuid>"}`

Decision body is **identical for every deny reason** — no leak of which check failed.

### 4.7 Metrics Emitter (PV1-010)

**Prometheus**, exposed on `127.0.0.1:9090/metrics` by the shell (separate port from the gRPC/HTTP listener so admin/scrape traffic is isolated).

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `authz_requests_total` | counter | `outcome=allow\|deny\|skip\|error`, `route_class` | Total requests by outcome |
| `authz_rejections_total` | counter | `reason=context_expired\|context_tampered\|...` | Why we rejected |
| `authz_latency_seconds` | histogram | `outcome` | End-to-end sidecar latency (target P95 ≤ 10 ms) |
| `authz_pcs_latency_seconds` | histogram | `result=allow\|deny\|error` | PCS call latency |
| `authz_pcs_failures_total` | counter | `kind=timeout\|5xx\|4xx\|unreachable` | PCS failure breakdown |
| `authz_header_errors_total` | counter | `header=sso\|context\|action` | Missing/malformed headers |
| `authz_decrypt_failures_total` | counter | `kind=encoding\|version\|unknown_key\|tampered\|malformed` | Decryption-stage failures |
| `authz_route_match_total` | counter | `class=protected\|skipped\|unmatched` | Route classification distribution |
| `authz_config_reloads_total` | counter | `source=routes\|credentials`, `outcome=success\|failure` | Hot-reload health |

**Label cardinality discipline:** `route_class` is a coarse bucket (`document_api`, `user_profile`, etc.) configured in the route YAML — never raw path. Full path on a label would explode cardinality.

### 4.8 Logging

JSON to stdout via `log/slog`. Mandatory fields per log line: `ts`, `level`, `msg`, `request_id`, `decision_id`, `outcome`, `reason`, `method`, `route_class`. **Never logged:** SSO token, encrypted context contents, decrypted plaintext, AES keys, PCS response bodies beyond status.

Log levels:

- `INFO`: every decision (one line). High volume by design — sampling is Phase 2.
- `WARN`: PCS 5xx/timeout, decrypt failure, hot-reload partial failure.
- `ERROR`: shell-level errors (listener can't bind, config file disappears).

## 5. `authz-grpc` Shell — Envoy `ext_authz` Integration

### 5.1 gRPC service

Implements the Envoy v3 ext_authz service:

```proto
service Authorization {
  rpc Check(CheckRequest) returns (CheckResponse);
}
```

The shell translates Envoy's `CheckRequest` (which contains `attributes.request.http` with method, path, headers, request_id) into an `AuthZRequest`, calls `engine.Check(...)`, and renders the `CheckResponse`:

- Allow → `status: { code: OK }` with `ok_response` (optional headers to inject upstream — none in Phase 1)
- Deny → `status: { code: PERMISSION_DENIED }` with `denied_response: { status: { code: 403 }, body: {"error":"permission_denied"...}, headers: [{key: X-Decision-Id, value: <uuid>}] }`

### 5.2 Listener

- gRPC on `127.0.0.1:8181` — **loopback only**, no other pod can reach this sidecar's authz endpoint.
- Plain gRPC (no TLS) — traffic is in-pod loopback.
- Health check: gRPC standard `grpc.health.v1.Health` service so Envoy/Istio can probe.

### 5.3 Istio integration

App teams configure a namespace-level `AuthorizationPolicy`:

```yaml
apiVersion: security.istio.io/v1
kind: AuthorizationPolicy
metadata:
  name: workspace-authz
  namespace: chat
spec:
  selector:
    matchLabels:
      authz.workspace.io/enabled: "true"   # apps opt-in by label
  action: CUSTOM
  provider:
    name: workspace-authz-grpc            # registered in MeshConfig
  rules:
  - to:
    - operation:
        notPaths: ["/healthz", "/metrics"]  # match the YAML skipped routes
```

And the platform team registers the extension provider once in `istio-system`:

```yaml
apiVersion: install.istio.io/v1alpha1
kind: IstioOperator
spec:
  meshConfig:
    extensionProviders:
    - name: workspace-authz-grpc
      envoyExtAuthzGrpc:
        service: "127.0.0.1:8181"
        port: 8181
```

App teams' Deployment YAML adds the `authz-grpc` sidecar container.

## 6. `authz-http` Shell — Standalone HTTP Sidecar

### 6.1 Reverse proxy

Built on `net/http/httputil.ReverseProxy`. Two-step flow per request:

1. Build an `AuthZRequest` from the inbound `*http.Request` (method, path, three headers).
2. Call `engine.Check(...)`.
3. On `Allow`/`Skip`, hand the request to `ReverseProxy.ServeHTTP` targeting the upstream app URL.
4. On `Deny`, write `403` with the standard body and `X-Decision-Id` response header.

### 6.2 Listener

- HTTP on `0.0.0.0:8080` — pod-external; K8s `Service` routes here.
- Upstream URL: `http://127.0.0.1:<APP_PORT>` — configurable via env, default `127.0.0.1:8088`.
- TLS termination: **out of scope for Phase 1**. The K8s `Service` and any ingress in front handle TLS; loopback to the app is plain HTTP.

### 6.3 K8s wiring

Service points to the sidecar:

```yaml
apiVersion: v1
kind: Service
metadata:
  name: documents-app
spec:
  selector:
    app: documents
  ports:
  - name: http
    port: 80
    targetPort: 8080    # → authz-http, not the app
```

App Deployment's two containers: `authz-http` on 8080, app on 8088. Both `containerPort`s declared so probes work.

## 7. Configuration Surface

### 7.1 Environment variables (both shells)

| Var | Default | Required | Purpose |
|---|---|---|---|
| `APP_ID` | — | yes | Used as audience check for decrypted context |
| `ROUTE_CONFIG_PATH` | `/etc/authz/routes.yaml` | no | Path to route config |
| `APP_CREDENTIALS_DIR` | `/etc/authz/credentials` | no | Directory of `<keyId>.key` files |
| `PCS_ENDPOINT` | — | yes | Base URL of Permission Checking Service |
| `PCS_TIMEOUT` | `500ms` | no | Per-request PCS timeout |
| `LISTEN_ADDR` | `:8181` (grpc) / `:8080` (http) | no | Shell-specific listener |
| `UPSTREAM_URL` | `http://127.0.0.1:8088` | http shell only | Where to forward Allowed requests |
| `METRICS_ADDR` | `:9090` | no | Prometheus metrics listener |
| `LOG_LEVEL` | `INFO` | no | `DEBUG\|INFO\|WARN\|ERROR` |
| `SSO_HEADER_NAME` | `Authorization` | no | Configurable for non-standard setups |
| `CONTEXT_HEADER_NAME` | `X-Auth-Context` | no | Same |
| `ACTION_HEADER_NAME` | `X-Requested-Action` | no | Same |

Parsed with `caarlos0/env` matching chat repo convention. Missing required vars → log fatal, exit 1.

### 7.2 ConfigMap (route config)

Mounted at `ROUTE_CONFIG_PATH`. Schema documented in §4.2. App teams own the content; platform team owns the schema.

### 7.3 Secret (app credentials)

Mounted at `APP_CREDENTIALS_DIR` as a directory of files, one per key:

```text
/etc/authz/credentials/
├── 0a1b2c3d4e5f60718293a4b5c6d7e8f9.key    (32 bytes raw)
└── (future keys for rotation)
```

Mode `0400`, sidecar process runs as non-root UID. Secret provisioning is out of this spec's scope — platform admins/Access Management provision them at app registration.

## 8. Failure Mode Catalogue (Consolidated)

Defense-in-depth: a request must clear every check to be forwarded. Any failure → `403` with body `{"error":"permission_denied","decisionId":"<uuid>"}` (same shape every time).

| Stage | Check | On failure | Logged reason | Metric label |
|---|---|---|---|---|
| Route | Matches protected/skipped? | If `defaultBehavior=deny` and no match → deny | `route_unmatched` | `outcome=deny`, `route_class=unmatched` |
| Header | SSO present? | deny | `header_missing_sso` | `header=sso` |
| Header | Context present? | deny | `header_missing_context` | `header=context` |
| Header | Action present? | deny | `header_missing_action` | `header=action` |
| Decrypt | Base64 decodes? | deny | `context_encoding` | `decrypt=encoding` |
| Decrypt | Version supported? | deny | `context_version` | `decrypt=version` |
| Decrypt | KeyId known? | deny | `context_unknown_key` | `decrypt=unknown_key` |
| Decrypt | GCM auth passes? | deny | `context_tampered` | `decrypt=tampered` |
| Decrypt | JSON parses? | deny | `context_malformed` | `decrypt=malformed` |
| Validate | Required fields? | deny | `context_incomplete` | `decrypt=malformed` |
| Validate | `appId` audience? | deny | `context_audience` | `decrypt=audience` |
| Validate | `expiresAt` future? | deny | `context_expired` | `decrypt=expired` |
| Validate | `issuedAt` not future? | deny | `context_future` | `decrypt=future` |
| PCS | Reachable, ≤500ms, 2xx? | deny | `pcs_unavailable` / `pcs_unreachable` / `pcs_bad_request` | `pcs=timeout\|5xx\|4xx\|unreachable` |
| PCS | `allowed: true`? | deny | `pcs_denied` | `outcome=deny`, `pcs=denied` |
| Allow | All checks passed | forward | `granted` | `outcome=allow` |

## 9. Testing Strategy (PV1-011)

### 9.1 Unit tests (per component)

- RouteMatcher: 30+ table-driven cases covering skipped > protected precedence, glob behavior, method matching, default behavior on unmatched.
- HeaderExtractor: missing each of the three; empty/whitespace; case-insensitive header names.
- ContextDecryptor: golden vectors for valid decrypt + each failure mode (wrong key, tampered byte, expired, future, audience mismatch, malformed JSON, encoding error, version mismatch). At least one round-trip test that encrypts in the test and decrypts via the production code.
- PCSClient: fake HTTP server returning each scenario from §4.5; timeout test via httptest with controlled delay.
- Engine.Check: end-to-end against fakes — verifies the orchestration order and that the outcome matches the expected decision for each failure mode in §8.

### 9.2 Integration tests

Per PV1-011, two integration tests — one per shell:

- `authz-grpc`: spin up the shell + a fake PCS server + a tiny Envoy in test mode pointed at the shell. Send HTTP requests through Envoy; assert allow/deny/error paths.
- `authz-http`: spin up the shell + a fake PCS server + a fake upstream app. Send HTTP requests directly; assert forwarding on allow, 403 on deny, never-reached-upstream on deny.

Both use `httptest` and `testcontainers-go` (for Envoy in the gRPC case if Envoy testing proves easier in a container than as a Go subprocess).

### 9.3 Performance / SLO test

Single load test: 5,000 RPS sustained for 10 minutes against the standalone `authz-http` shell with a fake PCS returning at deterministic 1 ms latency. Pass criterion: P95 sidecar overhead (excluding PCS) ≤ 5 ms; P95 total added latency ≤ 10 ms; zero memory growth after warm-up.

The Envoy-shell load test is deferred to a Phase 1 milestone validation (PV1-001's "proof-of-concept or benchmark scope" acceptance criterion).

## 10. Onboarding Example (PV1-012)

Two minimal examples under `examples/`:

- `examples/istio/` — chat-app Deployment with `authz-grpc` sidecar, namespace `AuthorizationPolicy`, route config ConfigMap, credentials Secret, decision tree in README ending at *"Run `kubectl apply -k .` and curl the service."*
- `examples/no-istio/` — chat-app Deployment with `authz-http` sidecar, Service routing through 8080, route config + credentials.

Each example includes a small CLI tool (`cmd/encctx`) that takes plain JSON and a key file and prints a `X-Auth-Context` header value — so onboarders can verify the flow with `curl` before integrating with their real Access Management.

## 11. Repository Layout

```text
~/ashwini-repos/workspace/
├── prd/permission-validation/        (existing — unchanged)
├── docs/superpowers/specs/
│   └── 2026-05-13-permission-validation-phase1-sidecar-design.md  (this file)
├── pkg/
│   └── authzcore/                    Shared core library
│       ├── engine.go                 Engine + Check entry point
│       ├── route.go                  Route matcher + config loader
│       ├── header.go                 Header extractor
│       ├── decrypt.go                AES-256-GCM decrypt + validate
│       ├── pcs.go                    PCS HTTP client
│       ├── metrics.go                Prometheus metrics registry
│       └── *_test.go
├── authz-grpc/                       gRPC shell (Envoy ext_authz)
│   ├── main.go
│   ├── service.go                    ext_authz translator
│   ├── service_test.go
│   └── deploy/Dockerfile
├── authz-http/                       HTTP reverse-proxy shell
│   ├── main.go
│   ├── proxy.go
│   ├── proxy_test.go
│   └── deploy/Dockerfile
├── cmd/encctx/                       Encrypt context CLI for testing
│   └── main.go
└── examples/
    ├── istio/
    └── no-istio/
```

## 12. Trade-offs and Open Decisions

### 12.1 Why support both shells instead of picking one

App teams onboard at different speeds. Some namespaces have Istio; others don't. Requiring mesh adoption before Phase 1 ships would gate the rollout on someone else's work. The shared-core design makes the dual-shell cost ~1.15× the work of a single shell, not 2×. If the platform later mandates Istio everywhere, `authz-http` is a clean delete and the core is untouched.

### 12.2 Why no decision caching in Phase 1

Caching is real performance value but it introduces correctness risk (stale grant after revocation). The PRD explicitly lists "decision caching" as a Phase 2 goal (PV-021 through PV-025) with event-driven invalidation. Phase 1 ships correctness first; performance is a separate story once the gate is proven.

### 12.3 Why no retries on PCS errors

A failed PCS call could be transient (50% case) or a real backend issue (50% case). Retrying doubles load on an already-struggling PCS. For a *gate* (vs. a side effect), the safer default is fail-closed-quickly and let the client retry. Phase 2 can revisit with circuit-breaker + bounded retry once we have PCS p99 + error-rate data.

### 12.4 Why same `403` body for every reason

Different bodies for different reasons leak validation oracles (an attacker can probe which check failed). Reason codes live in our logs and metrics; the client gets only `permission_denied` + a `decisionId` for support correlation.

### 12.5 AES-256-GCM vs. JWE

JWE (RFC 7516) is the standards-compliant option and supports asymmetric variants. AES-256-GCM in our own envelope is ~30 lines of Go vs. a JWE library dependency + JSON-structured encrypted payload (larger header size). For Phase 1 with one symmetric key per app and a self-contained version byte, the simpler envelope wins on size and dependency count. Phase 2 can migrate to JWE if asymmetric is needed for federation.

### 12.6 Trade-offs deferred to PV1-001 benchmark

Per PV1-001 acceptance criteria: a small benchmark comparing `authz-grpc + Envoy` vs. `authz-http` standalone, both at 5k RPS, to confirm both meet the 10 ms P95 SLO. Expected: `authz-http` is slightly faster (one fewer process hop) but Envoy contributes value beyond Phase 1 (retry policy, mTLS, observability), so Istio-enabled namespaces still prefer it.

## 13. Mapping Back to User Stories

| User story | Spec section |
|---|---|
| PV1-001 (Envoy vs custom sidecar comparison) | §3 (dual-shell decision); §12.6 (benchmark) |
| PV1-002 (Phase 1 request contract) | §4.3, §4.5, §8 |
| PV1-003 (encrypted context format) | §4.4 (wire format, fields, validation) |
| PV1-004 (route config) | §4.2 (YAML schema) |
| PV1-005 (route match) | §4.2 (matcher behavior) |
| PV1-006 (header extract) | §4.3 |
| PV1-007 (decrypt + validate) | §4.4 |
| PV1-008 (PCS request build) | §4.5 |
| PV1-009 (decision enforce) | §4.6, §5.1, §6.1 |
| PV1-010 (SRE metrics) | §4.7 |
| PV1-011 (integration tests) | §9 |
| PV1-012 (onboarding example) | §10 |

## 14. References

- [`prd/permission-validation/PRD.md`](../../../prd/permission-validation/PRD.md) — full PRD
- [`prd/permission-validation/phase-1-user-stories.md`](../../../prd/permission-validation/phase-1-user-stories.md) — user stories driving this design
- [`prd/permission-validation/phase-1-architecture.md`](../../../prd/permission-validation/phase-1-architecture.md) — component diagram this spec implements
- Envoy `ext_authz` filter: <https://www.envoyproxy.io/docs/envoy/latest/configuration/http/http_filters/ext_authz_filter>
- Istio extension providers: <https://istio.io/latest/docs/reference/config/istio.mesh.v1alpha1/#MeshConfig-ExtensionProvider>
- AES-GCM Go stdlib: <https://pkg.go.dev/crypto/cipher#NewGCM>
