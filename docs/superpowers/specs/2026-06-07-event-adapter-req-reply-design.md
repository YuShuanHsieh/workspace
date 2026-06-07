# event-adapter: NATS request-reply responder mode

**Date:** 2026-06-07
**Status:** Design — pending implementation
**Component:** `event-adapter`

## Summary

Make **NATS request-reply the primary model** of the event-adapter sidecar, with
the existing JetStream consume-and-publish path kept as an **opt-in** mechanism
for durable, fire-and-forget fan-out.

A backend app answers NATS request-reply calls without writing any NATS code: the
sidecar subscribes to a core-NATS request subject, dispatches the request to the
local HTTP app over loopback, and sends the app's HTTP response back on the
request's reply inbox.

Both paths share **one dispatch core** — `parse CloudEvent → match by type →
POST to the loopback app` — reusing the CloudEvent parser, the HTTP dispatcher,
and the loopback trust boundary. They differ only in delivery semantics:

- **Request-reply (primary):** caller waits on the reply inbox; at-most-once;
  no ack/retry/DLQ; failures return an error reply that the caller may retry.
- **JetStream event (opt-in):** publisher does not wait; at-least-once with
  redelivery; retry + DLQ; the response is a *published* event for 1:N fan-out.

Most "HTTP call switched to event-based" flows are synchronous and use
request-reply. JetStream is reserved for genuinely asynchronous fan-out where a
crash must not lose the work (e.g. post-upload scan/thumbnail processing).

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

- Inbound responder: NATS request → local HTTP dispatch → NATS reply, as the
  **primary** model.
- Unify the dispatch core so the request-reply path and the existing JetStream
  path share one `parse → match → dispatch` implementation.
- Reposition the JetStream consume/publish path as **opt-in** (kept, unchanged in
  behavior; demoted in positioning).

### Which model to use

| Use request-reply (primary) when… | Use JetStream event (opt-in) when… |
|---|---|
| A caller is synchronously waiting for an answer | The publisher fires and moves on |
| 1:1 call/response | 1:N fan-out to independent consumers |
| Caller can retry on failure | Work must survive a consumer crash (at-least-once) |
| e.g. upload presign, most HTTP→event calls | e.g. post-upload scan/thumbnail/finalize |

### Out of scope

- Outbound publish (app emitting events through the sidecar). The NATS-native
  client publishes `uploads.completed` itself; the backend never publishes.
- Outbound requester (app acting as a req-reply client through the sidecar).
- An HTTP ingress gateway (HTTP→NATS conversion). Clients are NATS-native.

## Design

### Architecture

The responder is the primary path; the event consumer runs **concurrently** as an
opt-in path in the same process. Both share the single NATS connection and the
unified dispatch core; they are otherwise independent.

```text
                          event-adapter (one process)
  ┌───────────────────────────────────────────────────────────────────┐
  │  core NATS req   ──▶ responder ──┐                                   │  (primary)
  │                                  ├──▶ dispatch core ──▶ loopback HTTP │
  │  JetStream pull  ──▶ consumer  ──┘        │                          │  (opt-in)
  │                                           ▼                          │
  │  responder → msg.Respond(reply)   consumer → publish *.processed     │
  └───────────────────────────────────────────────────────────────────┘
                                              │
                                              ▼
                                       local app handlers
```

The responder is a sibling of `consumer`, not a branch inside it. The
synchronous request lifecycle (no ack, no retry, no DLQ; every outcome is a
reply) is kept physically separate from the durable event lifecycle. What they
share is the dispatch core (`parse → match → dispatch`), extracted so both call
it.

### Config schema

`requests:` is the primary top-level block. The JetStream blocks (`nats:` +
`routes:`) become **opt-in**: a deployment may configure `requests:` alone (a
pure responder), the JetStream blocks alone (backward-compatible with today), or
both. At least one of the two must be present. Omitting `requests:` preserves
today's behavior exactly.

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
| `config` | Add `RequestsConfig{Subject, QueueGroup, WorkerPoolSize, Routes []RequestRouteConfig}`, `RequestRouteConfig{Name, Match MatchConfig, Dispatch DispatchConfig, Reply ReplyConfig}`, `ReplyConfig{Source, Type, DataSchema}`. Validation reuses `validateDispatch` and the reserved-header checks; adds: `subject`/`queueGroup` non-empty, `workerPoolSize > 0`, unique request `match.type`, `reply.type`/`reply.source` required. Reject `response`/`retry`/`dlq` keys on request routes. Require at least one of `requests` or the JetStream blocks to be present. |
| `natsjs` | Add `SubscribeRequests(subject, queue string, h func(RequestMsg)) (*nats.Subscription, error)` using the existing `c.nc.QueueSubscribe`. Add a `RequestMsg` wrapper exposing `Data []byte`, `Subject`, `ReplyTo string`, and `Respond([]byte) error`. No JetStream. |
| `dispatcher` | **Targeted refactor:** change the core method signature to `Dispatch(ctx, d config.DispatchConfig, ev *clevent.Event) (Result, error)` — it already reads only `route.Dispatch.*`. The event path passes `route.Dispatch`; the responder passes the request route's `Dispatch`. Shared, no duplication. (`setPublisherHeaders` reads `DispatchConfig.ForwardHeaders` directly.) |
| `cloudevent` | Add `BuildReply(in *Event, reply config.ReplyConfig, status int, contentType string, body []byte) (*ce.Event, error)` — sibling of `BuildResponse` minus `subject`. Sets `type`/`source` from `reply`, the `httpstatus` extension, `causationid` (= request id), and passes through `correlationid`. Deterministic id derived from request id + reply type. |
| `router` | Add `NewRequests([]config.RequestRouteConfig) (*RequestMatcher, error)` building a `map[type]RequestRouteConfig`, rejecting duplicate types. Separate index from the event matcher. |
| **`responder`** (new package) | Mirrors `consumer`: bounded worker pool, `parse → match → dispatch → BuildReply → msg.Respond`. No ack/Nak/DLQ/retry. Owns the error-reply policy below. |
| `metrics` | Add `RequestReceived`, `RequestReplyLatency`, `RequestDispatchError`, `RequestNoReply`, `InvalidRequestEvent`. |
| `main.go` | Start whichever paths are configured: if `cfg.Requests` is set, build the request matcher + responder; if the JetStream blocks are set, build the event matcher + consumer. Run all configured paths concurrently (goroutines; block until ctx cancel). Fail fast if neither is configured. Print a startup line per active path. |

### Data flow (request path)

```text
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

Backward compatible. Existing deployments running only the JetStream blocks
(`nats:` + `routes:`) are unaffected — that path is unchanged, just repositioned
as opt-in. A service adopts request-reply by adding the `requests:` block and the
loopback handlers; no adapter code change is required per new request type beyond
config. New services default to request-reply and add JetStream only for genuine
fan-out.

## Open questions

None blocking. Subject/queue-group naming conventions to be aligned with the
platform's existing tenant subject scheme during implementation.
