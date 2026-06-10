# event-adapter — local dev environment

Stand up a local NATS server and the event-adapter sidecar to connect your app service
for end-to-end testing — no cloud infrastructure required.

## What's included

| File | Purpose |
|------|---------|
| `docker-compose.yml` | NATS server (JetStream + WebSocket + monitoring), stream setup, and event-adapter sidecar |
| `nats-server.conf` | NATS config — JetStream storage, WebSocket on port 4223, HTTP monitoring on 8222 |
| `event-adapter.yaml` | Template sidecar config — copy and edit for your service |
| `fixtures/sample-event.json` | Sample CloudEvent for JetStream publish/subscribe testing |
| `fixtures/sample-request.json` | Sample CloudEvent for request-reply testing |

## Prerequisites

- Docker + Docker Compose v2
- The event-adapter image (provided by your platform team)
- [`nats` CLI](https://github.com/nats-io/natscli) for manual pub/sub (optional but useful)

---

## Quick start

### 1. Configure the sidecar

Copy the template and edit it for your service:
```sh
cp event-adapter.yaml my-service.yaml
$EDITOR my-service.yaml
```

Key fields to set:

```yaml
app:
  id: my-service
  httpBaseURL: http://host.docker.internal:8080   # port your app listens on

nats:
  url: nats://nats:4222
  filterSubject: events.>                         # narrow to your own subjects

routes:
  - name: my-event
    match:
      type: com.example.my-event                  # CloudEvent type your app handles
    dispatch:
      path: /events/my-event                      # endpoint on your app
```

See [event-adapter.yaml](event-adapter.yaml) for the full template with comments.

### 2. Start everything

```sh
export EVENT_ADAPTER_IMAGE=<image provided by your platform team>
docker compose up -d
```

This starts:
- **NATS** on `localhost:4222` (Go client / CLI)
- **WebSocket** on `localhost:4223` (browser / `nats.ws`)
- **Monitoring** at http://localhost:8222
- **nats-setup** — one-shot container that creates the `workspace-events` JetStream stream
- **event-adapter** — sidecar forwarding events to your app on the host machine

### 3. Start your app

Start your service on the port you set in `app.httpBaseURL`. The event-adapter will
forward CloudEvents to it as HTTP POST requests.

Verify NATS and the sidecar are up:
```sh
nats server ping --server nats://localhost:4222
docker compose logs event-adapter -f
```

---

## End-to-end testing

### JetStream events (async)

Publish a test event — the adapter dispatches it to your app and publishes a response:

```sh
# Publish the sample event (edit fixtures/sample-event.json to match your routes)
nats pub --server nats://localhost:4222 \
  events.my-service.my-event \
  --stdin < fixtures/sample-event.json

# Watch responses land on the response subject
nats sub --server nats://localhost:4222 "events.my-service.processed.>"

# Watch DLQ for failures
nats sub --server nats://localhost:4222 "dlq.my-service"
```

### Request-reply (sync)

Send a synchronous request and wait for the reply:

```sh
nats request --server nats://localhost:4222 \
  q.my-service.requests \
  --stdin < fixtures/sample-request.json
```

### WebSocket client (browser / nats.ws)

Connect from JavaScript using [nats.ws](https://github.com/nats-io/nats.ws):

```js
import { connect } from "nats.ws";

const nc = await connect({ servers: "ws://localhost:4223" });

// Subscribe to responses
const sub = nc.subscribe("events.my-service.processed.>");
(async () => {
  for await (const msg of sub) {
    console.log("response:", new TextDecoder().decode(msg.data));
  }
})();

// Publish a test event
nc.publish(
  "events.my-service.my-event",
  JSON.stringify({
    specversion: "1.0",
    id: "ws-test-1",
    source: "browser/test",
    type: "com.example.my-event",
    datacontenttype: "application/json",
    data: { message: "hello from browser" },
  })
);
```

---

## Customising JetStream streams

The default stream (`workspace-events`) captures `events.>` and `dlq.>`.

To add or change streams, edit the `nats-setup` service in `docker-compose.yml`, then
re-run the setup container:

```sh
docker compose run --rm nats-setup
```

To inspect streams and consumers interactively:
```sh
nats stream ls   --server nats://localhost:4222
nats stream info --server nats://localhost:4222 workspace-events
nats consumer ls --server nats://localhost:4222 workspace-events
```

---

## Teardown

```sh
docker compose down          # stop containers, keep the nats-data volume
docker compose down -v       # stop and delete all data
```

---

## Ports reference

| Port | Protocol | Usage |
|------|----------|-------|
| 4222 | TCP | NATS client (Go, CLI, SDK) |
| 4223 | TCP (WS) | NATS WebSocket (`ws://localhost:4223`) |
| 8222 | HTTP | NATS monitoring dashboard |
