# Design: event-adapter e2e Mock App Server

**Date:** 2026-05-30
**Status:** Approved
**Branch:** feat/e2e-mock-server

## Problem

The existing `test/e2e/e2e_test.go` is hard to monitor and modify because:

- The fake application server is embedded in Go test code (`httptestApp`), making it invisible and unreachable outside `go test`.
- The sidecar config is generated as a Go string template (`writeE2EConfig`), so developers cannot edit it without changing source code.
- There is no documented manual workflow — publishing a test event requires writing Go code.
- The only entry point is `go test -tags=e2e ./test/e2e/...`, which requires Docker, NATS, and familiarity with Go test flags.

## Goal

Give developers a `docker compose up --build` entry point that starts all three components (NATS, mock-app, event-adapter), with config files they can edit and reload without rebuilding. Manual event publishing requires only the NATS CLI.

## Architecture

```
test/e2e/
  docker-compose.yaml        three services: nats, mock-app, event-adapter
  routes.yaml                sidecar config (httpBaseURL updated for compose networking)
  mock-app.yaml              NEW: handler definitions for mock-app
  fixtures/
    task-created.json        NEW: ready-to-publish CloudEvent payload
  Dockerfile.mock-app        NEW: builds cmd/mock-app binary
  Dockerfile.event-adapter   NEW: builds cmd/event-adapter binary
  e2e_test.go                simplified: no embedded server, no generated config
  README.md                  NEW: developer quick-start

cmd/mock-app/
  main.go                    NEW: standalone configurable HTTP server binary
```

**Runtime data flow:**

```
Developer
  │  nats pub … @fixtures/task-created.json
  ▼
NATS JetStream ──► event-adapter (sidecar) ──► mock-app (:18080)
                         │                         │ logs every request to stdout
                         ◄─ response CloudEvent ◄──┘
NATS response subject (observable via nats sub …)
```

Config files are bind-mounted into their respective containers. Editing `routes.yaml` or `mock-app.yaml` and restarting the affected service picks up changes immediately — no rebuild required.

> **Networking note:** `routes.yaml` changes `httpBaseURL` from `http://127.0.0.1:18080` to `http://mock-app:18080` — Docker's internal DNS name for the mock-app service. This is the only change to that file.

## Component: `cmd/mock-app`

A lightweight HTTP server with no business logic. All behaviour is declared in YAML.

### Config schema (`mock-app.yaml`)

```yaml
addr: 0.0.0.0:18080   # bind address (overridable via --addr flag)

handlers:
  - method: POST
    path: /events/task-created
    requireHeaders:           # returns 400 if any listed header is missing
      - X-Workspace-Actor-Id
    response:
      status: 200
      contentType: application/json
      body: '{"ok":true}'
```

Multiple handlers are supported — one per route declared in `routes.yaml`.

### Runtime behaviour

- Every request is logged to stdout: method, path, all headers, full body. `docker compose logs mock-app -f` gives full request visibility with no code changes.
- Missing required header → `400 Bad Request` with a plain-text explanation.
- No matching handler → `404 Not Found`.
- Graceful shutdown on SIGINT/SIGTERM.

### CLI flags

| Flag | Default | Description |
|---|---|---|
| `--config` | `mock-app.yaml` | Path to handler config file |
| `--addr` | _(from config)_ | Override bind address |

## Component: `docker-compose.yaml`

```yaml
services:
  nats:
    image: nats:2.11
    command: ["-js", "-sd", "/data"]
    ports: ["4222:4222"]

  mock-app:
    build:
      context: ../..
      dockerfile: test/e2e/Dockerfile.mock-app
    ports: ["18080:18080"]
    volumes:
      - ./mock-app.yaml:/config/mock-app.yaml:ro
    command: ["--config", "/config/mock-app.yaml"]

  event-adapter:
    build:
      context: ../..
      dockerfile: test/e2e/Dockerfile.event-adapter
    volumes:
      - ./routes.yaml:/config/routes.yaml:ro
    command: ["--config", "/config/routes.yaml"]
    depends_on: [nats, mock-app]
```

## Component: `test/e2e/fixtures/task-created.json`

A complete, ready-to-publish CloudEvent payload. Developers paste it directly into `nats pub`:

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

## Component: updated `e2e_test.go`

The following helpers are removed entirely:

- `httptestApp` — embedded fake HTTP server
- `writeE2EConfig` — programmatic config generation
- `safeBuffer` — goroutine-safe output buffer
- `waitForOutput` — polling for sidecar startup log line

The test assumes compose services are already running (CI runs `docker compose up -d` before `go test`). It connects to NATS at `nats://127.0.0.1:4222`, publishes the fixture payload, and asserts on the response CloudEvent on `t.tenant-a.app.task.event.processed`.

The `//go:build e2e` tag is retained — plain `go test ./...` still skips e2e.

## Developer Quick-Start (README summary)

```sh
# 1. Start all services
docker compose up --build -d

# 2. Watch responses
nats sub "t.tenant-a.app.task.event.processed"

# 3. Publish a test event
nats pub t.tenant-a.app.task.event.created \
  --stdin < fixtures/task-created.json

# 4. Observe mock-app request logs
docker compose logs mock-app -f

# 5. Tear down
docker compose down
```

To change sidecar behaviour, edit `routes.yaml` and restart:
```sh
docker compose restart event-adapter
```

To change mock-app response, edit `mock-app.yaml` and restart:
```sh
docker compose restart mock-app
```

## CI Integration

```sh
cd event-adapter/test/e2e
docker compose up --build -d
sleep 3   # wait for services to be ready
cd ../..
go test -tags=e2e ./test/e2e/... -v
docker compose -f test/e2e/docker-compose.yaml down
```

## Out of Scope

- Dynamic handler reloading without restart (SIGHUP).
- Response templating from request fields.
- Request recording / replay.
- Multiple fixture scenarios beyond `task-created`.
