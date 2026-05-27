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

- An **App Registry** service: HTTP CRUD over MongoDB. Owns app identity, status, and NATS NKey provisioning.
- An **Event Marketplace** service: HTTP CRUD over MongoDB. Owns versioned, immutable event-type schemas (JSON Schema, Draft 2020-12).
- A **Publisher/Subscriber SDK** that validates against cached schemas, builds CloudEvents envelopes, propagates W3C `traceparent`, and runs the consume-side idempotency check.
- A **nats-adapter sidecar** (§9) providing the same capabilities to apps that cannot link the SDK. The sidecar exposes a localhost HTTP publish API and delivers consumed events to the app via webhook (CloudEvents HTTP binary binding). It is an *additional integration mode*, not a fork of the design — internally it reuses the SDK's validation, envelope, and consumer logic.
- **NATS JetStream** as the durable transport: one stream `EVENTS` covering `events.>`, plus a `DLQ` stream covering `dlq.>`.
- **CloudEvents 1.0** as the envelope format, with W3C distributed-tracing extension (`traceparent`/`tracestate`) and a custom `causationid` extension carried explicitly by the publisher.
- A processed-events idempotency table colocated with each app's domain database, written transactionally with the domain change.
- Tests: integration tests for each service, SDK unit + integration tests with real NATS and MongoDB, an end-to-end Docker Compose harness, and explicit "bad citizen" and crash-recovery scenarios.

### Out of scope

- Multi-tenant isolation (no `tenant_id` plumbing, no NATS Accounts per tenant).
- Targeted command (A → B) semantics. All app-to-app communication in this design is **pub/sub broadcast**.
- Browser/WebSocket gateways and end-user-initiated flows.
- Large-payload references / object storage / presigned uploads.
- Per-event-type publisher allowlists. Any **registered, active** app may publish any **registered, active** event type. Schema is enforced; identity is recorded.
- Workspace Core / validator pool / sidecar topology from the broader Workspace PRD. The SDK is the single integration point in this iteration.
- Capacity planning. No scale target was given by the source PRD; see Open Questions.

## 3. Interaction model

> **Pub/sub broadcast.** Apps register event types into a shared marketplace. Any registered app may publish any registered event type; any registered app may subscribe. The producer does not know or care which apps consume.

This is deliberately simpler than the workspace PRD's command/event split. There are no targeted commands in this design — if `tasks` wants `crm` to do something, `tasks` publishes an event and `crm` subscribes. There is no "target app" field in the envelope.

Schemas are owned by whichever app registered them ("registered_by_app"), but ownership is metadata, not an authorization gate.

## 4. Components

Five units, each independently testable.

### 4.1 App Registry service

- HTTP CRUD over MongoDB collection `apps`.
- Record: `{ app_id, name, public_key, owner_team, status, nkey_user, created_at, updated_at }`.
- `status ∈ { active, suspended, retired }`. Only `active` apps may publish or subscribe.
- Onboarding flow provisions a NATS NKey/user with publish + subscribe scopes on `events.>`, and publish + subscribe on `dlq.<app_id>.>` (so the app's subscriber SDK can route rejections to its own DLQ subjects).
- Exposes a list/diff endpoint the SDK polls (or NATS KV watch) for cache invalidation.

### 4.2 Event Marketplace service

- HTTP CRUD over MongoDB collection `event_types`.
- Record: `{ _id: <subject>+<version>, subject, schema_version, schema, registered_by_app, status, created_at }`.
- Subject grammar: `events.<event_type>` where `<event_type>` is dotted lowercase (e.g. `events.task.created`).
- Schemas are **immutable per version**. Breaking change → new version (`"1.0"` → `"2.0"`). Old subscribers keep working until they upgrade.
- On register, validates: caller's app is `active` in Registry; JSON Schema is well-formed; subject matches grammar; `(subject, schema_version)` is unique.
- Same list/diff or NATS KV watch endpoint for SDK cache invalidation.

### 4.3 NATS JetStream

- One durable stream `EVENTS`, subjects `events.>`, file storage, replication R3, retention by time and/or max-bytes.
- One durable stream `DLQ`, subjects `dlq.>`, longer retention for inspection and replay.
- Per-app durable consumers under naming `<app_id>.<group>`. Each app's replicas attach to a single shared durable consumer so JetStream distributes deliveries across them (one in-flight delivery per message).
- NATS auth restricts publish/subscribe on `events.>` to active app NKeys.

### 4.4 Publisher/Subscriber SDK

One library (per language) used by every app. Boundary responsibilities only — does not own domain logic.

Boot:

- Fetches all `active` apps and all `active` event-type schemas from Registry and Marketplace.
- Maintains a local cache; refreshes on KV watch (preferred) or short HTTP poll.
- Fails closed on cache miss + service unreachable: publish/consume both reject rather than proceed unvalidated.

Publish path (see §5.3).
Subscribe path (see §5.4).

### 4.5 Processed-events table (per app)

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

### 5.1 Onboarding an app

```
Team ─HTTP─▶ App Registry  ──┐
                              ├─▶ MongoDB `apps`
                              └─▶ NATS user/NKey provisioned
                                  with publish/subscribe on `events.>`
```

Result: app has an `app_id`, an NKey, and `status: active`.

### 5.2 Registering an event type

```
Team ─HTTP─▶ Event Marketplace
              │
              ├─ verify caller's app_id is `active` (App Registry)
              ├─ validate JSON Schema well-formedness (Draft 2020-12)
              ├─ enforce subject grammar: events.<event_type>
              ├─ enforce (subject, schema_version) uniqueness
              └─▶ MongoDB `event_types`
```

### 5.3 Publishing an event

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

### 5.4 Consuming an event

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

### 5.5 End-to-end trace propagation

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

### Explicitly not handled (deferred)

- App Registry outage during onboarding — onboarding fails; existing publish/subscribe traffic continues from caches.
- MongoDB outage on Marketplace — SDK serves from cache; new schemas can't be registered until Mongo is back.
- NATS cluster outage — upstream NATS HA is the platform's responsibility.

## 8. Testing strategy

| Unit | Test type | Covers |
|---|---|---|
| App Registry | Integration (real MongoDB via testcontainers) | CRUD lifecycle, status transitions, NKey provisioning, duplicate `app_id` rejection. |
| Event Marketplace | Integration (real MongoDB) | Schema register/get/version, JSON Schema well-formedness, subject grammar, owner-app `active` precondition, immutability of versions. |
| SDK publisher | Unit + integration (real Marketplace + real NATS) | Envelope construction; schema validation; explicit causation handling; error classification; cache behavior; PubAck path; cache-miss-and-fetch; Marketplace-down fail-closed. |
| SDK subscriber | Unit + integration (real NATS + real Mongo) | Envelope sanity; source check against cached registry; schema validation; idempotency lookup; DLQ routing; NAK backoff; trace propagation across publish→consume. |
| End-to-end | Docker Compose harness | App A publishes → App B receives; `traceparent` + `causationid` preserved; second delivery suppressed; schema bump; old version still consumable. |

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

Sidecar runs the same flow as SDK publish (§5.3): cache lookup, schema validation, envelope construction, JetStream PubAck, fail-closed on cache miss + Marketplace unreachable.

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

The SDK design (§5.4) writes the `processed_events` row inside the app's DB transaction. The sidecar cannot be inside the app's DB transaction. The contract therefore shifts:

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

1. **Scale target.** The source PRD names none. Without a target we can't size NATS, MongoDB, or stream retention. Needs an answer before capacity planning.
2. **Cache invalidation transport.** NATS KV watch or HTTP poll? KV watch is cleaner if NATS is already the dependency; poll is simpler operationally. Default in this design is KV watch.
3. **NKey vs. JWT auth at NATS.** NKey is simpler; JWT (with operator/account/user) is more granular. NKey is the default here; revisit if per-event-type ACLs are added later.
4. **Schema deprecation policy.** Versions are immutable, but a `status: deprecated` flag could warn subscribers without breaking them. Not in scope for v1; flagged here.
5. **Outbox SDK helper.** App-side outboxes are the publisher's responsibility today. A shared outbox helper could be added later if every app reinvents the pattern.

## 11. References

- `prd/app-to-app/draft.md` — source PRD.
- `prd/event-driven/prd.md`, `prd/event-driven/architecture_suggestions.md` — broader Workspace Platform PRD; referenced for vocabulary, explicitly **not** assumed to be present in this design.
- CloudEvents 1.0 spec — envelope format.
- W3C Trace Context — `traceparent`/`tracestate`.
- NATS JetStream — durable streams, durable consumers, KV.
