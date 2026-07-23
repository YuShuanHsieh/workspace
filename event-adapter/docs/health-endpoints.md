# Health endpoints

The event-adapter serves two health endpoints on the **health port**
(`healthAddr`, default `:8080`) — separate from the metrics port (`:8200`).
Both respond with `Content-Type: application/json`.

| Endpoint | Probe | Healthy | Unhealthy |
|---|---|---|---|
| `GET /ready` | readiness | `200` | `503` |
| `GET /live` | liveness | `200` | `503` |

---

## `GET /ready` — readiness

Reports whether the adapter can do useful work, i.e. whether the **NATS
connection is up**. Use it as the Kubernetes *readiness* probe so traffic is
only routed to pods with a live NATS connection.

**Healthy — NATS connected → `200 OK`**

```json
{ "ready": true }
```

**Unhealthy — NATS connection down → `503 Service Unavailable`**

```json
{ "ready": false, "reason": "nats connection failure" }
```

---

## `GET /live` — liveness

Reports whether the event-processing loop is making progress. Use it as the
Kubernetes *liveness* probe so a wedged pod is restarted.

The consumer loop calls a heartbeat on every fetch iteration. `/live` fails if
that heartbeat goes stale beyond `maxHeartbeatAge` (**60s**), which signals the
loop is deadlocked.

**Healthy — heartbeat fresh → `200 OK`**

```json
{ "alive": true }
```

**Unhealthy — heartbeat stale → `503 Service Unavailable`**

```json
{ "alive": false, "reason": "event loop heartbeat stale" }
```

### Request-reply-only deployments

Both event loops drive the heartbeat: the JetStream **consumer** beats it on
every fetch iteration, and the request-reply **responder** beats it on a fixed
interval while serving. So a service configured with only a `requests:` block
(no `routes:`) still keeps `/live` fresh — the responder's periodic beat means
an idle sync-only deployment is neither reported dead nor left unmonitored.

---

## Kubernetes probe example

```yaml
readinessProbe:
  httpGet:
    path: /ready
    port: 8080
  periodSeconds: 10
livenessProbe:
  httpGet:
    path: /live
    port: 8080
  periodSeconds: 10
  failureThreshold: 3
```

> Point probes at the **health port (8080)**, not the metrics port (8200).
