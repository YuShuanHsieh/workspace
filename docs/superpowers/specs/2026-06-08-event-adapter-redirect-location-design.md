# event-adapter: Expose Redirect Location in Response and Reply Events — Design

GitHub issue: #11 — *fix(event-adapter): expose redirect location in response event; stop following redirects*

## Problem

The dispatcher uses `http.DefaultClient`, which follows HTTP redirects transparently (up to 10 hops). Two consequences:

1. **Any 3xx response is silently followed** — the resulting response event reflects the final destination, not the redirect itself. The event consumer has no way to know a redirect occurred.
2. **The `Location` header is never captured** — `dispatcher.Result` reads only `StatusCode`, `Content-Type`, and `Body`. Even if a redirect were returned, the `Location` header is dropped before it can reach the response event.

The loopback constraint on `httpBaseURL` (must be a loopback address) means transparent redirect-following provides no value: every redirect target is still on the same loopback host as the original. Following the redirect only hides information from the consumer.

## Scope expansion beyond the issue text

Issue #11 predates PR #19, which introduced the request-reply responder (`internal/responder/`) as the primary delivery model. The same bug exists symmetrically in that path: `responder.go` calls the same `Dispatcher` and uses `BuildReply` to construct replies, and `BuildReply` does not expose `Location` either. The fix is symmetric and small, so both code paths are addressed in this PR. Manager confirmed the bundled scope.

## Solution (Approach A — symmetric capture in both delivery models)

Stop the default HTTP client from following redirects, capture `Location` in `dispatcher.Result` for 3xx responses only, and thread that value through both response builders (`BuildResponse` and `BuildReply`) so consumers of either model receive an `httplocation` CloudEvent extension when the app returns a redirect.

### Approaches considered

- **Approach A — symmetric capture (chosen).** Add `Location` to `dispatcher.Result` once; both processors thread it to their respective builders; both builders emit the same `httplocation` extension. Consumers of either model get the same redirect information. Matches the spirit of the issue and the post-#19 architecture.
- **Approach B — JetStream path only, file follow-up for request-reply.** Strictly follows the literal issue text. Smaller PR, but leaves request-reply consumers with the same bug. Considered and rejected after manager input — symmetric fix is small enough not to warrant splitting.
- **Approach C — always capture `Location` regardless of status.** Simpler dispatcher code (no 3xx check), but `httplocation` would appear on 201 Created or 200 OK responses too. Consumers would have to check both status and location, weakening the semantic guarantee. Rejected; the issue's intent ("populated on 3xx responses") is clearer to honor.

## Wire format

A response event for a 307 redirect (JetStream model):

```json
{
  "specversion": "1.0",
  "id": "evt_<sha256>",
  "type": "<route.response.type>",
  "source": "<route.response.source>",
  "subject": "<route.response.subject>",
  "httpstatus": 307,
  "httplocation": "/new-path",
  "causationid": "<original-event-id>",
  "data": ""
}
```

A reply event for the same redirect (request-reply model) carries the same `httplocation` extension and travels on the requester's reply inbox; it has no `subject`.

When the app returns a non-3xx response, no `httplocation` extension is set. When the app returns a 3xx with no `Location` header (e.g. 304 Not Modified, or a malformed app), `httplocation` is also absent — consumers see only `httpstatus`.

## Code changes

Six files plus tests and docs.

### 1. `internal/dispatcher/dispatcher.go` — stop following + capture

- Add `Location string` to the `Result` struct.
- In `New()`, when the caller passes a `nil` client, build a custom `http.Client` with `CheckRedirect` returning `http.ErrUseLastResponse`:

  ```go
  client = &http.Client{
      CheckRedirect: func(*http.Request, []*http.Request) error {
          return http.ErrUseLastResponse
      },
  }
  ```

  `http.ErrUseLastResponse` is a sentinel: the client stops at the redirect response and returns it to the caller with `(resp, nil)`. The dispatcher's existing error path is unaffected. When the caller provides their own client, it is used as-is (their responsibility).
- In `Dispatch()`, populate `Result.Location` only for 3xx responses:

  ```go
  loc := ""
  if resp.StatusCode >= 300 && resp.StatusCode < 400 {
      loc = resp.Header.Get("Location")
  }
  return Result{
      StatusCode:  resp.StatusCode,
      ContentType: resp.Header.Get("Content-Type"),
      Body:        respBody,
      Location:    loc,
  }, nil
  ```

  Explicit status check protects against the (uncommon but possible) case where an app returns a `Location` header on a non-3xx response — we do not want that surfacing as an `httplocation` extension.

### 2. `internal/cloudevent/response.go` — pass location through both builders

- `BuildResponse` gains a `location string` parameter:

  ```go
  func BuildResponse(in *Event, route config.RouteConfig, status int, contentType string, body []byte, location string) (*ce.Event, error)
  ```

  When `location != ""`:

  ```go
  out.SetExtension("httplocation", location)
  ```

- `BuildReply` gains the same parameter and the same conditional `SetExtension` call, in the same position relative to the other extensions (`httpstatus`, `causationid`, `correlationid`).
- `BuildErrorReply` is unchanged. Errors generated by the sidecar itself (parse failures, no matching route) have no app response and therefore no `Location` to forward.

### 3. `internal/processor/processor.go` — thread to BuildResponse

One line: pass `res.Location` as the new `location` argument to `clevent.BuildResponse`. No other processor logic changes. The PRD already specifies that 3xx is treated the same as 2xx for retry purposes (prd.md:211), so the existing success path applies.

### 4. `internal/responder/responder.go` — thread to BuildReply

Two call sites in `handle()` build replies via `clevent.BuildReply`:

- The success path (`res.StatusCode`, `res.ContentType`, `res.Body`) — pass `res.Location`.
- The dispatch-error path (synthesised `application/json` error body) — pass `""` (no app response, no Location).

### 5. `cmd/mock-app/main.go` — support 3xx test responses

Add a `respondWith` block to the handler config:

```yaml
handlers:
  - method: POST
    path: /events/redirect-me
    respondWith:
      status: 307
      location: /events/redirect-target
```

When present and `status` is in the 3xx range, the handler writes the `Location` header and the configured status, with no body. Keeps the existing `response:` block unchanged for non-3xx handlers. Implementation is minor (one new code path in the handler, ~15 lines).

### 6. `test/e2e/{e2e_test.go, mock-app.yaml, fixtures/...}` — round-trip proof

Add an e2e case for the JetStream model and one for the request-reply model:

- New mock-app handler configured with `respondWith.status: 307` and a synthetic Location.
- New fixture event targeting that handler.
- New e2e assertion: the published response event (or reply CloudEvent on the inbox) carries `httpstatus: 307` and `httplocation: /events/redirect-target`, and the mock-app never serves the redirect target (because the dispatcher does not follow).

## Behavior decisions

- **3xx-only Location capture.** Explicit status check in the dispatcher protects the `httplocation` semantic guarantee.
- **3xx treated as success in both paths.** Consistent with prd.md:211 ("3xx responses are treated the same as 2xx"). No retry/DLQ on 3xx in the JetStream path; no error path triggered in the request-reply path.
- **No validation or normalization of `Location`.** The adapter forwards the header value as the app returned it. It may be absolute, relative, or malformed. Consumer is responsible for safely handling it. This matches the adapter's broader stance of faithful pass-through (headers, body, status code).
- **Body still captured on 3xx.** Some apps return a small body alongside a redirect (e.g. an HTML stub). The existing `setHTTPData` path handles empty and non-empty body uniformly; no change.
- **Default client gets `CheckRedirect`; caller-provided clients are respected.** Production wiring (`cmd/event-adapter/main.go:73`) passes `nil`, so production gets the redirect-stopping behavior by default. Tests that pass their own `http.Client` (via `roundTripFunc`) are unaffected; they never traverse the default-client path.

## Testing

### Unit tests (required, TDD)

- `internal/dispatcher/dispatcher_test.go`:
  - **307 with Location populates `Result.Location` and is not followed.** Use an `httptest.NewServer` that returns 307 with a `Location` pointing to a second URL whose handler increments a counter — assert the counter stays at zero and `Result.Location` equals the header value.
  - **3xx without `Location` leaves `Result.Location` empty** (e.g. 304 Not Modified).
  - **Non-3xx with a `Location` header leaves `Result.Location` empty** (e.g. 200 OK with a stray `Location` header — defensive coverage of the explicit status check).
  - **Default client (`nil` passed to `New`) does not follow redirects** — assert by counting hops on the test server.
- `internal/cloudevent/response_test.go`:
  - **`BuildResponse` sets `httplocation` extension when `location != ""`.**
  - **`BuildResponse` omits `httplocation` when `location == ""`** (regression guard for non-3xx responses).
  - **`BuildReply` same two cases.**
  - Existing tests updated to pass `""` as the new sixth argument; no assertion changes (because no `Location` was being asserted anyway).
- `internal/processor/processor_test.go`:
  - **3xx response with Location publishes a response event carrying `httplocation`** — verifies the wiring from `res.Location` through `BuildResponse`.
- `internal/responder/responder_test.go`:
  - **3xx response with Location replies with a CloudEvent carrying `httplocation`** — verifies the wiring from `res.Location` through `BuildReply`.

### End-to-end tests

- **JetStream path:** add a route + handler pair where the mock-app returns 307. Assert the response event published on the response subject carries `httpstatus: 307` and `httplocation: <expected>`, and the redirect target endpoint on the mock-app was never invoked.
- **Request-reply path:** add a request route + handler pair where the mock-app returns 307. Assert the reply CloudEvent on the requester's inbox carries `httpstatus: 307` and `httplocation: <expected>`.

## Documentation to update

- `prd/event-adapter/prd.md`:
  - Section 8 ("Response event format", around line 244–254): add `httplocation` extension entry next to the `httpstatus` entry, with a one-line explanation that it is populated only on 3xx responses.
  - Section 17 (request-reply): mirror the same line in the reply-event format section.
- `prd/event-adapter/app-developer-guide.md`:
  - Around line 132, where `httpstatus` consumer guidance lives, add a short paragraph: "If your handler returns a 3xx with a `Location` header, the sidecar publishes the response event with `httpstatus` set and an `httplocation` extension carrying the header value. The sidecar does not follow the redirect."

## Out of scope

- Validation, sanitization, or normalization of the `Location` value.
- Capturing the `Location` header on non-3xx responses (e.g. 201 Created).
- Supporting `Location` on `BuildErrorReply` (no app response exists at that point).
- A configuration flag to opt out of stopping redirects. The loopback constraint makes following redirects unhelpful by definition; a flag would only invite confusion.
- Forwarding any other 3xx-related response headers (e.g. `Retry-After`). Out of scope and not requested.

## Compatibility

- **CloudEvent extension addition** is backward-compatible: consumers that don't read `httplocation` are unaffected. Existing consumers of `httpstatus` see no change for non-3xx responses.
- **`BuildResponse` and `BuildReply` signatures change** (extra `location string` parameter). All callers are inside the adapter; the change is mechanical. There is no external API.
- **Default redirect-follow behavior changes for any code that called `dispatcher.New(baseURL, nil)`.** The only production caller is `cmd/event-adapter/main.go:73`, which is exactly the call site whose behavior we want to change. Existing dispatcher tests use custom clients and are unaffected.
