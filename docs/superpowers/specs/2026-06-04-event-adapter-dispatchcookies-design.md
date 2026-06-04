# event-adapter: `dispatchcookies` Support — Design

GitHub issue: #10 — *feat(event-adapter): add dispatchcookies support for per-event HTTP cookie forwarding*

## Problem

The HTTP app service that event-adapter dispatches to may rely on HTTP cookies
for session and permission validation. When a publisher originates from an HTTP
request, the user's cookies are lost once the request is turned into a
CloudEvent and sent through NATS. There is currently no way to carry those
cookies through to the outbound HTTP call the adapter makes to the app.

The existing `dispatchheaders` field already solves this for arbitrary headers,
but `Cookie` is a structured, multi-value header that deserves first-class
treatment. `Cookie` is also missing from the reserved-header list today, which
is a gap this feature closes.

## Solution (Approach A)

Add a new top-level `dispatchcookies` field to the CloudEvent envelope, mirroring
the existing `dispatchheaders` mechanism end to end: parse and strip it during
event decoding, attach the cookies to the outbound HTTP request, and reserve the
`Cookie` header so cookies can only travel through this one channel.

### Approaches considered

- **Approach A — dedicated `dispatchcookies` field (chosen).** First-class
  cookie support: parse, strip, `AddCookie`, and reserve the `Cookie` header.
  Consistent with the existing `dispatchheaders` pattern and closes the reserved
  header gap. Slightly more code than reusing headers, but predictable and clean.
- **Approach B — reuse `dispatchheaders` with a `Cookie` header.** Least code,
  but cookies are structured and multi-value, so publishers would hand-assemble
  `name=value; name2=value2` strings — error prone. It also leaves the reserved
  header gap open, which the issue explicitly wants closed. Rejected.
- **Approach C — `dispatchcookies` plus a per-route allowlist.** More control,
  but the issue marks a per-route `forwardCookies` allowlist as out of scope
  ("can be added later if needed"). Rejected as premature (YAGNI).

## Wire format

Publishers add a `dispatchcookies` object alongside `dispatchheaders`:

```json
{
  "specversion": "1.0",
  "id": "evt-123",
  "source": "orders-service",
  "type": "order.placed",
  "data": { "orderId": "abc" },
  "dispatchcookies": {
    "session": "abc123",
    "csrf-token": "xyz789"
  }
}
```

Values are `map[string]string` (cookie name → cookie value). Cookie attributes
(domain, path, httponly, secure, etc.) are not supported — those are
server-to-client semantics and irrelevant for outbound client requests.

## Code changes

Three files, all following the existing `dispatchheaders` pattern.

### 1. `internal/cloudevent/event.go` — parse and strip

- Add `DispatchCookies map[string]string` to the `Event` struct, parallel to
  `DispatchHeaders`.
- In `Parse()`, extract `dispatchcookies` from the raw JSON probe map via a new
  `parseDispatchCookies` helper, then `delete` it from the probe before handing
  the envelope to the CloudEvents SDK (identical to how `dispatchheaders` is
  handled today).
- Return the parsed cookies on the `Event`.
- Add `parseDispatchCookies`, a direct analogue of `parseDispatchHeaders`:
  returns `nil` when the field is absent (cookies are optional), and errors if
  the value is not a string-valued object.

### 2. `internal/dispatcher/dispatcher.go` — attach to the request

- Add `setPublisherCookies(req, ev)`, called in `Dispatch()` immediately after
  `setPublisherHeaders`:

  ```go
  func setPublisherCookies(req *http.Request, ev *clevent.Event) {
      for name, value := range ev.DispatchCookies {
          req.AddCookie(&http.Cookie{Name: name, Value: value})
      }
  }
  ```

  `req.AddCookie` handles proper encoding and merges correctly with any existing
  `Cookie` header. All declared cookies are forwarded — no per-route allowlist
  (mirrors the default `dispatchheaders` behavior when `forwardHeaders` is
  omitted).
- In `setCloudEventHeaders`, also skip `dispatchcookies` in the extensions loop
  (alongside the existing `dispatchheaders` skip) so it can never leak out as a
  `ce-dispatchcookies` header. This is defensive — `Parse()` already strips it.

### 3. `internal/config/validate.go` — reserve the `Cookie` header

- Add `"cookie": true` to `reservedHeaders`. This prevents a publisher from
  setting a cookie via `dispatchheaders`, making `dispatchcookies` the only
  supported path for cookie forwarding.

## Behavior decisions

- **Cookies are optional.** An event with no `dispatchcookies` is valid; nothing
  is forwarded. This matches `dispatchheaders` behavior.
- **Forwarded as-is.** No validation or transformation beyond what
  `http.AddCookie` applies. This is the behavior the issue specifies.
- **No per-route allowlist.** Out of scope per the issue.
- **No cookie attributes.** Out of scope per the issue.

## Trust model

This feature does not protect against a malicious publisher: anyone able to
publish events to NATS can set `dispatchcookies`, and those cookies will be
forwarded. That is by design. The trust boundary is "who can publish to the
stream," which is enforced elsewhere (NATS authentication). The adapter forwards;
it does not authorize. Reserving the `Cookie` header is about funnelling cookies
through a single, predictable channel — not about defending against an untrusted
publisher.

## Testing

### Unit tests (required, TDD)

- `internal/cloudevent`: `dispatchcookies` is parsed into `DispatchCookies` and
  stripped from the envelope; absent field yields `nil`; non-string-valued object
  errors.
- `internal/dispatcher`: declared cookies appear on the outbound request's
  `Cookie` header via `req.Cookies()`; absent cookies add nothing.
- `internal/config`: `Cookie` is reported as a reserved header.

### End-to-end test (Approach 2 — full coverage)

Extend the existing e2e harness to prove cookies survive the full round-trip:

- `cmd/mock-app/main.go`: add a `requireCookies []string` field to the handler
  config and enforce it (analogous to the existing `requireHeaders`, using
  `r.Cookie(name)`). A missing cookie returns 400, which fails the dispatch and
  therefore the test.
- `test/e2e/mock-app.yaml`: declare `requireCookies` on the task-created handler.
- `test/e2e/fixtures/task-created.json`: add a `dispatchcookies` block.
- `test/e2e/e2e_test.go`: the existing round-trip assertion covers it — a missing
  cookie yields no response and the test times out; an explicit assertion may be
  added for clarity.

Note: `requireCookies`/`requireHeaders` are test-harness constructs to prove
forwarding, not adapter behavior. The adapter never requires cookies or headers.

## Documentation to update

- `prd/event-adapter/app-developer-guide.md` — document `dispatchcookies` next to
  `dispatchheaders`; note that `Cookie` is now reserved.
- `prd/event-adapter/prd.md` — add a line referencing cookie forwarding.
- `event-adapter/examples/onboarding/README.md` — show a short cookie example.

## Out of scope (explicitly excluded by the issue)

- Per-route `forwardCookies` allowlist.
- Cookie attributes (path, domain, secure, httponly).
- Any encoding transform beyond what `http.AddCookie` applies.
