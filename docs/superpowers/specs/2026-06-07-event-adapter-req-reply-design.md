# event-adapter: NATS request-reply responder mode

**Date:** 2026-06-07
**Status:** Design — pending implementation
**Component:** `event-adapter`

## Summary

Add an inbound **request-reply responder** capability to the event-adapter
sidecar so a backend app can answer NATS request-reply calls without writing any
NATS code, the same way it already handles JetStream events. The sidecar
subscribes to a core-NATS request subject, dispatches the request to the local
HTTP app over loopback, and sends the app's HTTP response back on the request's
reply inbox.

This is the synchronous sibling of the existing consume-and-dispatch path. It
reuses the CloudEvent parser, the HTTP dispatcher, and the loopback trust
boundary; it does **not** use JetStream, ack/Nak, retry, or DLQ.

## Motivation

The platform is moving synchronous HTTP-style calls into the event world via
NATS request-reply. Clients are NATS-native and issue the request-reply
themselves; only file **byte transfer** stays on raw HTTP (presigned URLs to
object storage). Backend services should not have to implement NATS connection
handling to participate — the sidecar already owns the NATS connection for
events, so it should also bridge request-reply to the app's local HTTP handlers.

Concrete first use case: the file-upload presign endpoint. A NATS-native client
issues `com.workspace.uploads.presign.request`; the upload service validates and
mints presigned URL(s) via its local HTTP handler; the sidecar returns the reply
to the caller. (The `uploads.completed` event is published by the client and
consumed via the **existing** event path — out of scope here.)

### In scope

- Inbound responder: NATS request → local HTTP dispatch → NATS reply.

### Out of scope

- Outbound publish (app emitting events through the sidecar). The NATS-native
  client publishes `uploads.completed` itself; the backend never publishes.
- Outbound requester (app acting as a req-reply client through the sidecar).
- An HTTP ingress gateway (HTTP→NATS conversion). Clients are NATS-native.

## Design

### Architecture

The responder runs **concurrently** with the existing event consumer in the same
process. Both share the single NATS connection and the HTTP dispatcher; they are
otherwise independent.

```
                          event-adapter (one process)
  ┌───────────────────────────────────────────────────────────────────┐
  │  JetStream pull  ──▶ consumer ──▶ dispatcher ──▶ publish *.processed │  (existing)
  │                                       │                              │
  │  core NATS req   ──▶ responder ──────┘──▶ msg.Respond(reply)         │  (new)
  └───────────────────────────────────────────────────────────────────┘
                                         │ loopback HTTP
                                         ▼
                                   local app handlers
```

The responder is a sibling of `consumer`, not a branch inside it. The
synchronous request lifecycle (no ack, no retry, no DLQ; every outcome is a
reply) is kept physically separate from the durable event lifecycle.

### Config schema

New optional top-level `requests:` block, alongside `nats:` and `routes:`.
Omitting it preserves today's behavior exactly. A pure-responder deployment may
configure `requests:` with an empty event `routes:` list.

```yaml
requests:
  subject: q.tenant-a.app.uploads.request      # core-NATS subject to answer; may be wildcard
  queueGroup: upload-service-responders        # queue group: one delivery per group, horizontal scale
  workerPoolSize: 16                            # bounded in-flight HTTP dispatches
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request   # matched by CloudEvent type only
      dispatch:
        method: POST
        path: /requests/upload-presign          # loopback HTTP handler
        timeout: 3s
        forwardHeaders: [X-Workspace-Tenant-Id]
      reply:
        source: upload-service                   # reply CloudEvent source
        type: com.workspace.uploads.presign.reply  # reply CloudEvent type
        # dataSchema: optional
```

Differences from event routes, enforced by validation:

- Request routes have **no** `response.subject`, `retry`, or `dlq`. The reply
  goes to the request's `_INBOX`; a synchronous call has no durable
  retry/DLQ lifecycle.
- Request routes carry a `reply` block (`source` + `type`, optional
  `dataSchema`) in place of `response`.
- Request `match.type` values are unique **within** `requests.routes`, in a
  namespace separate from event routes. (An event route and a request route may
  legitimately share a type; they are matched by different subscriptions.)

### Components

| Package | Change |
|---|---|
| `config` | Add `RequestsConfig{Subject, QueueGroup, WorkerPoolSize, Routes []RequestRouteConfig}`, `RequestRouteConfig{Name, Match MatchConfig, Dispatch DispatchConfig, Reply ReplyConfig}`, `ReplyConfig{Source, Type, DataSchema}`. Validation reuses `validateDispatch` and the reserved-header checks; adds: `subject`/`queueGroup` non-empty, `workerPoolSize > 0`, unique request `match.type`, `reply.type`/`reply.source` required. Reject `response`/`retry`/`dlq` keys on request routes. |
| `natsjs` | Add `SubscribeRequests(subject, queue string, h func(RequestMsg)) (*nats.Subscription, error)` using the existing `c.nc.QueueSubscribe`. Add a `RequestMsg` wrapper exposing `Data []byte`, `Subject`, `ReplyTo string`, and `Respond([]byte) error`. No JetStream. |
| `dispatcher` | **Targeted refactor:** change the core method signature to `Dispatch(ctx, d config.DispatchConfig, ev *clevent.Event) (Result, error)` — it already reads only `route.Dispatch.*`. The event path passes `route.Dispatch`; the responder passes the request route's `Dispatch`. Shared, no duplication. (`setPublisherHeaders` reads `DispatchConfig.ForwardHeaders` directly.) |
| `cloudevent` | Add `BuildReply(in *Event, reply config.ReplyConfig, status int, contentType string, body []byte) (*ce.Event, error)` — sibling of `BuildResponse` minus `subject`. Sets `type`/`source` from `reply`, the `httpstatus` extension, `causationid` (= request id), and passes through `correlationid`. Deterministic id derived from request id + reply type. |
| `router` | Add `NewRequests([]config.RequestRouteConfig) (*RequestMatcher, error)` building a `map[type]RequestRouteConfig`, rejecting duplicate types. Separate index from the event matcher. |
| **`responder`** (new package) | Mirrors `consumer`: bounded worker pool, `parse → match → dispatch → BuildReply → msg.Respond`. No ack/Nak/DLQ/retry. Owns the error-reply policy below. |
| `metrics` | Add `RequestReceived`, `RequestReplyLatency`, `RequestDispatchError`, `RequestNoReply`, `InvalidRequestEvent`. |
| `main.go` | If `cfg.Requests` is set: build the request matcher and responder, and run it concurrently with the event consumer (both in goroutines; block until ctx cancel). Print a startup line analogous to the consumer's. |

### Data flow (request path)

```
NATS-native client ──request(subject, CloudEvent)──▶ core NATS [queue group]
  responder worker:
    ev   := clevent.Parse(msg.Data)
    route, ok := requestMatcher.Match(ev)
    res  := dispatcher.Dispatch(ctx, route.Dispatch, ev)   // HTTP POST loopback
    reply := clevent.BuildReply(ev, route.Reply, res.StatusCode, res.ContentType, res.Body)
    msg.Respond(json(reply))                                // → caller's _INBOX
```

The reply is itself a CloudEvent: `type` = `reply.type`, `data` = the app's HTTP
body, `httpstatus` extension carries the HTTP status, `causationid` = request id,
`correlationid` passed through when present. A presign success and a validation
rejection are **both replies**, distinguished by `httpstatus`.

### Error handling

Synchronous path: every outcome is a reply to a live caller; nothing is silently
dropped, and there is no DLQ.

| Situation | Result |
|---|---|
| App returns 4xx (e.g. content-type not allowed) | Normal reply, `httpstatus`=4xx, app body forwarded. Not an error. |
| Parse failure (malformed CloudEvent) | Reply CloudEvent, `httpstatus`=400, error body. |
| No matching route | Reply, `httpstatus`=404, "no matching route". |
| App down / connection refused | Reply, `httpstatus`=502. No retry — caller owns re-request. |
| App exceeds `dispatch.timeout` | Reply, `httpstatus`=504. |
| Message has no `ReplyTo` (misuse) | Metric `RequestNoReply`, drop, do not dispatch. |
| Reply exceeds NATS `max_payload` | `Respond` fails → log + metric. Doc guidance: cap batch size (e.g. ≤ 50 presigned URLs per request). |

Rationale: a synchronous request that fails returns an error reply to a waiting
caller, so there is nothing to dead-letter. Retry is the caller's decision.

### Concurrency & lifecycle

- Bounded worker pool sized by `requests.workerPoolSize` (buffered channel +
  N workers, same shape as `consumer`). Core NATS delivers on the subscription
  goroutine; the responder hands work to the pool, yielding natural backpressure
  when saturated.
- Graceful shutdown on ctx cancel: unsubscribe, stop accepting, drain in-flight,
  return. `nc.Drain()` in `Client.Close()` flushes pending replies.
- The event consumer and responder run independently; neither tears down the
  other short of process exit.

### Security & trust boundary

- Dispatch stays **loopback-only**, reusing `app.httpBaseURL` validation.
- Reserved-header rules (ce-*, authorization, cookie, idempotency-key, trace*,
  hop-by-hop) apply unchanged to request-route dispatch.
- Subjects should be tenant-scoped and queue groups per-service; auth/identity
  travels in the CloudEvent (`dispatchheaders`) or via NATS account/JWT, since
  request-reply carries no HTTP headers from the original caller.

## Testing

### Unit

- `config` parse/validate for the `requests` block, including rejection of
  `retry`/`dlq`/`response` on request routes, duplicate request `match.type`,
  and missing `reply.type`/`reply.source`.
- `responder` happy path: mock dispatcher + fake `RequestMsg` capturing
  `Respond`; assert reply bytes and CloudEvent fields.
- Each error-reply row in the table above.
- `cloudevent.BuildReply` field assertions (type, source, httpstatus,
  causationid, correlationid passthrough, deterministic id).
- Worker-pool concurrency bound.

### End-to-end

- Extend the docker-compose harness: drive `nats request <subject> < fixture`
  and assert the reply CloudEvent.
- `mock-app` gains a `/requests/upload-presign` handler returning a
  presign-shaped body.
- New fixture + a section in `test/e2e/README.md`.

## Rollout

Backward compatible. Existing deployments without a `requests:` block are
unaffected. A service opts in by adding the block and a loopback handler; no
adapter code change is required per new request type beyond config.

## Open questions

None blocking. Subject/queue-group naming conventions to be aligned with the
platform's existing tenant subject scheme during implementation.
