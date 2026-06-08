# event-adapter: Dynamic Path Parameters from CloudEvent Data — Design

GitHub issue: #18 — *feat(event-adapter): support dynamic path parameters from CloudEvent data*

## Problem

`DispatchConfig.Path` is a static string (e.g. `/events/task-created`). Real-world HTTP APIs often require dynamic path segments — for example `PUT /api/tasks/{taskId}/complete` where `taskId` comes from the CloudEvent data payload. Today there is no way to express this, forcing consumers either to publish to a single generic endpoint and re-route internally, or to maintain one route per concrete resource.

## Solution

Support `{fieldName}` template tokens in `dispatch.path`. Each token is resolved against the top-level fields of the CloudEvent `data` JSON object at dispatch time and substituted with the URL-path-escaped value.

### Template syntax

```yaml
dispatch:
  method: PUT
  path: /api/tasks/{taskId}/complete
  timeout: 2s
```

`{taskId}` is replaced with the value of `data.taskId` from the incoming CloudEvent, after running it through `url.PathEscape`.

### Token regex

`{[a-zA-Z][a-zA-Z0-9_]*}` — a token name must start with a letter, then any letters, digits, or underscores. This is validated at config-load time.

### CloudEvent example

```json
{
  "specversion": "1.0",
  "id": "evt-001",
  "type": "com.workspace.task.updated",
  "source": "workspace/task",
  "subject": "task-42",
  "datacontenttype": "application/json",
  "data": {
    "taskId": "task-42",
    "status": "complete",
    "assigneeId": "user-7"
  }
}
```

With `path: /api/tasks/{taskId}/complete`, the dispatcher resolves the URL to:

```
PUT http://<baseURL>/api/tasks/task-42/complete
```

Multiple tokens in the same path are supported:

```yaml
path: /api/tenants/{tenantId}/tasks/{taskId}
```

resolves to `/api/tenants/acme/tasks/task-42` given:

```json
{
  "data": {
    "tenantId": "acme",
    "taskId": "task-42"
  }
}
```

The same token may appear more than once in the path; both occurrences resolve to the same value.

## Value type handling

The CloudEvent `data` field is JSON, so field values may be any JSON type. The dispatcher accepts scalar types and coerces them to string before URL-escaping:

| JSON value type | Behaviour |
|---|---|
| `string` (non-empty) | Used verbatim |
| `string` (`""`) | Permanent failure → DLQ |
| `number` (e.g. `42`, `3.14`) | Coerced to canonical string form (`"42"`, `"3.14"`) |
| `bool` (`true` / `false`) | Coerced to `"true"` / `"false"` |
| `null` | Permanent failure → DLQ |
| object / array | Permanent failure → DLQ |
| field absent | Permanent failure → DLQ |

Rationale: real-world publisher JSON often carries numeric IDs unquoted. Forcing publishers to always quote scalars would create friction. Objects and arrays are not meaningful URL path segments, so rejecting them protects against malformed URLs.

After coercion, the resulting string is checked for emptiness — if it is `""`, the resolution fails permanently. This catches publisher bugs where a field is technically present but carries no meaningful value.

## New package: `internal/pathtemplate`

```go
package pathtemplate

import (
    "errors"
)

// ErrPermanent wraps payload-related failures that cannot succeed on retry
// because the event data does not change between attempts.
var ErrPermanent = errors.New("pathtemplate: permanent failure")

// Validate parses a path string at config-load time and rejects unknown
// token syntax (e.g. {123bad}, unclosed {x). It does not require any event
// data — it checks only the path itself.
func Validate(path string) error

// Resolve substitutes {field} tokens in path against the top-level fields
// of ev.Data() (parsed as a JSON object). Returns the resolved path on
// success, or an error wrapping ErrPermanent if any token cannot be resolved
// from the data payload.
func Resolve(path string, ev *cloudevent.Event) (string, error)
```

Static paths (no `{` characters) are detected by `Resolve` as a fast path — no JSON parsing performed, original path returned unchanged. This keeps the cost of templating zero for routes that don't use it.

## Changes to existing files

| File | Change |
|---|---|
| `internal/pathtemplate/pathtemplate.go` | New package: `Validate` and `Resolve`, `ErrPermanent` sentinel |
| `internal/pathtemplate/pathtemplate_test.go` | Unit tests for both functions across all value-type and error cases |
| `internal/config/validate.go` | Call `pathtemplate.Validate(route.Dispatch.Path)` for every route (JetStream + request-reply) |
| `internal/config/validate_test.go` | Tests asserting bad templates fail config-load with a route-scoped `ValidationError` |
| `internal/dispatcher/dispatcher.go` | Call `pathtemplate.Resolve(dc.Path, ev)` before `url.JoinPath` |
| `internal/dispatcher/dispatcher_test.go` | Tests asserting the resolved URL is what gets dispatched |
| `internal/processor/processor.go` | When `dispatchErr` wraps `pathtemplate.ErrPermanent`, skip retries and go straight to DLQ |
| `internal/processor/processor_test.go` | Test asserting permanent path errors bypass retry |
| `internal/responder/responder.go` | When `dispatchErr` wraps `pathtemplate.ErrPermanent`, return a 400 error reply (no retry possible on synchronous calls anyway) |
| `internal/responder/responder_test.go` | Test asserting permanent path errors yield a 400 reply, not a 502 |

## Error handling

Path resolution errors are **permanent** — the event data does not change between attempts, so retrying is pointless.

| Scenario | Where caught | Behaviour |
|---|---|---|
| Invalid token syntax in config (e.g. `{123}`, unclosed `{x`) | `Validate` at config-load | Service won't start; clear `ValidationError` pointing at the offending route |
| `data` is not a JSON object (e.g. an array, a string, a number) | `Resolve` at dispatch | Wrapped `ErrPermanent` → processor sends straight to DLQ |
| Referenced field absent from data | `Resolve` at dispatch | Wrapped `ErrPermanent` → DLQ |
| Field present but value is `""`, `null`, an object, or an array | `Resolve` at dispatch | Wrapped `ErrPermanent` → DLQ |
| Field present, value is a string / number / bool, but URL-escape produces empty | (cannot happen after the empty-string check) | — |
| No tokens in path (static) | `Resolve` fast path | Original path returned, no parsing |
| Network / transient HTTP error after resolution | Dispatcher → processor as today | Retried as before, then DLQ |

The processor checks for the sentinel before deciding to requeue:

```go
res, dispatchErr := p.dispatcher.Dispatch(ctx, route.Dispatch, ev)
if dispatchErr != nil {
    if errors.Is(dispatchErr, pathtemplate.ErrPermanent) {
        return p.toDLQ(ctx, route, ev, dispatchErr.Error(), 0, delivery, msg)
    }
    if isNetworkError(dispatchErr) && delivery < policy.MaxAttempts {
        return msg.Nak(ctx, policy.Delay(delivery))
    }
    return p.toDLQ(ctx, route, ev, dispatchErr.Error(), 0, delivery, msg)
}
```

The responder handles it symmetrically by returning a 400 error reply instead of the usual 502 (no retry mechanism exists for the request-reply path; the caller learns the failure synchronously).

## Behavior decisions

- **Coerce scalars (string, number, bool) to string.** Real-world JSON publishers often carry numeric IDs. Forcing them to quote-stringify would be friction. Non-scalar types (object, array, null) are not coercible to a meaningful URL segment, so they are rejected.
- **Reject empty strings as permanent failures.** An empty path segment produces malformed URLs (`/api/tasks//complete`) that virtually no API expects. This catches publisher bugs early instead of forwarding garbage to the app.
- **`url.PathEscape` everywhere in the path string.** Per the issue. Query-string-aware escaping is out of scope; query templating is allowed but uses the same `PathEscape` (functionally correct for alphanumeric IDs, slightly over-escapes some characters in edge cases; not a v1 concern).
- **Same token may appear multiple times.** No special handling required — both occurrences are independently substituted with the same resolved value.
- **Static paths are zero-cost.** `Resolve` returns the original path string without parsing JSON if no `{` is present, so routes that don't use templating pay nothing.
- **Tokens resolve only against top-level `data` fields.** No `ce.*` or `ext.*` namespace, no nested `{user.id}` syntax. Future extension if needed; YAGNI for v1.

## Wire format examples

**Static path (today, unchanged):**

```yaml
dispatch:
  path: /events/task-created
```

```
POST http://app/events/task-created
```

Event data is sent in the request body. No URL change.

**Single token:**

```yaml
dispatch:
  path: /api/tasks/{taskId}/complete
```

Event `data.taskId = "task-42"` →

```
PUT http://app/api/tasks/task-42/complete
```

**Multiple tokens, numeric value:**

```yaml
dispatch:
  path: /api/tenants/{tenantId}/users/{userId}
```

Event `data.tenantId = "acme"`, `data.userId = 7` →

```
GET http://app/api/tenants/acme/users/7
```

**Permanent failure (missing field):**

Path: `/api/tasks/{taskId}/complete`

Event `data = { "status": "done" }` (no `taskId`) →

Dispatcher returns `fmt.Errorf("%w: field %q not found in event data", ErrPermanent, "taskId")`. Processor publishes a DLQ event for inspection, acks the original. No HTTP call made.

## Testing

### Unit tests (required, TDD)

- `internal/pathtemplate/pathtemplate_test.go`:
  - `Validate`: accepts valid paths (`/x/{y}/z`, `/{a}/{b}`, `/static`); rejects bad tokens (`{123}`, `{}`, `{a-b}`, unclosed `{x`).
  - `Resolve` happy path: single token, multiple tokens, same token twice, static path (no-token fast path), string value, number value (coerced), bool value (coerced).
  - `Resolve` permanent failures (each wraps `ErrPermanent`): missing field, `null` value, empty string, object value, array value, `data` not a JSON object.
  - `errors.Is(err, ErrPermanent)` returns true for every permanent failure case.
- `internal/config/validate_test.go`:
  - Bad template in `Dispatch.Path` fails config-load with a `ValidationError` whose `Path` points at `routes[i].dispatch.path` (and same for request-reply routes).
- `internal/dispatcher/dispatcher_test.go`:
  - Tokens resolve in the actual outbound URL (assert via `roundTripFunc`).
  - Static path passes through unchanged.
- `internal/processor/processor_test.go`:
  - Permanent path error → message goes to DLQ on the first attempt, no Nak.
- `internal/responder/responder_test.go`:
  - Permanent path error → reply CloudEvent with `httpstatus: 400`, not 502.

### End-to-end test

Extend `test/e2e/`:
- New mock-app handler at `/api/tasks/{path-segment}/complete` (matched via Go's `http.ServeMux` wildcard).
- New JetStream route with `path: /api/tasks/{taskId}/complete`.
- New fixture event with `data.taskId = "e2e-task-1"`.
- Test asserts the mock-app received the request at `/api/tasks/e2e-task-1/complete` (echo the resolved path back in the response body so the test can assert on it).

## Documentation to update

- `prd/event-adapter/prd.md` — document path templating in the route configuration section (around the dispatch.path explanation).
- `prd/event-adapter/app-developer-guide.md` — add a short example of writing a handler whose path segment is filled from event data, alongside the existing routing examples.
- `event-adapter/examples/onboarding/README.md` — show a one-line example of `{taskId}` syntax (optional; aligns with how dispatchcookies was documented).

## Out of scope

- Nested field access (`{user.id}`). Top-level only for v1.
- CloudEvent envelope-level access (`{ce.id}`, `{ext.tenantId}`). Data-only.
- Query-string-specific escaping rules. Templates work in query strings but use `url.PathEscape` (good enough for alphanumeric values).
- Default values when a field is missing (`{taskId|default}`). YAGNI.
- Conditional segments (`/foo{?taskId}/bar`). YAGNI.
- Tokens in HTTP headers, body, or response config. v1 covers `dispatch.path` only.

## Compatibility

- **Existing static paths are unchanged.** `Resolve` short-circuits on paths with no `{` character.
- **No public API change to `dispatcher.Dispatch`.** The path resolution happens inside the dispatcher; callers see the same `(Result, error)` shape.
- **New error sentinel.** `pathtemplate.ErrPermanent` is wrapped into the existing `error` return; callers that don't import `pathtemplate` continue to work and treat the error as opaque (which is fine — only the processor and responder need to discriminate).
- **Config-load may newly reject previously-valid configs** if anyone happened to use literal `{...}` in a path before. This is extremely unlikely (curly braces are not valid in URLs without escaping); flagging as a theoretical risk.
