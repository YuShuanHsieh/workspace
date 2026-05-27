# Product Requirements Document: Client-to-Server Event Dispatch Sidecar

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

### Non-Goals

- Providing an outbound publishing API where the application calls the sidecar directly. Phase 1 is inbound event consumption only.
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
3. **Route Matcher:** Maps the NATS subject and CloudEvent attributes, such as `type` and `source`, to a configured HTTP route.
4. **HTTP Dispatcher:** Sends the event payload to the configured local HTTP server path with the configured method, timeout, and headers.
5. **Response Event Builder:** Wraps the HTTP response body as the `data` field of a new CloudEvent with configured response metadata.
6. **NATS Publisher:** Publishes the response CloudEvent to the configured NATS subject and confirms durable publication before acknowledging the original event.
7. **Retry and DLQ Handler:** Retries transient failures with bounded backoff, publishes the original event plus failure metadata to a dead-letter subject after retry exhaustion, and acknowledges the original event only after the DLQ publish is confirmed.

## 5. Event Flow

1. The sidecar starts and loads its static route configuration.
2. The sidecar connects to NATS JetStream and binds to a configured durable consumer.
3. JetStream delivers a CloudEvent to the sidecar and keeps it unacknowledged while processing is in progress.
4. The sidecar validates the CloudEvent envelope.
5. The sidecar matches the event against route configuration using subject, CloudEvent `type`, and CloudEvent `source`.
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
- Match rules using NATS subject and CloudEvent attributes.
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
- The sidecar forwards publisher-supplied `dispatchheaders` only when the header name is explicitly listed in the matched route's `dispatch.forwardHeaders` allowlist.
- The sidecar must treat `dispatchheaders` as sidecar control metadata and must not forward it to the backend as a `ce-dispatchheaders` header.
- Publisher-supplied headers must not override CloudEvent, trace context, authorization, idempotency, hop-by-hop, or route-configured static headers.
- The sidecar must support JSON `data` payloads in Phase 1. Binary payloads and `data_base64` are out of scope unless explicitly enabled by route configuration.
- The application should return a `2xx` status code for successful processing.
- `4xx` and `5xx` responses are not retried. The sidecar publishes them to the route response subject as response events carrying the HTTP status code, so the publisher can observe the error outcome. `3xx` responses are treated the same as `2xx`.
- The application response body becomes the `data` field of the response CloudEvent for both success and error outcomes.

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

Publisher-supplied backend headers are forwarded after CloudEvent metadata and before route-configured static headers. If the same non-reserved header appears in both `dispatchheaders` and static route headers, the static route header wins.

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

1. Should route matching require exact subject matches only, or support NATS wildcards?
2. What is the default `ackWait`, `maxDeliver`, and `maxAckPending` policy for pilot applications?
3. Should Phase 1 allow non-deterministic response event IDs only when a durable sidecar response outbox is enabled?
