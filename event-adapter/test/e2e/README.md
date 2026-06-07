# event-adapter — End-to-End Testing

Three containers, one command. NATS JetStream, the mock application server,
and the event-adapter sidecar all start together via Docker Compose.

## Prerequisites

- Docker with the Compose plugin — verify with `docker compose version`
- [NATS CLI](https://github.com/nats-io/natscli) — used to publish test events
  and inspect subjects

## Quick start

Run all commands from this directory (`test/e2e/`).

```sh
# 1. Build images and start all services
docker compose up --build -d

# 2. Verify all containers are running
docker compose ps

# 3. Watch response events (keep this terminal open)
nats sub --server nats://127.0.0.1:4222 "t.tenant-a.app.task.event.processed"

# 4. Publish a test event (in a second terminal)
nats pub --server nats://127.0.0.1:4222 \
  t.tenant-a.app.task.event.created \
  --stdin < fixtures/task-created.json

# 5. Inspect what mock-app received from the sidecar
docker compose logs mock-app

# 6. Tear down
docker compose down
```

You should see a response CloudEvent appear in terminal 3 within a second of
publishing in terminal 4.

## Running the automated Go test

The Go test connects to the running stack, publishes the fixture, and asserts
on the response CloudEvent. The stack must be up before running it.

```sh
# From this directory
docker compose up --build -d

# From the event-adapter module root (one level up from test/)
cd ../..
go test -tags=e2e ./test/e2e/... -v

# Tear down
docker compose -f test/e2e/docker-compose.yaml down
```

The `//go:build e2e` tag means `go test ./...` (no tag) skips this suite.

## TestRequestReplyPresign

Exercises the request-reply responder. Sends a `com.workspace.uploads.presign.request`
CloudEvent via `nats request` to `q.tenant-a.app.uploads.request`; the adapter
dispatches to the mock-app `/requests/upload-presign` handler and replies on the
request inbox. Asserts the reply CloudEvent type, `causationid`, `httpstatus=200`,
and the presigned `uploadId` from the mock response.

## Services

| Service | Port | Description |
|---|---|---|
| `nats` | 4222 | NATS JetStream broker |
| `nats-setup` | — | One-shot: creates `workspace-events` stream, then exits |
| `mock-app` | 18080 | Configurable mock HTTP server; logs all requests to stdout |
| `event-adapter` | — | The sidecar under test |

## Config files

All config files are bind-mounted. Edit and restart the relevant service —
no rebuild needed.

| File | Affects | Restart command |
|---|---|---|
| `routes.yaml` | Sidecar routing and response subjects | `docker compose restart event-adapter` |
| `mock-app.yaml` | Mock handler paths, required headers, responses | `docker compose restart mock-app` |

## Fixtures

`fixtures/task-created.json` — a complete CloudEvent payload ready to publish:

```json
{
  "specversion": "1.0",
  "id": "evt-manual-1",
  "source": "workspace/task",
  "type": "com.workspace.task.created",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1",
    "X-Workspace-Tenant-Id": "tenant-a"
  },
  "data": {"taskId": "task-1"}
}
```

To test a different scenario, create a new JSON file in `fixtures/` and publish
it the same way.

## Testing the DLQ path

Trigger a DLQ delivery by sending an event that mock-app will reject (missing
the required `X-Workspace-Actor-Id` header). Remove it temporarily from
`mock-app.yaml`:

```yaml
handlers:
  - method: POST
    path: /events/task-created
    # requireHeaders removed — mock-app now returns 200 for any request
    response:
      status: 400
      contentType: text/plain
      body: "rejected"
```

```sh
docker compose restart mock-app
nats sub --server nats://127.0.0.1:4222 "dlq.tenant-a.task-service"
nats pub --server nats://127.0.0.1:4222 \
  t.tenant-a.app.task.event.created \
  --stdin < fixtures/task-created.json
```

After `maxAttempts` retries the sidecar publishes a DLQ event to
`dlq.tenant-a.task-service`. Restore `mock-app.yaml` and restart when done.

## Modifying mock-app handlers

`mock-app.yaml` supports multiple handlers — one per route. Add a handler for
each route you add to `routes.yaml`:

```yaml
handlers:
  - method: POST
    path: /events/task-created
    requireHeaders:
      - X-Workspace-Actor-Id
    response:
      status: 200
      contentType: application/json
      body: '{"ok":true}'

  - method: POST
    path: /events/task-updated
    response:
      status: 204
      body: ""
```

## Troubleshooting

**`event-adapter` exits immediately**

The sidecar cannot subscribe to a NATS stream that does not exist. Check
`nats-setup` completed successfully:

```sh
docker compose logs nats-setup
```

If it failed, recreate the stack: `docker compose down && docker compose up --build -d`

**No response event appears after publishing**

Check the sidecar is running and processing events:

```sh
docker compose logs event-adapter
```

Check mock-app received the request and returned 2xx:

```sh
docker compose logs mock-app
```

**`go test` times out**

The test connects to `nats://127.0.0.1:4222`. Verify the stack is up and port
4222 is reachable:

```sh
docker compose ps
nats --server nats://127.0.0.1:4222 server ping
```

**Stale durable consumer after stream purge**

If you delete and recreate the NATS stream manually, the event-adapter's
durable consumer is gone too. Restart the sidecar to re-subscribe:

```sh
docker compose restart event-adapter
```
