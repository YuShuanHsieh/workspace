# Workspace Architecture Design

> Audience: Engineering team manager + tech leads. This document is the canonical PRD for the Workspace Platform. It builds on the original draft in [`core.md`](core.md) and is intentionally compact; deeper technical detail lives in [`architecture_suggestions.md`](architecture_suggestions.md) and [`claude_reviewed.md`](claude_reviewed.md), referenced inline.

---

## 1. Problem Statement

### End-user pain
Knowledge workers in the target enterprise juggle 15–20 internal applications every day — CRM, tasks, chat, files, approvals, dashboards. Today these apps are silos:

- Data lives in one app, but useful actions span several (e.g. a chat message → create a task → attach a file → notify a reviewer).
- Notifications fragment across each app's own UI, so users miss things.
- Permissions and audit trails are per-app and inconsistent.
- There is no unified place to see "what is happening to me right now across everything."

### Developer pain
Every internal app team re-implements the same plumbing:

- Authentication, session, and tenant scoping.
- Eventing, retries, and at-least-once delivery.
- Cross-app integration glue (often direct HTTP between backends, with schema drift).
- Notification surfaces and inbox semantics.
- Audit logging and compliance retention.

The result is duplicated effort, inconsistent UX, and integration bugs that surface only in production.

### Platform thesis
We will build a **multi-tenant Workspace Platform**: a workspace shell + event mesh that hosts apps, exposes a single inbox/notification surface to end users, and provides a shared SDK + permission sidecar so app developers focus only on domain logic. Cross-app communication, authorization, audit, and realtime delivery are platform concerns, not app concerns.

---

## 2. High-Level Requirements

### End-user
1. View and update cross-application data from a single platform.
2. Interact with all apps through a unified flow — for example, when a chat message arrives or a task is assigned, it lands in one inbox/notification center, and the user can act on it inline.
3. Only see and invoke data and actions allowed by their permissions.

### Developer
1. The workspace is a platform service that lets app developers onboard their app onto our platform.
2. The workspace provides a unified interface and SDK so developers can quickly build their app with platform-provided tooling.
3. The workspace provides a unified, convenient interface for an app to interact with other apps.
4. The workspace can monitor and audit all events and actions happening inside it.
5. When an app does something, it can notify other apps and let them act on the notification.

---

## 3. Event & Traffic Estimation

### Inputs (target scale)

| Metric | Value |
|---|---:|
| Users | 200,000 |
| Applications onboarded | 20 |
| End-user requests / day | 6,000,000 |
| Peak end-user RPS | 500 |
| App events / day | 240,000,000 |
| Max event delivery latency | < 500 ms (P99) |
| Event loss budget | 0 (zero business-event loss) |

### Derived load

- **End-user RPS:** 6M / 86,400 ≈ **70 RPS sustained**, **500 RPS peak**. Peak/avg ≈ 7×, consistent with business-hours skew.
- **Event throughput:** 240M / 86,400 ≈ **2,800 EPS sustained**. With a 5× peak factor for synchronized fan-out (notifications, sync, presence), expect **~14,000 EPS peak**.
- **Per-user event share:** 240M / 200k users / 20 apps ≈ **60 events / user / app / day**, dominated by realtime sync, presence, and app-to-app fan-out rather than direct user actions.

### Storage sizing (rough)

Assume a ~1 KB envelope and ~5 KB average payload (envelope + small JSON; large payloads flow out-of-band, see §5).

- **Hot tier (NATS JetStream, 7-day retention):** 240M × 5 KB × 7 ≈ **8.4 TB** working set; with R3 replication ≈ 25 TB raw.
- **Cold audit tier (S3 + Parquet, 1-year retention):** 240M × 1 KB × 365 ≈ **88 TB / yr**. Compresses well; Parquet+zstd typically 4–6×.

### Reliability target
At-least-once delivery with idempotent consumers. The platform does not promise exactly-once business effects — see §5 and [`architecture_suggestions.md`](architecture_suggestions.md) §10–§11.

> **Note for management:** [`architecture_suggestions.md`](architecture_suggestions.md) and [`claude_reviewed.md`](claude_reviewed.md) were drafted against an earlier 10M events/day estimate. This PRD uses 240M, the figure carried over from [`core.md`](core.md). The discrepancy must be reconciled before final capacity planning — see Open Decision #4 in §8.

---

## 4. Challenges

The system has to satisfy realtime UX, durability, multi-tenant isolation, and 14k EPS peak simultaneously. The following are the hard problems a naïve "browser → NATS → app" design does not solve:

1. **Multi-tenant isolation.** Multiple customer organizations share one platform. One tenant must not be able to read another tenant's events, exhaust shared quotas, or DoS shared subjects.
2. **Realtime + durability tradeoff.** Sub-500 ms P99 latency *and* zero event loss rule out synchronous RPC. They force durable pub/sub with at-least-once delivery and idempotent consumers.
3. **Cross-app data correctness.** When app A's event drives a write in app B, schema drift, version skew, and partial failures must not leave the workspace inconsistent.
4. **Authorization across boundaries.** Transport-level ACLs, platform-level policy, and app-level domain rules each cover different concerns; relying on any single one is insufficient.
5. **Browser-as-NATS-client safety.** Letting browsers terminate WebSocket directly on JetStream is unsafe — clients cannot be trusted to enforce their own scoping, fan-out limits, or input validation.
6. **Centralized validator as SPOF.** Schema validation must be server-side and authoritative (because partner apps can't be trusted to self-validate), but a single validator service becomes a bottleneck and outage risk.
7. **Large payload handling.** Files, attachments, exports, and large JSON should not flow through event payloads. They need an out-of-band reference pattern with checksum, size limits, scanning, and lifecycle.
8. **Audit retention beyond JetStream.** JetStream is a hot transport, not a long-term audit store. Compliance audit needs a separate cold tier with multi-year retention.
9. **Operational visibility.** 14k EPS peak with 20 app teams shipping independently demands shared dashboards, dead-letter queues, and replay tooling — otherwise an outage in one app cascades silently.

---

## 5. Proposed Architecture

The platform is a **governed realtime event mesh** built on NATS JetStream. The headline rule:

> NATS carries intent, metadata, references, workflow state, and delivery semantics.
> App services and object storage hold domain state and large bytes.

### High-level layout

```text
                    Identity / Policy (IdP + OpenFGA)
                              |
         Browser <--TLS WS--+ | +-- HTTPS upload (presigned URL)
                            | | |
                  +---------+ | +---------+
                  |           |           |
                  v           v           v
              WebSocket Gateway    Workspace Core / Validator Pool
              (JWT-scoped creds)   (schema, routing, governance)
                  |                       |
                  |   ingress.t.<tid>.*   |   t.<tid>.app.*  (validated)
                  +-----------+-----------+
                              v
                    +-------------------+
                    | NATS JetStream    |
                    | one Account per   |
                    | tenant + SYS +    |
                    | WORKSPACE accts   |
                    +---------+---------+
                              |
                       +------+------+
                       |             |
                       v             v
                 Permission     Object Storage
                 Sidecar        (S3 / GCS / MinIO)
                 (per app)
                       |
                       v  localhost HTTP/gRPC
                 App Service (domain DB + outbox)
```

### Component responsibilities (one-line each — full detail in [`architecture_suggestions.md`](architecture_suggestions.md) §3–§4)

- **Browser / Client.** Connects via NATS WebSocket using short-lived, JWT-scoped credentials. Subscribes only to its own user/tenant subjects; cannot publish to internal `app.*` subjects.
- **WebSocket Gateway.** Terminates user sessions, verifies JWT, and bridges to NATS subjects with tenant scoping enforced at the protocol level.
- **Workspace Core.** Owns cross-app governance — schema validation, routing, audit emission. Implemented as a **stateless Validator Pool** on a two-tier `ingress.*` → `t.*` subject scheme: partner apps publish only to `ingress.*`, the validator drains it as a queue group, validates schema + permission, and republishes to validated `t.*` subjects. NATS auth is the enforcement mechanism — bypass is impossible at the protocol layer.
- **NATS JetStream.** The durable event backbone. **One NATS Account per tenant** for hard isolation, plus `SYS` and `WORKSPACE` accounts. Cross-tenant flow is only via explicit `import`/`export` declarations.
- **Permission Sidecar.** The required boundary for every app. Owns the app's NATS credentials, enforces subject ACL + schema + permission + idempotency, then forwards authorized events to the app service over private localhost. App services never hold NATS credentials directly.
- **App Service.** Domain logic plus a transactional outbox table; emits app events through the sidecar.
- **Object Storage.** Files and large payloads (>10 KB JSON or any binary) live here. JetStream events carry a reference (storage key, sha256, size, content type), not the bytes.

### Subject naming (summary; full grammar in [`architecture_suggestions.md`](architecture_suggestions.md) §8)

```text
user.<tid>.<uid>.inbox.>            # user-scoped inbox subjects
user.<tid>.<uid>.notifications.>    # user-scoped notifications
ingress.t.<tid>.app.<aid>.<action>  # partner apps publish here only
t.<tid>.app.<aid>.event.<type>      # validated subjects (validator-only PUB)
workspace.<tid>.cmd.<aid>.<action>  # cross-app commands via Workspace Core
audit.<tid>.>                       # per-tenant audit
dlq.<tid>.>                         # per-tenant dead-letter
```

### Event envelope (summary; full schema in [`architecture_suggestions.md`](architecture_suggestions.md) §9)

Every event/command carries: `event_id`, `idempotency_key`, `event_type`, `schema_version`, `tenant_id`, `actor_id`, `source_app`, `target_app`, `correlation_id`, `causation_id`, `timestamp`, `payload` (or `payload_ref` for large bytes). Commands and events are kept separate — commands request work, events describe completed facts.

> **For full architecture diagrams and per-component responsibilities,** see [`architecture_suggestions.md`](architecture_suggestions.md) §3–§4 and [`claude_reviewed.md`](claude_reviewed.md) §1.1.

---

## 6. Problems We Solved

How each challenge in §4 maps to the proposed architecture in §5:

| Challenge | Mechanism |
|---|---|
| 1. Multi-tenant isolation | NATS Accounts per tenant + `t.<tid>.*` subject prefix + per-tenant quotas + per-tenant audit prefix in S3 |
| 2. Realtime + durability | At-least-once JetStream delivery + idempotent consumer pattern (`processed_events` table + atomic outbox) |
| 3. Cross-app correctness | Workspace Core Validator Pool + Schema Registry + commands-vs-events separation + schema versioning |
| 4. Layered authorization | Transport ACL (NATS) → Workspace Core policy → Permission Sidecar → App domain rules (defense-in-depth) |
| 5. Browser safety | WebSocket Gateway with JWT-scoped, short-lived credentials; clients cannot publish to internal `app.*` subjects; subject prefix must match JWT `tenant_id` |
| 6. Validator SPOF | **Stateless** Validator Pool, NATS queue-group, horizontally scaled; NATS auth enforces validator-only publish on validated subjects (no honor system) |
| 7. Large payloads | Presigned URL upload → object storage → JetStream metadata-only event with sha256/size/content-type reference |
| 8. Audit retention | JetStream `audit.*` → Audit Archiver → S3 + Parquet (cold) and ClickHouse (queryable) for multi-year retention |
| 9. Operational visibility | OTel pipeline (traces/metrics/logs) + consumer-lag and DLQ dashboards + per-tenant replay tooling |

---

## 7. Enhancements & Production Hardening

These are the upgrades over the original "Idea" sketch in [`core.md`](core.md) that move the platform from prototype-grade to production-grade. Each one closes a specific gap that would otherwise block launch at the stated scale:

- **WebSocket / SSE Gateway** in front of NATS, instead of letting browsers terminate WS on JetStream directly. ([`claude_reviewed.md`](claude_reviewed.md) §1)
- **Stateless Validator Pool** on a two-tier `ingress.*` → `t.*` subject scheme, replacing a single central validator service. ([`claude_reviewed.md`](claude_reviewed.md) §1.1)
- **NATS Accounts per tenant** with explicit `import` / `export` declarations as the only path for cross-tenant flow. ([`claude_reviewed.md`](claude_reviewed.md) §1)
- **Schema Registry + commands-vs-events separation** with schema version checks before rollout. ([`architecture_suggestions.md`](architecture_suggestions.md) §9)
- **Idempotent consumer pattern** with `processed_events` table and transactional outbox in each app's domain DB. ([`architecture_suggestions.md`](architecture_suggestions.md) §10)
- **DLQ + bounded retry + replay tooling** by tenant, subject, event type, and time range. ([`architecture_suggestions.md`](architecture_suggestions.md) §11)
- **Audit Archiver** streaming JetStream `audit.*` to S3 + Parquet for multi-year compliance retention. ([`architecture_suggestions.md`](architecture_suggestions.md) §12)
- **Object-storage references for large payloads** with presigned URL upload, sha256 verification, malware scanning, and lifecycle cleanup. ([`architecture_suggestions.md`](architecture_suggestions.md) §6)
- **OpenFGA-style fine-grained authorization** with tenant as the topmost relation and per-app permissions nested under it. ([`claude_reviewed.md`](claude_reviewed.md) §1.1)
- **Production readiness checklist** covering security, governance, reliability, and operations gates before launch. ([`architecture_suggestions.md`](architecture_suggestions.md) §13)

---

## 8. Open Decisions for Engineering Management

Four go/no-go decisions are blocking. The first three come from [`architecture_suggestions.md`](architecture_suggestions.md) §14; the fourth was raised by this PRD pass.

1. **Approve the client-to-NATS hybrid model.** NATS WebSocket is allowed for realtime UX with strict scoped credentials; sensitive cross-app mutations must pass through Workspace Core; large payloads must use object storage references.
2. **Approve the Permission Sidecar as mandatory infrastructure.** App services do not connect to NATS directly. The platform-provided sidecar owns NATS credentials, permission checks, schema validation, idempotency, and audit emission.
3. **Approve the delivery guarantee.** Durable at-least-once delivery + idempotent consumers + DLQ/replay tooling + separate long-term audit storage. The platform does **not** promise exactly-once business effects.
4. **Reconcile the event-volume estimate.** This PRD targets 240M events/day (carried from [`core.md`](core.md)); the two companion docs were drafted against 10M/day. Confirm one figure before final capacity, cost, and SLO planning.

---

## 9. References

- [`core.md`](core.md) — original draft PRD with high-level requirements, "Idea" sketch, and initial estimations.
- [`architecture_suggestions.md`](architecture_suggestions.md) — production-oriented architecture proposal with full component responsibilities, subject naming grammar, event envelope schema, large-payload patterns, idempotency design, audit pipeline, and production readiness checklist.
- [`claude_reviewed.md`](claude_reviewed.md) — multi-tenant production review with end-to-end architecture diagram, validator-pool design, NATS account model, OpenFGA authorization model, and capacity math.
