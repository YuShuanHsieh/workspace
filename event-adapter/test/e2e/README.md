# event-adapter — End-to-End Testing

Three containers, one command. NATS JetStream, the mock application server,
and the event-adapter sidecar all start together via Docker Compose.

## Prerequisites

- Docker with the Compose plugin (`docker compose version`)
- NATS CLI (`nats`) for publishing test events — install from https://github.com/nats-io/natscli

## Quick start

```sh
# 1. Build images and start all services
docker compose up --build -d

# 2. Open a terminal to watch response events
nats sub --server nats://127.0.0.1:4222 "t.tenant-a.app.task.event.processed"

# 3. Publish a test event
nats pub --server nats://127.0.0.1:4222 \
  t.tenant-a.app.task.event.created \
  --stdin < fixtures/task-created.json

# 4. Watch mock-app request logs (method, path, headers, body)
docker compose logs mock-app -f

# 5. Tear down
docker compose down
```

## Modifying behaviour without rebuilding

**Change sidecar routing** — edit `routes.yaml`, then:
```sh
docker compose restart event-adapter
```

**Change mock-app response** — edit `mock-app.yaml`, then:
```sh
docker compose restart mock-app
```

**Add a new event fixture** — create a JSON file in `fixtures/` and publish it
the same way as step 3 above.

## Services

| Service | Port | Description |
|---|---|---|
| `nats` | 4222 | NATS JetStream broker |
| `nats-setup` | — | One-shot container that creates the `workspace-events` stream |
| `mock-app` | 18080 | Configurable mock HTTP server (logs all requests to stdout) |
| `event-adapter` | — | The sidecar under test |

## Config files

| File | Purpose |
|---|---|
| `routes.yaml` | Sidecar route config (bind-mounted; edit + restart to reload) |
| `mock-app.yaml` | Mock-app handler config (bind-mounted; edit + restart to reload) |
| `fixtures/task-created.json` | Sample CloudEvent payload for `task.created` |

## Running the automated e2e test

The Go test requires the compose stack to be running:

```sh
docker compose up --build -d
sleep 3

# from the event-adapter module root
go test -tags=e2e ./test/e2e/... -v

docker compose down
```
