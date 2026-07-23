# App Developer Guide: File Upload With The event-adapter

**Audience:** Application developers implementing file upload flows with the
`event-adapter` sidecar.
**Scope:** Storage-provider-neutral upload workflow using request-reply for
presign, direct HTTP for byte transfer, and JetStream for asynchronous
completion.
**Related Docs:** [`app-developer-guide.md`](./app-developer-guide.md),
[`prd.md`](./prd.md)

---

## 1. Why Upload Is A Split-Protocol Flow

File upload is different from ordinary request-reply or ordinary JetStream event
handling because the workflow crosses two transport layers:

1. The client needs an immediate answer telling it where and how to upload.
2. The file bytes must go directly to object storage over HTTP.
3. The application still needs a durable event after the upload completes.

The sidecar only bridges CloudEvents and local HTTP handlers. It does not proxy
file bytes, and it does not verify whether object storage actually persisted the
upload.

Use the existing delivery models as-is:

- **Request-reply** for `com.workspace.files.presign.request`
- **Direct HTTP** from the client to the returned upload URL
- **JetStream event consumption** for `com.workspace.files.uploaded`

## 2. End-To-End Sequence

```text
client
  -> NATS request CloudEvent: com.workspace.files.presign.request
  -> event-adapter request route
  -> app HTTP handler: POST /requests/file-presign
  -> reply CloudEvent: com.workspace.files.presign.reply
  -> direct HTTP upload to object storage
  -> publish JetStream CloudEvent: com.workspace.files.uploaded
  -> event-adapter event route
  -> app HTTP handler: POST /events/file-uploaded
  -> app performs asynchronous post-upload work
```

This split is intentional:

- the client blocks only for the presign phase
- the upload itself uses storage-provider HTTP semantics
- post-upload processing uses the adapter's normal retry and DLQ behavior

## 3. Presign Request-Reply Contract

The client sends a request CloudEvent and waits for a reply on its inbox. The
app answers through a normal local HTTP handler.

Representative request CloudEvent:

```json
{
  "specversion": "1.0",
  "id": "req-file-presign-1",
  "source": "workspace/files-client",
  "type": "com.workspace.files.presign.request",
  "datacontenttype": "application/json",
  "data": {
    "fileName": "photo.jpg",
    "contentType": "image/jpeg",
    "size": 20480,
    "checksum": "sha256:abc123"
  }
}
```

Your presign handler should validate:

- file metadata and business policy
- any app-specific authorization or quota rules
- whether the requested content type and size are allowed

Representative reply payload:

```json
{
  "uploadUrl": "https://object-storage.example/uploads/tenant-a/photo-123",
  "method": "PUT",
  "headers": {
    "content-type": "image/jpeg"
  },
  "objectKey": "tenant-a/uploads/photo-123",
  "expiresInSeconds": 900
}
```

Required reply guidance:

- return an upload URL and HTTP method
- return any HTTP headers the client must send during upload
- return a deterministic object identifier such as `objectKey` or `objectUrl`
- do not require a platform `uploadId`

The client must reuse the returned object identifier later. It must not invent a
different one during completion.

### Example request route

```yaml
app:
  id: file-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222

requests:
  subject: q.tenant-a.app.files.request
  queueGroup: file-service-responders
  workerPoolSize: 16
  routes:
    - name: file-presign
      match:
        type: com.workspace.files.presign.request
      dispatch:
        method: POST
        path: /requests/file-presign
        timeout: 3s
      reply:
        source: file-service
        type: com.workspace.files.presign.reply
```

## 4. Direct HTTP Upload Responsibilities

After the client receives the presign reply, the file bytes move directly to
object storage. This phase is outside the sidecar.

Client responsibilities:

- use the exact `uploadUrl`, `method`, and `headers` returned by the app
- treat non-2xx storage responses as upload failures
- publish `file.uploaded` only after the HTTP upload succeeds
- retry the HTTP upload only according to the storage provider's semantics

App responsibilities:

- return upload instructions that are valid long enough for the client to use
- make the object identifier stable enough to be referenced later
- avoid assuming that a presign reply guarantees a later successful upload

Sidecar responsibilities:

- none during the byte-transfer phase

## 5. File Uploaded Event Contract

After the HTTP upload succeeds, the client publishes a normal JetStream
CloudEvent. This tells the app that the client observed a successful upload and
that asynchronous processing may begin.

Representative completion CloudEvent:

```json
{
  "specversion": "1.0",
  "id": "evt-file-uploaded-1",
  "source": "workspace/files-client",
  "type": "com.workspace.files.uploaded",
  "datacontenttype": "application/json",
  "data": {
    "objectKey": "tenant-a/uploads/photo-123",
    "fileName": "photo.jpg",
    "contentType": "image/jpeg",
    "size": 20480,
    "checksum": "sha256:abc123",
    "uploadedAt": "2026-06-08T12:00:00Z"
  }
}
```

Important meaning:

- `file.uploaded` means the client saw the HTTP upload succeed
- it does **not** mean the platform independently verified object durability
- if stronger verification is needed later, design that separately

### Example JetStream route

```yaml
app:
  id: file-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://nats:4222
  stream: workspace-events
  durableConsumer: file-service-dispatcher
  filterSubject: t.tenant-a.app.files.event.uploaded
  workerPoolSize: 16
  fetchBatch: 16
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.file-service

routes:
  - name: file-uploaded
    match:
      type: com.workspace.files.uploaded
    dispatch:
      method: POST
      path: /events/file-uploaded
      timeout: 5s
    response:
      type: com.workspace.files.uploaded.processed
      source: file-service
      subject: t.tenant-a.app.files.event.uploaded.processed
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.file-service
```

## 6. Correlation And Idempotency

This workflow does not use a platform-generated `uploadId`. Correlation depends
on the object identifier returned in the presign reply.

Rules:

- the app returns `objectKey` or `objectUrl` in the presign reply
- the client echoes that exact value in `file.uploaded`
- the app uses that identifier plus its own business context to find and process
  the uploaded object

Recommended completion-handler behavior:

1. Parse the `file.uploaded` payload.
2. Reject the event if the object identifier is missing.
3. Look up or derive the expected upload target from app state.
4. If the event is a duplicate, return a successful no-op response.
5. If the identifier is unknown or inconsistent, reject it as a business error.

The asynchronous phase is at-least-once, so completion processing must be
idempotent.

## 7. Failure Handling

Split failures by phase:

| Phase | Behavior |
|---|---|
| `presign.request` | synchronous reply with app-chosen `2xx`, `4xx`, or `5xx` status |
| direct HTTP upload | client handles retry or abort using storage-provider semantics |
| `file.uploaded` publish | client retries publishing if the upload succeeded but the event publish failed |
| `file.uploaded` processing | JetStream route uses adapter retry and DLQ behavior |

Additional guidance:

- do not publish `file.uploaded` before the HTTP upload succeeds
- do not assume a presign request always leads to an upload
- treat duplicate completion events as normal

## 8. Testing Checklist

Before enabling the upload flow in a shared environment, verify:

- the presign handler returns a valid URL, method, headers, and object identifier
- the direct HTTP upload succeeds with exactly those returned instructions
- the client publishes `file.uploaded` only after upload success
- the app accepts a valid `file.uploaded` event and performs post-upload work
- duplicate `file.uploaded` events do not create duplicate business effects
- missing or unknown object identifiers are rejected cleanly
- asynchronous handler failures exercise the expected retry and DLQ behavior

Recommended local validation sequence:

1. Call the app's local presign endpoint directly with `curl`.
2. Perform a test upload against a development object-storage target.
3. Publish a `com.workspace.files.uploaded` fixture through NATS.
4. Inspect app logs and sidecar logs to confirm post-upload processing.

## 9. Common Mistakes

- Trying to send file bytes through the sidecar instead of direct HTTP upload
- Publishing `file.uploaded` before the storage upload actually succeeds
- Omitting the object identifier from the presign reply
- Letting the client invent a different object identifier during completion
- Treating `file.uploaded` as exactly-once
- Designing the guide around one storage provider's special fields
