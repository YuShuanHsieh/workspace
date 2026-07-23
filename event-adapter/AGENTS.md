# event-adapter — Agent Context

NATS → local HTTP dispatch sidecar. Bridges NATS messages to a colocated app's
loopback HTTP handlers so the app needs no NATS code. Two inbound delivery models
share one dispatch core (`parse CloudEvent → match by type → dispatch to local app`):

- **Request-reply (primary):** core-NATS request → dispatch → reply on the
  caller's inbox. Synchronous; no ack/retry/DLQ. Configured by the `requests:`
  block. Direct dispatch is an opt-in request-reply fallback.
- **JetStream event consumption (opt-in):** durable consumer → dispatch →
  publish response CloudEvent; retry + DLQ. Configured by the `nats:` + `routes:`
  blocks.

A config may set either block alone or both; **at least one is required**. A
pure request-reply (responder-only) deployment does NOT need the JetStream
`stream`/`durableConsumer`/`ackWait`/`maxDeliver`/`routes` fields — those are
validated only when `routes:` is present.

## Internal packages

| Package | Role |
|---|---|
| `internal/config` | Parse + validate the sidecar config schema (`nats:`/`routes:` and/or `requests:`) |
| `internal/cloudevent` | CloudEvent envelope construction, response/reply wrapping |
| `internal/natsjs` | NATS connection, JetStream consumer + core-NATS request subscription |
| `internal/dispatcher` | HTTP client that calls app handlers (shared by both models) |
| `internal/router` | Match incoming CloudEvents to event routes or request routes |
| `internal/processor` | Event model: retry logic and DLQ publication |
| `internal/responder` | Request-reply model: dispatch and reply on the caller's inbox |
| `internal/metrics` | OpenTelemetry counters + histograms |

## Config schema

`app.id` + `app.httpBaseURL` (loopback only) and `nats.url` are always required.
Then configure **at least one** of the two blocks below.

### JetStream event consumption (opt-in: `nats:` + `routes:`)

Required only when `routes:` is present. Validation enforces the full `nats`
JetStream section and the `routes` array.

```yaml
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  filterSubject: t.tenant-a.app.task.event.created
  workerPoolSize: 16
  fetchBatch: 16
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      type: com.workspace.task.created   # route match key (type only)
    dispatch:
      method: POST
      path: /events/task-created
      timeout: 2s
    response:
      type: com.workspace.task.created.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
```

### Request-reply (primary: `requests:`)

Configured independently of `routes:`. A responder-only deployment omits the
JetStream `nats` fields and the `routes` array entirely. Request routes carry a
`reply` block (no `response`/`retry`/`dlq` — the strict decoder rejects those
keys here).

```yaml
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222            # connection only; required for both models
requests:
  subject: q.tenant-a.app.uploads.request   # core-NATS subject (may be wildcard)
  queueGroup: upload-responders             # one delivery per group; horizontal scale
  workerPoolSize: 16                         # bounded in-flight dispatches
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes: [/orders/]
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request
      dispatch:
        method: POST
        path: /requests/upload-presign
        timeout: 3s
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
```

`requests.directDispatch` is request-reply-only and opt-in. An exact
`requests.routes` type always wins. If no exact route matches and direct
dispatch is enabled, the publisher must supply resolved `dispatchmethod` and
`dispatchpath` metadata; the latter is a relative path joined only to the
validated loopback `app.httpBaseURL`. Supported methods are `GET`, `POST`,
`PUT`, `PATCH`, and `DELETE`. `allowedPathPrefixes` (when non-empty) limits
validated paths using path-segment boundaries. Invalid targets return a 400
reply without calling the app; disabled direct dispatch with no matching route
returns 404.

`directDispatch.timeout` is required and must be positive; it applies to every
direct dispatch.

Direct replies use type `io.eventadapter.direct.reply`, source `app.id`, and no
subject. They preserve correlation/causation, HTTP status, redirect location,
and response content type/body. Incoming publisher headers and cookies continue
to follow the existing forwarding and reserved-header rules, and the direct
timeout applies to the backend call.
Specifically, inbound CloudEvent `dispatchheaders` and `dispatchcookies` are
request metadata and are distinct from reply fields; response headers/cookies
are not copied into the direct reply CloudEvent. Operators should configure
`allowedPathPrefixes` whenever the local app exposes internal or admin endpoints.
`dispatchmethod` is case-insensitive and normalized to uppercase. Both
`dispatchmethod` and `dispatchpath` are control metadata stripped before
CloudEvent SDK parsing, so they are not sent as `ce-` headers. Paths require
exactly one leading slash and reject full/network URLs, fragments, backslashes,
traversal (including encoded separators), and control characters. Direct reply
IDs are deterministic, and telemetry uses the bounded route label `direct`.
Static JetStream routes may use `DELETE`, but JetStream never accepts
publisher-selected targets.

## Delivery guarantees

### Event model (JetStream)

- NATS message is **ack'd only after** the response CloudEvent is published (or DLQ write confirmed).
- Exhausted retries (`maxDeliver` reached) → publish to `dlq.subject` → ack original.
- App HTTP non-2xx → wrap status + body as response CloudEvent, publish, ack. No retry.
- Network/transport error → retry with exponential backoff up to `maxAttempts`, then DLQ.

### Request-reply model

- Synchronous: every outcome is a reply on the caller's inbox; **no ack/retry/DLQ**.
- App HTTP response (incl. 4xx/5xx) → forwarded as a normal reply CloudEvent with the status in `httpstatus`.
- Malformed request → 400 error reply; no matching route → 404; app unreachable → 502; app timeout → 504.
- Missing reply inbox → dropped (metric only). Reply `causationid` = request id; `correlationid` passed through.

## Testing

```sh
go test ./...    # unit tests (no NATS needed)
go test $(go list ./... | grep -v 'cmd/mock-app\|examples') -cover    # coverage (excludes non-production packages)
```

E2e (requires Docker / NATS):
```sh
cd test/e2e && docker compose up -d
go test -tags=e2e ./test/e2e/... -v
cd test/e2e && docker compose down
```

## Example config

`examples/onboarding/` — annotated `routes.yaml` and `publish.sh` for local
smoke-testing with a real NATS instance.
