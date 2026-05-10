# Workspace Platform Recommended Architecture

This document refines `prd/core.md` into a production-oriented architecture proposal.
The recommendation keeps NATS JetStream as the primary communication backbone, while
adding guardrails for permission control, large payloads, idempotency, auditability,
and operational readiness.

## 1. Executive Recommendation

Use a **NATS-first hybrid event architecture**:

```text
Browser / Client
  -> NATS WebSocket for realtime commands and subscriptions
  -> scoped subjects and short-lived credentials
  -> NATS JetStream as the durable event backbone
  -> platform-provided permission sidecar
  -> app service over private localhost HTTP/gRPC
```

JetStream should be the primary path for commands, events, workflow state changes,
notifications, audit signals, and app-to-app communication. It should not be treated
as the universal payload transport for every byte. Large files and oversized payloads
should be stored outside the event payload and referenced by JetStream events.

The key architecture principle:

> NATS carries intent, metadata, references, workflow state, and delivery semantics.
> App services and object storage handle domain state and large bytes.

## 2. Requirements Interpreted From PRD

### End-user requirements

1. Users can view and update cross-application data from a single workspace.
2. Users can interact through a unified flow, especially inbox and notification actions.
3. Users only see and invoke data/actions allowed by their permissions.
4. Realtime interaction should support low-latency updates.

### Developer requirements

1. App developers can onboard apps into the workspace platform.
2. Developers get a unified interface, SDK, and sidecar integration model.
3. Apps can communicate with other apps through platform-defined contracts.
4. The platform can monitor and audit events and actions.
5. Apps can emit events that notify or trigger actions in other apps.

### Non-functional requirements

| Requirement | Target |
|---|---:|
| Users | 200k |
| End-user requests | 6M / day |
| Peak RPS | 500 |
| Applications | 20 |
| App events | 10M / day |
| Event latency | < 500 ms |
| Event durability | No business event loss |

## 3. Target Architecture

```text
                         +----------------------+
                         | Identity / Policy    |
                         | short-lived creds    |
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

## 4. Component Responsibilities

### Browser / client

The client may connect to NATS through WebSocket, but only with narrow permissions.

Responsibilities:

- Connect using TLS WebSocket.
- Use short-lived NATS credentials issued after workspace authentication.
- Publish only to allowed client or workspace command subjects.
- Subscribe only to user-specific or tenant-scoped subjects.
- Use object upload flow for large files or large payloads.

The client must not have broad publish/subscribe access to internal app subjects.

### NATS JetStream

JetStream is the platform communication backbone.

Responsibilities:

- Durable event and command delivery.
- Pub/sub fanout.
- Queue groups for scalable consumers.
- Replay for recovery.
- Dead-letter streams for poison messages.
- Realtime event distribution to clients and sidecars.

JetStream should carry small command/event payloads directly and large payload
references indirectly.

### Workspace Core

Workspace Core owns cross-app governance.

Responsibilities:

- Validate cross-app command schemas.
- Enforce global workspace policy.
- Route cross-app commands to target app subjects.
- Maintain event catalog and schema registry integration.
- Emit audit records for orchestration decisions.
- Coordinate workflows that involve multiple apps.

App-local flows can stay low-latency through app namespaces. Cross-app mutations
should pass through Workspace Core.

### Permission sidecar

The sidecar is the required app integration boundary.

Responsibilities:

- Own the app's NATS connection and credentials.
- Subscribe to app-specific JetStream subjects.
- Enforce subject allowlists.
- Validate event schemas.
- Check tenant, actor, resource, and action permissions.
- Handle idempotency and duplicate suppression.
- Emit allow/deny audit records.
- Forward authorized events to the app service over localhost HTTP/gRPC.
- Publish app responses/events back to NATS through controlled subjects.

The app service should not receive raw NATS credentials. Network policy should
prevent app services from bypassing the sidecar and connecting directly to NATS.

### App service

The app service owns domain behavior.

Responsibilities:

- Execute domain logic.
- Perform app-local validation.
- Store state in the app's domain database.
- Use transactional outbox for publishing app events.
- Implement app-level idempotency constraints.

The app service should treat the sidecar as a policy-enforced local gateway, not as
a replacement for domain validation.

### Object / blob storage

Object storage handles bytes that should not travel through JetStream payloads.

Recommended options:

- S3, GCS, Azure Blob, or MinIO for production file uploads.
- NATS Object Store for smaller internal artifacts or edge-local deployments.

Object storage should be tenant-scoped, immutable by default, checksum-verified,
and lifecycle-managed.

## 5. Client-to-Server Communication Policy

Use NATS WebSocket as a primary realtime transport, but classify traffic by payload
type and risk.

| Case | Recommended path |
|---|---|
| Small command/event payload | Direct JetStream payload |
| Realtime notification | NATS WebSocket subscription |
| App-local low-risk action | NATS WebSocket to scoped subject |
| Cross-app mutation | NATS WebSocket -> Workspace Core -> target sidecar |
| Large JSON/document payload | Object storage + JetStream metadata event |
| File upload | Presigned upload URL + JetStream completion event |
| Video/image/PDF/import/export | Upload Gateway + object storage + scan pipeline |
| Internal artifact | Consider NATS Object Store + reference event |

Set a platform payload guideline even if NATS allows larger payloads.

Recommended platform default:

```text
<= 10 KB: JetStream payload is preferred.
10 KB to 1 MB: prefer payload reference unless direct payload is clearly safe.
> 1 MB or any file: object storage or NATS Object Store reference.
```

## 6. Large Payload And File Upload Design

### 6.1 Payload reference pattern

For large payloads, publish metadata and a reference instead of embedding the data:

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

Consumers fetch the object only after the sidecar validates permission, schema,
size, checksum, and scan status.

### 6.2 Presigned URL upload pattern

Recommended browser file upload flow:

```text
1. Browser requests upload session from Workspace API.
2. Workspace API validates permission and returns upload_id + presigned URL.
3. Browser uploads bytes directly to object storage.
4. Object storage or Upload Gateway confirms upload.
5. Browser or Upload Gateway publishes file.upload.completed event to JetStream.
6. Sidecar / Workspace Core validates the object reference.
7. App service processes the file after validation and scan pass.
```

### 6.3 Upload status events

Use explicit upload lifecycle events:

```text
file.upload.requested
file.upload.started
file.upload.completed
file.upload.validated
file.upload.scan_passed
file.upload.scan_failed
file.upload.rejected
file.upload.processed
```

### 6.4 Production controls

Large payload flows require:

- Short-lived presigned URLs.
- Tenant-scoped object keys.
- Content type allowlist.
- File size limits.
- SHA-256 checksum verification.
- Malware scanning.
- Abandoned upload cleanup.
- Immutable object versioning for audit-sensitive uploads.
- Audit event for every upload state transition.

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

### Recommended enforcement points

1. **NATS subject ACLs**
   - Limit publish/subscribe by tenant, user, app, and subject pattern.

2. **Workspace Core**
   - Enforce cross-app command policy and schema validation.

3. **Permission sidecar**
   - Enforce app-local policy before the app service receives the event.

4. **App service**
   - Enforce domain-specific validation and invariants.

This is defense in depth. No single layer should be the only security boundary.

## 8. Subject Naming Model

Recommended subject classes:

```text
user.<tenant_id>.<user_id>.inbox.>
user.<tenant_id>.<user_id>.notifications.>
client.<tenant_id>.<user_id>.cmd.<app_id>.<action>
client.<tenant_id>.<user_id>.reply.>

workspace.<tenant_id>.cmd.<target_app_id>.<action>
workspace.<tenant_id>.event.<event_type>

app.<tenant_id>.<app_id>.cmd.<action>
app.<tenant_id>.<app_id>.event.<event_type>

audit.<tenant_id>.>
dlq.<tenant_id>.>
```

Clients should not publish directly to internal `app.*.event.*` subjects. App
services should not subscribe directly to NATS; the sidecar should do that.

## 9. Event Contract

Every event and command should use a standard envelope:

```json
{
  "event_id": "uuid",
  "idempotency_key": "tenant:user:action:resource",
  "event_type": "task.created",
  "schema_version": "1.0",
  "tenant_id": "tenant_123",
  "actor_id": "user_456",
  "source_app": "crm",
  "target_app": "tasks",
  "correlation_id": "trace_abc",
  "causation_id": "evt_parent",
  "timestamp": "2026-04-27T10:00:00Z",
  "payload": {}
}
```

Required design rules:

- Separate commands from events.
- Commands request work; events describe completed facts.
- Schema versions must be explicit.
- Backward compatibility must be checked before schema rollout.
- Payload references must include checksum, size, content type, and storage key.

## 10. Idempotent Consumer Design

JetStream should be treated as at-least-once delivery for business effects. That
means duplicate delivery is possible and must be safe.

### Recommended consumer flow

```text
1. Receive event from JetStream.
2. Sidecar validates schema and permission.
3. Check durable processed_events store by event_id or idempotency_key.
4. If already processed, ACK the message and emit duplicate-suppressed audit.
5. If new, call app service with event envelope and idempotency_key.
6. App service starts a database transaction.
7. App service applies business change.
8. App service writes processed_events record in the same transaction.
9. App service commits.
10. Sidecar ACKs JetStream message only after durable success.
```

### Processed events table

```text
processed_events
- consumer_name
- event_id
- idempotency_key
- tenant_id
- processed_at
- result_status
- result_ref
```

Recommended unique constraints:

```text
unique(consumer_name, event_id)
unique(tenant_id, idempotency_key)
```

The business update and processed-event record must commit atomically.

### ACK policy

| Case | Action |
|---|---|
| Durable processing succeeds | ACK |
| Duplicate event | ACK and suppress duplicate side effect |
| Temporary downstream failure | Do not ACK; allow retry |
| Permanent validation failure | ACK and publish rejected event |
| Poison message after retry limit | Move to DLQ and ACK original |

## 11. Reliability And Recovery

Recommended reliability model:

- Use at-least-once delivery.
- Require idempotent consumers.
- Use JetStream publish acknowledgments.
- Use retry with bounded backoff.
- Use dead-letter streams for poison messages.
- Provide replay tooling by tenant, subject, event type, and time range.
- Maintain an outbox table in each app service for events emitted from domain writes.
- Store long-term audit outside JetStream retention.

Do not promise exactly-once business effects. Promise durable delivery plus
idempotent processing.

## 12. Audit And Observability

JetStream is useful for audit streaming, but not sufficient as the long-term audit
store.

Recommended audit architecture:

```text
JetStream audit subjects
  -> Audit Archiver
  -> long-term storage: S3/Parquet, ClickHouse, Elasticsearch, or warehouse
```

Audit events should include:

- Actor identity.
- Tenant.
- Source app.
- Target app.
- Action.
- Resource reference.
- Permission decision.
- Correlation ID.
- Event ID.
- Timestamp.
- Result: allowed, denied, failed, retried, dead-lettered.

Operational metrics:

- Publish latency.
- End-to-end event latency.
- Consumer lag.
- Retry count.
- DLQ count.
- Sidecar allow/deny count.
- Upload scan failure count.
- Object cleanup backlog.
- Policy bundle freshness.
- WebSocket connection count.

## 13. Production Readiness Checklist

Before production launch, complete:

### Security

- TLS for all client and service communication.
- Short-lived client NATS credentials.
- Subject-level ACLs.
- Tenant isolation.
- Sidecar-owned NATS credentials.
- Network policy preventing app-to-NATS bypass.
- Rate limits by tenant, user, app, and subject.

### Governance

- Schema registry.
- Event catalog.
- Command/event naming rules.
- Subject naming standard.
- Backward compatibility checks.
- Payload size policy.
- Large payload reference contract.

### Reliability

- Idempotent consumer library.
- Sidecar duplicate suppression.
- App-level unique constraints.
- Outbox pattern.
- Retry and DLQ policy.
- Replay tooling.

### Operations

- NATS cluster SLOs.
- Consumer lag dashboards.
- DLQ dashboards.
- Audit archive retention.
- Upload cleanup jobs.
- Malware scanning pipeline.
- Load testing at target RPS and event rate.
- Chaos testing for sidecar, NATS, object storage, and policy service failures.

## 14. High-Level Management Decisions

Three decisions should be made explicitly:

### Decision 1: Client-to-NATS boundary

Approve the hybrid model:

```text
NATS WebSocket is allowed for realtime UX and scoped commands.
Sensitive cross-app mutations must pass through Workspace Core.
Large payloads/files must use object storage references.
```

### Decision 2: Sidecar as mandatory boundary

Approve the sidecar as required platform infrastructure:

```text
App services do not directly consume NATS.
The platform-provided sidecar owns NATS credentials, permission checks, schema
validation, idempotency, and audit emission.
```

### Decision 3: Delivery guarantee

Approve the production reliability promise:

```text
The platform provides durable at-least-once delivery with idempotent consumers,
DLQ/replay tooling, and separate long-term audit storage.
The platform does not promise exactly-once business effects.
```

## 15. Final Recommendation

Build the workspace platform as a **governed realtime event mesh**:

1. Use NATS JetStream as the primary communication backbone.
2. Use NATS WebSocket for low-latency browser communication with strict scoped
   credentials.
3. Use Workspace Core for cross-app governance and schema validation.
4. Use a required permission sidecar for all app integration.
5. Use object storage or NATS Object Store for files and large payloads, with
   JetStream carrying references and lifecycle events.
6. Standardize idempotent consumer behavior across sidecar and app service.
7. Store long-term audit outside JetStream.

This design preserves the original PRD's low-latency event-driven direction while
adding the controls needed for production security, reliability, and operability.
