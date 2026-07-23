# Event Adapter Direct Request Dispatch Design

**Date:** 2026-07-23

**Issue:** [#36](https://github.com/YuShuanHsieh/workspace/issues/36)

**Status:** Approved for implementation planning

## Summary

Add an opt-in fallback to the event adapter's synchronous NATS request-reply
path. When no exact request route matches, the publisher may select the local
HTTP method and provide a fully resolved relative request path in the
CloudEvent. This removes the need for one route entry per backend operation
while preserving existing routes, replies, worker limits, and the loopback-only
trust boundary.

JetStream does not gain publisher-directed dispatch. Its static routes gain
`DELETE` support, while response, retry, acknowledgement, and DLQ behavior stay
unchanged.

## Goals

- Let a synchronous publisher provide a resolved backend path such as
  `/orders/ord-456` and a supported method such as `POST`.
- Avoid per-CloudEvent-type route entries for services with many synchronous
  operations.
- Preserve exact configured routes as controlled overrides.
- Preserve the existing request-reply response, correlation, header, cookie,
  timeout, and concurrency behavior.
- Prevent publishers from selecting another host or reaching paths outside an
  optional configured allowlist.
- Support `DELETE` consistently in direct dispatch and existing static request
  and event routes.

## Non-goals

- Direct dispatch for JetStream event routes.
- Publisher-selected schemes, hosts, ports, or Kubernetes services.
- Path-template or path-parameter substitution by the adapter.
- Removing static request routes.
- Supporting HTTP methods beyond `GET`, `POST`, `PUT`, `PATCH`, and `DELETE`.
- Changing the JetStream response, retry, acknowledgement, or DLQ contracts.

## Configuration

Direct dispatch is disabled unless explicitly configured:

```yaml
app:
  id: order-service
  httpBaseURL: http://127.0.0.1:8080

nats:
  url: nats://127.0.0.1:4222

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

`requests.routes` becomes optional when direct dispatch is enabled. Existing
configurations remain valid and retain their current behavior.

The direct-dispatch fields have these semantics:

- `enabled` must be `true` to activate the fallback.
- `timeout` is required and positive. It applies to every direct dispatch.
- `allowedPathPrefixes` is optional. When non-empty, the validated request path
  must fall under at least one prefix. When empty, any otherwise valid local
  path is accepted.

Each configured prefix must be an absolute, normalized path. Prefix matching
uses path-segment boundaries: `/orders` matches `/orders` and `/orders/...`,
but not `/orders-admin`. A trailing slash has the same boundary semantics.

Static routes continue to define their own dispatch timeout, reply metadata,
headers, and header forwarding rules. Direct dispatch uses the existing default
publisher-header behavior: forward all non-reserved `dispatchheaders`.

## CloudEvent Contract

A direct request supplies two top-level control fields:

```json
{
  "specversion": "1.0",
  "id": "req-123",
  "source": "checkout-service",
  "type": "com.workspace.orders.create.request",
  "dispatchmethod": "POST",
  "dispatchpath": "/orders/ord-456",
  "data": {
    "items": []
  }
}
```

- `dispatchmethod` is case-insensitively parsed and normalized to uppercase.
- `dispatchpath` is already resolved by the publisher. The adapter performs no
  template substitution.
- A query string may be included, for example
  `/orders/ord-456?include=lines`. Prefix checks apply only to the path.
- Both fields are adapter control metadata. They are removed before CloudEvent
  SDK parsing and are not forwarded as `ce-dispatchmethod` or
  `ce-dispatchpath` headers.
- Configured static routes ignore these fields and use only their configured
  method and path.

## Routing and Dispatch Flow

For every valid synchronous request:

1. Parse the CloudEvent and extract direct-dispatch control metadata.
2. Look up an exact static request route by CloudEvent `type`.
3. If a static route exists, dispatch with that route exactly as today.
4. Otherwise, if direct dispatch is disabled, return the existing `404` error
   reply.
5. Otherwise validate `dispatchmethod` and `dispatchpath`.
6. Construct an in-memory dispatch configuration using the validated method,
   validated path, and shared direct-dispatch timeout.
7. Dispatch through the existing HTTP dispatcher and return the app's response
   on the NATS reply inbox.

Exact route precedence lets operators retain controlled behavior for sensitive
or exceptional operations. Publisher metadata can never override an exact
route.

The dispatcher continues to join the path only to the validated
`app.httpBaseURL`. Direct dispatch cannot alter the base URL.

## Path and Method Safety

Before any HTTP request is created, direct-dispatch validation must:

- Allow only `GET`, `POST`, `PUT`, `PATCH`, and `DELETE`.
- Require an absolute-path reference beginning with exactly one `/`.
- Reject full URLs, scheme-relative paths beginning with `//`, authorities,
  fragments, malformed escaping, control characters, and backslashes.
- Decode escaped path content for validation and reject `.` or `..` path
  segments, including encoded forms.
- Reject encoded path separators that could change routing after another decode.
- Normalize the path used for prefix comparison and enforce a path-segment
  boundary.
- Preserve a separately parsed valid query string when building the outbound
  request.

Invalid metadata is a permanent request error. It returns a structured `400`
reply and makes no backend call.

The design deliberately keeps `app.httpBaseURL` loopback-only. It does not turn
the adapter into a general HTTP proxy.

## Replies and Errors

The NATS reply inbox, not a per-operation CloudEvent reply type, identifies a
synchronous response. Direct dispatch therefore uses shared reply metadata:

- `source`: `app.id`
- `type`: `io.eventadapter.direct.reply`
- no subject

The reply preserves the current response contract:

- deterministic reply ID
- `httpstatus`
- `causationid` set to the request ID
- incoming `correlationid`, when present
- `httplocation` for redirects
- backend response content type and body

Existing static routes keep their configured `reply` values.

Error mapping remains consistent with the responder:

| Condition | Reply status |
|---|---:|
| CloudEvent parse failure | 400 |
| Missing or invalid direct-dispatch metadata | 400 |
| No exact route and direct dispatch disabled | 404 |
| Backend connection or transport failure | 502 |
| Direct dispatch exceeds its timeout | 504 |
| Backend returns an HTTP response | Backend status |
| Internal reply construction failure | 500 |

There is no retry or DLQ in the synchronous path.

## Internal Boundaries

- `internal/cloudevent` extracts and stores `dispatchmethod` and `dispatchpath`
  alongside the existing dispatch control metadata.
- `internal/config` adds and validates `requests.directDispatch`, and permits
  requests with no static routes only when direct dispatch is enabled.
- A focused request-target validator validates and canonicalizes publisher
  methods and paths without depending on the responder.
- `internal/router` retains exact type matching. It does not implement wildcard
  matching.
- `internal/responder` owns precedence: exact route, direct fallback, or `404`.
- `internal/dispatcher` remains the single HTTP execution path for static and
  direct dispatch.
- `internal/cloudevent` builds the generic direct reply using the existing reply
  envelope behavior.

These boundaries keep publisher-target validation independently testable and
avoid adding direct-dispatch policy to the generic HTTP client.

## Observability

Direct dispatch uses a bounded route label such as `direct`, never the
publisher-provided path or CloudEvent type. This avoids high-cardinality metric
dimensions.

Existing request counters and latency histograms apply with the `direct` label.
Invalid metadata increments the invalid-request metric with a bounded reason
such as `invalid_dispatch_target`. Logs may include the method and validated
path, but must not include request bodies, credentials, cookies, or
authorization values.

## Testing

Unit coverage must include:

- parsing and removal of both new CloudEvent control fields
- config parsing and validation, including direct-only request configurations
- exact-route precedence over publisher metadata
- fallback when no exact route matches
- disabled fallback returning `404`
- missing metadata and unsupported methods returning `400`
- `DELETE` dispatch through direct requests and static request/event routes
- full URLs, `//` paths, fragments, traversal, encoded traversal, encoded
  separators, malformed escaping, and prefix escapes being rejected
- path-prefix boundary behavior
- query-string preservation
- timeout, transport failure, backend status, redirect, and `GET` body behavior
- generic reply metadata and correlation/causation preservation
- bounded observability labels
- no behavior changes in JetStream processing or existing static routes except
  accepting `DELETE` as a configured dispatch method

An e2e test must send a synchronous request for a type absent from
`requests.routes`, dispatch it to a resolved local path, and verify the reply
status and data.

## Rollout and Compatibility

The feature is opt-in and additive. Deployments without
`requests.directDispatch.enabled: true` keep their existing behavior; they may
add `DELETE` routes explicitly. Services can migrate incrementally: keep
exceptional static routes and remove repetitive routes after their publishers
send valid direct-dispatch metadata.

Documentation must emphasize that enabling direct dispatch delegates local
endpoint selection to authorized NATS publishers. Operators should configure
`allowedPathPrefixes` whenever the colocated application exposes internal or
administrative endpoints.
