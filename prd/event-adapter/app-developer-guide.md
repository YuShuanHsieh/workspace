# App Developer Guide: Integrating the event-adapter sidecar

**Audience:** Application developers exposing local HTTP handlers for platform
events and request-reply calls.
**Scope:** Inbound delivery. Your app handles HTTP on loopback; the sidecar owns
NATS.
**Related Docs:** [`prd.md`](./prd.md),
[`file-upload-app-developer-guide.md`](./file-upload-app-developer-guide.md)

---

## 1. What You Build

Your application does not need NATS client code for inbound calls handled by the
sidecar. You build normal HTTP endpoints. The sidecar receives CloudEvents from
NATS, calls your loopback endpoint, and converts your HTTP response back into the
right event shape.

There are two delivery models:

- **Request-reply (primary):** use this for synchronous, HTTP-style app calls
  that have moved to the event backbone. A caller sends a NATS request, waits for
  a reply, and receives your handler response as a reply CloudEvent.
- **JetStream event consumption (opt-in):** use this for durable,
  fire-and-forget fan-out where a published event may be consumed by one or more
  services and must survive consumer restarts.

Most app developers should start with request-reply. JetStream matters when your
service is reacting to an event asynchronously, not when it is answering a direct
call.

## 2. Default Flow: Request-Reply

```text
NATS-native caller sends request CloudEvent
  -> sidecar responder receives it on a core-NATS subject
  -> sidecar matches a request route by CloudEvent type
  -> sidecar sends CloudEvent data to your localhost HTTP endpoint
  -> app returns a JSON HTTP response
  -> sidecar wraps that response as a reply CloudEvent
  -> sidecar replies on the caller's inbox
```

Your app only handles the local HTTP request. The caller blocks waiting for the
reply, so keep handlers within the configured timeout.

Use request-reply for examples like:

- minting file-upload presigned URLs
- validating a synchronous command
- fetching a small service-owned answer that a caller needs before continuing

If you are implementing file upload, the full workflow is a special case:
request-reply for presign, direct HTTP upload for the file bytes, then a
JetStream `file.uploaded` event after the upload completes. See
[`file-upload-app-developer-guide.md`](./file-upload-app-developer-guide.md) for
the full pattern.

## 3. Step-by-Step Request-Reply Integration

### Step 1: Choose the request type

For each request your service answers, record:

- Request NATS subject.
- CloudEvent `type` (the route match key).
- Local HTTP method and path.
- Reply CloudEvent `type` and `source`.
- Expected success and error response bodies.

Example:

```text
Request subject: q.tenant-a.app.uploads.request
Request type:    com.workspace.files.presign.request
HTTP endpoint:   POST /requests/upload-presign
Reply type:      com.workspace.files.presign.reply
Reply source:    upload-service
```

Request routes match by CloudEvent `type` only. The request subject decides which
messages reach the responder; it is not part of route matching.

### Step 2: Add an HTTP handler

Create an endpoint in your app service for each request route. The sidecar sends
the incoming CloudEvent JSON `data` payload as the request body and forwards
CloudEvent metadata as HTTP headers.

Example request received by your app:

```http
POST /requests/upload-presign HTTP/1.1
Host: 127.0.0.1:8080
Content-Type: application/json
ce-id: req-presign-1
ce-type: com.workspace.files.presign.request
ce-source: workspace/files-client
ce-specversion: 1.0
Idempotency-Key: req-presign-1
traceparent: 00-...

{
  "filename": "photo.jpg",
  "contentType": "image/jpeg",
  "size": 20480
}
```

Your handler should:

- Parse the request body as the request payload.
- Read `ce-id` or `Idempotency-Key` if the operation must deduplicate caller
  retries.
- Use `ce-type`, `ce-source`, and trace context for logging.
- Return the JSON answer the caller needs.
- Return clear `4xx` responses for invalid requests and `5xx` responses for
  processing failures.

Example success response:

```http
HTTP/1.1 200 OK
Content-Type: application/json

{
  "uploadUrl": "https://object-storage.example/upload/tenant-a/photo-123",
  "objectKey": "tenant-a/uploads/photo-123",
  "expiresInSeconds": 900
}
```

Business rejections are still replies. For example, if the file type is not
allowed, return `415` or `422` with a JSON body; the sidecar forwards that body
as a reply CloudEvent with the HTTP status in the `httpstatus` extension.

### Step 3: Configure the request route

```yaml
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222

requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: upload-responders
  workerPoolSize: 16
  routes:
    - name: upload-presign
      match:
        type: com.workspace.files.presign.request
      dispatch:
        method: POST
        path: /requests/upload-presign
        timeout: 3s
        forwardHeaders:
          - X-Workspace-Tenant-Id
      reply:
        source: upload-service
        type: com.workspace.files.presign.reply
        # dataschema: optional
```

Important fields:

- `app.httpBaseURL` must use `http` and must point to `127.0.0.1`, `localhost`,
  or another loopback IP. External hosts fail validation.
- `nats.url` is required so the sidecar can connect to NATS.
- `requests.subject` is the core-NATS subject the responder answers. It may be a
  wildcard when the subject scheme requires it.
- `requests.queueGroup` lets replicas share work; one request is delivered to
  one member of the group.
- `requests.workerPoolSize` bounds in-flight local HTTP dispatches.
- `match.type` must identify exactly one request route.
- `dispatch.method` must be `GET`, `POST`, `PUT`, `PATCH`, or `DELETE`.
- `dispatch.path` must start with `/` and match an endpoint your app exposes.
- `dispatch.timeout` should fit the caller's waiting budget.
- `dispatch.headers` defines static headers the sidecar adds to app requests.
  These cannot override reserved CloudEvent, authorization, idempotency, trace,
  cookie, or hop-by-hop headers.
- `dispatch.forwardHeaders` optionally restricts which publisher-supplied
  `dispatchheaders` are forwarded. When omitted or empty, all non-reserved
  `dispatchheaders` are forwarded.
- `reply.type` and `reply.source` define the reply CloudEvent envelope.

A request route has no `response`, `retry`, or `dlq` keys. The strict YAML parser
rejects those fields under `requests.routes`.

### Optional direct dispatch

Direct dispatch is an opt-in request-reply fallback for operations that would
otherwise require many static routes. Exact request type routes always win. If
no exact route matches, the publisher supplies a fully resolved relative
`dispatchpath` and `dispatchmethod`; the adapter joins that path only to the
validated loopback `app.httpBaseURL`.

```yaml
requests:
  subject: q.tenant-a.app.orders.request
  queueGroup: order-responders
  workerPoolSize: 16
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes:
      - /orders/
```

Allowed methods are `GET`, `POST`, `PUT`, `PATCH`, and `DELETE`. Prefixes are
optional and use path-segment boundaries. A full URL, traversal, malformed
path, unsupported method, or other invalid target returns 400 without calling
your app. With direct dispatch disabled and no exact route, the caller gets
404. `directDispatch.timeout` is required, must be positive, and applies to
every direct dispatch. Direct dispatch is never available for JetStream; static
JetStream routes may nevertheless use `DELETE`.

Example direct request (the query string is preserved):

```json
{
  "specversion": "1.0",
  "id": "req-456",
  "source": "checkout-service",
  "type": "com.workspace.orders.modify.request",
  "dispatchmethod": "DELETE",
  "dispatchpath": "/orders/ord-456?hard=true",
  "correlationid": "checkout-789",
  "data": {}
}
```

Generic direct replies use type `io.eventadapter.direct.reply`, source
`app.id`, and no subject. They preserve correlation/causation, HTTP status,
redirect location, and response content type/body. Incoming publisher headers
and cookies continue to follow the existing forwarding and reserved-header
rules; response headers and cookies are not copied into the reply CloudEvent.

## 4. Reply Contract

The sidecar sends a reply CloudEvent on the caller's inbox:

- `type` and `source` come from the route's `reply` config.
- `data` is your HTTP response body.
- `httpstatus` is your HTTP response status.
- `causationid` is the request CloudEvent `id`.
- `correlationid` is copied from the request when present.

If the sidecar cannot dispatch to your handler, the caller still receives a
reply:

| Situation | Reply status |
|---|---:|
| Malformed CloudEvent | 400 |
| No matching route | 404 |
| App unreachable | 502 |
| App timeout | 504 |

There is no sidecar retry or DLQ in request-reply. A synchronous failure is
returned to the caller, and the caller decides whether to retry.

## 5. Handler Requirements

### Keep handlers fast

The caller is waiting. Keep work inside `dispatch.timeout`. If the operation is
long-running, return an accepted response quickly only when the workflow really
supports asynchronous completion.

If your handler returns a `3xx` redirect status with a `Location` header, the
sidecar publishes the response event, or the reply for request-reply routes,
with both `httpstatus` and an `httplocation` extension carrying that value. The
sidecar does not follow the redirect. This applies to all `3xx` codes. If you
return `3xx` without a `Location` header, only `httpstatus` is set.

### Use clear status codes

- Return `2xx` when the request succeeded.
- Return `4xx` when the caller sent an invalid or unauthorized request.
- Return `5xx` when your service failed while processing.

The sidecar does not treat HTTP `4xx` or `5xx` from your handler as transport
errors. It wraps them as normal reply CloudEvents so the caller can make a
decision.

### Make retry-sensitive operations idempotent

Request-reply has no sidecar redelivery, but callers may retry after timeouts or
error replies. If a duplicate caller retry would create duplicate side effects,
use `ce-id` or `Idempotency-Key` as the operation key.

Recommended pattern:

1. Start a database transaction.
2. Insert the incoming request ID into a processed-requests table with a unique
   constraint.
3. If the insert conflicts, return the existing result or a successful no-op
   response.
4. Apply the business change.
5. Commit the transaction.
6. Return the response.

### Do not publish the reply yourself

Your app returns an HTTP response. The sidecar wraps it as a reply CloudEvent and
sends it to the caller's inbox.

## 6. Optional Flow: JetStream Event Consumption

Use JetStream routes when your service reacts to an event asynchronously and the
work should be redelivered if the consumer crashes before completion.

```text
NATS JetStream durable consumer
  -> sidecar consumes CloudEvent
  -> sidecar matches an event route by CloudEvent type
  -> sidecar sends CloudEvent data to your localhost endpoint
  -> app returns a response body
  -> sidecar wraps response body as a response CloudEvent
  -> sidecar publishes the response CloudEvent to NATS
  -> sidecar acknowledges the original JetStream message
```

Compared with request-reply:

- JetStream is at-least-once; handlers must be idempotent.
- Network-class dispatch failures are retried by the sidecar.
- Exhausted delivery failures go to DLQ.
- The response is published to a configured NATS subject instead of returned to a
  waiting caller.

### JetStream route example

```yaml
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  filterSubject: t.tenant-a.app.task.event.created
  workerPoolSize: 16
  fetchBatch: 16
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
  # Optional: path to a NATS credentials file (.creds JWT) for authenticated
  # NATS deployments. Leave unset for local dev or unauthenticated NATS.
  credsFilePath: /etc/nats/svc.creds

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
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
```

Event-route notes:

- `nats.stream`, `nats.durableConsumer`, `ackWait`, `maxDeliver`, and DLQ fields
  are required only when `routes:` is configured.
- `match.type` is the route match key. `subject` and `source` document and scope
  intent, but matching is by type.
- `response.type`, `response.source`, and `response.subject` define the
  CloudEvent the sidecar publishes after your handler returns.
- `retry` controls bounded dispatch retry before DLQ.

You can configure `requests:` and `routes:` in the same sidecar. At least one
must be present.

### Routes with dynamic path segments

If your handler lives at a path with a dynamic segment — for example `PUT /api/tasks/{taskId}/complete` — declare the template in your route config:

```yaml
dispatch:
  method: PUT
  path: /api/tasks/{taskId}/complete
```

Publishers carry the path parameter values in a top-level `dispatchpathparams` envelope field, kept separate from the `data` request payload:

```json
{
  "specversion": "1.0",
  "id": "evt-001",
  "type": "com.workspace.task.updated",
  "source": "workspace/task",
  "dispatchpathparams": { "taskId": "task-42" },
  "data": { "title": "Buy milk", "priority": "high" }
}
```

The sidecar resolves `{taskId}` from `dispatchpathparams` and dispatches `PUT /api/tasks/task-42/complete` with `{"title":"Buy milk","priority":"high"}` as the HTTP request body. Your handler receives a clean request — path params come from the URL, request payload comes from the body, no mixing.

If the event omits the referenced parameter from `dispatchpathparams`, the sidecar sends the event to your route's DLQ subject. There are no retries — the event data does not change between attempts.

## 7. Headers, Cookies, And CloudEvent Data

Phase 1 supports JSON CloudEvent `data` payloads. CloudEvents using
`data_base64` are rejected by the sidecar.

The sidecar forwards CloudEvent attributes as `ce-*` headers and sets
`Idempotency-Key` from the incoming CloudEvent id.

Publisher-supplied backend headers must be placed in the CloudEvent
`dispatchheaders` extension. They are forwarded by default; if the route declares
`dispatch.forwardHeaders`, only headers on that allowlist are forwarded.

`dispatchcookies` is an optional top-level CloudEvent field, a `name -> value`
object, for forwarding HTTP cookies to your app. The sidecar attaches each entry
as a request cookie via `http.AddCookie`. Cookie attributes such as path, domain,
secure, and httponly are not supported. The `Cookie` header is reserved, so
cookies must be sent through `dispatchcookies`, not `dispatchheaders`.

## 8. Kubernetes Integration

Your deployment should run the app container and sidecar container in the same
pod.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: upload-service
spec:
  template:
    spec:
      containers:
        - name: app
          image: upload-service:latest
          ports:
            - containerPort: 8080
        - name: event-adapter
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
            name: upload-service-sidecar-config
```

Platform-owned deployment templates may provide the actual image, credentials,
health probes, and volume names. App teams should mainly own the HTTP endpoints
and route entries.

Because dispatch is loopback-only, the app container and sidecar container must
run in the same pod or otherwise share the expected loopback network namespace.

## 9. Local Testing Checklist

Before asking the platform team to enable a route in a shared environment,
verify:

- The app endpoint accepts the expected JSON payload.
- The route path and method match the app endpoint.
- The route uses the CloudEvent `type` to match.
- The app base URL uses `http://127.0.0.1`, `http://localhost`, or another
  loopback IP.
- Timeout settings are realistic for the handler.
- Logs include event ID, event type, and correlation ID when present.
- Response bodies are valid JSON when callers or downstream consumers expect
  JSON.

Example direct request to your app:

```bash
curl -i \
  -X POST http://127.0.0.1:8080/requests/upload-presign \
  -H 'Content-Type: application/json' \
  -H 'ce-id: req-presign-1' \
  -H 'ce-type: com.workspace.files.presign.request' \
  -H 'ce-source: workspace/files-client' \
  -H 'ce-specversion: 1.0' \
  -H 'Idempotency-Key: req-presign-1' \
  -H 'X-Workspace-Tenant-Id: tenant-a' \
  --data '{"filename":"photo.jpg","contentType":"image/jpeg","size":20480}'
```

Example request-reply test through the sidecar:

```bash
nats request --server nats://127.0.0.1:4222 \
  q.tenant-a.app.uploads.request \
  "$(cat event-adapter/test/e2e/fixtures/upload-presign.json)"
```

The command blocks until the reply CloudEvent arrives and prints it.

## 10. Operational Expectations

During production support, app teams should be able to answer:

- Which request or event types does this service handle?
- Which HTTP handler processes each type?
- What timeout does each handler need?
- Which status codes can callers expect?
- Which operations must be idempotent across caller retries or event redelivery?
- Which publisher-supplied headers, if any, must be allowed through
  `dispatch.forwardHeaders`?

For JetStream event routes, app teams should also know:

- Which response event does each handler produce?
- Which failures should be retried versus treated as permanent payload errors?
- Which DLQ subject receives exhausted failures?

The sidecar exposes delivery metrics, request-reply metrics, retry metrics, DLQ
metrics, and publish metrics. App teams should add application-level logs and
metrics around the business operation performed by each handler.

## 11. Common Mistakes

- Starting with JetStream for a direct call where request-reply is the right
  model.
- Connecting the application directly to NATS for inbound sidecar-managed flows.
- Configuring `app.httpBaseURL` to a Kubernetes service name or external host
  instead of loopback.
- Assuming `subject` or `source` affect route matching; only CloudEvent `type`
  is matched.
- Sending CloudEvents with `data_base64`.
- Forgetting to update route config when renaming an HTTP path.
- Letting publisher-supplied headers override authorization, trace,
  idempotency, cookie, or CloudEvent metadata.
- Adding `response`, `retry`, or `dlq` under a request route.
- Returning non-JSON when callers expect JSON reply data.
- For JetStream routes, treating duplicate delivery as an error instead of
  making the handler idempotent.

## 12. Integration Review Templates

Use this checklist for a request-reply route:

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
Status-code contract:
Idempotency strategy across caller retries:
Forwarded backend headers:
Operational dashboard/log link:
```

Use this checklist for a JetStream event route:

```text
Service name:
Owner:
Incoming NATS subject:
Incoming CloudEvent type (route match key):
Incoming CloudEvent source:
HTTP method and path:
Expected event payload schema:
Response CloudEvent type:
Response NATS subject:
Handler timeout:
Retry max attempts:
DLQ subject:
Idempotency strategy across redelivery:
Forwarded backend headers:
Operational dashboard/log link:
```
