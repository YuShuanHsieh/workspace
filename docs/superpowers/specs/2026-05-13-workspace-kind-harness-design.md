# Workspace Kind Harness — MVP Design

> **Status:** Draft for review
> **Audience:** Platform team building the workspace event-mesh; engineering managers evaluating the MVP scope.
> **Related documents:** [`prd/event-driven/core_completed.md`](../../../prd/event-driven/core_completed.md), [`prd/event-driven/workspace_platform_prd.md`](../../../prd/event-driven/workspace_platform_prd.md), [`prd/event-driven/claude_reviewed.md`](../../../prd/event-driven/claude_reviewed.md).
> **Reference implementation pattern:** The ECK Elasticsearch kind harness shipped on the `claude/eck-elasticsearch-setup-VCAsc` branch of the `hmchangw/chat` repository, at `charts/elasticsearch/kind/`.

---

## 1. Background and Motivation

The workspace platform PRDs (`prd/event-driven/`) describe a multi-tenant event mesh built on NATS JetStream, where cross-app authorization is enforced at the NATS protocol layer via per-subject NATS user JWTs minted by a workspace **Auth Service**. The auth service is described as a tier-0 control-plane dependency that sits **off the data path** — sidecars consult it only at token mint and refresh time.

The auth service exists today in a private internal repository. It exposes `POST /auth`, accepts either an SSO token (for frontend users) or an app token (for backend services), a NATS user **public** nkey, and a tag/namespace pair, and returns a signed NATS user JWT scoped to that caller. The workspace's intended deployment model is multi-namespace: the platform (NATS, auth service, etc.) runs in one namespace; partner apps such as chat, drive, or wiki run in their own namespaces and reach the platform via cluster DNS.

Before scaling this to the production design described in the PRDs (multi-tenant NATS accounts, OpenFGA, audit archiver, schema registry, OpenTelemetry pipeline), the platform team needs a **local verification harness** that runs end-to-end on a single MacBook and proves the headline architectural claim. The Elasticsearch CCS harness (`charts/elasticsearch/kind/` on the referenced branch) is the structural model: a one-shot, idempotent `setup.sh`, vendored chart `.tgz`s with no `helm repo add`, a minimal Istio gateway, two sample workloads, and a verification path that exercises the platform's central feature.

## 2. Goal and Non-Goals

### 2.1 Goal

Stand up a single-node `kind` cluster that demonstrates the workspace platform's headline architectural claim end-to-end:

> Cross-app authorization is enforced at the NATS protocol layer via NATS user JWTs minted by the workspace auth service. Apps in different Kubernetes namespaces can publish and subscribe to NATS only with the permissions the auth service granted them.

Concretely, the harness must produce these observable outcomes:

1. `chat-app` running in the `chat` namespace publishes events to subject `chat.message.created` and the publishes succeed.
2. `drive-app` running in the `drive` namespace subscribes to `chat.>` and receives those events.
3. A browser-based stub running against the `fe-test` namespace calls `POST /auth` with a dev-mode SSO token, opens a NATS WebSocket connection using the returned JWT + nkey, and successfully publishes/subscribes within the user's own subject space.
4. Stripping or replacing an app's auth credentials causes NATS to refuse the publish at the protocol layer — not at any application-level check.

### 2.2 Non-Goals (deferred to Phase 2)

The PRDs describe many production capabilities that are deliberately **excluded** from this MVP. Each is straightforward to layer on top of the harness later; including any of them now would push scope past the headline claim.

| Capability | Status | Reason for deferral |
|---|---|---|
| Multi-tenant NATS Accounts (one per tenant) | Out | MVP runs one account; tenancy is orthogonal to the auth flow being proved. |
| Real OIDC for SSO tokens | Out | The stub auth service runs in dev mode and accepts account names verbatim. The contract is identical to the production code. |
| OpenFGA / SpiceDB fine-grained authorization | Out | NATS subject ACLs from the JWT cover the MVP. FGA-style relationship checks are the next authorization layer. |
| Audit archiver to S3/Parquet | Out | The harness logs allow/deny to stdout. |
| Schema registry | Out | Sample apps send free-form JSON. |
| Object storage for large payloads | Out | Sample events are small. |
| Custom mutating webhook for sidecar auto-injection | Out | Sidecars are declared in Deployment YAML for MVP. Auto-injection is a Phase 2 ergonomics improvement. |
| Vault + Vault Secrets Operator | Out | App tokens and nkey seeds are seeded directly as Kubernetes Secrets by `setup.sh`. The ES harness used Vault for prod parity; the workspace harness defers it. |
| TLS between internal services, NetworkPolicy, rate limits, OTel pipeline | Out | Production hardening. |
| Helm chart for the workspace components | Out | The harness uses plain YAML manifests until there is enough multi-environment variation to justify templating. |
| Cross-cluster federation (multi-region) | Out | Single-cluster only. |

## 3. Architecture

### 3.1 Cluster and Namespace Layout

A single-node `kind` cluster hosts every component. Namespaces partition the workload to mirror the intended production deployment model where the platform and apps are operated by different teams.

```text
                       ┌───────────────────────────────────────────────────┐
                       │                  kind cluster                     │
                       │                                                   │
  Browser  ─── wss:// ─┤  Namespace: istio-system                          │
   (FE stub)           │    istiod (control plane)                         │
                       │                                                   │
                       │  Namespace: workspace            (platform team)  │
                       │    NATS JetStream (1-node StatefulSet)            │
                       │    auth-service-stub (Deployment + Service)       │
                       │    workspace-ingressgateway (per-ns Istio GW)     │
                       │                                                   │
                       │  Namespace: chat                 (app team)       │
                       │    chat-app (Deployment)                          │
                       │      └─ permission-sidecar container in same Pod  │
                       │    Secret: chat-app-creds                         │
                       │                                                   │
                       │  Namespace: drive                (app team)       │
                       │    drive-app (Deployment)                         │
                       │      └─ permission-sidecar container in same Pod  │
                       │    Secret: drive-app-creds                        │
                       │                                                   │
                       │  Namespace: fe-test              (FE harness)     │
                       │    nginx serving the FE stub static page          │
                       └───────────────────────────────────────────────────┘
```

Host port mappings (`extraPortMappings` in `kind-config.yaml`):

| Host port | Container port | Purpose |
|---|---|---|
| 80 | 30080 | Plain HTTP to `workspace-ingressgateway` (FE stub static assets) |
| 443 | 30443 | HTTPS / WS to `workspace-ingressgateway` (auth service + NATS WebSocket) |

Required `/etc/hosts` additions:

```text
127.0.0.1  workspace.local auth.workspace.local nats.workspace.local
```

Apps in `chat` and `drive` reach the platform via cluster DNS — they never go through the ingress gateway:

- `auth-service.workspace.svc.cluster.local:8080` (HTTP)
- `nats.workspace.svc.cluster.local:4222` (NATS native TCP — but accessed via the sidecar, see §3.4)

The ingress gateway exists only to expose `POST /auth` and NATS WebSocket to the browser-based FE stub.

### 3.2 NATS Configuration Model

NATS supports decentralized auth via nkey-based JWTs in a three-level hierarchy: an **operator** signs **account** JWTs; an account's signing key signs **user** JWTs; the NATS server validates incoming connections against the operator and account JWTs it has been configured to trust.

For the MVP the harness uses a single operator and a single account. The operator is created at setup time with `nsc`; the account signing seed is mounted as a Kubernetes Secret in the `workspace` namespace and read by the auth service. This matches the configuration model of the existing chat auth service (`AUTH_SIGNING_KEY` environment variable), so the production workspace auth service can be substituted into the harness without changing the NATS configuration.

```text
nsc add operator workspace-op
nsc add account  workspace-app
nsc add user     bootstrap   # used only for nats-server self-check; not handed to apps
```

The resulting artifacts are wired into the cluster as follows:

| Artifact | Storage | Consumer |
|---|---|---|
| Operator JWT | ConfigMap `nats-config` in `workspace` ns | `nats-server.conf` inline |
| Account JWT | ConfigMap `nats-config` in `workspace` ns | `nats-server.conf` via local file resolver |
| Account **signing seed** (`SAA…`) | Secret `auth-signing-key` in `workspace` ns | `auth-service-stub` env `AUTH_SIGNING_KEY` |
| Per-app user nkey **seed** (`SU…`) | Secret `<app>-creds` in each app ns | Permission sidecar |
| Per-app `app-token` | Secret `<app>-creds` in each app ns | Permission sidecar |

User JWTs are minted on demand by the auth service when the sidecar calls `POST /auth`. NATS server validates each user JWT against the account public key it already trusts, then enforces the publish/subscribe ACL claims at the protocol layer on every PUB and SUB command.

### 3.3 Auth Service Contract (stub)

The harness ships a stub auth service that mirrors the contract of the production auth service. It is modeled on the chat repository's `auth-service` (Go + Gin + `nats-io/jwt/v2` + `nats-io/nkeys`) and extends it with the second flow (`tag: "app"`) the workspace auth service supports.

**Endpoint:** `POST /auth`

**Request body (tag = `sso`, frontend user):**

```json
{
  "tag": "sso",
  "ssoToken": "<opaque string>",
  "natsPublicKey": "U<base32 nkey user public key>"
}
```

**Request body (tag = `app`, backend service):**

```json
{
  "tag": "app",
  "appToken": "<opaque string>",
  "namespace": "chat",
  "natsPublicKey": "U<base32 nkey user public key>"
}
```

**Successful response:**

```json
{
  "natsJwt": "eyJ…",
  "principal": {
    "type": "sso" | "app",
    "account": "alice",
    "namespace": "chat",
    "expiresAt": "2026-05-13T15:00:00Z"
  }
}
```

**Permissions encoded in the NATS JWT, by tag:**

| Tag | Pub allow | Sub allow |
|---|---|---|
| `sso` | `workspace.user.<account>.>`, `_INBOX.>` | `workspace.user.<account>.>`, `workspace.room.>`, `_INBOX.>` |
| `app` | `<namespace>.>`, `_INBOX.>` | `<namespace>.>`, plus any subjects from the app's static allow-list in `auth-service-stub` config, `_INBOX.>` |

The static allow-list is a small in-process map keyed by `namespace`. For the MVP it contains:

```text
chat   → pub: chat.>;  sub: chat.>
drive  → pub: drive.>; sub: drive.>, chat.>
```

This permits the cross-app subscribe needed to demonstrate the headline claim (drive subscribes to chat events) without standing up a full app-to-app policy registry. The map will be replaced by a proper policy service in Phase 2; the contract surface stays the same.

**Dev-mode behavior:** the stub does **not** validate the SSO or app token contents — it accepts any non-empty string. The token is recorded in the structured log line for traceability but is not cryptographically verified. The production auth service swaps this for OIDC validation (SSO) or app-token signature verification (app); the rest of the flow is unchanged.

**Signing:** the stub reads `AUTH_SIGNING_KEY` (the account signing seed) on startup, decodes it via `nkeys.FromSeed`, and signs each minted JWT via `jwt.NewUserClaims(...).Encode(signingKey)`. JWT expiry is configurable via `NATS_JWT_EXPIRY` (default 2 hours).

### 3.4 Permission Sidecar

The permission sidecar is the integration boundary between an app container and NATS. App containers do not hold app tokens, nkey seeds, or NATS credentials. Instead they connect to `nats://127.0.0.1:4222` (their pod-local sidecar) using a standard NATS client library in any language, with **no client-side auth configuration**.

The sidecar is a small Go binary (~200 lines of code) that runs alongside the app container in the same Pod. Its responsibilities, in order:

1. **Load credentials.** Read `WORKSPACE_APP_TOKEN`, `NATS_NKEY_SEED`, `WORKSPACE_NAMESPACE`, and `WORKSPACE_AUTH_URL` from the environment (the seed and token are projected from a Kubernetes Secret).
2. **Derive nkey public key.** From the seed, derive the user public key (`U…`).
3. **Mint JWT.** `POST` to `WORKSPACE_AUTH_URL` with `{tag: "app", appToken, namespace, natsPublicKey}`; receive the NATS user JWT.
4. **Start a NATS leaf node.** Run an embedded `nats-server` configured in **leaf node mode**, bound to `127.0.0.1:4222`, with no authentication required on the local listener. The leaf node connects upstream to `WORKSPACE_NATS_URL` (the cluster's real NATS service) using `{jwt, seed}` credentials. NATS's leaf node mode is a native, well-supported pattern: a downstream NATS server forwards traffic from its local clients up to an authenticated remote server.
5. **Refresh in the background.** Every `(JWT TTL − 5 minutes)`, call `POST /auth` again, obtain a fresh JWT, and rotate the upstream leaf-node connection.
6. **Surface health.** Expose `GET /healthz` on `127.0.0.1:8181` reporting upstream connection state and JWT expiry. The Kubernetes liveness probe targets this.

The app container observes a plain unauthenticated NATS server on `localhost:4222`. Anything published locally is forwarded upstream via the authenticated leaf-node connection, where the real NATS server enforces the per-subject ACLs encoded in the JWT. If the JWT permits `chat.>`, an attempted publish to `drive.something` is rejected by the upstream NATS server with a NATS-protocol-level permission error — no application code is involved in the rejection.

**Why a leaf node and not a custom proxy.** NATS leaf node mode already implements the exact behavior we want: a local server that accepts unauthenticated client connections and forwards them upstream with the leaf node's own credentials. Reimplementing this as a custom NATS-protocol-parsing proxy would duplicate hundreds of lines of nontrivial protocol handling that the official `nats-server` binary already does correctly. The sidecar binary embeds `nats-server` as a library (it is importable as `github.com/nats-io/nats-server/v2/server`) so we end up with one binary, not two processes.

**Sidecar image.** Multi-stage Dockerfile, `golang:1.25-alpine` builder → `alpine:3.21` runtime. Final image is approximately 30 MB.

**Why not an SDK / library.** An SDK pushes the auth complexity into every app and forces a separate implementation per language (Go, JavaScript, Python, Java, …). A sidecar gives every app the same integration surface — a plain NATS connection at localhost — regardless of the app's language. The total platform-team code is roughly the same, but the burden is concentrated in one binary rather than spread across N language ports.

**Why not Envoy + `ext_authz`.** Envoy's `ext_authz` filter is designed for HTTP request authorization. NATS is not HTTP at the wire level — it is a binary/text protocol over TCP. The NATS WebSocket transport (used by browsers) starts with an HTTP/1.1 upgrade handshake but transitions immediately to opaque WebSocket frames carrying NATS protocol commands; Envoy sees opaque frames after the upgrade and cannot inspect or authorize individual PUB/SUB commands. Per-subject authorization at the Envoy layer would require a custom Wasm filter that parses NATS PUB/SUB inside WebSocket frames, which is materially more work than the leaf-node sidecar described here and duplicates functionality NATS already provides natively. Envoy and Istio remain in the picture for **edge concerns** — TLS termination at the gateway, hostname routing, optional WS upgrade authorization — but they are not a substitute for the NATS-level enforcement that the platform's authorization story depends on.

**Deployment in MVP vs Phase 2.** The MVP harness declares the sidecar container directly in each sample app's Deployment YAML. Phase 2 will replace explicit declaration with a **mutating admission webhook that injects the sidecar based on Pod labels** (opt-in per Pod, not per namespace). See §8.5 for the selector design and rationale. MVP Deployments carry the Phase 2 labels (`workspace.io/sidecar: "enabled"`, `workspace.io/app-namespace: "<ns>"`) on their Pod template from day one, so the Phase 2 transition is purely additive: deploy the webhook, remove the explicit sidecar container from the Deployment YAML, and the labels already match what the webhook expects.

### 3.5 Sample Apps

Two minimal Go services demonstrate the cross-app flow.

**chat-app** (`sample-apps/chat-app/main.go`, ~30 lines):

```go
nc, _ := nats.Connect("nats://127.0.0.1:4222") // talks to the sidecar
defer nc.Close()
for {
    payload := fmt.Sprintf(`{"id":%q,"text":"hello from chat-app"}`, uuid.NewString())
    _ = nc.Publish("chat.message.created", []byte(payload))
    time.Sleep(5 * time.Second)
}
```

**drive-app** (`sample-apps/drive-app/main.go`, ~30 lines):

```go
nc, _ := nats.Connect("nats://127.0.0.1:4222")
defer nc.Close()
_, _ = nc.Subscribe("chat.>", func(m *nats.Msg) {
    slog.Info("drive received chat event", "subject", m.Subject, "payload", string(m.Data))
})
select {} // block forever
```

Both apps deliberately avoid pulling in a workspace SDK. The integration surface is the standard `github.com/nats-io/nats.go` client connecting to `127.0.0.1:4222`. The same code would compile and run identically against a real NATS server with auth, against the sidecar in production, and against the local-dev sidecar.

### 3.6 Frontend Stub

A small static page demonstrates that the browser-direct path works with the same auth-service contract. It lives at `sample-apps/fe-stub/` and is served by nginx in the `fe-test` namespace.

**Flow:**

1. Page loads. JavaScript generates an nkey keypair in-browser via the `nkeys.js` library and stores the seed in memory (never persisted).
2. User enters a dev account name (e.g. `alice`) and clicks "Connect."
3. Page POSTs to `https://workspace.local/auth` with `{tag: "sso", ssoToken: "alice", natsPublicKey: "U…"}` (dev mode treats the SSO token as the account name).
4. Receives the NATS user JWT.
5. Opens a NATS WebSocket connection via `nats.ws` to `wss://workspace.local/nats`, authenticating with `{jwt, seed}`.
6. Subscribes to `workspace.user.alice.>` and `workspace.room.>`.
7. UI buttons publish test events to `workspace.user.alice.test` and `workspace.room.lobby.message`.

The page is a single HTML file. No build tooling, no bundler. It serves as the executable specification of the FE → auth-service → NATS flow.

## 4. Component Specifications

### 4.1 Stub Auth Service

| Field | Value |
|---|---|
| Path on disk | `~/ashwini-repos/workspace/auth-service-stub/` |
| Language / framework | Go 1.25, Gin, `nats-io/jwt/v2`, `nats-io/nkeys` |
| Files | `main.go`, `handler.go`, `routes.go`, `go.mod`, `deploy/Dockerfile` |
| Listen port | 8080 (configurable via `PORT`) |
| Required env | `AUTH_SIGNING_KEY` (nkey account seed `SAA…`) |
| Optional env | `NATS_JWT_EXPIRY` (default `2h`), `DEV_MODE` (default `true` for the harness), `APP_ALLOWLIST_PATH` (JSON file with the namespace→permissions map) |
| Health endpoint | `GET /healthz` returns `{"status":"ok"}` |
| Container image | Built locally and loaded into kind via `kind load docker-image workspace/auth-service-stub:dev` |
| Estimated LOC | ~250 |

### 4.2 Permission Sidecar

| Field | Value |
|---|---|
| Path on disk | `~/ashwini-repos/workspace/permission-sidecar/` |
| Language | Go 1.25 |
| Files | `main.go`, `leafnode.go`, `refresh.go`, `go.mod`, `deploy/Dockerfile` |
| Embedded dependency | `github.com/nats-io/nats-server/v2/server` (run as a library, not a separate process) |
| Local listener | `127.0.0.1:4222` (unauthenticated) |
| Health endpoint | `GET 127.0.0.1:8181/healthz` |
| Required env | `WORKSPACE_AUTH_URL`, `WORKSPACE_NATS_URL`, `WORKSPACE_TAG`, `WORKSPACE_NAMESPACE`, `WORKSPACE_APP_TOKEN`, `NATS_NKEY_SEED` |
| Container image | `workspace/permission-sidecar:dev` |
| Estimated LOC | ~200 |

### 4.3 Sample Apps

| App | Path | Image | LOC | Role |
|---|---|---|---|---|
| chat-app | `sample-apps/chat-app/` | `workspace/chat-app:dev` | ~30 | Publisher of `chat.message.created` every 5s |
| drive-app | `sample-apps/drive-app/` | `workspace/drive-app:dev` | ~30 | Subscriber to `chat.>` (cross-app receive) |
| fe-stub | `sample-apps/fe-stub/` | nginx + static HTML | ~150 (HTML+JS) | Browser-side proof of SSO flow + NATS WebSocket |

### 4.4 NATS StatefulSet

| Field | Value |
|---|---|
| Replicas | 1 |
| Image | `nats:2.10-alpine` (vendored or pulled at setup-time — TBD per §6) |
| Storage | 1 Gi PVC, `standard` storage class (kind default) |
| Ports | 4222 (client TCP), 8222 (HTTP monitoring), 7422 (leaf nodes, for sidecars upstream), 8080 (WebSocket for FE) |
| Config | Generated from `nats-config` ConfigMap; trusts the operator JWT inline and the account JWT via local file resolver; enables JetStream with file storage at `/data` |

### 4.5 Istio Gateway

A single per-namespace ingressgateway in `workspace` ns, deployed via the same vendored `istio/gateway` chart used by the ES harness (chart version 1.24.2). It exposes two virtual hosts:

| Host | Path | Target |
|---|---|---|
| `auth.workspace.local` | `/auth`, `/healthz` | `auth-service.workspace.svc:8080` |
| `nats.workspace.local` | `/` (with WebSocket upgrade) | `nats.workspace.svc:8080` |
| `workspace.local` | `/` | `fe-stub.fe-test.svc:80` |

TLS is `SIMPLE` (Gateway-terminated) with self-signed certs generated at setup time. Configuration is the same shape as the ES harness's `chat-ingressgateway` — only the names differ.

## 5. Bring-up Flow (`kind/setup.sh`)

The script is idempotent — re-running picks up where it left off. Steps in order:

1. **Create kind cluster.** Reuse if `kind get clusters` shows `workspace`; otherwise `kind create cluster --config kind-config.yaml`. Switch kubectl context to `kind-workspace`.
2. **Install Istio.** `helm upgrade --install` from vendored `istio-base-*.tgz` and `istiod-*.tgz` in `istio-system`. Same shape as the ES harness.
3. **Create namespaces.** `workspace`, `chat`, `drive`, `fe-test`. The `workspace` namespace gets `istio-injection=enabled` (gateway pod lives there); `chat` and `drive` do **not** — app traffic to NATS goes via the sidecar, not via Istio.
4. **Install the workspace ingressgateway.** Vendored `gateway-1.24.2.tgz` into `workspace` ns with values matching the ES harness pattern (NodePort 30080/30443, labels `istio: workspace-ingressgateway`).
5. **Generate NATS operator and account credentials.** Use `nsc` in an ephemeral working directory to create operator + account; extract operator JWT, account JWT, and account signing seed. Apply as ConfigMap (`nats-config`) and Secret (`auth-signing-key`) in `workspace` ns.
6. **Render and apply `nats-server.conf`.** Embed the operator JWT inline and configure the local file resolver pointing at the account JWT. Apply the NATS StatefulSet + Service.
7. **Wait for NATS readiness.** `kubectl -n workspace wait --for=condition=Ready pod/nats-0 --timeout=180s`.
8. **Build and load images.** `docker build` each of `auth-service-stub`, `permission-sidecar`, `chat-app`, `drive-app`, `fe-stub`. `kind load docker-image` each into the cluster.
9. **Deploy auth service.** Apply Deployment + Service in `workspace` ns. Mount the `auth-signing-key` Secret as `AUTH_SIGNING_KEY` env.
10. **Provision per-app credentials.** For each app namespace (`chat`, `drive`):
    - Generate an nkey user seed via `nsc` (or `nk`).
    - Generate an app-token (random hex; for dev mode the value does not matter).
    - Create Secret `<app>-creds` in the target namespace with keys `nkeySeed` and `appToken`.
11. **Deploy sample apps.** Apply chat-app and drive-app Deployments. Each Pod has two containers: the app and the permission-sidecar. The sidecar receives `WORKSPACE_AUTH_URL=http://auth-service.workspace.svc:8080/auth`, `WORKSPACE_NATS_URL=nats://nats.workspace.svc:7422` (leaf node port), `WORKSPACE_TAG=app`, `WORKSPACE_NAMESPACE=<chat|drive>`, plus the Secret-projected `WORKSPACE_APP_TOKEN` and `NATS_NKEY_SEED`.
12. **Deploy FE stub.** Apply nginx Deployment + Service in `fe-test`. Apply Istio Gateway + VirtualService for `workspace.local`, `auth.workspace.local`, `nats.workspace.local`.
13. **Print verification commands.** See §6.

Total wall-clock time on a MacBook with a warm Docker cache should be approximately 2 minutes.

## 6. Verification

The setup script prints these commands at the end.

**Watch the cross-app flow:**

```bash
kubectl -n chat  logs deploy/chat-app  -c app -f
# chat-app publishes chat.message.created every 5s

kubectl -n drive logs deploy/drive-app -c app -f
# drive-app receives each chat.message.created event within the JWT-allowed namespaces
```

**Confirm sidecars are connected upstream:**

```bash
kubectl -n chat  logs deploy/chat-app  -c sidecar | grep "leaf node connected"
kubectl -n drive logs deploy/drive-app -c sidecar | grep "leaf node connected"
```

**Exercise the FE flow:**

```bash
open https://workspace.local/    # accept self-signed cert
# Enter "alice" as the dev SSO token, click Connect, push the Publish button
# Browser DevTools network panel should show:
#   POST /auth → 200 with natsJwt
#   WS upgrade to /nats → 101
#   NATS frames flowing
```

**Prove protocol-level enforcement (the headline claim):**

```bash
# Strip drive-app's credentials → leaf node fails to connect upstream
kubectl -n drive set env deploy/drive-app -c sidecar WORKSPACE_APP_TOKEN=invalid-token
kubectl -n drive rollout restart deploy/drive-app
kubectl -n drive logs deploy/drive-app -c sidecar
# Expected: auth-service returns 200 (dev mode accepts any token), but if we
# replace the app-token with a namespace mismatch — e.g. token claims namespace
# "wiki" while the env says "drive" — NATS rejects the leaf-node connection
# with a permission error and the sidecar's healthz reports degraded.
```

A second test illustrates the per-subject ACL:

```bash
# Patch drive-app's sidecar to use chat-app's namespace claim → drive can no longer subscribe to chat.> (it's outside its own namespace once the static allow-list is loaded that way), and chat.message.created events stop arriving.
# Restore by reverting the patch and rolling the Deployment.
```

The headline observable property: **the failure happens at NATS, not in any application code**. The sidecar logs `permissions violation` from the upstream NATS server; the app container is never involved in the decision.

## 7. Directory Layout

```text
~/ashwini-repos/workspace/
├── prd/                                          (existing — unchanged)
│   ├── event-driven/
│   └── permission-validation/
├── docs/
│   └── superpowers/
│       └── specs/
│           └── 2026-05-13-workspace-kind-harness-design.md   (this document)
├── auth-service-stub/
│   ├── main.go
│   ├── handler.go
│   ├── routes.go
│   ├── handler_test.go
│   ├── go.mod
│   └── deploy/
│       └── Dockerfile
├── permission-sidecar/
│   ├── main.go
│   ├── leafnode.go
│   ├── refresh.go
│   ├── main_test.go
│   ├── go.mod
│   └── deploy/
│       └── Dockerfile
├── sample-apps/
│   ├── chat-app/
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── deploy/Dockerfile
│   ├── drive-app/
│   │   ├── main.go
│   │   ├── go.mod
│   │   └── deploy/Dockerfile
│   └── fe-stub/
│       ├── index.html
│       ├── nats-client.js
│       └── deploy/Dockerfile
└── kind/
    ├── README.md
    ├── kind-config.yaml
    ├── setup.sh
    ├── teardown.sh
    ├── charts/
    │   ├── base-1.24.2.tgz
    │   ├── istiod-1.24.2.tgz
    │   └── gateway-1.24.2.tgz
    └── manifests/
        ├── namespace-workspace.yaml
        ├── namespace-chat.yaml
        ├── namespace-drive.yaml
        ├── namespace-fe-test.yaml
        ├── istio-base-values.yaml
        ├── istiod-values.yaml
        ├── istio-gateway-values.yaml
        ├── nats-config-template.yaml
        ├── nats-statefulset.yaml
        ├── nats-service.yaml
        ├── auth-service-deployment.yaml
        ├── auth-service-svc.yaml
        ├── chat-app-deployment.yaml
        ├── drive-app-deployment.yaml
        ├── fe-stub-deployment.yaml
        ├── istio-gateway.yaml
        └── istio-virtualservices.yaml
```

The layout mirrors the ES harness's `charts/elasticsearch/kind/` skeleton: vendored chart `.tgz`s, plain-YAML manifests, a single shell script entry point. The harness deliberately does not introduce a Helm chart for the workspace components; there is too little variation across environments to justify templating. A Helm chart can be added later when multi-site or multi-tenant variants emerge.

## 8. Trade-offs and Risks

### 8.1 Single NATS account

The harness runs one NATS account, not one per tenant. This is correct for the MVP — the property being proved (per-subject ACL enforcement via auth-service-minted JWTs) holds at the account level. Multi-tenant accounts are a configuration change in NATS, an `nsc` change in `setup.sh`, and a small change to the auth service's tenant resolution; they do not change the harness's overall shape. Adding tenancy in MVP would dilute the verification target.

### 8.2 Dev-mode token acceptance

The stub auth service accepts any non-empty token. This is intentional: the harness's goal is to prove the flow once a JWT is issued, not to verify token-validation code. The production auth service (in the private gitlab repository) plugs in OIDC validation for SSO and an app-token signature/lookup for app flows. The contract — the JSON request body, response body, JWT permission encoding, and HTTP status semantics — is fixed by this design and stays identical across dev and prod.

### 8.3 Static app permission allow-list

`chat → chat.>`, `drive → drive.>, chat.>` is hard-coded in the auth service. A real platform replaces this with a dynamic policy registry (the Developer Portal in the PRDs, or an admin API). The harness intentionally omits that surface because (a) the headline claim is observable without it and (b) the policy registry has its own design space that should not be conflated with this harness.

### 8.4 Sidecar declared in Deployment YAML (no auto-injection in MVP)

For the MVP every sample-app Deployment includes the sidecar container explicitly. Production wants auto-injection via a mutating webhook so app teams' manifests don't carry workspace-internal infrastructure details. Auto-injection is straightforward to add later — one webhook server, one `MutatingWebhookConfiguration`, one Pod-label selector — and the sidecar binary itself does not change. Including the webhook in MVP would double the moving parts (TLS cert provisioning, CA bundle, webhook server RBAC) without affecting what the harness verifies. The Phase 2 webhook design is pinned in §8.5 so MVP Deployments can carry the future labels today, making the transition additive rather than a migration.

### 8.5 Phase 2 auto-injection uses Pod-label opt-in, not namespace-level

When auto-injection ships in Phase 2 the `MutatingWebhookConfiguration` will use a Pod `objectSelector` (per-Pod opt-in), **not** a Namespace `namespaceSelector` (namespace-wide opt-in). The trigger label is `workspace.io/sidecar: "enabled"`. Onboarding for an app team is one line in their Deployment template:

```yaml
spec:
  template:
    metadata:
      labels:
        workspace.io/sidecar: "enabled"             # triggers the webhook
        workspace.io/app-namespace: "chat"          # value the webhook sets as WORKSPACE_NAMESPACE
```

The webhook configuration uses the matching selector:

```yaml
objectSelector:
  matchLabels:
    workspace.io/sidecar: "enabled"
```

The rationale is failure isolation. Pod-label opt-in injects the sidecar **only** into Pods that explicitly request it. Unrelated workloads in the same namespace — Redis caches, nightly cron jobs, debug Pods, third-party operators — are untouched. Namespace-level opt-in, by contrast, mutates every Pod in a labeled namespace, including Pods that have no business holding workspace credentials; those Pods would either fail to start (no Secret matching the convention) or run an unused sidecar. The five seconds saved by namespace-level labeling do not justify that failure mode for a multi-tenant platform shared by many app teams.

The webhook's `failurePolicy` is `Fail`: if the webhook is unreachable, Pod creation for matching Pods is rejected. This prevents silently starting a workspace app without its sidecar (the app would have no NATS access and the failure would be diagnosed at the wrong layer). Pods without the trigger label are unaffected by webhook health — the webhook is never called for them, regardless of their namespace.

Trade-offs accepted:

- App teams must remember to add the label. Mitigated by including it in the platform-provided Deployment template; missing the label produces a Pod that obviously cannot reach NATS (no sidecar container, no NATS endpoint), which is a fast and clear failure.
- The webhook becomes a tier-0 dependency for new Pod creation (via `failurePolicy: Fail`). Mitigated by running the webhook with 2+ replicas, a `PodDisruptionBudget`, and HA admission certs.

This is the pattern used by Dapr and Vault Agent Injector. Istio chose namespace-level opt-in because its mental model is "this entire namespace is part of the mesh"; the workspace platform's mental model is "this individual Pod wants workspace access," which Pod-label opt-in expresses more directly.

### 8.6 Single-node NATS

The 1-node NATS StatefulSet is fine for local verification but does not exercise JetStream's R3 replication, leader election, or cross-zone behavior. The kind cluster is also single-node, so true multi-replica testing requires real Kubernetes. The harness is explicitly a local verification tool, not a load-testing platform.

### 8.7 No schema enforcement

Sample apps send free-form JSON. Schema registry integration is part of the broader platform design (PRD §6) but adding it to this harness would obscure the auth-flow proof. A future harness extension can add a schema-registry component and demonstrate publisher-side validation; the design surfaces are independent.

## 9. Open Questions

These need resolution before implementation begins. Each is small but each affects a specific file.

1. **NATS image source.** Vendor `nats:2.10-alpine` as a tarball (matching the ES harness's no-pull stance) or allow runtime pull during `setup.sh`? Vendoring is more disciplined; pulling is one less file to maintain. Default recommendation: pull at runtime, since `nats` is a stable image and the harness already depends on Docker being online enough to load locally built images.
2. **NATS Helm chart vs raw manifests.** The official `nats` Helm chart exists. Using it would parallel the ES harness's `eck-operator` chart usage. Using raw manifests is simpler for a 1-pod StatefulSet. Default recommendation: raw manifests for MVP, switch to the chart when JetStream cluster mode is added.
3. **`nsc` versus inline `nkeys` library calls.** `nsc` is the canonical NATS auth tooling and produces operator/account artifacts identical to what production uses. Inline Go code using `nkeys` is shorter but reimplements `nsc`'s structure. Default recommendation: use `nsc` in `setup.sh` for fidelity; the production auth service's signing flow will use `nkeys` directly anyway.
4. **JWT expiry default.** The PRD's `core_completed.md` §5 suggests ~10 minutes for revocation-latency reasons. The chat auth service uses 2 hours. The harness will refresh tokens regardless of TTL, so the shorter value exercises the refresh path more often. Default recommendation: 30 minutes for MVP — short enough to see refresh logs every few minutes, long enough that a token outage during `nsc` operations doesn't immediately break the demo.

## 10. Success Criteria

The harness is considered successful when, on a fresh checkout of `~/ashwini-repos/workspace/`:

1. `./kind/setup.sh` runs to completion in ≤ 5 minutes on a 16 GB MacBook with Docker Desktop allocated ≥ 6 GB.
2. `kubectl -n drive logs deploy/drive-app -c app -f` shows `chat.message.created` events arriving from chat-app within 30 seconds of setup completion.
3. Opening `https://workspace.local/` in a browser, entering any account name, and clicking "Publish" produces a visible round-trip event in the same UI.
4. Replacing chat-app's app-token claim with a namespace it should not have access to causes NATS to reject the leaf-node connection at the protocol layer, with the rejection visible in the sidecar logs and **not** in the app container's logs.
5. `./kind/teardown.sh` removes everything cleanly.

The harness deliberately does not target throughput, latency, or any production-grade SLO. It is a correctness harness for the auth flow.

## 11. References

- [`prd/event-driven/core_completed.md`](../../../prd/event-driven/core_completed.md) — authoritative PRD describing the Auth-Service-issued per-subject JWT model; supersedes the Validator Pool design.
- [`prd/event-driven/workspace_platform_prd.md`](../../../prd/event-driven/workspace_platform_prd.md) — parallel PRD framing of the same architecture with the app-to-app publish grant model.
- [`prd/event-driven/claude_reviewed.md`](../../../prd/event-driven/claude_reviewed.md) — production review with full architecture diagram, sequence diagrams, NATS account model.
- `charts/elasticsearch/kind/` on `hmchangw/chat` branch `claude/eck-elasticsearch-setup-VCAsc` — structural template for the harness.
- `auth-service/` on `hmchangw/chat` main branch — reference shape of the `POST /auth` handler, OIDC integration pattern, and NATS JWT signing.
- NATS leaf node documentation: <https://docs.nats.io/running-a-nats-service/configuration/leafnodes>
