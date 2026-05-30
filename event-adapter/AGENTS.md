# event-adapter — Agent Context

NATS JetStream → local HTTP dispatch sidecar. Consumes CloudEvents from a
durable consumer, POSTs to a colocated app server, publishes response
CloudEvents, DLQs exhausted messages.

## Internal packages

| Package | Role |
|---|---|
| `internal/config` | Parse + validate `routes.yaml` schema |
| `internal/cloudevent` | CloudEvent envelope construction and response wrapping |
| `internal/natsjs` | NATS JetStream connection, consumer management |
| `internal/dispatcher` | HTTP client that calls app handlers |
| `internal/router` | Match incoming CloudEvents to route config |
| `internal/processor` | Retry logic and DLQ publication |
| `internal/metrics` | OpenTelemetry counters + histograms |

## Routes config (routes.yaml)

```yaml
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      subject: t.tenant-a.app.task.event.created
      type: com.workspace.task.created
      source: workspace/task
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

## Delivery guarantees

- NATS message is **ack'd only after** the response CloudEvent is published (or DLQ write confirmed).
- Exhausted retries (`maxDeliver` reached) → publish to `dlq.subject` → ack original.
- App HTTP non-2xx → retry with exponential backoff up to `maxAttempts`.

## Testing

```sh
go test ./...    # unit tests (no NATS needed)
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
