# Workspace Architecture Design

> Audience: Engineering team manager + tech leads. This document is the completed PRD for the Workspace Platform, building on the original draft in [`core.md`](core.md). It is intentionally compact; deeper technical detail lives in [`architecture_suggestions.md`](architecture_suggestions.md) and [`claude_reviewed.md`](claude_reviewed.md), referenced inline.
>
> **Revision note (2026-05-03):** the cross-app eventing design has changed since the two companion docs were written. The Validator Pool has been replaced by a sidecar + Auth-Service-issued per-subject NATS JWT model. Where the companion docs describe a centrally-validated `ingress.*` → `t.*` two-tier scheme, the authoritative design is now the one in §5 below.

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

### Auth Service load

Token mints and refreshes are the only Auth Service traffic on the data path's critical bootstrap. Order-of-magnitude:

- 20 apps × ~10 active target subjects per app × 6 refreshes/hour (10-min TTL) ≈ **1,200 refreshes / hour ≈ 0.3 RPS**.
- Cold-start mints (new app deploy, new subject permission granted) are infrequent and bursty, but bounded.
- Compared to 14k EPS event traffic, Auth Service load is four orders of magnitude smaller. It can run as a small HA service, not a sharded data-path tier.

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
6. **Cross-app authorization without a data-path bottleneck.** Every cross-app event must be authorized, but routing every event through a central validator service creates a SPOF and a latency hop. Authorization must be enforced inline by the transport itself, while the issuer of authorization decisions stays off the hot path.
7. **Token issuance availability and revocation latency.** Once authorization moves into NATS subject ACLs via signed JWTs, the JWT issuer becomes a tier-0 dependency. Its outage halts new token mints, and token TTL bounds worst-case revocation latency.
8. **Large payload handling.** Files, attachments, exports, and large JSON should not flow through event payloads. They need an out-of-band reference pattern with checksum, size limits, scanning, and lifecycle.
9. **Audit retention beyond JetStream.** JetStream is a hot transport, not a long-term audit store. Compliance audit needs a separate cold tier with multi-year retention.
10. **Operational visibility.** 14k EPS peak with 20 app teams shipping independently demands shared dashboards, dead-letter queues, and replay tooling — otherwise an outage in one app cascades silently.

---

## 5. Proposed Architecture

The platform is a **governed realtime event mesh** built on NATS JetStream. The headline rule:

> NATS carries intent, metadata, references, workflow state, and delivery semantics.
> App services and object storage hold domain state and large bytes.

Cross-app authorization is enforced at the **NATS protocol layer** via per-subject JWTs minted by a Workspace Auth Service. The Auth Service sits **off the data path** — sidecars consult it only at token-mint and refresh time. The Validator Pool from earlier drafts is gone; schema validation happens in the publisher's sidecar before publishing.

### High-level layout

```text
                +---------------------------------------+
                |      Workspace Core (control plane)   |
                |  - Auth Service (per-subject JWTs)    |
                |  - Schema Registry                    |
                |  - Developer Portal (contracts, app   |
                |    registration, allow-list mgmt)     |
                |  - Audit Archiver                     |
                +-----+-------------+-------+-----------+
                      |             |       ^
                      | mint token  | fetch | consume
                      | (rare)      | schema| audit.*
                      v             v       |
   Browser  --WSS-->  +-----------+         +-------+
   (WS Gateway)       | Sidecar A |   <-->  | NATS  |  <-->  Sidecar B
                      | (publish) |  PUB    | JS    |  SUB   (consume)
                      +-----+-----+         | acct  |              ^
                            |               | per   |              |
                            | localhost     | tenant|              | localhost
                            v   RPC         +-------+              v   RPC
                      +-----------+                          +-----------+
                      | App A svc |                          | App B svc |
                      +-----------+                          +-----------+
```

### Component responsibilities (full detail in [`architecture_suggestions.md`](architecture_suggestions.md) §3–§4, with the validator-pool sections superseded by this document)

- **Browser / Client.** Connects via NATS WebSocket using short-lived, JWT-scoped credentials. Subscribes only to its own user/tenant subjects; cannot publish to internal `app.*` subjects.
- **WebSocket Gateway.** Terminates user sessions, verifies JWT, and bridges to NATS subjects with tenant scoping enforced at the protocol level.
- **Workspace Core (control plane).** No longer in the data path. Hosts:
  - **Auth Service** — signs per-(app, target subject) NATS JWTs for cross-app publishing.
  - **Schema Registry** — sidecars fetch schemas from here and cache them locally.
  - **Developer Portal** — the cross-app contract registry: each app publishes its event/command catalog (subjects + schemas), other apps browse and request publish permission, admins approve, and approved entries land in Auth Service's allow-list.
  - **Audit Archiver** — drains `audit.<tid>.>` to S3+Parquet (cold) and ClickHouse (queryable).
- **Permission Sidecar.** The required boundary for every app. Owns the app's NATS connection. Outbound: token cache, schema fetch, payload validation, envelope stamping, publish. Inbound: dedupe by `event_id`, envelope-shape sanity check, forward to app service over private localhost. App services never hold NATS credentials directly. **The platform owner builds and ships the sidecar as a signed, versioned artifact; app developers are required to deploy it as a sidecar to their service. Custom or third-party sidecar implementations are not permitted** — the entire trust model rests on the sidecar being platform-controlled.
- **NATS JetStream.** The durable event backbone. **One NATS Account per tenant** for hard isolation, plus `SYS` and `WORKSPACE` accounts. Cross-tenant flow only via explicit `import`/`export` declarations, which the Auth Service must be tenant-aware about when issuing tokens.
- **App Service.** Domain logic plus a transactional outbox table; emits app events through the sidecar over localhost RPC.
- **Object Storage.** Files and large payloads (>10 KB JSON or any binary) live here. JetStream events carry a reference (storage key, sha256, size, content type), not the bytes.

### Cross-app eventing flow

The end-to-end flow when app A publishes an event to a subject app B subscribes to:

```text
1. Onboarding (one-time per cross-app permission)
   - In Developer Portal, A's team browses B's published catalog (subjects + schemas).
   - A's team requests permission to publish to b.subject.task_assigned.
   - Admin (or B's owner) approves. The (A → b.subject.task_assigned) entry lands
     in Auth Service's allow-list.

2. Runtime — first publish to a given subject (token cache miss)
   - A's app service calls A's sidecar localhost RPC: publish(target=b.subject.task_assigned, payload=...)
   - Sidecar checks token cache. Miss.
   - Sidecar -> Auth Service: getToken(app=A, subject=b.subject.task_assigned)
   - Auth Service verifies (A is real, A is allowed) and signs a NATS JWT with
     pub permission = b.subject.task_assigned, TTL ~10 min.
   - Sidecar caches the token, fetches schema from Schema Registry (also cached),
     validates payload, stamps envelope (event_id, idempotency_key, tenant_id,
     source_app=A, correlation_id, timestamp), publishes via NATS using the token.
   - NATS server enforces the publish ACL at protocol level — A literally cannot
     publish to a subject not in its token claims.
   - Sidecar emits audit.<tid>.publish.allowed.

3. Runtime — subsequent publishes (token cache hit)
   - Same as step 2 but skip the Auth Service round-trip. Hot-path latency: schema
     validate + stamp + publish, all in-process in A's sidecar.

4. Consume (B-side)
   - B's sidecar is subscribed to b.subject.task_assigned (using B's own SUB-only token).
   - On message: envelope-shape check (event_id / schema_version / tenant_id present
     and well-formed) → dedupe by event_id (processed_events table) → forward to
     B's app service over localhost RPC.
   - Sidecar emits audit.<tid>.consume.delivered.

5. Background — token refresh
   - Sidecar refreshes each cached token ~1 min before expiry.
   - Auth Service load = O(apps × subjects × 1/TTL), independent of event volume.
   - At target scale: ~0.3 RPS sustained on Auth Service.

6. Validation failure (publish-side)
   - Sidecar refuses to publish → emits audit.<tid>.publish.schema_rejected →
     returns error to A's app service over localhost RPC.
   - No event lands on the bus. A is responsible for surfacing or fixing.

7. Permission revocation
   - Admin removes (A → subject) from Auth Service allow-list.
   - Existing cached token works until expiry (≤10 min). Default revocation latency
     is 10 minutes. For urgent revocation, rotate the NATS account signing key
     (blunt — invalidates all of that account's tokens).
```

### Subject naming (single-tier; full grammar in [`architecture_suggestions.md`](architecture_suggestions.md) §8 — but ignore the `ingress.*` two-tier scheme there)

```text
user.<tid>.<uid>.inbox.>          # user-scoped inbox subjects (sub-only for clients)
user.<tid>.<uid>.notifications.>  # user-scoped notifications
t.<tid>.app.<aid>.event.<type>    # app event subjects (PUB requires Auth-Service-signed token)
t.<tid>.app.<aid>.cmd.<action>    # app command subjects (PUB requires explicit allow-list entry)
audit.<tid>.>                     # per-tenant audit (sidecar PUB only)
dlq.<tid>.>                       # per-tenant dead-letter
```

### Event envelope (summary; full schema in [`architecture_suggestions.md`](architecture_suggestions.md) §9)

Every event/command carries: `event_id`, `idempotency_key`, `event_type`, `schema_version`, `tenant_id`, `actor_id`, `source_app`, `target_app`, `correlation_id`, `causation_id`, `timestamp`, `payload` (or `payload_ref` for large bytes). Commands and events are kept separate — commands request work, events describe completed facts.

### Trust model and platform-owned sidecar

The new design moves payload validation from a central service into the publisher's sidecar. Two consequences:

- **The trust boundary is the sidecar.** If A's sidecar publishes garbage, B will receive garbage (bypassing the mostly-cosmetic envelope-shape check). The platform mitigates this by:
  1. Building, signing, and distributing the sidecar binary itself. Developers must deploy the platform-provided artifact, not write their own.
  2. Network policy: A's app service can reach NATS only through the sidecar (localhost RPC); direct NATS connections from app code are blocked.
  3. Sidecar version pinning, signature verification, and a kill-switch in Workspace Core to revoke an app's NATS Account credentials if an outdated or modified sidecar is detected.
- **Auth Service is a tier-0 dependency.** It is off the data path for steady-state event traffic, but new token mints and refreshes go through it. Operating posture:
  - Run HA with at least 3 replicas and an active-active deployment.
  - Sidecars cache tokens with a refresh window — an Auth Service outage of <10 min is invisible to existing flows.
  - Sidecars must degrade gracefully on refresh failure: keep using the still-valid cached token, log a warning, retry with backoff. Stop publishing when the token actually expires, not at the first refresh failure.
  - SLO target: Auth Service availability ≥ 99.95%.

> **For the rest of the architecture (large-payload handling, idempotent consumer pattern, DLQ/replay, audit pipeline, production readiness checklist),** see [`architecture_suggestions.md`](architecture_suggestions.md) §6, §10, §11, §12, §13. Where those sections describe a Validator Pool, treat the role as moved into the publisher-side sidecar per this document.

---

## 6. Problems We Solved

How each challenge in §4 maps to the proposed architecture in §5:

| Challenge | Mechanism |
|---|---|
| 1. Multi-tenant isolation | NATS Accounts per tenant + `t.<tid>.*` subject prefix + per-tenant quotas + per-tenant audit prefix in S3 |
| 2. Realtime + durability | At-least-once JetStream delivery + idempotent consumer pattern (`processed_events` table + atomic outbox) |
| 3. Cross-app correctness | Publisher-side schema validation in sidecar + Schema Registry + commands-vs-events separation + schema versioning |
| 4. Layered authorization | NATS subject ACL (transport) → Auth Service allow-list (platform) → Permission Sidecar (per-app) → App domain rules (defense-in-depth) |
| 5. Browser safety | WebSocket Gateway with JWT-scoped, short-lived credentials; clients cannot publish to internal `t.<tid>.app.*` subjects; subject prefix must match JWT `tenant_id` |
| 6. Cross-app authz without bottleneck | Auth-Service-signed per-(app, target subject) NATS JWTs; NATS server enforces publish ACL at protocol level. Auth Service is consulted only at mint/refresh time, not per event. |
| 7. Token availability + revocation | Auth Service runs HA tier-0; sidecars cache tokens (≤10 min TTL) and degrade gracefully on refresh failure; revocation latency ≤ TTL by default, or immediate via signing-key rotation |
| 8. Large payloads | Presigned URL upload → object storage → JetStream metadata-only event with sha256 / size / content-type reference |
| 9. Audit retention | JetStream `audit.<tid>.>` → Audit Archiver → S3+Parquet (cold) and ClickHouse (queryable) for multi-year retention |
| 10. Operational visibility | OTel pipeline (traces / metrics / logs) + consumer-lag and DLQ dashboards + per-tenant replay tooling |

---

## 7. Enhancements & Production Hardening

These are the upgrades over the original "Idea" sketch in [`core.md`](core.md) that move the platform from prototype-grade to production-grade. Each one closes a specific gap that would otherwise block launch at the stated scale:

- **WebSocket / SSE Gateway** in front of NATS, instead of letting browsers terminate WS on JetStream directly. ([`claude_reviewed.md`](claude_reviewed.md) §1)
- **Auth-Service-issued per-subject NATS JWTs** with subject-scoped publish permissions, enforced by the NATS server at the protocol layer. The Auth Service stays off the data path; only mint and refresh consult it. *(Replaces the Validator Pool design in the companion docs.)*
- **Platform-owned, mandatory Permission Sidecar** — built, signed, and distributed by the platform owner; required as a deployment for every app; the only component that holds NATS credentials. Custom sidecar implementations are not permitted.
- **Token cache + background refresh in the sidecar** so Auth Service load is O(apps × subjects × 1/TTL), independent of event volume.
- **Developer Portal as cross-app contract registry** — every app publishes its event/command catalog there; consuming apps discover and request publish permission; admins approve; approved entries flow to Auth Service's allow-list.
- **Publisher-side schema validation** in A's sidecar, with cheap envelope-shape sanity check on B's side.
- **NATS Accounts per tenant** with explicit `import` / `export` declarations as the only path for cross-tenant flow; Auth Service is tenant-aware when issuing tokens. ([`claude_reviewed.md`](claude_reviewed.md) §1)
- **Schema Registry + commands-vs-events separation** with schema version checks before rollout. ([`architecture_suggestions.md`](architecture_suggestions.md) §9)
- **Idempotent consumer pattern** with `processed_events` table and transactional outbox in each app's domain DB. ([`architecture_suggestions.md`](architecture_suggestions.md) §10)
- **DLQ + bounded retry + replay tooling** by tenant, subject, event type, and time range. ([`architecture_suggestions.md`](architecture_suggestions.md) §11)
- **Audit Archiver** streaming JetStream `audit.*` to S3+Parquet for multi-year compliance retention. ([`architecture_suggestions.md`](architecture_suggestions.md) §12)
- **Object-storage references for large payloads** with presigned URL upload, sha256 verification, malware scanning, and lifecycle cleanup. ([`architecture_suggestions.md`](architecture_suggestions.md) §6)
- **OpenFGA-style fine-grained authorization** with tenant as the topmost relation and per-app permissions nested under it. ([`claude_reviewed.md`](claude_reviewed.md) §1.1)
- **Production readiness checklist** covering security, governance, reliability, and operations gates before launch. ([`architecture_suggestions.md`](architecture_suggestions.md) §13)

---

## 8. Open Decisions for Engineering Management

Five go/no-go decisions are blocking. Decisions 2–4 come from [`architecture_suggestions.md`](architecture_suggestions.md) §14; 1 and 5 were raised by this PRD pass.

1. **Approve the Auth-Service-issued per-subject token model.** Cross-app authorization is enforced at the NATS protocol layer via JWTs minted per (app, target subject) by a workspace Auth Service. Replaces the central Validator Pool design. Implies: Auth Service is tier-0; revocation latency is bounded by token TTL (~10 min default); the sidecar owns schema validation on the publisher side.
2. **Approve the Permission Sidecar as platform-owned, mandatory infrastructure.** App services do not connect to NATS directly. The sidecar is built and shipped by the platform owner, deployed alongside every app, and is the only component with NATS credentials. Custom sidecar implementations are not permitted.
3. **Approve the client-to-NATS hybrid model.** NATS WebSocket is allowed for realtime UX with strict scoped credentials; sensitive cross-app mutations go through the sidecar; large payloads use object storage references.
4. **Approve the delivery guarantee.** Durable at-least-once delivery + idempotent consumers + DLQ/replay tooling + separate long-term audit storage. The platform does **not** promise exactly-once business effects.
5. **Reconcile the event-volume estimate.** This PRD targets 240M events/day (carried from [`core.md`](core.md)); the two companion docs were drafted against 10M/day. Confirm one figure before final capacity, cost, and SLO planning.

---

## 9. References

- [`core.md`](core.md) — original draft PRD with high-level requirements, "Idea" sketch, and initial estimations.
- [`architecture_suggestions.md`](architecture_suggestions.md) — production-oriented architecture proposal with full component responsibilities, subject naming grammar, event envelope schema, large-payload patterns, idempotency design, audit pipeline, and production readiness checklist. **The Validator Pool design described there is superseded by the sidecar + Auth Service model in §5 of this document.**
- [`claude_reviewed.md`](claude_reviewed.md) — multi-tenant production review with end-to-end architecture diagram, NATS account model, OpenFGA authorization model, and capacity math. **The validator-pool subject-tier scheme described there is superseded by the design in §5 of this document.**
