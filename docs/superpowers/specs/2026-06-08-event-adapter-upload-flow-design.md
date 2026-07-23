# event-adapter: file upload flow integration guide

**Date:** 2026-06-08
**Status:** Design — pending documentation update
**Component:** `event-adapter`

## Summary

Document a storage-provider-neutral file upload pattern that uses the existing
`event-adapter` delivery models without introducing a new transport path.

The flow is intentionally split across two protocols:

- **Request-reply** for `presign.request`, where the client needs an immediate
  answer before uploading bytes.
- **Direct HTTP** from the client to object storage using the returned upload
  instructions.
- **JetStream event consumption** for `file.uploaded`, where the app processes
  completion asynchronously and durably.

The sidecar continues to move only CloudEvent payloads and local HTTP requests.
It does not proxy file bytes and does not verify object storage writes.

## Motivation

File upload is a special case for app teams because the full workflow is not
purely event-based:

1. The client needs synchronous upload instructions.
2. The file bytes must move over HTTP to object storage.
3. The app still needs a durable event after upload completes.

This is already compatible with the current adapter design, but the developer
experience is underspecified. The existing app developer guide mentions presign
requests as an example, yet it does not describe the complete upload lifecycle,
the handoff between protocols, or the contract that correlates the presign and
completion phases.

The new guide should make that pattern explicit so app teams can implement file
upload without inventing incompatible shapes.

## In scope

- A new upload-focused app developer guide describing the full upload lifecycle.
- Provider-neutral request/reply/event payload guidance for upload flows.
- Correlation rules between the presign reply and the later `file.uploaded`
  event.
- Failure-handling and idempotency guidance for app teams and client authors.

## Out of scope

- Any change to `event-adapter` runtime behavior or config schema.
- A storage-provider-specific contract such as S3 form uploads.
- Storage callback/webhook verification of completed uploads.
- Sidecar-mediated byte upload or sidecar validation of object existence.
- A platform-generated `uploadId`.

## Recommended architecture

Use the existing adapter delivery models exactly as they are today:

```text
client
  ├─ request-reply CloudEvent: com.workspace.files.presign.request
  │    └─ event-adapter → app local HTTP handler
  │         └─ reply CloudEvent: com.workspace.files.presign.reply
  │
  ├─ direct HTTP upload to object storage using presigned instructions
  │
  └─ JetStream CloudEvent: com.workspace.files.uploaded
       └─ event-adapter → app local HTTP event handler
```

### Responsibilities

- **App service**
  - validates upload policy and returns presigned upload instructions
  - chooses the storage object identifier returned to the client
  - processes the later upload-completion event asynchronously
- **Client**
  - requests upload instructions
  - performs the HTTP upload
  - publishes `file.uploaded` only after the HTTP upload succeeds
- **event-adapter**
  - bridges request/reply and event delivery to local HTTP handlers
  - does not transfer file bytes
- **Object storage**
  - accepts the HTTP upload through the provider-specific presigned URL

## Contract design

The guide should define a minimal, provider-neutral platform contract. The
payload examples may use representative field names, but they must be presented
as conventions for app teams to adopt, not as sidecar-enforced schema.

### Presign request

Suggested `presign.request` payload fields:

- `fileName`
- `contentType`
- `size`
- optional `checksum`
- optional app-specific business metadata

### Presign reply

Suggested `presign.reply` payload fields:

- `uploadUrl`
- `method`
- `headers` as an optional string map
- `objectKey` or `objectUrl`
- `expiresAt` or `expiresInSeconds`
- optional metadata that the client must preserve for completion

The reply must give the client a deterministic object identifier. The client
must not invent that identifier later.

### File uploaded event

Suggested `file.uploaded` payload fields:

- `objectKey` or `objectUrl` copied from the presign reply
- `fileName`
- `contentType`
- `size`
- optional uploaded `checksum`
- optional echoed metadata from the presign reply
- `uploadedAt`

The guide should state clearly that `file.uploaded` means the client observed a
successful HTTP upload, not that the platform independently verified storage
durability.

## Correlation rules

Because this design does not introduce a platform `uploadId`, correlation
between phases relies on values already returned by the app:

- The app returns `objectKey` or `objectUrl` in the presign reply.
- The client must echo that same value in the later `file.uploaded` event.
- The app uses that echoed identifier plus its own business context to find the
  uploaded object and process it idempotently.

The guide should recommend rejecting completion events that omit the object
identifier or provide a value that the app cannot map to an expected upload.

## Failure model

The guide should separate failures by phase:

| Phase | Behavior |
|---|---|
| `presign.request` | synchronous request-reply; failures are ordinary reply CloudEvents with `httpstatus` |
| direct HTTP upload | outside the sidecar; client handles retry/abort using provider HTTP semantics |
| `file.uploaded` publish | client responsibility; if publish fails after upload succeeds, retry publishing |
| `file.uploaded` processing | asynchronous JetStream delivery with existing retry/DLQ behavior |

This keeps the sidecar model simple and aligned with current behavior.

## Idempotency guidance

The new guide should tell app teams:

- presign requests may be retried by clients, so presign generation should be
  safe under retry when duplicate effects matter
- `file.uploaded` must be processed idempotently
- at-least-once delivery applies to the asynchronous completion phase

The minimum idempotency key for completion handling is the storage object
identifier plus any app-specific context required to distinguish legitimate
replays from conflicting uploads.

## Documentation changes

Add a new app developer guide dedicated to file upload integration under
`prd/event-adapter/`. It should complement the general app developer guide
rather than replace it.

Recommended sections:

1. Why upload is a split-protocol flow
2. End-to-end sequence
3. Presign request-reply contract
4. Direct HTTP upload responsibilities
5. `file.uploaded` event contract
6. Correlation and idempotency rules
7. Failure handling
8. Testing checklist

The general guide may then link to this upload-specific guide from the existing
request-reply section.

## Testing guidance

The upload guide should include a validation checklist covering:

- successful presign request-reply
- successful direct HTTP upload using the returned method, URL, and headers
- successful `file.uploaded` publication after upload completion
- duplicate `file.uploaded` handling
- rejection of missing or unknown object identifiers
- retry/DLQ behavior for asynchronous post-upload failures

## Open questions

No blocking questions. If the platform later needs stronger confirmation than a
client-declared completion event, that should be designed separately as a
storage-callback or object-verification flow.
