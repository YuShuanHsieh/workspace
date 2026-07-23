# Event-Adapter Dynamic NATS JWT Credentials — Implementation Spec

**Date:** 2026-07-14
**Status:** Implemented in the workspace reference repo (see `internal/natscreds`,
`cmd/event-adapter/resolve.go`, config + natsjs changes). This spec matches the
as-built contract.
**Audience:** implementer (human or coding agent) working in the internal repo that
contains `event-adapter` and the `auth-service` (`POST /api/v1/auth`). The **app token**
is supplied by **Vault as an environment variable** (injected via the k8s
deployment template) and sent to `/auth` as the `token` field.

---

## Replace-before-use checklist

Contract confirmed against the internal auth-service (below). When porting to the
internal repo, only verify the existing NATS package name (spec says `natsjs`)
and whether a static `credsFilePath` field exists (§0, §5) `⟵ TODO`.

**Fixed by us (event-adapter owns this image; app teams consume it):**
- Config resolves **env-first, then routes.yaml, then default** (§5) — reusing
  the repo's existing o11y override pattern (`getEnvOrDefault`).
- The **app token is env-only** via the constant **`EVENT_ADAPTER_APP_TOKEN`**
  (same for every app team; a secret must never sit in the mounted config).
  Other values (auth URL, namespace, refreshBuffer) may come from env
  (`EVENT_ADAPTER_*`) **or** routes.yaml.
- `token_type` is **hardcoded to `"app"`** (not configurable — the sidecar
  always uses the app flow).

**Confirmed contract (internal auth-service):**

Request body of `POST {authSvcUrl}/api/v1/auth`:
```json
{ "token_type": "app",          // hardcoded; sidecar always uses the app flow
  "token": "<app token>",       // the app-team token, from the Vault env var
  "publicKey": "<pubkey>",      // event-adapter's generated nkey public key
  "namespace": "<auth_ns>" }    // app.namespace (app-flow only)
```

Response body:
```json
{ "natsToken": "<signed NATS user JWT>",
  "expiresIn": 3600 }           // seconds; available but we decode the JWT's
                                // own exp claim (authoritative)
```

**`namespace` comes from a new `app.namespace` field** in routes.yaml — added
for this feature. It is **distinct** from `app.id` (which is the CloudEvent
`source` / `sidecarAppID` for event provenance).

Why it must be its own explicit field (not defaulted from `app.id`): the
auth-service **validates `namespace` against the app's registered namespace in
its DB** (looked up from the token). It must **exactly match** that registered
value or auth fails. Defaulting from `app.id` (the event-source identity) would
silently produce a mismatch → auth failure whenever the two differ. So
the resolved `namespace` is **explicitly required** when dynamic auth is enabled,
with no fallback to `app.id`.

**Already confirmed for the internal repo — no action needed:**
- NATS server `2.10.29` supports JWT auth + expiry enforcement. ✅
- The internal `event-adapter`'s nats.go has `UserJWT` **and** `ForceReconnect()`
  (verified via `go doc`). ✅ The refresh mechanism below works as-is.

---

## 0. Instruction to the implementer

Implement dynamic NATS credential minting + proactive refresh in `event-adapter`
as described below. Before writing code:

1. **Locate and read the real contracts** in this repo:
   - How Vault injects the **app token** into `event-adapter` (the env var name)
     — read from the environment, **not** fetched from any endpoint.
   - The `auth-service` `POST /api/v1/auth` handler — the request body is confirmed
     (see the checklist above / §2); the response is `{natsToken, expiresIn}`.
   - `event-adapter`'s existing NATS connection code (the `natsjs`-equivalent
     package) and its config schema/validation.
2. The `/auth` **request** field names (`token_type`, `token`, `publicKey`,
   `namespace`) are the confirmed internal contract. Still verify the **response**
   field names (`natsToken` + optional expiry/user/app info) and the env var name
   against the real code, and adjust the structs accordingly.
3. Follow the existing code's conventions (error wrapping, config style, test
   style). Keep new logic in a dedicated package so it is unit-testable without
   a live NATS server or live auth stack.

Acceptance criteria are listed in section 8.

---

## 1. Problem & goal

`event-adapter` today authenticates to NATS with a **static credentials file**
mounted at deploy time (`nats.UserCredentials(credsFilePath)`). One file = one
fixed identity.

`event-adapter` runs as a **per-app sidecar**: one instance per app, and every
app has its own NATS account. It must **not** hold or share a single static
identity across apps.

**Goal:** at startup, `event-adapter` obtains **app-scoped NATS credentials
dynamically** from the auth stack, and keeps them fresh for the life of the
process, per sidecar instance.

Non-goal: per-request / multi-tenant credential switching. One instance = one
app = one identity = one connection.

---

## 2. Credential minting flow

`event-adapter` **generates its own NATS user nkey pair**. The private seed
never leaves the process and is never written to disk or logged.

```text
0. resolve config (env-first, then routes.yaml, then default — see §5):
     appToken, authURL, namespace, refreshBuffer   (token_type is hardcoded "app")
1. generate nkey pair                       → (publicKey, seed)   [in memory]
2. POST {authURL}/api/v1/auth   {token_type: "app",               [auth-service]
                             token:       appToken,     // env-only secret
                             publicKey:   publicKey,     // generated nkey pubkey
                             namespace:   namespace}     // env or app.namespace
                                → { natsToken, expiresIn }
3. creds = natsToken + seed                                         [in memory]
4. nats.Connect(url, nats.UserJWT(jwtCB, sigCB), <reconnect opts>)
```

Each input is **resolved env-first, then routes.yaml, then a default** (§5). The
**app token** is a **long-lived credential provisioned by Vault**, injected via
**`EVENT_ADAPTER_APP_TOKEN`** (env-only — never in the mounted config). On each
refresh (§4) the same app token is reused to mint a fresh JWT — only the JWT
rotates.

Facts:
- The app token is env-only (`EVENT_ADAPTER_APP_TOKEN`); it is static for the
  process lifetime (a Vault rotation that changes it takes effect on the next pod
  start).
- `token_type` is **`app`** for event-adapter (`sso` is the human path). The
  `namespace` field carries the auth-registered namespace, used only in the app
  flow.
- The minted `natsToken` carries an **`exp` (expiry)** claim. The expiry is read
  **from the JWT itself** (`jwt.Decode`) — no external store is required to know
  when to refresh. (If `/auth` also returns an explicit expiry in the response
  body, either source is fine; the JWT claim is authoritative.)

---

## 3. Why refresh is required, and the mechanism

Two hard NATS facts drive the design:

1. **The server enforces JWT expiry.** When the JWT's `exp` passes, the NATS
   **server** disconnects the client. You cannot stay connected past expiry with
   the same JWT.
2. **NATS authenticates only at connect time.** There is no "re-auth in place"
   on a live connection — a new JWT can only be presented during a new CONNECT
   handshake. **Therefore a reconnect is unavoidable, at least once per JWT
   lifetime.**

NATS does **not** know how to obtain a new JWT (it has no knowledge of the auth
stack). Minting a fresh JWT is **our** responsibility; NATS only provides the
reconnect mechanics and a callback hook.

**Mechanism (nats.go v1.50.0 provides `UserJWT(jwtCB, sigCB)` and
`Conn.ForceReconnect()`):**

- Configure the connection with `nats.UserJWT(jwtCB, sigCB)`:
  - `jwtCB()` returns the **current cached JWT**. NATS calls it on every connect
    and reconnect.
  - `sigCB(nonce)` signs the server nonce with the fixed **seed**.
- A **background goroutine** (the refresh loop) keeps the cached JWT fresh and
  triggers the reconnect **before** expiry, on our schedule.

The reconnect is a **brief, controlled, self-healing blip**, not a true outage:
nats.go auto-restores subscriptions / the JetStream consumer on reconnect, and
JetStream redelivers in-flight messages via normal ack semantics. (Do not
describe it as "seamless / zero-downtime" — it is a short reconnect.)

---

## 4. Refresh loop (local in-memory cache, no polling, no external store)

State lives **in memory** in the provider: `{ seed (fixed), currentJWT,
expiry }`, with `currentJWT`/`expiry` guarded by a mutex; `seed` is immutable.

The refresh loop runs in its **own goroutine**, separate from the event loop:

```text
loop (until ctx cancelled):
    sleep until (expiry − refreshBuffer)        # one-shot timer to a known
                                                # deadline — NOT a polling loop
    newJWT, newExpiry := mint(ctx)              # re-call /auth (with env
                                                # app token) for the SAME nkey
                                                # (seed unchanged)
    if err == nil:
        cache = {newJWT, newExpiry}             # mutex-guarded swap
        nc.ForceReconnect()                     # WE choose when the blip happens,
                                                # after confirming the new JWT
    else:
        backoff and retry                       # keep serving on the current
                                                # connection; do NOT tear it down
```

Design notes:
- **Not polling.** We know the exact `exp` from the JWT, so we sleep once to
  `exp − refreshBuffer` and wake exactly when needed (~one wake-up per token
  lifetime). No repeated "expired yet?" checks.
- **No Valkey / external store.** For a single-process per-app sidecar there is
  nothing to share across processes; the deadline is self-contained in the JWT.
  An external store would add a dependency and failure mode while buying nothing.
- **Same nkey reused** across refreshes — only the JWT is re-minted, signed for
  the same public key. The seed never changes, so `sigCB` is stable.
- **`ForceReconnect` before expiry** (rather than waiting for the server's
  expiry-kick) lets us pick the moment — after the new JWT is confirmed valid —
  instead of being surprised at the exact expiry instant.

---

## 5. Backward compatibility & config

Static and dynamic credentials are **mutually exclusive**:
- `credsFilePath` set → static file (existing behavior; local dev/tests).
- a resolved `authURL` present → dynamic minting.

**Each dynamic-auth value resolves env-first, then routes.yaml, then a default**
— the **same override pattern the repo already uses for o11y config**
(`getEnvOrDefault("O11Y_...", cfg...)` in `cmd/event-adapter/main.go`). A
deployment can inject values via Vault/env **or** set them in the mounted config,
whichever fits. The **app token is env-only** (a secret must not live in the
mounted config file).

| Value | Env var (wins) | routes.yaml fallback | Default |
|---|---|---|---|
| app token | `EVENT_ADAPTER_APP_TOKEN` | — (secret: env only) | required |
| auth URL | `EVENT_ADAPTER_AUTH_URL` | `natsAuth.authURL` | required |
| namespace | `EVENT_ADAPTER_NAMESPACE` | `app.namespace` | required |
| refreshBuffer | `EVENT_ADAPTER_REFRESH_BUFFER` | `natsAuth.refreshBuffer` | `1m` |

(`token_type` is not resolved — it is hardcoded to `"app"`.)

Optional routes.yaml side (any of these may instead come from env):

```yaml
app:
  id: task-service          # existing: CloudEvent source / sidecarAppID
  namespace: <auth_ns>      # /auth "namespace"; overridden by EVENT_ADAPTER_NAMESPACE
  httpBaseURL: ...
natsAuth:
  authURL: https://...      # required; overridden by EVENT_ADAPTER_AUTH_URL
  refreshBuffer: 1m         # optional; overridden by EVENT_ADAPTER_REFRESH_BUFFER
```

`app.namespace` (and thus the `namespace` sent to `/auth`) stays **distinct from
`app.id`** — `app.id` is the CloudEvent `source` / `sidecarAppID`; namespace is
the auth-registered value.

**Validation (applied to the resolved values, after env+config merge):**
- `credsFilePath` XOR dynamic (resolved `authURL` non-empty) — reject both set.
- Dynamic mode requires resolved **`authURL`**, **`namespace`**, and the
  **app token** all non-empty at startup (fail fast). `app.id` remains required
  independently, as today.
- The resolved **namespace** must be a **single NATS-subject-safe token** — no
  `.`, `*`, `>`, or whitespace (mirrors the auth-service's account rule). Validate
  format at startup and fail fast, so a bad namespace is caught with a clear error
  before it reaches `/auth`.
- `token_type` is hardcoded to `"app"`; default `refreshBuffer` to `1m` when
  neither env nor config sets it.

**App-team consumer contract** (what a team using the event-adapter image must
provide to enable dynamic creds):
1. Provide the **app token** via **`EVENT_ADAPTER_APP_TOKEN`** (Vault/env only).
2. Provide **auth URL** and **namespace** — via env
   (`EVENT_ADAPTER_AUTH_URL`, `EVENT_ADAPTER_NAMESPACE`) **or** routes.yaml
   (`natsAuth.authURL`, `app.namespace`).
3. Omit `credsFilePath`. (`app.id` is already required today, independently.)

---

## 6. Components to build

### 6.1 New package (e.g. `internal/natscreds`)
Single focused, unit-testable unit owning all credential state and logic. It
receives **already-resolved** values (`authURL`, `namespace`, `refreshBuffer`,
`appToken`) — the env-or-config resolution happens in `main`
(§6.3), so this package stays pure and testable.
- **Construct:** `nkeys.CreateUser()` once; store `seed`, derive `publicKey`.
  Fail if `appToken`, `authURL`, or `namespace` is empty.
- **`mint(ctx) error`:** `POST /api/v1/auth` with `token_type`, `token` (app token),
  `publicKey` (the generated nkey public key), and `namespace`; decode the
  returned JWT for `exp`; store `{jwt, expiry}` under a mutex.
- **`jwtCB() (string, error)`** and **`sigCB(nonce []byte) ([]byte, error)`** for
  `nats.UserJWT`.
- **`Run(ctx, nc)`:** the refresh loop from section 4; returns on `ctx` cancel.
- Uses an injectable `*http.Client` and configurable base URLs so tests can point
  it at an `httptest` server.

### 6.2 Config schema + validation
Add the `natsAuth` block (`authURL`, `refreshBuffer`) and a new
**`Namespace`** field on `AppConfig` (`yaml:"namespace"`, under `app:`) as the
routes.yaml *fallbacks*. Add the XOR + required-field + namespace-format rules
(section 5), applied to the **resolved** values.

### 6.3 `main` resolution + connect wiring
In `run()`, **resolve** each value env-first then config, reusing the existing
`getEnvOrDefault` helper (as o11y config already does):
`authURL := getEnvOrDefault("EVENT_ADAPTER_AUTH_URL", cfg.NatsAuth.AuthURL)`, etc.;
the app token is env-only (`EVENT_ADAPTER_APP_TOKEN`). Dynamic mode is enabled
when the resolved `authURL` is non-empty (mutually exclusive with
`credsFilePath`). Then construct the provider with the resolved values, **mint
once** (fail fast — same non-zero exit as a bad creds file today), pass
`nats.UserJWT(jwtCB, sigCB)` + unlimited reconnects into `Connect`, and keep the
provider so its refresh loop can run/stop.

### 6.4 `main` wiring
Start the provider's refresh goroutine after connect, tied to the process `ctx`;
stop it on graceful shutdown alongside the event loop.

---

## 7. Error handling & secret hygiene

- **Startup failure** (missing app token / `authURL` / `namespace` after
  resolution, bad namespace format, or `/auth` call fails): return an error →
  process exits non-zero. Identical to today's behavior when a mounted creds file
  is wrong; no new orchestration coupling — the process simply does not start.
- **Refresh failure** (already connected): keep the healthy connection, retry the
  re-mint with backoff. A transient auth-stack hiccup must not tear down an
  adapter that is still processing events. Only an actual expiry before recovery
  causes a NATS drop, which auto-reconnect heals once minting succeeds.
- **Secrets:** the env **app token**, `natsToken`, and `seed` are never written to
  disk and never logged. Ensure error messages do not embed them (redact before
  wrapping).
- **Missing app-token env** at startup is a fail-fast error (process exits; no
  partial startup).

---

## 8. Testing & acceptance criteria

**Tests (no live NATS or live auth stack required for units):**
- `natscreds` unit tests against an `httptest` server stubbing `/auth` (pass
  resolved values — `authURL`, `namespace`, `appToken`, … — to the constructor):
  - happy path: `mint` posts the expected body (`token_type`, `token`,
    `publicKey`, `namespace`), returns a JWT, and parses `exp` correctly;
  - `/auth` failure surfaces a wrapped error;
  - missing/empty `appToken` / `authURL` / `namespace` fails at construction;
  - refresh loop re-mints at `exp − refreshBuffer` (use an injected clock/short
    durations);
  - secret hygiene: the app token / JWT do not appear in returned errors.
- Resolution test (`main`/config): `EVENT_ADAPTER_*` env wins over the routes.yaml
  fallback, which wins over the default (mirrors the o11y `getEnvOrDefault` tests).
- Connect-options test: dynamic mode (resolved `authURL` non-empty) yields the
  `UserJWT` option and is mutually exclusive with `credsFilePath`.
- Config validation tests: XOR rule, required-field rules, and the resolved
  **namespace** format rule (reject values containing `.`, `*`, `>`, or
  whitespace; accept a valid single token).

**Acceptance criteria:**
1. With dynamic auth configured (values from env and/or routes.yaml, app token
   from `EVENT_ADAPTER_APP_TOKEN`), the adapter generates an nkey, mints a JWT via
   `POST /api/v1/auth` (`token_type`, `token`, `publicKey`, `namespace`), and connects to
   NATS using `UserJWT` (no creds file on disk).
2. Before the JWT expires, the adapter re-mints and `ForceReconnect`s; event
   processing resumes automatically after the brief reconnect.
3. A refresh failure does not drop a healthy connection; it retries with backoff.
4. A startup mint failure exits the process non-zero (no partial startup).
5. `credsFilePath` and dynamic auth (resolved `authURL`) are mutually exclusive
   and validated.
6. No secret material is logged or written to disk.
7. All new and existing tests pass; `go build`, `go vet`, `go test ./...` clean.

---

## 9. Open items to verify against the real repo

- The contract is confirmed both ways: request `{token_type, token, publicKey,
  namespace}` and response `{natsToken, expiresIn}`. When porting to the internal
  repo, only confirm the existing NATS package name and whether a static
  `credsFilePath` field exists.
- The real config package's naming conventions for the new `natsAuth` fields.

## 10. Out of scope

- Per-request / multi-tenant credential switching.
- Any changes to the auth-service itself, or to how Vault provisions/injects the
  app token (event-adapter only reads it from the environment).
- Reconciling the two Authorization-forwarding approaches in `event-adapter`
  (tracked separately).
