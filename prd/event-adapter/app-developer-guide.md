# App Developer Guide: Integrating the event-adapter sidecar

**Audience:** Application developers exposing HTTP handlers for platform events.
**Scope:** Inbound delivery. Two models, both ending at your local HTTP handler:
  - **Request-reply (primary):** NATS request -> sidecar -> app HTTP endpoint -> reply CloudEvent on the caller's inbox. Sections 1–10 plus [section 11](#11-request-reply-integration).
  - **JetStream event (opt-in):** NATS JetStream CloudEvent -> sidecar -> app HTTP endpoint -> response CloudEvent -> NATS. The flow documented in sections 2–10.
**Related PRD:** [`prd.md`](./prd.md) (request-reply: section 17)

---

## 1. What You Build

Your application does not need to connect to NATS or publish response events directly for this flow. The sidecar owns NATS consumption, retry, DLQ, response event construction, and response publishing.

As an app developer, you provide:

- One or more HTTP endpoints on your app service.
- A route config that maps incoming CloudEvents to those endpoints.
- Idempotent handler logic for each event type.
- A normal HTTP response body that the sidecar can wrap as a deterministic response CloudEvent.

## 2. Request Flow

```text
NATS JetStream durable consumer
  -> sidecar consumes CloudEvent
  -> sidecar validates JSON CloudEvent data and matches route config exactly
  -> sidecar sends CloudEvent data to localhost app endpoint
  -> app returns 2xx response body
  -> sidecar wraps response body as CloudEvent
  -> sidecar publishes response CloudEvent to NATS
  -> sidecar acknowledges original JetStream message
```

Your app only handles the local HTTP request. Everything before and after that belongs to the sidecar.

## 3. Step-by-Step Integration

### Step 1: Choose the Event Types You Handle

Identify the CloudEvent types your app should consume. For each event, record:

- Incoming NATS subject (scopes the consumer filter subject).
- CloudEvent `type` (the route match key).
- CloudEvent `source` (optional; not used for matching).
- Local HTTP method and path.
- Response CloudEvent `type`.
- Response publish subject.

Example:

```text
Incoming subject: t.tenant-a.app.task.event.created
Incoming type:    com.workspace.task.created
Incoming source:  workspace/task
HTTP endpoint:    POST /events/task-created
Response type:    com.workspace.task.created.processed
Response subject: t.tenant-a.app.task.event.processed
```

Phase 1 route matching is by CloudEvent `type`. The `type` must match exactly one configured route. The NATS filter subject already scopes which messages reach the sidecar, so `subject` and `source` are not used for matching.

### Step 2: Add an HTTP Handler

Create an endpoint in your app service for each event route. The sidecar sends the incoming CloudEvent JSON `data` payload as the request body.

Example request received by your app:

```http
POST /events/task-created HTTP/1.1
Host: 127.0.0.1:8080
Content-Type: application/json
ce-id: evt-123
ce-type: com.workspace.task.created
ce-source: workspace/task
ce-specversion: 1.0
ce-subject: task-456
Idempotency-Key: evt-123
traceparent: 00-...

{
  "taskId": "task-456",
  "title": "Review onboarding request",
  "assigneeId": "user-789"
}
```

Your handler should:

- Parse the request body as the event payload.
- Read `ce-id` or `Idempotency-Key` for idempotency.
- Use `ce-type`, `ce-source`, and correlation headers for logging and trace context.
- Return `2xx` only after the business operation has succeeded.
- Return a JSON response body that should become the response CloudEvent `data`.

Phase 1 supports JSON CloudEvent `data` payloads. CloudEvents using `data_base64` are rejected by the sidecar.

Example response:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "taskId": "task-456",
  "status": "processed"
}
```

## 4. Handler Requirements

### Be Idempotent

The sidecar provides at-least-once delivery. Your endpoint may receive the same event more than once after retry, process restart, or JetStream redelivery.

Use the incoming `ce-id` or `Idempotency-Key` as the operation key. Before creating side effects, check whether that event has already been processed.

Recommended pattern:

1. Start a database transaction.
2. Insert the incoming event ID into a processed-events table with a unique constraint.
3. If the insert conflicts, return the existing result or a successful no-op response.
4. Apply the business change.
5. Commit the transaction.
6. Return `2xx`.

### Use Supported Methods And Clear Status Codes

- Configure handlers with `POST`, `PUT`, or `PATCH`.
- Return `2xx` when processing succeeded.
- Return `4xx` when the event payload is invalid for your handler.
- Return `5xx` when processing failed and you want the publisher to observe the failure as an error response event.

The sidecar publishes a response event for every HTTP response (success or error) and carries the status code in the `httpstatus` CloudEvent extension. The sidecar does not retry on `4xx` or `5xx` — if you need a retry on a transient failure, do it inside the handler before returning. Only network-class failures (timeout, connection refused, TLS error) are retried by the sidecar.

### Keep Handlers Fast

The sidecar route config includes an HTTP timeout. If your handler times out, the sidecar treats the attempt as failed and retries.

Long-running work should be accepted quickly and moved to your app's own background processing if the platform workflow allows that pattern.

### Do Not Publish the Response Event Yourself

For this Phase 1 flow, your app returns an HTTP response body. The sidecar wraps it as the configured response CloudEvent and publishes it to NATS.

Response event IDs are deterministic for the incoming event and route. This means retries of the same successfully processed input produce the same response event ID.

## 5. Route Configuration

Add a route entry for each event your app handles.

Example sidecar config:

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
      headers:
        X-Platform-Route: task-created
      forwardHeaders:
        - X-Workspace-Actor-Id
        - X-Workspace-Tenant-Id
    response:
      type: com.workspace.task.created.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed
      dataschema: https://schemas.example.com/task-created-processed.json
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
```

Important fields:

- `app.httpBaseURL` must use `http` and must point to `127.0.0.1`, `localhost`, or another loopback IP. External hosts fail validation.
- `nats.stream` and `nats.durableConsumer` configure JetStream durable consumption. Queue subscriptions are not part of Phase 1.
- `match` must set `type` to identify the CloudEvents this route accepts. `subject` and `source` are optional and not used for matching.
- `dispatch.method` must be `POST`, `PUT`, or `PATCH`.
- `dispatch.path` must start with `/` and match an endpoint your app exposes.
- `dispatch.timeout` should be shorter than the JetStream acknowledgement window.
- `dispatch.headers` defines static headers the sidecar adds to app requests. These cannot override reserved CloudEvent, authorization, idempotency, trace, or hop-by-hop headers.
- `dispatch.forwardHeaders` is an optional allowlist restricting which publisher-supplied `dispatchheaders` are forwarded. By default (field omitted or empty), the sidecar forwards every header in `dispatchheaders` except reserved names (CloudEvent metadata, `Idempotency-Key`, `Authorization`, trace context (`traceparent`), hop-by-hop). Set this list only when a route needs to lock forwarding down to a specific set of headers.
- `dispatchcookies` is an optional top-level CloudEvent field (a `name → value` object) for forwarding HTTP cookies to your app, e.g. session or CSRF tokens. The sidecar attaches each entry as a request cookie via `http.AddCookie`. Cookies are forwarded as-is with no per-route allowlist; cookie attributes (path, domain, secure, httponly) are not supported. The `Cookie` header is reserved, so cookies must be sent through `dispatchcookies`, not `dispatchheaders`.
- `response.type` and `response.subject` define what the sidecar publishes after success.
- `response.dataschema` is optional and sets the response CloudEvent `dataschema`.
- `retry` controls bounded retry before DLQ.
- `dlq.subject` receives events that cannot be delivered after retry exhaustion.

The YAML parser is strict. Unknown fields are rejected so configuration mistakes fail at startup instead of being silently ignored.

## 6. Kubernetes Integration

Your deployment should run the app container and sidecar container in the same pod.

Conceptual deployment shape:

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: task-service
spec:
  template:
    spec:
      containers:
        - name: app
          image: task-service:latest
          ports:
            - containerPort: 8080
        - name: event-dispatch-sidecar
          image: event-adapter-sidecar:latest
          args:
            - --config=/etc/sidecar/config.yaml
          volumeMounts:
            - name: sidecar-config
              mountPath: /etc/sidecar
              readOnly: true
      volumes:
        - name: sidecar-config
          configMap:
            name: task-service-sidecar-config
```

Platform-owned deployment templates may provide the actual image, credentials, health probes, and volume names. App teams should mainly own the HTTP endpoints and route entries.

Because dispatch is loopback-only, the app container and sidecar container must run in the same pod or otherwise share the same loopback network namespace expected by the deployment template.

## 7. Local Testing Checklist

Before asking the platform team to enable the route in a shared environment, verify:

- The app endpoint accepts the expected JSON payload.
- The endpoint returns `2xx` only after the operation is complete.
- Replaying the same `ce-id` does not duplicate side effects.
- The response body is valid JSON for the expected response event.
- The route path and method match the app endpoint.
- The route uses the CloudEvent `type` to match.
- The app base URL uses `http://127.0.0.1`, `http://localhost`, or another loopback IP.
- Timeout settings are realistic for the handler.
- Logs include event ID, event type, and correlation ID when present.

Example local request:

```bash
curl -i \
  -X POST http://127.0.0.1:8080/events/task-created \
  -H 'Content-Type: application/json' \
  -H 'ce-id: evt-123' \
  -H 'ce-type: com.workspace.task.created' \
  -H 'ce-source: workspace/task' \
  -H 'ce-specversion: 1.0' \
  -H 'Idempotency-Key: evt-123' \
  -H 'X-Workspace-Actor-Id: user-1' \
  --data '{"taskId":"task-456","title":"Review onboarding request","assigneeId":"user-789"}'
```

When testing through the sidecar instead of calling the app directly, publisher-supplied backend headers must be placed in the CloudEvent `dispatchheaders` extension. They are forwarded by default; if the route declares `dispatch.forwardHeaders`, only headers on that allowlist are forwarded.

## 8. Operational Expectations

During production support, app teams should be able to answer:

- Which event types does this service consume?
- Which HTTP handler processes each event type?
- Is each handler idempotent?
- What timeout does each handler need?
- Which failures should be retried versus treated as permanent payload errors?
- What response event does each handler produce?
- Which publisher-supplied headers, if any, must be allowed through `dispatch.forwardHeaders`?

The sidecar exposes delivery metrics, retry metrics, DLQ metrics, and publish metrics. App teams should add application-level logs and metrics around the business operation performed by each handler.

## 9. Common Mistakes

- Connecting the application directly to NATS for this inbound flow.
- Returning `200 OK` before the business operation is durable.
- Using event payload fields instead of `ce-id` or `Idempotency-Key` for deduplication.
- Treating duplicate delivery as an error.
- Assuming `subject` or `source` affect route matching — only the CloudEvent `type` is matched.
- Configuring `app.httpBaseURL` to a Kubernetes service name or external host instead of loopback.
- Sending CloudEvents with `data_base64`.
- Forgetting to update route config when renaming an HTTP path.
- Returning non-JSON when downstream consumers expect JSON response event data.
- Letting publisher-supplied headers override authorization, trace, idempotency, or CloudEvent metadata.

## 11. Request-Reply Integration

The request-reply model is the primary way to expose a synchronous, "HTTP-style" call over the event backbone (for example, minting a file-upload presigned URL). The caller is NATS-native and blocks on a reply; your app still only handles a local HTTP request. Sections 2–10 above describe the asynchronous JetStream event model; this section covers what differs for request-reply.

### 11.1 What you build

Same as the event model: a local HTTP endpoint plus a route entry. The differences are:

- You register the route under a `requests:` block instead of `routes:`.
- You declare a `reply` (the CloudEvent type/source the sidecar stamps on the reply) instead of a `response` with a publish subject.
- You do **not** configure `retry` or `dlq` — there is no durable redelivery; a failed request returns an error reply the caller may retry.

### 11.2 Request flow

```text
NATS-native caller issues request-reply
  -> sidecar responder consumes the request CloudEvent (queue group, worker pool)
  -> sidecar validates JSON CloudEvent data and matches a request route by type
  -> sidecar sends CloudEvent data to your localhost endpoint
  -> app returns a response body (2xx success, or 4xx/5xx business rejection)
  -> sidecar wraps the response as a reply CloudEvent (httpstatus + causationid)
  -> sidecar sends the reply on the caller's reply inbox
```

### 11.3 Handler requirements (differences from the event model)

- **Reply body is the answer.** Return the JSON the caller needs (e.g. the presigned URL and upload id). The sidecar wraps it as the reply CloudEvent `data`. Do not publish anything yourself.
- **Business rejections are normal replies.** Return `4xx` for an invalid request (e.g. disallowed content-type) or `5xx` for a processing error. The sidecar forwards these as replies with the status in `httpstatus`; they are **not** retried. Validation failures reaching the caller as a reply is the intended behavior.
- **No redelivery-driven idempotency requirement.** Unlike the event model, the sidecar does not redeliver requests. A duplicate only happens if the *caller* retries; key on the incoming `ce-id` if your operation must be exactly-once across caller retries.
- **Latency matters more.** The caller is blocked. Keep handlers within `dispatch.timeout`; on timeout the caller receives a `504` error reply.

### 11.4 Route configuration

```yaml
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222           # connection only; required for both models

requests:
  subject: q.tenant-a.app.uploads.request   # core-NATS subject your responder answers
  queueGroup: upload-responders             # one delivery per group across replicas
  workerPoolSize: 16                         # bounded in-flight dispatches
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request   # route match key (type only)
      dispatch:
        method: POST
        path: /requests/upload-presign        # your loopback endpoint
        timeout: 3s
        forwardHeaders:
          - X-Workspace-Tenant-Id
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
        # dataschema: optional
```

Notes:

- `requests.subject` may be a wildcard; the queue group load-balances across your replicas.
- `match`, `dispatch`, `dispatch.forwardHeaders`, `dispatchheaders`, and `dispatchcookies` work exactly as in the event model (sections 3, 5, 7 of the PRD).
- A request route has **no** `response`, `retry`, or `dlq` keys — the strict YAML parser rejects them.
- You can configure both a `requests:` block and a `routes:` block in the same sidecar; at least one must be present.

### 11.5 Reply contract

The sidecar sends a reply CloudEvent on the caller's inbox:

- `type` / `source` from your `reply` config.
- `data` = your HTTP response body.
- `httpstatus` extension = your HTTP status code.
- `causationid` = the request's `id`; `correlationid` copied from the request when present.
- No `subject` (it travels on the inbox).

If the request is malformed the caller gets a `400` error reply; if no route matches, `404`; if your app is unreachable, `502`; if it times out, `504`. Every case is a reply — the caller never just hangs.

### 11.6 Local testing

Call your endpoint directly the same way as the event model (section 7), then test end-to-end through the sidecar with the NATS CLI:

```bash
nats request --server nats://127.0.0.1:4222 \
  q.tenant-a.app.uploads.request \
  "$(cat request-presign.json)"
```

The command blocks until the reply CloudEvent arrives and prints it. A `504`/`502` reply means the sidecar reached its dispatch error path; check that your handler is up and within the timeout.

## 12. Integration Review Template

Use this checklist when submitting a new route:

```text
Service name:
Owner:
Incoming NATS subject:
Incoming CloudEvent type (route match key):
Incoming CloudEvent source (optional):
HTTP method and path:
Expected request payload schema:
Response CloudEvent type:
Response NATS subject:
Handler timeout:
Retry max attempts:
DLQ subject:
Idempotency strategy:
Forwarded backend headers:
Operational dashboard/log link:
```

For a **request-reply** route, use this checklist instead:

```text
Service name:
Owner:
Request NATS subject:
Queue group:
Request CloudEvent type (route match key):
HTTP method and path:
Expected request payload schema:
Reply CloudEvent type:
Reply CloudEvent source:
Handler timeout (caller is blocked):
Status-code contract (which 4xx/5xx the caller should expect):
Idempotency strategy (across caller retries):
Forwarded backend headers:
Operational dashboard/log link:
```
