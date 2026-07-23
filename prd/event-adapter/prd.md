# Product Requirements Document: event-adapter Event Dispatch Sidecar

**Status:** Draft
**Target Audience:** Platform Team, Application Development Teams
**Related Documents:** [`../event-driven/prd.md`](../event-driven/prd.md), [`../app-to-app/draft.md`](../app-to-app/draft.md), [`../permission-validation/PRD.md`](../permission-validation/PRD.md)

---

## 1. Background

The workspace platform uses NATS as the event backbone for application integration. Application teams need a reliable way to receive platform events, process them in their own services, and publish response events back to the event bus.

Without a platform-provided adapter, every application team must implement the same infrastructure concerns:

- NATS connection management and subject subscription.
- CloudEvent parsing and validation.
- Event-to-handler routing.
- Retry, timeout, and dead-letter behavior.
- Response event construction and publication.
- Delivery metrics, tracing, and audit-friendly logs.

This creates duplicated boilerplate, inconsistent reliability behavior, and a higher adoption cost for teams that only want to expose business logic through ordinary HTTP handlers.

## 2. Purpose

The goal of this initiative is to provide a Kubernetes sidecar that consumes CloudEvents from NATS, dispatches them to configured HTTP endpoints on the colocated application server, and publishes the HTTP response as a new CloudEvent.

The sidecar lets application teams participate in the event-driven platform without embedding NATS client code in their services. App teams expose local HTTP paths; the platform sidecar owns event delivery mechanics.

### 2.1 Delivery Models

The sidecar supports two inbound delivery models that share one dispatch core (`parse CloudEvent → match by type → call the local HTTP handler`):

- **Request-reply (primary).** A NATS-native caller issues a request and blocks on the reply inbox. The sidecar dispatches to the local handler and returns the HTTP response as a reply CloudEvent on the caller's inbox. This is the default model for "HTTP-style" synchronous calls switched onto the event backbone (e.g. minting a file-upload presigned URL). At-most-once; no durable retry or DLQ — a failed request returns an error reply that the caller may retry. See [section 17](#17-request-reply-responder-primary-synchronous-model).

- **JetStream event consumption (opt-in).** A publisher fires a durable event and does not wait; one or more consumers react, and the response is itself a *published* event for 1:N fan-out. At-least-once with retry and DLQ. Reserved for genuinely asynchronous, fire-and-forget work that must survive a crash (e.g. post-upload scan/thumbnail processing). Covered by sections 4–10.

A deployment may configure either model alone or both together; at least one must be present. Existing JetStream-only deployments are unaffected — that path is unchanged, only repositioned as opt-in.

## 3. Goals & Non-Goals

### Goals

- Deploy a sidecar container next to each target application server using the Kubernetes sidecar pattern.
- Subscribe to configured NATS subjects and consume CloudEvents.
- Validate required CloudEvent envelope fields before dispatch.
- Route events to local HTTP handlers using static declarative configuration.
- Call the configured HTTP method and path on the colocated application server.
- Wrap successful HTTP responses as new CloudEvents.
- Publish response CloudEvents to configured NATS subjects.
- Retry failed dispatches with bounded backoff.
- Send events to a configured dead-letter subject after retry exhaustion.
- Provide logs, metrics, and trace correlation for consumed, dispatched, retried, failed, dead-lettered, and published events.

### Goals (Request-Reply)

- Subscribe to a configured core-NATS request subject within a queue group and answer request-reply calls.
- Dispatch each request to the local HTTP handler using the same route-by-`type` and loopback-only dispatch as the event path.
- Return the app's HTTP response as a reply CloudEvent on the caller's reply inbox, carrying the HTTP status in the `httpstatus` extension.
- Return a structured error reply (never a silent timeout) for malformed requests, unmatched routes, and dispatch failures.
- Bound in-flight dispatches with a configurable worker pool.

### Non-Goals

- Providing an **outbound** publishing or requesting API where the application calls the sidecar to publish events or originate NATS requests. Both delivery models remain **inbound**: the app only ever answers local HTTP calls. (Note: answering inbound request-reply *is* in scope — it does not require the app to call the sidecar.)
- Runtime event marketplace or registry lookup. Phase 1 uses static YAML configuration.
- Direct permission evaluation inside this sidecar. Permission checks should remain handled by the permission-validation flow where required.
- Exactly-once business effects. The platform provides at-least-once delivery with idempotency guidance.
- Long-term audit storage. The sidecar emits operational logs and metrics, but durable audit retention is handled by the broader platform.

## 4. Proposed Architecture

The sidecar runs in the same Kubernetes pod as the target application server.

```text
               +------------------+
               | NATS Event Bus   |
               +--------+---------+
                        |
                        | CloudEvent on subscribed subject
                        v
        +---------------+----------------+
        | Client-to-Server Sidecar       |
        | - NATS subscriber              |
        | - CloudEvent validator         |
        | - Static route matcher         |
        | - HTTP dispatcher              |
        | - Response event publisher     |
        | - Retry / DLQ handler          |
        +---------------+----------------+
                        |
                        | localhost HTTP
                        v
               +--------+---------+
               | App HTTP Server  |
               +--------+---------+
                        |
                        | HTTP response body
                        v
        +---------------+----------------+
        | Sidecar wraps response as      |
        | CloudEvent and publishes       |
        +---------------+----------------+
                        |
                        v
               +------------------+
               | NATS Event Bus   |
               +------------------+
```

### Component Responsibilities

1. **NATS JetStream Consumer:** Connects to NATS with platform-provided credentials and consumes configured subjects through a JetStream durable consumer.
2. **CloudEvent Validator:** Verifies the event is valid enough for routing and dispatch. At minimum, the sidecar must require `id`, `source`, `specversion`, `type`, and payload presence when the route expects a body.
3. **Route Matcher:** Maps the CloudEvent `type` to a configured HTTP route.
4. **HTTP Dispatcher:** Sends the event payload to the configured local HTTP server path with the configured method, timeout, and headers.
5. **Response Event Builder:** Wraps the HTTP response body as the `data` field of a new CloudEvent with configured response metadata.
6. **NATS Publisher:** Publishes the response CloudEvent to the configured NATS subject and confirms durable publication before acknowledging the original event.
7. **Retry and DLQ Handler:** Retries transient failures with bounded backoff, publishes the original event plus failure metadata to a dead-letter subject after retry exhaustion, and acknowledges the original event only after the DLQ publish is confirmed.

## 5. Event Flow

1. The sidecar starts and loads its static route configuration.
2. The sidecar connects to NATS JetStream and binds to a configured durable consumer.
3. JetStream delivers a CloudEvent to the sidecar and keeps it unacknowledged while processing is in progress.
4. The sidecar validates the CloudEvent envelope.
5. The sidecar matches the event against route configuration using the CloudEvent `type`.
6. The sidecar dispatches the request to the configured local HTTP method and path.
7. The app processes the event and returns an HTTP response.
8. On any HTTP response (success or error), the sidecar wraps the response body as a new CloudEvent and stamps the HTTP status code into the `httpstatus` extension.
9. The sidecar publishes the response CloudEvent to the configured NATS response subject and waits for durable publish confirmation.
10. After response publish confirmation, the sidecar acknowledges the original JetStream message.
11. On network-class dispatch failure (timeout, connection refused, TLS error, transport error), the sidecar retries according to route policy. After retry exhaustion, it publishes the failed event to the configured DLQ subject and acknowledges the original message only after DLQ publish confirmation.

## 6. Route Configuration

Phase 1 uses static YAML configuration. The sidecar must fail startup if the configuration is invalid.

Example shape:

```yaml
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
  credsFilePath: /etc/nats/svc.creds   # optional — set for authenticated NATS

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
      forwardHeaders:
        - X-Workspace-Actor-Id
        - X-Workspace-Tenant-Id
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

The final implementation may adjust field names, but the PRD requires these concepts:

- Application identity and local HTTP base URL.
- NATS JetStream connection, stream, durable consumer, acknowledgement settings, and default DLQ subject for pre-route failures.
- One or more route entries.
- Match rules using the CloudEvent `type`. `match.subject` and `match.source` are optional and do not affect matching; they may be omitted unless kept for documentation.
- Dispatch method, path, timeout, optional static headers, and an allowlist of publisher-supplied HTTP headers that may be forwarded to the app backend.
- Response CloudEvent type, source, and publish subject.
- Retry policy.
- DLQ subject.

## 7. HTTP Dispatch Contract

For Phase 1, the sidecar forwards the incoming CloudEvent payload to the configured HTTP endpoint.

Required behavior:

- The request method and path come from route configuration.
- The request target is the local application server, normally `127.0.0.1` or `localhost`.
- The request body is the CloudEvent `data` payload.
- The sidecar forwards selected CloudEvent metadata using the CloudEvents HTTP protocol binding. Standard CloudEvent attributes use `ce-` headers, and extension attributes use `ce-<extension-name>` headers.
- The sidecar must forward the incoming event `id` as `ce-id` and as the `Idempotency-Key` value unless the incoming event already contains an explicit idempotency extension mapped by the route.
- The sidecar must preserve `datacontenttype`, `dataschema`, and extension attributes that are required for correlation, causation, tenant identity, and app identity.
- The publisher may provide backend-required HTTP headers in a CloudEvent extension named `dispatchheaders`. The extension value must be a JSON object whose keys are HTTP header names and whose values are strings.
- By default, the sidecar forwards every header in `dispatchheaders` to the backend, except names on the reserved-header set (CloudEvent metadata, `Idempotency-Key`, `Authorization`, trace context (`traceparent`), hop-by-hop headers). Reserved names are dropped at runtime regardless of configuration.
- A route may set `dispatch.forwardHeaders` as an opt-in allowlist to restrict which `dispatchheaders` are forwarded for that route. When the list is set, only listed names are forwarded; when it is empty or omitted, the default-forward behavior applies.
- The sidecar must treat `dispatchheaders` as sidecar control metadata and must not forward it to the backend as a `ce-dispatchheaders` header.
- Publishers may also supply a `dispatchcookies` object (`name → value`) to forward HTTP cookies (e.g. session tokens) onto the outbound request. Cookies are forwarded as-is; the `Cookie` header is reserved so `dispatchcookies` is the only path.
- Publisher-supplied headers must not override CloudEvent, trace context, authorization, idempotency, hop-by-hop, or route-configured static headers.
- The sidecar must support JSON `data` payloads in Phase 1. Binary payloads and `data_base64` are out of scope unless explicitly enabled by route configuration.
- The application should return a `2xx` status code for successful processing.
- `4xx` and `5xx` responses are not retried. The sidecar publishes them to the route response subject as response events carrying the HTTP status code, so the publisher can observe the error outcome. `3xx` responses are treated the same as `2xx`.
- The application response body becomes the `data` field of the response CloudEvent for both success and error outcomes.

`dispatch.path` supports `{fieldName}` template tokens that are resolved at dispatch time against the envelope-level `dispatchpathparams` map (separate from the `data` request payload). Token names must match `[a-zA-Z][a-zA-Z0-9_]*`. Values are URL-path-escaped. Multiple tokens are supported, and the same token may appear more than once.

Example:

```yaml
dispatch:
  method: PUT
  path: /api/tasks/{taskId}/complete
```

Incoming CloudEvent:

```json
{
  "dispatchpathparams": { "taskId": "task-42" },
  "data": { "title": "Buy milk" }
}
```

The sidecar dispatches `PUT /api/tasks/task-42/complete` with `{"title":"Buy milk"}` as the request body. Path parameters travel in their own envelope-level field so the `data` payload stays as the pure HTTP request body.

If a referenced token is absent from `dispatchpathparams`, the event is treated as a permanent failure: the sidecar publishes it to the route DLQ immediately, with no retries. The map values must be strings (the CloudEvent parser rejects non-string values at receive time).

Required forwarded headers:

```text
ce-id
ce-type
ce-source
ce-specversion
Idempotency-Key
```

Additional forwarded headers when present:

```text
ce-subject
ce-time
ce-datacontenttype
ce-dataschema
ce-correlationid
ce-causationid
traceparent
```

Publisher-supplied `dispatchheaders` are applied after CloudEvent metadata and before route-configured static headers. Reserved-set names are dropped at runtime even if they appear in `dispatchheaders`. If the same non-reserved header appears in both `dispatchheaders` and static route headers, the static route header wins.

## 8. Response Event Contract

The sidecar creates a new CloudEvent whenever the app returns any HTTP response (successful or error). Network failures do not produce a response event; they are retried and, after retry exhaustion, sent to DLQ (see section 9).

The response event must include:

- A new event `id`.
- Configured response `type`.
- Configured response `source`.
- CloudEvents `specversion`.
- Current timestamp.
- `datacontenttype` matching the HTTP response content type when present.
- `dataschema` when configured for the response route.
- Response payload in `data`.
- A `httpstatus` extension carrying the HTTP status code returned by the app. Consumers use this to distinguish success (`2xx`/`3xx`) from error (`4xx`/`5xx`).
- A `httplocation` extension carrying the value of the HTTP `Location` response header. Populated only when the app returns a `3xx` status; absent otherwise. The sidecar does not follow redirects — consumers receive the redirect intent and decide how to act on it.
- Correlation metadata copied from the incoming event where available.
- Causation metadata that points back to the incoming event ID.

The sidecar publishes the response event to the configured NATS response subject for both success and error outcomes.

The response event ID must be deterministic for a given incoming event and route, or the sidecar must persist the generated response event until publish confirmation. This prevents duplicate response events when the app succeeds but the response publish or original-message acknowledgement fails.

## 9. Failure Behavior

The sidecar must treat delivery failures consistently and conservatively.

Failures include:

- Invalid CloudEvent envelope.
- No matching route.
- HTTP timeout.
- Connection failure to the local app server.
- Response event construction failure.
- NATS response publish failure.
- NATS DLQ publish failure.

Note: an app-returned `4xx`/`5xx` HTTP status is not a failure for retry purposes. It is delivered as a response event (see section 8).

Retry behavior:

- Network-class dispatch failures (timeout, connection refused, TLS error, transport error) should retry with bounded exponential backoff.
- App-returned HTTP responses, including `4xx` and `5xx`, are not retried. They are published as response events.
- Retry count and backoff limits are route-configurable.
- The sidecar must not retry forever.
- After retry exhaustion of network-class failures, the sidecar publishes to the configured route DLQ subject.
- Pre-route failures, such as invalid CloudEvents or no matching route, must publish to the configured default DLQ subject because no route-specific DLQ is available.
- Response publish failures route the event to the DLQ. The sidecar must not acknowledge the original JetStream message until response publish succeeds or the event is durably published to DLQ.
- DLQ publish failures must leave the original JetStream message unacknowledged so JetStream can redeliver it according to the durable consumer policy.

DLQ events should include:

- The original CloudEvent.
- Failure reason.
- Last HTTP status code when available.
- Attempt count.
- Sidecar app ID.
- Timestamp.

If the sidecar cannot publish to the DLQ, it must log the failure, expose a critical metric, and leave the original JetStream message unacknowledged. The implementation must tune `ackWait`, `maxDeliver`, and backoff so redelivery is bounded and observable.

## 10. Delivery Semantics

The sidecar provides at-least-once delivery using NATS JetStream durable consumers. Queue subscriptions are not a Phase 1 durable-delivery mechanism.

Application teams must make HTTP handlers idempotent because the same event may be delivered more than once after retries, sidecar restarts, or NATS redelivery.

Application handlers must treat the incoming CloudEvent `id` as the idempotency key unless a route explicitly maps another CloudEvent extension as the idempotency key. Reprocessing the same event ID must not create duplicate business effects.

The sidecar supports idempotency by forwarding the incoming CloudEvent `id`, `Idempotency-Key`, trace context, correlation metadata, and causation metadata to the HTTP server.

To avoid duplicate response events, one of the following must be true for every route:

- The response event ID is deterministically derived from the incoming event ID and route name.
- The sidecar durably stores the generated response event before publishing and reuses it on retry after restart.

Phase 1 should prefer deterministic response event IDs unless a route requires non-deterministic response metadata.

## 11. Security Requirements

- The sidecar owns NATS credentials; application containers should not need direct NATS access for inbound delivery.
- NATS credentials must be scoped to only the subjects required by the sidecar.
- The sidecar should dispatch only to configured local HTTP targets.
- Configuration must not allow arbitrary external HTTP destinations in Phase 1.
- The sidecar must avoid logging raw secrets, credentials, or sensitive payloads.
- If tenant or app identity is present in event metadata, the sidecar should validate it against configuration before dispatch.

## 12. Observability Requirements

The sidecar must expose metrics for:

- Events consumed.
- Events dispatched successfully.
- Dispatch latency.
- HTTP response status counts.
- Retry attempts.
- DLQ publishes.
- Response events published.
- NATS publish failures.
- NATS acknowledgement failures.
- JetStream redeliveries.
- Duplicate event IDs observed per route.
- Route match failures.
- Invalid CloudEvents.

The sidecar must emit structured logs for:

- Startup and configuration load.
- Route match failures.
- Dispatch failures and retry exhaustion.
- DLQ publish failures.
- Response publish failures.

The sidecar should propagate trace context from incoming CloudEvents to HTTP requests and response events when present.

## 13. SLOs & Performance Requirements

Initial Phase 1 targets:

- Dispatch latency overhead: less than 20 ms P95 excluding application handler time.
- Successful response publish latency: less than 50 ms P95 after receiving the app response.
- Availability: 99.9% for sidecar process readiness under normal NATS and app-server availability.
- Reliability: no intentional event drop. Invalid or exhausted events must go to DLQ when possible, and original JetStream messages must remain unacknowledged when neither response publish nor DLQ publish has been durably confirmed.

These targets should be validated with load tests before production rollout.

## 14. Rollout Plan

1. Define the static route configuration schema.
2. Implement a local development example with one NATS subject and one HTTP endpoint.
3. Validate CloudEvent parsing, route matching, dispatch, deterministic response IDs, response publishing, acknowledgement timing, retry, redelivery, and DLQ behavior.
4. Add observability dashboards and alerts.
5. Onboard one pilot application.
6. Expand to additional applications after config, SLOs, and operational runbooks are proven.

## 15. Future Enhancements

- Runtime route discovery from an event marketplace or registry.
- App-to-sidecar outbound publishing API.
- Schema validation for CloudEvent `data` payloads using marketplace metadata.
- Per-route authorization integration with the permission-validation sidecar or platform policy service.
- Replay tooling for DLQ events.
- Response event templates for richer metadata mapping.

## 16. Open Questions

1. Resolved: route matching is by CloudEvent `type` only. Subject scoping is handled by the NATS consumer filter subject, so route matching no longer uses `subject` or `source`.
2. What is the default `ackWait`, `maxDeliver`, and `maxAckPending` policy for pilot applications?
3. Should Phase 1 allow non-deterministic response event IDs only when a durable sidecar response outbox is enabled?
4. What are the platform conventions for request subjects and queue-group names (tenant scoping) for the request-reply model?

## 17. Request-Reply Responder (Primary Synchronous Model)

The request-reply responder lets a backend service answer NATS request-reply calls over its existing loopback HTTP handlers, with no NATS code of its own. It is the **primary** model; the JetStream consumption path (sections 4–10) is opt-in for durable fan-out.

Design reference: [`../../docs/superpowers/specs/2026-07-23-event-adapter-direct-request-dispatch-design.md`](../../docs/superpowers/specs/2026-07-23-event-adapter-direct-request-dispatch-design.md).

### 17.1 When to use which model

| Use request-reply (primary) when… | Use JetStream event (opt-in) when… |
|---|---|
| A caller is synchronously waiting for an answer | The publisher fires and moves on |
| 1:1 call/response | 1:N fan-out to independent consumers |
| The caller can retry on failure | Work must survive a consumer crash (at-least-once) |
| e.g. upload presign, most HTTP→event calls | e.g. post-upload scan/thumbnail/finalize |

### 17.2 Flow

1. A NATS-native caller publishes a request CloudEvent to the configured subject, with a reply inbox set (standard NATS request-reply).
2. The sidecar's responder, subscribed within a queue group, receives the request on a bounded worker pool.
3. The responder validates the CloudEvent envelope and matches it to a request route by `type`.
4. An exact route takes precedence. If none matches and direct dispatch is enabled, the responder validates publisher-supplied `dispatchmethod` and fully resolved relative `dispatchpath`, then dispatches to the loopback app; otherwise it returns 404.
5. The app returns an HTTP response (success or business rejection).
6. The responder builds a reply CloudEvent (response body as `data`, HTTP status in `httpstatus`, `httplocation` carrying the `Location` header when the app returns a `3xx`, `causationid` = request id, `correlationid` passed through) and sends it on the caller's reply inbox.

There is no ack, retry, or DLQ on this path. A synchronous request that fails returns an error reply to a waiting caller; the caller owns whether to retry.

### 17.3 Configuration

Request-reply is configured with a top-level `requests:` block. The JetStream blocks (`nats:` + `routes:`) become optional; at least one of the two must be present. `nats.url` is always required because both models share the connection.

```yaml
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222

requests:
  subject: q.tenant-a.app.uploads.request   # core-NATS subject to answer; may be wildcard
  queueGroup: upload-responders             # one delivery per group; horizontal scale
  workerPoolSize: 16                         # bounded in-flight HTTP dispatches
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes:
      - /orders/
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request   # matched by CloudEvent type only
      dispatch:
        method: POST
        path: /requests/upload-presign        # loopback HTTP handler
        timeout: 3s
        forwardHeaders: [X-Workspace-Tenant-Id]
      reply:
        source: upload-service                 # reply CloudEvent source
        type: com.workspace.uploads.presign.reply
        # dataschema: optional
```

Request routes differ from event routes (enforced by validation): they carry a `reply` block (`source` + `type`, optional `dataschema`) instead of `response`, and have **no** `retry` or `dlq`. Request `match.type` values are unique within `requests.routes`, in a namespace separate from event routes.

Direct dispatch is opt-in and request-reply-only. It supports `GET`, `POST`,
`PUT`, `PATCH`, and `DELETE`. `dispatchpath` is relative and is joined only to
the validated loopback `app.httpBaseURL`; `allowedPathPrefixes`, when set,
uses path-segment boundaries. Invalid targets return 400 without a backend
call, while direct dispatch disabled with no exact route returns 404. Static
JetStream routes may use `DELETE`, but JetStream cannot use publisher-selected
targets.

### 17.4 Reply contract

- The reply is a CloudEvent: configured `reply.type` and `reply.source` for static routes; direct replies use type `io.eventadapter.direct.reply` and source `app.id`. Response body is in `data`, with current timestamp, `httpstatus`, `causationid` set to the request id, and `correlationid` copied from the request when present. The reply has **no** `subject` — it travels on the inbox.
- An app success and an app business rejection are **both replies**, distinguished by `httpstatus`. App `4xx`/`5xx` responses are forwarded as normal replies, not treated as failures.
- Reply IDs are deterministic for a given request id and route.

### 17.5 Error handling

Every outcome is a reply to a live caller; nothing is silently dropped, and there is no DLQ.

| Situation | Reply |
|---|---|
| App returns 4xx/5xx | Normal reply, `httpstatus` = the app status, app body forwarded |
| Malformed request CloudEvent | Error reply, `httpstatus` = 400 |
| No matching route | Error reply, `httpstatus` = 404 |
| Invalid direct-dispatch metadata or target | Error reply, `httpstatus` = 400; no backend call |
| App down / connection refused | Error reply, `httpstatus` = 502 (no retry) |
| App exceeds `dispatch.timeout` | Error reply, `httpstatus` = 504 |
| Request has no reply inbox (misuse) | Dropped; counted in a metric, no dispatch |

### 17.6 Concurrency, security, and observability

- In-flight dispatches are bounded by `requests.workerPoolSize`. On shutdown the responder drains the subscription, finishes in-flight work, then stops.
- Dispatch stays loopback-only and reuses the reserved-header rules from section 7. Request subjects should be tenant-scoped and queue groups per-service; caller identity travels in the CloudEvent (e.g. `dispatchheaders`) or via NATS account/JWT, since request-reply carries no HTTP headers from the original caller.
- The responder exposes metrics for requests received, reply latency, dispatch errors, missing-reply-inbox drops, and invalid request events, in addition to the shared dispatch metrics in section 12.

### 17.7 Out of scope

- Outbound publish or outbound requester APIs (the app calling the sidecar to emit events or originate requests). Both models remain inbound.
- An HTTP ingress gateway (HTTP→NATS conversion). Callers are NATS-native.
