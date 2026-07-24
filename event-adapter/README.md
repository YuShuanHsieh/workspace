# event-adapter

NATS-to-local-HTTP dispatch sidecar. Bridges NATS messages to an app's loopback
HTTP handlers so the app never embeds NATS client code.

Design source: `../prd/event-adapter/prd.md`. Request-reply designs:
`../docs/superpowers/specs/2026-06-07-event-adapter-req-reply-design.md` and
`../docs/superpowers/specs/2026-07-23-event-adapter-direct-request-dispatch-design.md`.

Two inbound delivery models share one dispatch core (`parse CloudEvent → match by
type → call the local HTTP handler`):

**Request-reply (primary)** — for synchronous, HTTP-style calls switched onto the
event backbone:

- subscribe to a core-NATS request subject within a queue group
- dispatch JSON CloudEvent data to configured localhost HTTP handlers
- return the HTTP response as a reply CloudEvent on the caller's inbox
- return structured error replies (400/404/502/504) instead of hanging; no retry/DLQ

**JetStream event consumption (opt-in)** — for durable, fire-and-forget fan-out:

- consume CloudEvents from JetStream durable consumers
- dispatch JSON CloudEvent data to configured localhost HTTP handlers
- publish deterministic response CloudEvents
- publish exhausted failures to DLQ
- acknowledge original messages only after response or DLQ publish confirmation

Either model may be configured alone or both together; at least one is required.

### Direct request dispatch

Request-reply direct dispatch is disabled unless `requests.directDispatch.enabled`
is explicitly enabled. Exact request routes take precedence. For an unmatched
type, an authorized publisher may provide a fully resolved relative
`dispatchpath` and `dispatchmethod` (`GET`, `POST`, `PUT`, `PATCH`, or `DELETE`):

```yaml
requests:
  subject: q.tenant-a.app.orders.request
  queueGroup: order-responders
  workerPoolSize: 16
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes:
      - /orders/
```

The adapter joins the relative path only to the validated loopback
`app.httpBaseURL`; publishers cannot choose a host, scheme, or port. Invalid
targets return a structured 400 reply without a backend call. If direct
dispatch is disabled and no exact route matches, the reply is 404. Generic
direct replies use type `io.eventadapter.direct.reply`, source `app.id`, and no
subject while preserving correlation/causation, status, location, and response
content type/body. Incoming publisher headers and cookies use the existing
forwarding and reserved-header rules. `directDispatch.timeout` is required,
must be positive, and applies to every direct dispatch. JetStream routes remain
static (though they may now use `DELETE`) and never accept publisher-selected
targets. Inbound CloudEvent `dispatchheaders` and `dispatchcookies` are request
metadata, distinct from reply fields; response headers/cookies are not copied
into the direct reply CloudEvent. Configure `allowedPathPrefixes` whenever the
local app exposes internal or admin endpoints.
`dispatchmethod` is case-insensitive and normalized to uppercase. The
`dispatchmethod` and `dispatchpath` fields are control metadata removed before
CloudEvent parsing and are not forwarded as `ce-` headers. Paths require
exactly one leading slash and reject full/network URLs, fragments, backslashes,
traversal (including encoded separators), and control characters. Direct reply
IDs are deterministic; metrics use the bounded route label `direct`.

## Repo layout

```
cmd/
  event-adapter/   sidecar entrypoint
  mock-app/        standalone mock HTTP server for local development and e2e testing
internal/
  cloudevent/      CloudEvent parsing and response construction
  config/          YAML schema + validator
  dispatcher/      HTTP client that calls app handlers
  metrics/         OpenTelemetry counters and histograms
  natsjs/          NATS connection, JetStream consume, and request subscription helpers
  processor/       retry logic and DLQ publication (event model)
  responder/       request-reply loop: dispatch and reply on the caller's inbox
  router/          match incoming CloudEvents to event or request route config
examples/onboarding/   annotated routes.yaml and smoke-test scripts
test/e2e/          end-to-end test suite (docker compose + Go test)
```

## Build

Requires Go 1.25.

```sh
go build ./cmd/event-adapter
go build ./cmd/mock-app
```

## Configuration

Route config is a YAML file passed via `--config` (default `routes.yaml`).
See `examples/onboarding/routes.yaml` for an annotated example and
`../prd/event-adapter/prd.md` for the full schema reference.

## Testing

### Unit tests

```sh
go test ./...
go vet ./...
test -z "$(gofmt -l .)"
```

Unit tests require no external services. The `//go:build e2e` tag keeps the
end-to-end suite out of this command.

### Linting

```sh
golangci-lint run ./...
```

### End-to-end tests

The e2e suite runs the full sidecar round-trip: NATS → event-adapter → mock-app
→ response CloudEvent back on NATS.

**Prerequisites:** Docker with the Compose plugin, and the
[NATS CLI](https://github.com/nats-io/natscli) for manual event publishing.

**Start the stack:**

```sh
cd test/e2e
docker compose up --build -d
```

This starts four containers:

| Container | Description |
|---|---|
| `nats` | NATS JetStream broker (port 4222) |
| `nats-setup` | One-shot: creates the `workspace-events` stream, then exits |
| `mock-app` | Configurable mock HTTP server (port 18080); logs every request |
| `event-adapter` | The sidecar under test |

**Publish a test event and watch the response:**

```sh
# Terminal 1 — watch response events
nats sub --server nats://127.0.0.1:4222 "t.tenant-a.app.task.event.processed"

# Terminal 2 — publish
nats pub --server nats://127.0.0.1:4222 \
  t.tenant-a.app.task.event.created \
  --stdin < test/e2e/fixtures/task-created.json

# Terminal 3 — inspect what the sidecar sent to mock-app
docker compose -f test/e2e/docker-compose.yaml logs mock-app -f
```

**Run the automated Go test** (stack must be running):

```sh
cd test/e2e && docker compose up --build -d && cd ../..
go test -tags=e2e ./test/e2e/... -v
docker compose -f test/e2e/docker-compose.yaml down
```

**Modify config without rebuilding:**

```sh
# Change sidecar routing
$EDITOR test/e2e/routes.yaml
docker compose -f test/e2e/docker-compose.yaml restart event-adapter

# Change mock-app response or add a new handler
$EDITOR test/e2e/mock-app.yaml
docker compose -f test/e2e/docker-compose.yaml restart mock-app
```

**Tear down:**

```sh
docker compose -f test/e2e/docker-compose.yaml down
```

For full details on fixtures, services, and troubleshooting see
[`test/e2e/README.md`](test/e2e/README.md).
