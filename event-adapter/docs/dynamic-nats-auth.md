# Dynamic NATS Credentials

Event-adapter can obtain its NATS credentials dynamically from the auth-service
instead of mounting a static creds file. It generates its own NATS user nkey
pair (the private seed never leaves the process), mints a short-lived NATS JWT
from `/auth`, and proactively re-mints + reconnects before the JWT expires.

This is the recommended mode for a per-app sidecar: each app authenticates as
its own NATS identity, and no long-lived creds file is shared across apps.

## Enabling it

Dynamic auth turns on when an **auth URL** is resolved (from env or config).
It is **mutually exclusive** with `nats.credsFilePath` — set only one.

Each value resolves **env-first, then routes.yaml, then a default**:

| Value | Env var (wins) | routes.yaml fallback | Default |
|---|---|---|---|
| app token | `EVENT_ADAPTER_APP_TOKEN` | — (secret: env only) | required |
| auth URL | `EVENT_ADAPTER_AUTH_URL` | `natsAuth.authURL` | required |
| namespace | `EVENT_ADAPTER_NAMESPACE` | `app.namespace` | required |
| refresh buffer | `EVENT_ADAPTER_REFRESH_BUFFER` | `natsAuth.refreshBuffer` | `1m` |

The app token is a secret and is **env-only** — never put it in the mounted
config file. `token_type` is always `app` (not configurable).

## What an app team must provide

1. Inject the **app token** into the container as `EVENT_ADAPTER_APP_TOKEN`
   (via Vault in the k8s deployment template).
2. Set the **auth namespace** — either `EVENT_ADAPTER_NAMESPACE` or
   `app.namespace` in routes.yaml. It must exactly match the namespace registered
   for the app in the auth service, and be a single NATS-subject token (no `.`,
   `*`, `>`, or whitespace).
3. Set the **auth URL** — either `EVENT_ADAPTER_AUTH_URL` or `natsAuth.authURL`.
4. Do **not** set `nats.credsFilePath`.

Note: `app.namespace` is distinct from `app.id`. `app.id` is the CloudEvent
`source` / `sidecarAppID` (event provenance); `app.namespace` is the auth
identity sent to `/auth`. They may hold different values.

## Example routes.yaml

```yaml
app:
  id: task-service          # CloudEvent source / sidecarAppID
  namespace: task-service   # sent to /auth as "namespace" (auth-registered)
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://nats:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  filterSubject: t.tenant-a.app.task.event.created
natsAuth:
  authURL: https://auth-service            # or EVENT_ADAPTER_AUTH_URL
  refreshBuffer: 1m                        # optional
routes:
  - name: task-created
    match:
      subject: t.tenant-a.app.task.event.created
    dispatch:
      method: POST
      path: /events/task-created
```

Plus the env var (via Vault):

```env
EVENT_ADAPTER_APP_TOKEN=<app token>
```

## How refresh works

1. On startup: generate nkey, `POST /api/v1/auth` once, connect with
   `nats.UserJWT(...)`. If any of the token / auth URL / namespace is missing or
   invalid, the process exits (fail fast, same as a bad creds file).
2. A background goroutine sleeps until `expiry − refreshBuffer`, re-mints a fresh
   JWT (same app token, same nkey), and calls `ForceReconnect()`.
3. NATS reconnects with the fresh JWT and auto-restores subscriptions — a brief,
   self-healing blip. In-flight JetStream messages redeliver via normal acks.
4. If a refresh fails, the current connection stays up and the mint is retried
   with backoff.

## API contract (auth-service)

Request — `POST {authURL}/api/v1/auth`:

```json
{ "token_type": "app",
  "token": "<app token>",
  "publicKey": "<generated nkey public key>",
  "namespace": "<auth namespace>" }
```

Response:

```json
{ "natsToken": "<signed NATS user JWT>",
  "expiresIn": 3600 }
```

Event-adapter decodes the JWT's own `exp` claim for the expiry (authoritative);
`expiresIn` is informational.
