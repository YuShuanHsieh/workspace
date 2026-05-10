# Workspace Platform PRD

## 1. Problem Statement

Modern workspaces often contain many independent applications: chat, task
management, CRM, approvals, documents, reporting, and internal tools. Each app
usually has its own UI, permission model, backend API, notification flow, audit
logs, and integration contract. This creates three problems:

1. **Fragmented user experience**
   - Users must switch between apps to understand what needs attention.
   - Notifications are scattered across products.
   - Cross-app actions are hard to complete from one place.

2. **High app integration cost**
   - Every app developer needs to build authentication, authorization, event
     publishing, event consumption, audit, monitoring, and cross-app workflow
     logic repeatedly.
   - Integrations become inconsistent across teams and languages.

3. **Weak centralized governance**
   - The platform needs to know which user did what, in which app, against which
     resource, and whether the action was allowed.
   - App-to-app events need durable delivery, schema validation, permission
     enforcement, replay, and auditability.

The product goal is to build a **workspace platform** that lets users interact
with multiple apps through a unified experience, while giving developers a
standard integration model for realtime communication, permission enforcement,
and cross-app workflows.

## 2. Target Users And Requirements

### 2.1 End-user requirements

1. Users can view and update cross-application data from a single workspace.
2. Users can receive notifications from different apps in one inbox or
   notification center.
3. Users can take app actions directly from notifications when allowed.
4. Users can only view data and perform actions permitted by their role, tenant,
   app, and resource permissions.
5. Users receive realtime updates with low perceived latency.

### 2.2 Developer requirements

1. Developers can onboard apps into the workspace platform.
2. Developers get a unified SDK, sidecar, subject naming model, and event
   contract.
3. Apps can publish events and consume commands from other apps through a
   platform-governed interface.
4. Apps do not need to expose domain backends directly to the public internet.
5. Developers can monitor app events, delivery failures, retries, and audit
   records.

### 2.3 Platform requirements

1. The platform can monitor and audit all important user actions and app events.
2. The platform can enforce global workspace policy before granting app-to-app
   publish permission.
3. The platform can support app-local low-latency interaction.
4. The platform can support durable event delivery, retry, replay, and DLQ.
5. The platform can support large payloads and file uploads without forcing all
   bytes through the event broker.

## 3. Event Estimation

### 3.1 Input estimates

| Metric | Estimate |
|---|---:|
| Users | 200,000 |
| End-user requests | 6,000,000 / day |
| Peak request rate | 500 RPS |
| Applications | 20 |
| App events | 240,000,000 / day |
| Event durability | No business event loss |
| Event latency target | < 500 ms |

### 3.2 Derived event math

| Metric | Calculation | Result |
|---|---:|---:|
| Average end-user request rate | 6,000,000 / 86,400 sec | ~70 RPS |
| Peak / average request multiplier | 500 / 70 | ~7.1x |
| Average event rate | 240,000,000 / 86,400 sec | ~2,778 events/sec |
| Suggested burst planning factor | 5x average event rate | ~14,000 events/sec |
| Suggested stress target | 10x average event rate | ~28,000 events/sec |

### 3.3 Event storage estimate

Actual storage depends on envelope size, payload size, retention, replication, and
compression. For planning:

| Assumption | Estimate |
|---|---:|
| Average event envelope + payload | 1 KB |
| Raw event data / day | ~240 GB |
| 7-day raw retention | ~1.7 TB |
| 30-day raw retention | ~7.2 TB |
| 3x replicated storage | ~21.6 TB for 30 days |

Decision needed: define JetStream retention by event class. Not all events need the
same retention period. Audit events should be archived to long-term storage outside
JetStream.

## 4. Challenges

### 4.1 Client-to-server security

NATS WebSocket can provide low-latency communication, but browser clients are not
trusted infrastructure. A client must not receive broad publish or subscribe access
to internal app subjects.

Key challenge:

```text
How do we use NATS as the primary realtime path without giving clients unsafe
access to the full event bus?
```

### 4.2 Permission enforcement

Users can only access data and actions they are allowed to use. Permission checks
must happen before app services receive sensitive commands.

Key challenge:

```text
How do we enforce transport permission, platform permission, and app-domain
permission without making every app team rebuild the same logic?
```

### 4.3 Cross-app correctness

App-to-app actions require permission checks, audit, retry, schema validation,
and version compatibility. For effective cross-application communication, the
producer app should be able to publish directly to the consumer app's subscribed
subject after the platform grants permission.

Key challenge:

```text
How do we let App A publish directly to App B's subject while still proving that
App A was authorized to publish there?
```

### 4.4 Event delivery semantics

The PRD says events cannot be lost. In production, the safer promise is durable
at-least-once delivery plus idempotent processing.

Key challenge:

```text
How do we avoid duplicate side effects when events are retried or redelivered?
```

### 4.5 Large payloads and file uploads

NATS JetStream should not be the transport for every large payload or file. Files,
large JSON documents, videos, PDFs, exports, and imports need another path.

Key challenge:

```text
How do we keep NATS as the primary coordination path while handling payloads that
are too large or unsuitable for broker messages?
```

### 4.6 Audit and observability

JetStream can stream audit events, but long-term audit needs queryable durable
storage, retention policies, and operational dashboards.

Key challenge:

```text
How do we keep every action traceable without using JetStream as the only audit
database?
```

## 5. Proposed Architecture

### 5.1 Recommendation

Use a **NATS-first hybrid event architecture**:

```text
Browser / Client
  -> NATS WebSocket for realtime commands and subscriptions
  -> scoped subjects and short-lived credentials
  -> NATS JetStream as the durable event backbone
  -> Workspace Auth Service for app-to-app publish grants
  -> platform-provided permission sidecar
  -> app service over private localhost HTTP/gRPC
```

NATS JetStream is the primary communication backbone. It carries commands, events,
workflow state changes, notification signals, audit signals, and metadata
references. It should not carry every large payload directly.

For cross-application communication, the recommended model is **direct
subject-based publish with platform-issued grants**:

```text
1. App A requests a NATS user token from Workspace Auth Service.
2. Workspace Auth validates App A identity and app-to-app policy.
3. Workspace Auth signs a short-lived NATS user token scoped to B's subject.
4. App A connects to NATS and publishes directly to the subject B subscribes to.
5. NATS enforces the token's publish permission at the subject level.
6. App B consumes the event and validates data/object schema as usual.
```

### 5.2 High-level architecture

```text
                         +----------------------+
                         | Workspace Auth       |
                         | app grants + creds   |
                         | RBAC / ABAC / FGA    |
                         +----------+-----------+
                                    |
                                    v
+-------------+       +-------------+-------------+
| Browser     |       | Workspace API / Upload    |
| Web/Mobile  |       | Gateway for edge cases     |
+------+------+       +-------------+-------------+
       |                            |
       | NATS WebSocket             | HTTP upload / presigned URL
       v                            v
+------+-----------------------------+------------------+
| NATS JetStream                                        |
| durable streams, subjects, consumers, replay, DLQ     |
+------+-----------------------------+------------------+
       |                             |
       | app command/event subjects  | metadata events
       v                             v
+------+------------------+   +------+------------------+
| App Permission Sidecar |   | Object / Blob Storage    |
| schema, authz, audit,  |   | S3 / GCS / MinIO / NATS  |
| idempotency, NATS I/O  |   | Object Store             |
+------+------------------+   +-------------------------+
       |
       | private localhost HTTP/gRPC
       v
+------+------------------+
| App Service            |
| domain logic + app DB   |
+-------------------------+
```

### 5.3 Core components

#### Browser / client

- Connects to NATS over TLS WebSocket.
- Uses short-lived scoped credentials.
- Publishes only to allowed client or workspace command subjects.
- Subscribes only to allowed user, tenant, or app-local subjects.
- Uses the upload gateway for large payloads and files.

#### NATS JetStream

- Durable event and command delivery.
- Pub/sub fanout.
- Queue groups for scalable consumers.
- Replay and recovery.
- DLQ for poison messages.
- Realtime client and app communication.
- Subject-level enforcement of app-to-app publish grants.

#### Workspace Auth Service

- Owns app identity and app-to-app authorization.
- Validates that App A is allowed to publish to App B's subject.
- Issues short-lived NATS user tokens with scoped publish/subscribe permissions.
- Embeds app identity, tenant, allowed subjects, expiry, and grant ID in the token.
- Emits audit records for token issuance, denial, and policy decisions.

#### App-to-app policy registry

- Stores which apps can publish to which consumer subjects.
- Supports tenant-specific app-to-app permissions.
- Supports per-action or per-event-type allowlists.
- Provides the policy source used by Workspace Auth Service.

#### App permission sidecar

- Required for app integration.
- Owns the app's default NATS connection and consumer credentials.
- Requests and caches short-lived per-subject publish tokens for approved
  app-to-app communication.
- Validates schema, subject, tenant, actor, resource, and action permission.
- Handles idempotency and duplicate suppression.
- Emits allow/deny audit records.
- Forwards authorized events to app service over localhost HTTP/gRPC.
- For consuming apps, performs consumer-side schema validation before app delivery
  if the app delegates validation to the sidecar.

#### App service

- Owns domain logic and domain database.
- Performs app-local validation.
- Applies business changes.
- Uses app-level idempotency constraints.
- Publishes domain events through sidecar or transactional outbox.
- For consuming apps, validates data/object schema before applying business
  effects when validation is not delegated to the sidecar.

#### Object / blob storage

- Stores files and large payloads.
- Supports presigned upload URLs.
- Provides checksum, metadata, retention, and lifecycle controls.
- Emits object references through JetStream events.

## 6. Client-to-Server Communication Policy

| Case | Recommended path |
|---|---|
| Small command/event payload | Direct JetStream payload |
| Realtime notification | NATS WebSocket subscription |
| App-local low-risk action | NATS WebSocket to scoped subject |
| Cross-app app-to-app event | App A sidecar/SDK gets per-subject NATS token -> publishes to B subject |
| Cross-app high-risk workflow | Scoped NATS token + stronger approval/policy gate |
| Large JSON/document payload | Object storage + JetStream metadata event |
| File upload | Presigned upload URL + JetStream completion event |
| Video/image/PDF/import/export | Upload Gateway + object storage + scan pipeline |
| Internal artifact | Consider NATS Object Store + reference event |

Recommended platform payload policy:

```text
<= 10 KB: JetStream payload is preferred.
10 KB to 1 MB: prefer payload reference unless direct payload is clearly safe.
> 1 MB or any file: use object storage or NATS Object Store reference.
```

## 7. Permission And Access Control

Use layered authorization:

```text
Transport permission
  -> Can this client/app publish or subscribe to this subject?

Platform permission
  -> Can this actor perform this workspace action for this tenant/resource?

Domain permission
  -> Does the target app allow the action under its business rules?
```

Recommended enforcement points:

1. NATS subject ACLs restrict publish/subscribe by user, tenant, app, and subject.
2. Workspace Auth Service grants short-lived NATS user tokens only after app-to-app
   policy validation.
3. NATS enforces that App A can publish only to the B subjects included in the
   token.
4. Consumer sidecar or App B validates schema and object shape before business
   processing.
5. App service enforces domain-specific invariants.

The sidecar remains the recommended managed integration layer, but direct
producer publishing is allowed when Workspace Auth issues a scoped token.

### 7.1 App-to-app publish grant flow

```text
1. App A calls its sidecar/SDK to publish event type X to App B subject Y.
2. App A sidecar/SDK authenticates to Workspace Auth Service using app credentials.
3. App A sidecar/SDK requests a per-subject publish grant for App B subject Y.
4. Workspace Auth checks:
   - App A identity and tenant.
   - App B subject ownership.
   - App-to-app policy registry.
   - Optional user/delegation context if the event is user-initiated.
5. Workspace Auth signs a short-lived NATS user token with 5-15 minute TTL.
6. Token allows publish only to the approved App B subject.
7. App A sidecar/SDK caches the grant until shortly before expiry.
8. App A sidecar/SDK publishes the event to B's subscribed subject.
9. NATS rejects any publish outside the token's allowed subject list.
10. App B consumes and validates schema as usual.
```

Recommended token claims:

```json
{
  "tenant_id": "tenant_123",
  "producer_app_id": "app_a",
  "allowed_publish_subject": "app.tenant_123.app_b.event.order_created",
  "allowed_event_types": [
    "order.created"
  ],
  "grant_id": "grant_abc",
  "expires_at": "2026-05-03T10:15:00Z",
  "ttl_minutes": 10
}
```

Token rules:

- Grant tokens must be requested only through the producer app sidecar/SDK.
- Tokens must be short-lived; 5-15 minutes is acceptable.
- Tokens must be scoped per subject.
- Tokens must be tenant-scoped.
- Tokens must be revocable by grant ID or app policy version.
- Token issuance and denial must be audited.
- Apps should not receive wildcard publish permission such as `app.<tenant>.>`.

### 7.2 Consumer-side validation

App B owns schema validation entirely. The producer sidecar/SDK is responsible
for obtaining publish permission, but B is responsible for deciding whether the
event data/object is valid and safe to process.

Consumer validation should check:

- Event envelope schema.
- Payload schema version.
- Required object fields.
- Resource identifiers and tenant consistency.
- Producer app allowlist for the event type.
- Idempotency key.

Important distinction:

```text
Workspace Auth + NATS subject ACL proves App A is allowed to publish to B's subject.
Consumer schema validation proves the event data is valid and safe to process.
```

The platform should not rely only on B's schema validation for permission. NATS
subject-level permission should reject unauthorized publishes before B receives
the message.

B does not need to verify the grant token or grant ID inside the event payload.
That responsibility belongs to the producer sidecar/SDK, Workspace Auth, and NATS
authorization.

## 8. Idempotent Consumer Design

JetStream should be treated as at-least-once delivery for business effects.
Duplicate delivery must be safe.

Recommended flow:

```text
1. Receive event from JetStream.
2. NATS has already enforced transport permission through the scoped app token.
3. Consumer sidecar or App B validates schema and permission.
4. Check durable processed_events store by event_id or idempotency_key.
5. If already processed, ACK the message and emit duplicate-suppressed audit.
6. If new, call app service with event envelope and idempotency_key.
7. App service starts a database transaction.
8. App service applies business change.
9. App service writes processed_events record in the same transaction.
10. App service commits.
11. Consumer sidecar or App B ACKs JetStream message only after durable success.
```

Recommended unique constraints:

```text
unique(consumer_name, event_id)
unique(tenant_id, idempotency_key)
```

ACK policy:

| Case | Action |
|---|---|
| Durable processing succeeds | ACK |
| Duplicate event | ACK and suppress duplicate side effect |
| Temporary downstream failure | Do not ACK; allow retry |
| Permanent validation failure | ACK and publish rejected event |
| Poison message after retry limit | Move to DLQ and ACK original |

## 9. Large Payload And File Upload Design

Large payloads should use reference events:

```json
{
  "event_id": "evt_123",
  "event_type": "file.uploaded",
  "schema_version": "1.0",
  "tenant_id": "tenant_1",
  "actor_id": "user_1",
  "correlation_id": "trace_abc",
  "payload_ref": {
    "storage": "s3",
    "bucket": "workspace-uploads",
    "key": "tenant_1/uploads/file_abc",
    "sha256": "abc123...",
    "size_bytes": 24800000,
    "content_type": "application/pdf"
  }
}
```

Recommended upload flow:

```text
1. Browser requests upload session from Workspace API.
2. Workspace API validates permission and returns upload_id + presigned URL.
3. Browser uploads bytes directly to object storage.
4. Object storage or Upload Gateway confirms upload.
5. Browser or Upload Gateway publishes file.upload.completed event to JetStream.
6. Consumer sidecar or App B validates the object reference.
7. App service processes the file after validation and scan pass.
```

Required production controls:

- Short-lived presigned URLs.
- Tenant-scoped object keys.
- Content type allowlist.
- File size limits.
- SHA-256 checksum verification.
- Malware scanning.
- Abandoned upload cleanup.
- Audit event for every upload state transition.

## 10. The Problems We Solve

### 10.1 Unified user workflow

Users can receive app notifications in one workspace inbox and take permitted
actions without jumping across app-specific workflows.

### 10.2 Standard app integration

Developers integrate through a platform-provided SDK, subject naming model,
event envelope, and permission sidecar instead of rebuilding event plumbing in
every app.

### 10.3 Secure low-latency communication

NATS WebSocket enables realtime UX, while short-lived credentials, subject ACLs,
Workspace Auth grants, and sidecar checks prevent broad event-bus access.

### 10.4 Governed cross-app actions

App A can publish directly to App B's subscribed subject, but only after Workspace
Auth issues a short-lived NATS token that includes B's allowed subject. This keeps
the communication path fast while making app-to-app permission visible and
enforceable by the platform.

### 10.5 Durable and recoverable event processing

JetStream provides durable messaging. Idempotent consumers, retry, replay, and
DLQ make event processing safe under failure.

### 10.6 Large payload support

Files and large payloads are handled through object storage references, keeping
NATS focused on coordination instead of bulk transfer.

## 11. Enhancements

The following enhancements are recommended before production launch.

### 11.1 Developer experience

- Developer portal for app registration, schema registration, credentials, and
  event monitoring.
- SDKs for TypeScript, Go, and Python.
- Local sidecar dev mode.
- App onboarding checklist and integration tests.

### 11.2 Governance

- Schema registry and compatibility checks.
- Event catalog.
- Standard event envelope.
- Command/event naming rules.
- Subject naming standard.
- App-to-app policy registry.
- NATS token claim standard.
- Payload size policy.

### 11.3 Reliability

- Transactional outbox pattern for app-published events.
- Idempotent consumer library.
- Sidecar duplicate suppression.
- DLQ dashboards.
- Replay tooling by tenant, subject, event type, and time range.

### 11.4 Security

- TLS everywhere.
- Short-lived NATS credentials.
- Subject-level ACLs.
- Tenant isolation.
- Sidecar-owned consumer NATS credentials.
- App-to-app publish tokens scoped to exact B subjects.
- Network policy preventing app-to-NATS bypass.
- Rate limits by tenant, user, app, and subject.

### 11.5 Audit and observability

- Audit Archiver service.
- Long-term audit storage outside JetStream.
- Distributed tracing with correlation IDs.
- Consumer lag dashboard.
- Upload scan dashboard.
- Permission allow/deny dashboard.
- App-to-app token issuance and denial dashboard.

## 12. Management Decision Points

### Decision 1: Client-to-NATS boundary

Approve this model:

```text
NATS WebSocket is allowed for realtime UX and scoped commands.
App-to-app communication uses scoped NATS tokens issued by Workspace Auth.
Large payloads and files must use object storage references.
```

### Decision 2: App-to-app publish grant model

Approve this model:

```text
App A may publish directly to App B's subscribed subject only after Workspace Auth
validates app-to-app policy and signs a short-lived NATS token allowing that exact
B subject. Grant requests must go through App A sidecar/SDK. NATS enforces publish
permission; App B owns schema validation on consume.
```

### Decision 3: Consumer-side enforcement boundary

Approve this model:

```text
App-to-app publishing can be direct when App A has a short-lived scoped token.
For consuming, App B owns schema validation, idempotency, domain permission
checks, and audit emission before business effects are applied.
```

### Decision 4: Delivery guarantee

Approve this model:

```text
The platform provides durable at-least-once delivery with idempotent consumers,
DLQ/replay tooling, and separate long-term audit storage.
The platform does not promise exactly-once business effects.
```

### Decision 5: Large payload strategy

Approve this model:

```text
NATS remains the primary control plane.
Object storage or NATS Object Store handles large payloads.
JetStream events carry references, checksums, lifecycle status, and audit metadata.
```

## 13. Success Metrics

| Area | Metric |
|---|---|
| Realtime UX | P95 client notification latency < 500 ms |
| Event durability | 0 lost committed business events |
| Reliability | DLQ rate below agreed threshold |
| Recovery | Replay tool can replay by tenant, subject, and time range |
| Security | 100% cross-app publishes use Workspace Auth grants and NATS ACLs |
| Audit | 100% permission decisions emit audit records |
| Developer onboarding | New app can publish/consume first event within one working day |
| Large payloads | 100% file uploads use reference flow and scan pipeline |

## 14. Open Questions

1. What is the exact tenant isolation model: one NATS account per tenant, subject
   prefixes, or both?
2. Which authorization model should be used: RBAC, ABAC, relationship-based
   authorization, or a hybrid?
3. How should token revocation be enforced for already-issued 5-15 minute tokens?
4. Which object storage provider should be the default for production?
5. How long should JetStream retain each event class?
6. What is the audit retention requirement by tenant and event type?
7. Which languages need first-class SDK support for the first release?

## 15. Final Recommendation

Build the workspace platform as a governed realtime event mesh:

1. Use NATS JetStream as the primary communication backbone.
2. Use NATS WebSocket for low-latency browser communication with strict scoped
   credentials.
3. Use Workspace Auth Service to issue short-lived NATS tokens that allow App A to
   publish only to App B subjects approved by app-to-app policy.
4. Let App B validate data/object schema on consume.
5. Use a required permission sidecar for app integration and consumer-side
   enforcement where appropriate.
6. Use object storage or NATS Object Store for files and large payloads, with
   JetStream carrying references and lifecycle events.
7. Standardize idempotent consumer behavior across sidecar and app service.
8. Store long-term audit outside JetStream.

This design preserves the original low-latency event-driven direction while adding
the controls needed for production security, reliability, and operability.
