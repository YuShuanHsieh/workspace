# App-to-App Event Publishing — Design

**Status:** draft, pending review
**Date:** 2026-05-27
**Authors:** brainstormed with Claude
**Related:** `prd/app-to-app/draft.md` (source PRD)

## 1. Motivation

Today applications inside the workspace cannot react to each other's actions. The result is duplicated work: every team rebuilds cross-app glue, schema drift accumulates, and audit trails are fragmented per app.

This spec defines a focused, greenfield design for **app-to-app event publishing**: a pub/sub broadcast system in which apps register public event types (with schemas) into a marketplace, publish via a thin SDK, and consume from any other registered app. The scope intentionally excludes multi-tenancy, browser/WebSocket gateways, and large-payload object storage — those concerns belong in a separate platform spec.

`prd/app-to-app/draft.md` lists the requirements at a high level. This document turns them into concrete components, contracts, and flows.

## 2. Scope

### In scope

- A **Publisher/Subscriber SDK** that validates against cached schemas, builds CloudEvents envelopes, propagates W3C `traceparent`, and runs the consume-side idempotency check.
- A **nats-adapter sidecar** (§9) providing the same capabilities to apps that cannot link the SDK. Exposes a localhost HTTP publish API and delivers consumed events to the app via webhook (CloudEvents HTTP binary binding). Reuses the SDK internally.
- **JetStream stream layout** required by this design: stream `EVENTS` covering `events.>`, stream `DLQ` covering `dlq.>`, plus the per-app shared durable-consumer naming convention. Stream provisioning is operated externally (see §4.1); this spec defines the required shape.
- **CloudEvents 1.0** envelope contract, with W3C distributed-tracing extension (`traceparent`/`tracestate`) and a custom `causationid` extension carried explicitly by the publisher.
- The **idempotency contract**: a processed-events table colocated with each app's domain database, written transactionally with the domain change. In sidecar mode, the contract states that the app's webhook handler owns this write.
- The **DLQ contract**: subject grammar, payload shape, ACK/NAK/DLQ rules — shared between SDK and sidecar.
- Tests: SDK unit + integration tests against real NATS and MongoDB, sidecar integration tests with a scripted fake HTTP app, an end-to-end Docker Compose harness, and explicit "bad citizen" and crash-recovery scenarios.

### Out of scope

- **App Registry** and **Event Marketplace** — both already exist as external platform services. This spec defines the **contract** the SDK and sidecar consume from them (§4.1) but not their internal design, storage, schemas, or onboarding/registration flows.
- Provisioning of NATS users/NKeys for apps — handled by the existing App Registry.
- Creation and lifecycle management of JetStream streams — operated externally. This spec specifies the required *shape* (subject grammar, stream names, consumer naming), not the provisioning mechanism.
- Multi-tenant isolation (no `tenant_id` plumbing, no NATS Accounts per tenant).
- Targeted command (A → B) semantics. All app-to-app communication in this design is **pub/sub broadcast**.
- Browser/WebSocket gateways and end-user-initiated flows.
- Large-payload references / object storage / presigned uploads.
- Per-event-type publisher allowlists. Any **active** app may publish any **active** event type. Schema is enforced; identity is recorded.
- Workspace Core / validator pool topology from the broader Workspace PRD.
- Capacity planning. No scale target was given by the source PRD; see Open Questions.

## 3. Interaction model

> **Pub/sub broadcast.** Apps register event types into a shared marketplace. Any registered app may publish any registered event type; any registered app may subscribe. The producer does not know or care which apps consume.

This is deliberately simpler than the workspace PRD's command/event split. There are no targeted commands in this design — if `tasks` wants `crm` to do something, `tasks` publishes an event and `crm` subscribes. There is no "target app" field in the envelope.

Schemas are owned by whichever app registered them (per Marketplace records), but ownership is metadata, not an authorization gate.

## 4. Components

Split into **external dependencies** (what we consume) and **what we build**.

### 4.1 External dependencies (out of scope; contracts we consume)

These services and infrastructure exist already. This spec records only the *contract* the SDK and sidecar assume — sufficient detail to reason about behavior, integration, and failure modes. Their internal design is owned elsewhere.

**App Registry.** Authoritative source for app identity and status.

| Assumption | Required for |
|---|---|
| Per app: `{ app_id, status ∈ {active, suspended, retired}, nats_identity }`. Only `active` apps may publish or subscribe. | Source check (§5.2 step 2); publish-side identity. |
| A list-or-diff read API that the SDK can fetch on boot and watch for changes (NATS KV watch or HTTP polling — concrete mechanism is the Registry's choice; SDK abstracts over it). | SDK cache (§4.2). |
| Out-of-band onboarding provisions a NATS user/NKey whose grants cover publish + subscribe on `events.>` and publish + subscribe on `dlq.<app_id>.>`. | Sidecar/SDK can write rejections to their own DLQ subjects. |

**Event Marketplace.** Authoritative source for event-type schemas.

| Assumption | Required for |
|---|---|
| Per event type version: `{ subject, schema_version, schema (JSON Schema Draft 2020-12), status ∈ {active, deprecated, retired} }`. Versions are immutable. | Publish validation; consume validation; `dataschema` URI resolution (§6). |
| Subject grammar `events.<event_type>` (dotted lowercase). | Subject construction in publish path (§5.1). |
| A list-or-diff read API the SDK can fetch on boot and watch for changes — same shape as the Registry's. | SDK cache (§4.2). |

**NATS JetStream cluster.** Operated as platform infrastructure. Required shape:

- Stream `EVENTS`, subjects `events.>`, durable, replicated. Retention parameters are an ops decision.
- Stream `DLQ`, subjects `dlq.>`, longer retention than `EVENTS`.
- Per-app shared durable consumers, naming `<app_id>.<group>`. Each app's replicas attach to a single shared durable consumer so JetStream distributes deliveries (one in-flight delivery per message).
- NATS auth restricts publish on `events.>` to active app NKeys.

### 4.2 Publisher/Subscriber SDK (what we build)

One library (per supported language) used by every app that can link it. Boundary responsibilities only — does not own domain logic.

Boot:

- Fetches all `active` apps and all `active` event-type schemas from Registry and Marketplace.
- Maintains a local cache; refreshes on the dependency's change-feed mechanism (KV watch preferred; HTTP poll fallback).
- Fails closed on cache miss + dependency unreachable: publish/consume both reject rather than proceed unvalidated.

Publish path: §5.1. Subscribe path: §5.2.

### 4.3 nats-adapter sidecar (what we build)

A separately-deployable process for apps that cannot link the SDK. Reuses the SDK internally. Detailed in §9.

### 4.4 Processed-events table (per app)

Lives in each app's domain MongoDB. Requires a MongoDB deployment that supports multi-document transactions (replica set or sharded cluster) — a standalone `mongod` is not sufficient. Schema:

```
processed_events
  consumer_name : string
  event_id      : string
  processed_at  : datetime
  result_status : "ok" | "duplicate_suppressed" | "permanent_error"
  unique index (consumer_name, event_id)
```

Written inside the same transaction as the domain change. This is the only place the at-least-once → effectively-once contract is enforced.

## 5. Data flow

Onboarding an app (App Registry) and registering an event type (Event Marketplace) happen **out-of-band** in the external services and are out of scope for this spec (§4.1). The flows below assume:

- The publishing app is `active` in the App Registry and holds a valid NATS NKey.
- The event type is `active` in the Event Marketplace at the version the publisher uses.

### 5.1 Publishing an event

```
App A code
  │
  ▼
SDK.publish(eventType, data, causationEvent=<envelope or null>)
  │
  ├─ 1. look up schema in local cache (cache-miss → fetch from Marketplace; fail closed on miss)
  ├─ 2. validate `data` against JSON Schema → reject locally on fail
  ├─ 3. build CloudEvents envelope (§6)
  ├─ 4. publish to NATS subject `events.<eventType>`; await JetStream PubAck
  └─ 5. return PubAck (stream-seq) to caller
```

Notes:

- `causationid` is **explicit**: the caller passes the parent event envelope (or null). The SDK reads `.id` off it. The SDK does not infer causation from an ambient context.
- `traceparent` and `tracestate` are extracted automatically from the active OTel span — this is normal OTel propagation, not business causation.
- The SDK awaits PubAck before returning. Apps wrap `publish()` in their own outbox if at-least-once-from-domain-DB is required.

### 5.2 Consuming an event

```
JetStream ─pull─▶ SDK consumer loop (durable consumer "<app_id>.<group>")
                    │
                    ├─ 1. envelope sanity: required CloudEvents fields present
                    ├─ 2. source check: `source` resolves to an `active` app in Registry cache
                    ├─ 3. schema check: validate `data` against the schema named in `dataschema`
                    │                   (cache-miss → fetch; reject if version unknown)
                    ├─ 4. idempotency: lookup processed_events for (consumer_name, event_id)
                    │      hit → ACK + `subscribe.duplicate_suppressed` metric; done
                    ├─ 5. extract W3C trace context → continue trace span as child
                    ├─ 6. call app handler(envelope, data) **inside a DB transaction**
                    │      handler does domain work
                    │      handler writes processed_events row
                    │      both commit atomically
                    ├─ 7. on success → ACK
                    ├─ 8. on transient error → NAK with bounded exponential backoff (max-deliver capped, default 5)
                    └─ 9. on permanent error (schema mismatch, source unregistered, handler `PermanentError`)
                          → publish to `dlq.<app_id>.<group>` + ACK original
```

### 5.3 End-to-end trace propagation

A's HTTP request creates an OTel span → `publish()` injects current `traceparent` into the envelope → JetStream → B's consumer extracts `traceparent` → continues the span as a child → B's handler creates its own spans.

The OTel trace covers the *technical* hop chain. The `causationid` chain covers the *business* event chain. Both are needed: trace sampling drops spans, and the business chain must survive sampling.

## 6. Envelope (CloudEvents 1.0)

Every event uses the CloudEvents 1.0 JSON format. Required and used fields:

| Field | Source | Notes |
|---|---|---|
| `specversion` | SDK | `"1.0"` |
| `id` | SDK | ULID; globally unique per event |
| `source` | SDK | `"/apps/<app_id>"`; identifies producer |
| `type` | Caller via SDK | The event type, e.g. `"task.created"` |
| `time` | SDK | RFC3339, UTC |
| `datacontenttype` | SDK | `"application/json"` |
| `dataschema` | SDK | `"marketplace://events.<type>@<version>"` — points to active schema |
| `traceparent` | SDK (OTel) | W3C distributed-tracing extension |
| `tracestate` | SDK (OTel) | W3C distributed-tracing extension |
| `causationid` | Caller via SDK | Custom extension; id of the event that triggered this one, or absent |
| `data` | Caller | The JSON Schema-validated payload |

Subject = `events.<type>`. Version is **not** in the subject — it lives in `dataschema`. This lets the marketplace evolve schemas without resubjecting.

## 7. Error handling

| When | Where | Response |
|---|---|---|
| Publish-side schema mismatch | Publisher SDK, before NATS publish | Throw to caller. Emit `sdk.publish.rejected{reason=schema}` metric. |
| Publish-side cache miss + Marketplace unreachable | Publisher SDK | Fail closed: throw. (Don't publish unvalidated.) Circuit breaker + last-known-good cache covers brief outages. |
| JetStream PubAck timeout | Publisher SDK | Surface to caller. SDK does not retry blindly; the app's outbox owns the retry. |
| Subscribe-side envelope/source/schema failure | Subscriber SDK | Publish rejection record to `dlq.<app_id>.<group>` + ACK original. Permanent failures must not loop. |
| Subscribe-side handler transient error | Subscriber SDK | NAK with bounded exponential backoff. Last redelivery → DLQ. |
| Subscribe-side handler permanent error | App handler raises `PermanentError` | Treat as envelope failure → DLQ + ACK. |
| Duplicate delivery | Subscriber SDK | ACK + `subscribe.duplicate_suppressed` metric. No handler call. |

### DLQ record

```
subject: dlq.<app_id>.<group>
payload: {
  original_envelope: { ... full CloudEvents envelope ... },
  reason: "schema_mismatch" | "source_unregistered" | "permanent_handler_error" | "max_redeliveries",
  detail: "...",
  consumer_name: "...",
  failed_at: "..."
}
```

Ops tooling against the `DLQ` stream covers inspect / replay / discard.

### Explicitly not handled (external concerns)

- App Registry outage — SDK serves from last-known-good cache; publish/consume continues for already-known apps. Caller-driven onboarding (an external flow) fails. SDK behavior on prolonged outage is "fail closed for unknown identities, fail open for cached ones."
- Event Marketplace outage — SDK serves from cache; publish/consume continues for already-known event types; an unknown event type causes a publish-side fail-closed. Schema registration (an external flow) is unavailable.
- NATS cluster outage — upstream NATS HA is the platform's responsibility.

## 8. Testing strategy

| Unit | Test type | Covers |
|---|---|---|
| SDK publisher | Unit + integration (fake Registry/Marketplace + real NATS) | Envelope construction; schema validation; explicit causation handling; error classification; cache behavior; PubAck path; cache-miss-and-fetch; dependency-unreachable fail-closed. |
| SDK subscriber | Unit + integration (fake Registry/Marketplace + real NATS + real Mongo) | Envelope sanity; source check against cached registry; schema validation; idempotency lookup; DLQ routing; NAK backoff; trace propagation across publish→consume. |
| nats-adapter sidecar | Integration (fake Registry/Marketplace + real NATS + scripted fake HTTP app) | Localhost publish API; CloudEvents HTTP-binary delivery; status-code → ACK/NAK/DLQ mapping; webhook timeout; trace header in/out. |
| End-to-end | Docker Compose harness (real NATS + Mongo, fake Registry/Marketplace, two demo apps — one SDK-linked, one sidecar) | App A publishes → App B receives; `traceparent` + `causationid` preserved; second delivery suppressed; schema bump; old version still consumable; sidecar app interoperates with SDK app. |

Registry and Marketplace are stubbed in tests with a fake that returns canned `apps`/`event_types` snapshots and supports change notifications. Their real implementations are out of scope (§4.1) and have their own test suites.

Two scenarios called out explicitly:

1. **Bad-citizen fixture.** A synthetic app publishes garbage envelopes / unknown source / unknown schema. Subscriber SDK must reject all of it without crashing or losing position. This is the test that catches what real apps will eventually do.
2. **Crash-between-commit-and-ACK.** Kill the subscriber between the handler's DB commit and the JetStream ACK; on restart, redelivery must hit the processed_events check and be suppressed. This proves the at-least-once + idempotency contract.

### Deferred

- Load and scale testing. The source PRD names no target. Open question.
- Chaos testing of NATS/Mongo HA. Platform-level concern.

## 9. HTTP adapter sidecar mode

The SDK assumes the app can link a library in the SDK's supported language. Many existing services are HTTP-only or written in languages the SDK doesn't target. For those, the platform ships **`nats-adapter`**, a sidecar deployed alongside the app.

The sidecar reuses the SDK internally. From the rest of the system's perspective (Registry, Marketplace, NATS), a sidecar-integrated app is indistinguishable from an SDK-integrated one.

### 9.1 Topology

```
HTTP App ── localhost POST /v1/publish ──▶ nats-adapter sidecar ──▶ NATS (events.>)
                                              │   (same SDK logic
                                              │    internally:
                                              │    schema cache,
                                              │    envelope, PubAck)
                                              ▼
                                          App Registry + Marketplace

NATS ──▶ nats-adapter sidecar ── HTTP POST <configured webhook> ──▶ HTTP App
         (JetStream durable consumer,         (CloudEvents HTTP binary binding)
          envelope+schema validated)
```

Sidecar listens on `127.0.0.1` only. App talks to it over localhost. The sidecar holds the app's NATS NKey; the app never sees NATS credentials.

### 9.2 Publish API (app → sidecar)

`POST /v1/publish` (localhost only). Request body:

```json
{
  "event_type": "task.created",
  "data": { ... },
  "causation_event_id": "<id or null>"
}
```

Headers: optional `traceparent` / `tracestate` (sidecar uses them if present; otherwise starts a fresh trace).

Sidecar runs the same flow as SDK publish (§5.1): cache lookup, schema validation, envelope construction, JetStream PubAck, fail-closed on cache miss + Marketplace unreachable.

Response: `200 { "event_id": "...", "stream_seq": 123 }` on success; `4xx { "reason": "..." }` on schema mismatch or unknown event type; `503` on Marketplace unreachable + cache miss.

### 9.3 Consume delivery (sidecar → app)

CloudEvents HTTP **binary binding**: envelope fields → `ce-*` headers, payload → request body.

```
POST <configured webhook>
Content-Type: application/json
ce-specversion: 1.0
ce-id: <ulid>
ce-source: /apps/<source_app_id>
ce-type: task.created
ce-time: 2026-05-27T10:00:00Z
ce-dataschema: marketplace://events.task.created@1.0
ce-causationid: <parent id or absent>
traceparent: <W3C>
tracestate:  <W3C>

{ ...data... }
```

Subscriptions are declared in a static config file the sidecar reads at startup:

```yaml
subscriptions:
  - event_type: task.created
    webhook: http://127.0.0.1:8080/webhooks/task-created
    group: tasks-worker
    timeout: 5s
  - event_type: file.uploaded
    webhook: http://127.0.0.1:8080/webhooks/file-uploaded
    group: tasks-worker
    timeout: 5s
```

`group` maps to the JetStream durable-consumer name (`<app_id>.<group>`). Same-group webhooks share delivery; different-group webhooks each get every event independently. Runtime registration is **not** supported in v1 — declarative config keeps the sidecar's behavior auditable and reproducible.

### 9.4 ACK / NAK / DLQ via HTTP status

| Response | Sidecar action |
|---|---|
| 2xx | ACK |
| 408, 429, 5xx | NAK + bounded exponential backoff (transient) |
| Timeout (no response within `timeout`) | NAK + backoff |
| Other 4xx | DLQ + ACK (permanent — the request will never succeed) |
| Connection refused / unreachable | NAK + backoff (treated as transient) |

DLQ subject and record format match §7 — `dlq.<app_id>.<group>`, original envelope plus reason.

### 9.5 Idempotency contract

The SDK design (§5.2) writes the `processed_events` row inside the app's DB transaction. The sidecar cannot be inside the app's DB transaction. The contract therefore shifts:

- The sidecar passes `event_id` in `ce-id`.
- The app's webhook handler is responsible for the idempotency check: lookup → domain write → `processed_events` insert, all in one **local** transaction.
- The app returns 2xx only after the transaction commits durably. Returning 2xx before commit causes silent loss on crash.
- The sidecar **may** keep an in-memory short-window dedup cache as a best-effort optimization to avoid pointless HTTP calls on rapid redelivery, but it is not authoritative. The app's table is the source of truth.

This is the same correctness contract as SDK mode; only the boundary moves from "inside the SDK call" to "inside the webhook handler."

### 9.6 Trace propagation across HTTP

- Publish path: sidecar reads `traceparent` from incoming HTTP request and copies into the CloudEvents envelope. If absent, sidecar starts a new trace.
- Consume path: sidecar extracts envelope `traceparent` and injects it into the outbound webhook request as a header. The app's HTTP server continues the span.

### 9.7 Operational notes

- Sidecar binary is the same image regardless of which app it serves. Per-app config: NKey/credentials, declarative subscription file, log/metric tags.
- The sidecar must not be reachable from outside the pod/host. Bind explicitly to `127.0.0.1`.
- A misbehaving app's webhook (e.g. 500 forever) drains JetStream into the DLQ at the configured retry cap — no head-of-line blocking on the stream, but the app's own consumer falls behind. Standard consumer-lag alerting applies.

### 9.8 Testing additions for sidecar mode

- Integration: real NATS + real Mongo + a fake HTTP app whose webhook responses can be scripted (2xx, 4xx, 5xx, slow, hung, crash-between-handler-and-2xx).
- The "bad-citizen fixture" and "crash-between-commit-and-ACK" scenarios from §8 must also run against sidecar mode — same correctness contract, different boundary.

## 10. Open Questions

1. **Scale target.** The source PRD names none. Without a target we cannot size NATS streams or set retention/backoff defaults. Needs an answer before capacity planning.
2. **Concrete change-feed mechanism exposed by Registry and Marketplace.** The SDK assumes a list-or-diff read with change notifications, abstracted over the transport. The concrete API (NATS KV watch vs. HTTP long-poll vs. server-sent events) is the external services' decision; the SDK needs only an adapter interface. Confirm the interface before SDK implementation.
3. **Outbox SDK helper.** App-side outboxes are the publisher's responsibility today. A shared outbox helper could be added later if every app reinvents the pattern.

## 11. References

- `prd/app-to-app/draft.md` — source PRD.
- `prd/event-driven/prd.md`, `prd/event-driven/architecture_suggestions.md` — broader Workspace Platform PRD; referenced for vocabulary, explicitly **not** assumed to be present in this design.
- CloudEvents 1.0 spec — envelope format.
- W3C Trace Context — `traceparent`/`tracestate`.
- NATS JetStream — durable streams, durable consumers, KV.
