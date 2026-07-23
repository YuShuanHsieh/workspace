# event-adapter: Path Templates Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

> **Historical note (2026-06-09):** This plan was authored before two design refinements landed on the implementation branch:
> 1. `Resolve` signature changed from `(path, ev *clevent.Event)` to `(path, data []byte)` to avoid an import cycle (commit `be03a4d`), then
> 2. Changed again from `(path, data []byte)` to `(path, params map[string]string)` after PR #24 review feedback to keep path parameters out of the `data` payload (commit `ff34088`). Path values now live in a new envelope-level `dispatchpathparams` field (parallel to `dispatchheaders` / `dispatchcookies`).
>
> The task descriptions below reflect the original plan and were the basis for implementation in the order they appear. Subsequent refactors are recorded in the spec at `docs/superpowers/specs/2026-06-08-event-adapter-path-templates-design.md` and on the branch's commit history; the plan is preserved as written for audit-trail clarity.

**Goal:** Support `{fieldName}` template tokens in `dispatch.path` (e.g. `/api/tasks/{taskId}/complete`) resolved from event-supplied parameters at dispatch time, with config-load validation and permanent-failure handling in both delivery models.

**Architecture:** A new internal package `internal/pathtemplate` owns parsing, validation, and substitution. Config-load wires `Validate` into the existing route validator. The dispatcher calls `Resolve` immediately before building the outbound URL. Path-resolution errors are surfaced via a sentinel `ErrPermanent` which the processor (JetStream path) treats as straight-to-DLQ and the responder (request-reply path) treats as a 400 reply.

**Tech Stack:** Go 1.25, cloudevents-sdk-go v2, gopkg.in/yaml.v3, Go 1.22+ `http.ServeMux` wildcards (for e2e), docker compose (for e2e).

**Spec reference:** `docs/superpowers/specs/2026-06-08-event-adapter-path-templates-design.md`

---

## File structure

| File | Responsibility | Action |
|---|---|---|
| `event-adapter/internal/pathtemplate/pathtemplate.go` | Token regex, `Validate`, `Resolve`, `ErrPermanent` sentinel | Create |
| `event-adapter/internal/pathtemplate/pathtemplate_test.go` | Unit tests for both functions | Create |
| `event-adapter/internal/config/validate.go` | Call `pathtemplate.Validate(route.Dispatch.Path)` for every JetStream and request-reply route at config-load | Modify |
| `event-adapter/internal/config/validate_test.go` | Tests asserting bad templates fail config-load with a route-scoped `ValidationError` | Modify |
| `event-adapter/internal/dispatcher/dispatcher.go` | Call `pathtemplate.Resolve(dc.Path, ev)` before `url.JoinPath` | Modify |
| `event-adapter/internal/dispatcher/dispatcher_test.go` | Tests asserting resolved URL is what gets dispatched | Modify |
| `event-adapter/internal/processor/processor.go` | When `dispatchErr` wraps `pathtemplate.ErrPermanent`, skip retries and DLQ directly | Modify |
| `event-adapter/internal/processor/processor_test.go` | Test asserting permanent path errors bypass retry | Modify |
| `event-adapter/internal/responder/responder.go` | When `dispatchErr` wraps `pathtemplate.ErrPermanent`, return a 400 error reply (not 502) | Modify |
| `event-adapter/internal/responder/responder_test.go` | Test asserting permanent path errors yield 400 reply | Modify |
| `event-adapter/cmd/mock-app/main.go` | New `echoPath: bool` option on the `response:` block — when true, response body becomes `{"path":"<request URL path>"}` | Modify |
| `event-adapter/test/e2e/mock-app.yaml` | New handler with templated path + `echoPath: true` | Modify |
| `event-adapter/test/e2e/routes.yaml` | New JetStream route using a path template | Modify |
| `event-adapter/test/e2e/fixtures/path-template.json` | New fixture event with `data.taskId` value | Create |
| `event-adapter/test/e2e/e2e_test.go` | Test asserting the response event's data echoes the resolved path | Modify |
| `prd/event-adapter/prd.md` | Document path templating in the route configuration section | Modify |
| `prd/event-adapter/app-developer-guide.md` | Add example of writing a handler whose path segment is filled from event data | Modify |

---

## Task 1: `pathtemplate.Validate` + token regex helper

**Goal:** Create the `internal/pathtemplate` package with the `ErrPermanent` sentinel, the token regex, a `tokenNames(path) ([]string, error)` helper that extracts all tokens and rejects bad syntax, and the `Validate(path string) error` function that wraps `tokenNames` for config-load use.

**Files:**
- Create: `event-adapter/internal/pathtemplate/pathtemplate.go`
- Create: `event-adapter/internal/pathtemplate/pathtemplate_test.go`

- [ ] **Step 1: Write the failing tests**

Create `event-adapter/internal/pathtemplate/pathtemplate_test.go`:

```go
package pathtemplate

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateAcceptsStaticPath(t *testing.T) {
	if err := Validate("/events/task-created"); err != nil {
		t.Fatalf("Validate static path: %v", err)
	}
}

func TestValidateAcceptsSingleToken(t *testing.T) {
	if err := Validate("/api/tasks/{taskId}/complete"); err != nil {
		t.Fatalf("Validate single-token path: %v", err)
	}
}

func TestValidateAcceptsMultipleTokens(t *testing.T) {
	if err := Validate("/api/tenants/{tenantId}/tasks/{taskId}"); err != nil {
		t.Fatalf("Validate multi-token path: %v", err)
	}
}

func TestValidateAcceptsSameTokenTwice(t *testing.T) {
	if err := Validate("/{taskId}/x/{taskId}/y"); err != nil {
		t.Fatalf("Validate same-token-twice: %v", err)
	}
}

func TestValidateRejectsTokenStartingWithDigit(t *testing.T) {
	err := Validate("/api/{123bad}/x")
	if err == nil {
		t.Fatal("expected error for {123bad}")
	}
	if !strings.Contains(err.Error(), "123bad") {
		t.Fatalf("error should name the bad token, got: %v", err)
	}
}

func TestValidateRejectsEmptyToken(t *testing.T) {
	if err := Validate("/api/{}/x"); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestValidateRejectsTokenWithHyphen(t *testing.T) {
	if err := Validate("/api/{a-b}/x"); err == nil {
		t.Fatal("expected error for {a-b}")
	}
}

func TestValidateRejectsUnclosedToken(t *testing.T) {
	if err := Validate("/api/{taskId/x"); err == nil {
		t.Fatal("expected error for unclosed brace")
	}
}

func TestValidateErrorIsNotPermanent(t *testing.T) {
	// Validation errors happen at config-load, not at dispatch.
	// They MUST NOT wrap ErrPermanent — the processor only checks for ErrPermanent
	// to bypass retry, and a config error should never reach the processor.
	err := Validate("/api/{123}/x")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if errors.Is(err, ErrPermanent) {
		t.Fatal("Validate errors must not wrap ErrPermanent")
	}
}
```

- [ ] **Step 2: Run tests, expect package-not-found**

Run from `event-adapter/`:

```bash
go test ./internal/pathtemplate/ -count=1
```

Expected: package compile errors — `Validate undefined` and `ErrPermanent undefined`. This confirms tests reach the symbols we're about to define.

- [ ] **Step 3: Write minimal implementation**

Create `event-adapter/internal/pathtemplate/pathtemplate.go`:

```go
// Package pathtemplate parses and resolves {field} tokens in HTTP path
// templates against the top-level fields of a CloudEvent's data payload.
package pathtemplate

import (
	"errors"
	"fmt"
	"regexp"
)

// ErrPermanent wraps payload-related Resolve failures that cannot succeed on
// retry because the event data does not change between attempts. The processor
// and responder use errors.Is to bypass their retry/error paths.
var ErrPermanent = errors.New("pathtemplate: permanent failure")

// tokenRegex matches a single {fieldName} token. Token names must start with
// a letter and contain only letters, digits, and underscores.
var tokenRegex = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// looseBraceRegex matches anything between { and } — used to find malformed
// tokens that tokenRegex does not match, so Validate can report them.
var looseBraceRegex = regexp.MustCompile(`\{([^}]*)\}`)

// Validate parses a path string at config-load time and rejects malformed
// tokens. It does not require any event data — it checks only the path itself.
// Errors returned by Validate do NOT wrap ErrPermanent (those are reserved for
// dispatch-time payload failures).
func Validate(path string) error {
	// Reject unclosed braces (e.g. "/api/{x").
	for i := 0; i < len(path); i++ {
		if path[i] == '{' {
			closing := indexFromOffset(path, i, '}')
			if closing == -1 {
				return fmt.Errorf("pathtemplate: unclosed { in path %q", path)
			}
		}
	}
	// Every {...} match must satisfy the strict token regex.
	for _, m := range looseBraceRegex.FindAllStringSubmatch(path, -1) {
		raw := m[0]
		if !tokenRegex.MatchString(raw) {
			return fmt.Errorf("pathtemplate: invalid token %q in path %q (must match {[a-zA-Z][a-zA-Z0-9_]*})", m[1], path)
		}
	}
	return nil
}

// tokenNames returns the names of every token in path, in order of appearance,
// without de-duplication. Callers that want unique names should de-dup themselves.
// Returns an error if any token is malformed (delegates to Validate).
func tokenNames(path string) ([]string, error) {
	if err := Validate(path); err != nil {
		return nil, err
	}
	matches := tokenRegex.FindAllStringSubmatch(path, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names, nil
}

func indexFromOffset(s string, off int, c byte) int {
	for i := off + 1; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/pathtemplate/ -count=1 -v
```

Expected: all 9 Validate tests pass.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/pathtemplate/pathtemplate.go event-adapter/internal/pathtemplate/pathtemplate_test.go
git commit -m "feat(event-adapter): add pathtemplate package with Validate (#18)"
```

---

## Task 2: `pathtemplate.Resolve`

**Goal:** Add `Resolve(path string, data []byte) (string, error)` that substitutes each `{field}` token in path with the URL-path-escaped value of `data.{field}` parsed from the raw JSON bytes. Static paths short-circuit without parsing JSON. Permanent failures (missing field, data not an object) wrap `ErrPermanent`.

> **Note:** The original plan and Task 2 example code below show `Resolve(path string, ev *clevent.Event)`. During implementation we discovered this would create an import cycle (`config → pathtemplate → cloudevent → config`) once Task 3 wires `pathtemplate.Validate` into `config.Validate`. The signature was refactored to take `data []byte` directly (see commit `be03a4d` on the implementation branch). Callers pass `ev.Data()` at the call site. Task 2 below retains the original example code for historical accuracy; the refactor commit covers the migration.

**Files:**
- Modify: `event-adapter/internal/pathtemplate/pathtemplate.go`
- Modify: `event-adapter/internal/pathtemplate/pathtemplate_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `event-adapter/internal/pathtemplate/pathtemplate_test.go`. Add `clevent "event-adapter/internal/cloudevent"` to the imports if not already there.

```go
func TestResolveStaticPathReturnsUnchanged(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e1","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"x"}}`)
	got, err := Resolve("/events/task-created", ev)
	if err != nil {
		t.Fatalf("Resolve static: %v", err)
	}
	if got != "/events/task-created" {
		t.Fatalf("Resolve static = %q, want unchanged", got)
	}
}

func TestResolveSingleToken(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e2","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"task-42"}}`)
	got, err := Resolve("/api/tasks/{taskId}/complete", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tasks/task-42/complete" {
		t.Fatalf("Resolve = %q, want /api/tasks/task-42/complete", got)
	}
}

func TestResolveMultipleTokens(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e3","source":"s","type":"t","datacontenttype":"application/json","data":{"tenantId":"acme","taskId":"task-42"}}`)
	got, err := Resolve("/api/tenants/{tenantId}/tasks/{taskId}", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tenants/acme/tasks/task-42" {
		t.Fatalf("Resolve = %q, want /api/tenants/acme/tasks/task-42", got)
	}
}

func TestResolveSameTokenTwice(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e4","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"abc"}}`)
	got, err := Resolve("/{taskId}/x/{taskId}/y", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/abc/x/abc/y" {
		t.Fatalf("Resolve = %q, want /abc/x/abc/y", got)
	}
}

func TestResolveURLEscapesValues(t *testing.T) {
	// Spaces and slashes in field values must be path-escaped so they don't
	// reshape the URL.
	ev := mustParse(t, `{"specversion":"1.0","id":"e5","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"a b/c"}}`)
	got, err := Resolve("/api/tasks/{taskId}", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tasks/a%20b%2Fc" {
		t.Fatalf("Resolve = %q, want path-escaped a%%20b%%2Fc", got)
	}
}

func TestResolveMissingFieldIsPermanent(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e6","source":"s","type":"t","datacontenttype":"application/json","data":{"status":"done"}}`)
	_, err := Resolve("/api/tasks/{taskId}/complete", ev)
	if err == nil {
		t.Fatal("expected permanent error for missing field")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("error must wrap ErrPermanent, got %v", err)
	}
	if !strings.Contains(err.Error(), "taskId") {
		t.Fatalf("error should name the missing field, got %v", err)
	}
}

func TestResolveDataNotAnObjectIsPermanent(t *testing.T) {
	// data is a JSON array, not an object.
	ev := mustParse(t, `{"specversion":"1.0","id":"e7","source":"s","type":"t","datacontenttype":"application/json","data":["not","an","object"]}`)
	_, err := Resolve("/api/tasks/{taskId}", ev)
	if err == nil {
		t.Fatal("expected permanent error for non-object data")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("error must wrap ErrPermanent, got %v", err)
	}
}

func TestResolveBadConfigDoesNotWrapPermanent(t *testing.T) {
	// If Resolve is somehow called with a bad path (config validation missed
	// it), the error must NOT wrap ErrPermanent — that would silently DLQ
	// the event when the real fix is to correct the config.
	ev := mustParse(t, `{"specversion":"1.0","id":"e8","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"x"}}`)
	_, err := Resolve("/api/{123bad}/x", ev)
	if err == nil {
		t.Fatal("expected config error")
	}
	if errors.Is(err, ErrPermanent) {
		t.Fatal("config errors must not wrap ErrPermanent")
	}
}

func mustParse(t *testing.T, raw string) *clevent.Event {
	t.Helper()
	ev, err := clevent.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return ev
}
```

- [ ] **Step 2: Run tests, expect compile failure on `Resolve`**

```bash
go test ./internal/pathtemplate/ -count=1
```

Expected: `Resolve undefined`. The Validate tests from Task 1 still pass at compile-time but don't run yet because the package itself fails to compile.

- [ ] **Step 3: Add Resolve to `pathtemplate.go`**

Append to `event-adapter/internal/pathtemplate/pathtemplate.go`. Also add `"net/url"`, `"encoding/json"`, and `clevent "event-adapter/internal/cloudevent"` to the imports block:

```go
// Resolve substitutes {field} tokens in path against the top-level fields of
// ev.Data() (parsed as a JSON object). Returns the resolved path on success,
// or an error wrapping ErrPermanent if any token cannot be resolved from the
// data payload. Static paths (no tokens) short-circuit without parsing JSON.
//
// Validation failures (bad token syntax) do NOT wrap ErrPermanent — those are
// config bugs, not payload bugs.
func Resolve(path string, ev *clevent.Event) (string, error) {
	names, err := tokenNames(path)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return path, nil
	}
	values, err := decodeDataAsObject(ev)
	if err != nil {
		return "", err
	}

	out := path
	for _, name := range uniqueNames(names) {
		raw, ok := values[name]
		if !ok {
			return "", fmt.Errorf("%w: field %q not found in event data", ErrPermanent, name)
		}
		s, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%w: field %q is not a string (got %T)", ErrPermanent, name, raw)
		}
		out = replaceAllToken(out, name, url.PathEscape(s))
	}
	return out, nil
}

func decodeDataAsObject(ev *clevent.Event) (map[string]any, error) {
	raw := ev.Data()
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: data is empty", ErrPermanent)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("%w: data is not a JSON object: %v", ErrPermanent, err)
	}
	return values, nil
}

func uniqueNames(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, n := range in {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func replaceAllToken(path, name, value string) string {
	token := "{" + name + "}"
	for {
		idx := indexOf(path, token)
		if idx < 0 {
			return path
		}
		path = path[:idx] + value + path[idx+len(token):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

Note on the loop pattern: we walk `uniqueNames(names)` and call `replaceAllToken` for each token name. This means if `{taskId}` appears twice in the path, both occurrences are replaced with the same resolved value in a single call to `replaceAllToken` — matching the spec.

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/pathtemplate/ -count=1 -v
```

Expected: all 17 tests pass (9 from Task 1 + 8 new Resolve tests).

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/pathtemplate/pathtemplate.go event-adapter/internal/pathtemplate/pathtemplate_test.go
git commit -m "feat(event-adapter): add pathtemplate.Resolve with ErrPermanent (#18)"
```

---

## Task 3: Wire `pathtemplate.Validate` into config validation

**Goal:** Call `pathtemplate.Validate(route.Dispatch.Path)` for every JetStream route and every request-reply route at config-load. Invalid path templates produce a `ValidationError` whose `Path` field points at the offending route.

**Files:**
- Modify: `event-adapter/internal/config/validate.go`
- Modify: `event-adapter/internal/config/validate_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `event-adapter/internal/config/validate_test.go`. Inspect the existing test patterns first — the file already loads config strings and asserts that `Validate` returns specific `ValidationError`s. Match the local style.

```go
func TestValidateRejectsBadPathTemplateInJetStreamRoute(t *testing.T) {
	cfg := mustParse(t, `
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:18080
nats:
  url: nats://localhost:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  filterSubject: t.tenant-a.app.task.event.created
  workerPoolSize: 4
  fetchBatch: 4
  ackWait: 30s
  maxDeliver: 3
  maxAckPending: 16
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      type: com.workspace.task.created
    dispatch:
      method: POST
      path: /api/{123bad}/x
      timeout: 2s
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
`)
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for bad path template")
	}
	if !strings.Contains(err.Error(), "routes[0].dispatch.path") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
	if !strings.Contains(err.Error(), "123bad") {
		t.Fatalf("error should name the bad token, got: %v", err)
	}
}

func TestValidateRejectsBadPathTemplateInRequestReplyRoute(t *testing.T) {
	cfg := mustParse(t, `
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:18080
nats:
  url: nats://localhost:4222
requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: q
  workerPoolSize: 2
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request
      dispatch:
        method: POST
        path: /api/{a-b}/presign
        timeout: 2s
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
`)
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for bad request-reply path template")
	}
	if !strings.Contains(err.Error(), "requests.routes[0].dispatch.path") {
		t.Fatalf("error should name the offending field, got: %v", err)
	}
}

func TestValidateAcceptsValidPathTemplate(t *testing.T) {
	cfg := mustParse(t, `
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:18080
nats:
  url: nats://localhost:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  filterSubject: t.tenant-a.app.task.event.created
  workerPoolSize: 4
  fetchBatch: 4
  ackWait: 30s
  maxDeliver: 3
  maxAckPending: 16
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      type: com.workspace.task.created
    dispatch:
      method: POST
      path: /api/tasks/{taskId}/complete
      timeout: 2s
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
`)
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate rejected a valid path template: %v", err)
	}
}
```

If `mustParse` doesn't already exist in `validate_test.go`, search for the file's existing config-string helper and reuse its name. If none exists, add at the bottom:

```go
func mustParse(t *testing.T, s string) *Config {
	t.Helper()
	cfg, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse config: %v", err)
	}
	return cfg
}
```

- [ ] **Step 2: Run tests, expect fail**

```bash
go test ./internal/config/ -run TestValidateRejectsBadPathTemplate -count=1 -v
```

Expected: tests fail because `Validate(cfg)` returns no error for bad path templates yet.

- [ ] **Step 3: Wire `pathtemplate.Validate` into `validate.go`**

In `event-adapter/internal/config/validate.go`, find the per-route JetStream validation loop (the one that already validates `routes[i].match`). Add this immediately after the existing per-route validation block:

```go
if err := pathtemplate.Validate(routes[i].Dispatch.Path); err != nil {
	errs = append(errs, ValidationError{
		Path: fmt.Sprintf("routes[%d].dispatch.path", i),
		Msg:  err.Error(),
	})
}
```

Then find the request-reply per-route validation loop (the one that validates `requests.routes[i].match`) and add:

```go
if err := pathtemplate.Validate(cfg.Requests.Routes[i].Dispatch.Path); err != nil {
	errs = append(errs, ValidationError{
		Path: fmt.Sprintf("requests.routes[%d].dispatch.path", i),
		Msg:  err.Error(),
	})
}
```

Add `"event-adapter/internal/pathtemplate"` to the imports.

- [ ] **Step 4: Run tests, expect pass**

```bash
go test ./internal/config/ -count=1 -v
```

Expected: all config tests pass, including the three new ones.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/config/validate.go event-adapter/internal/config/validate_test.go
git commit -m "feat(event-adapter/config): validate path templates at config-load (#18)"
```

---

## Task 4: Wire `pathtemplate.Resolve` into the dispatcher

**Goal:** Resolve the path template against the incoming event right before building the outbound URL. Static paths pay no JSON-parsing cost. Resolution failures propagate as errors (including `ErrPermanent` for payload bugs) so processor (Task 5) and responder (Task 6) can discriminate.

**Files:**
- Modify: `event-adapter/internal/dispatcher/dispatcher.go`
- Modify: `event-adapter/internal/dispatcher/dispatcher_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `event-adapter/internal/dispatcher/dispatcher_test.go`:

```go
func TestDispatchResolvesPathTemplate(t *testing.T) {
	var gotURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.Path
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"task-42"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/api/tasks/{taskId}/complete", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotURL != "/api/tasks/task-42/complete" {
		t.Fatalf("URL.Path = %q, want /api/tasks/task-42/complete", gotURL)
	}
}

func TestDispatchStaticPathUnchanged(t *testing.T) {
	var gotURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.Path
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"x"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/events/task-created", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotURL != "/events/task-created" {
		t.Fatalf("URL.Path = %q, want /events/task-created", gotURL)
	}
}

func TestDispatchMissingFieldReturnsErrPermanent(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport must not be invoked when path resolution fails")
		return nil, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-3","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"status":"done"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/api/tasks/{taskId}/complete", Timeout: time.Second,
	}, ev)
	if err == nil {
		t.Fatal("expected error for missing field")
	}
	if !errors.Is(err, pathtemplate.ErrPermanent) {
		t.Fatalf("error must wrap pathtemplate.ErrPermanent, got %v", err)
	}
}
```

Add `pathtemplate "event-adapter/internal/pathtemplate"` to the imports.

- [ ] **Step 2: Run tests, expect fail**

```bash
go test ./internal/dispatcher/ -run TestDispatchResolvesPathTemplate -count=1 -v
```

Expected: `URL.Path = "/api/tasks/{taskId}/complete", want /api/tasks/task-42/complete` — the dispatcher is still sending the literal template.

- [ ] **Step 3: Modify `dispatcher.go` to call `Resolve`**

In `event-adapter/internal/dispatcher/dispatcher.go`, find the line in `Dispatch()` that reads:

```go
u, err := url.JoinPath(d.baseURL, dc.Path)
```

Replace those lines with:

```go
resolvedPath, err := pathtemplate.Resolve(dc.Path, ev)
if err != nil {
	return Result{}, fmt.Errorf("dispatcher: resolve path: %w", err)
}
u, err := url.JoinPath(d.baseURL, resolvedPath)
```

Add `pathtemplate "event-adapter/internal/pathtemplate"` to the imports.

The `%w` wrap preserves the `ErrPermanent` sentinel through `errors.Is`.

- [ ] **Step 4: Run all dispatcher tests, expect pass**

```bash
go test ./internal/dispatcher/ -count=1 -v
```

Expected: all dispatcher tests pass, including the three new ones.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/dispatcher/dispatcher.go event-adapter/internal/dispatcher/dispatcher_test.go
git commit -m "feat(event-adapter/dispatcher): resolve path templates from event data (#18)"
```

---

## Task 5: Processor sends `ErrPermanent` straight to DLQ

**Goal:** When the dispatcher returns an error that wraps `pathtemplate.ErrPermanent`, the processor must skip retry/Nak logic and DLQ immediately. Network errors continue to retry as before.

**Files:**
- Modify: `event-adapter/internal/processor/processor.go`
- Modify: `event-adapter/internal/processor/processor_test.go`

- [ ] **Step 1: Write the failing test**

Append to `event-adapter/internal/processor/processor_test.go`:

```go
func TestProcessorPermanentPathErrorGoesStraightToDLQ(t *testing.T) {
	permErr := fmt.Errorf("dispatcher: resolve path: %w", pathtemplate.ErrPermanent)
	disp := &fakeProcessorDispatcher{err: permErr}
	pub := &fakeProcessorPublisher{}
	p := New(disp, pub)

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-proc","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"status":"done"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	route := config.RouteConfig{
		Name:     "task-created",
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/api/tasks/{taskId}/x", Timeout: time.Second},
		Response: config.ResponseConfig{Type: "x.proc", Source: "task-service", Subject: "out"},
		Retry:    config.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ:      config.DLQConfig{Subject: "dlq.tenant-a.task-service"},
	}

	msg := &fakeMessageHandle{}
	if err := p.Process(context.Background(), "t.x.created", ev, route, msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if msg.nakCount != 0 {
		t.Fatalf("Nak count = %d, want 0 (permanent error must not retry)", msg.nakCount)
	}
	if pub.dlqCount != 1 {
		t.Fatalf("DLQ count = %d, want 1", pub.dlqCount)
	}
	if msg.ackCount != 1 {
		t.Fatalf("Ack count = %d, want 1 (after DLQ the original is acked)", msg.ackCount)
	}
}
```

Add `pathtemplate "event-adapter/internal/pathtemplate"` to the imports if not already present. Match the existing fake names in `processor_test.go` for `fakeProcessorDispatcher` etc. — if they go by other names like `fakeDispatcher`/`fakePublisher`/`fakeHandle`, use those names instead (Task 3 of plan #11 went through this same naming reconciliation; follow the file's local convention).

- [ ] **Step 2: Run test, expect fail**

```bash
go test ./internal/processor/ -run TestProcessorPermanentPathErrorGoesStraightToDLQ -count=1 -v
```

Expected: depending on the existing retry path, you'll see either `Nak count = 1, want 0` (the current code Naks on first failure when delivery < MaxAttempts), or the test will pass for the wrong reason (e.g. because `isNetworkError` returns false and the existing fall-through DLQs). Either way the test bites — the new ErrPermanent guard is what makes the behavior contractual instead of accidental.

- [ ] **Step 3: Add the `ErrPermanent` guard to processor.go**

In `event-adapter/internal/processor/processor.go`, find the dispatch-error block in `Process()`:

```go
res, dispatchErr := p.dispatcher.Dispatch(ctx, route.Dispatch, ev)
if dispatchErr != nil {
	if isNetworkError(dispatchErr) && delivery < policy.MaxAttempts {
		return msg.Nak(ctx, policy.Delay(delivery))
	}
	return p.toDLQ(ctx, route, ev, dispatchErr.Error(), 0, delivery, msg)
}
```

Insert a new branch as the first check:

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

Add `pathtemplate "event-adapter/internal/pathtemplate"` to the imports.

- [ ] **Step 4: Run all processor tests, expect pass**

```bash
go test ./internal/processor/ -count=1 -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/processor/processor.go event-adapter/internal/processor/processor_test.go
git commit -m "feat(event-adapter/processor): DLQ permanent path-template errors immediately (#18)"
```

---

## Task 6: Responder returns 400 reply for `ErrPermanent`

**Goal:** When the dispatcher returns `ErrPermanent` on the request-reply path, the responder replies with `httpstatus: 400` (Bad Request) instead of the existing 502 fallback. The synchronous caller learns immediately that their event data was the cause.

**Files:**
- Modify: `event-adapter/internal/responder/responder.go`
- Modify: `event-adapter/internal/responder/responder_test.go`

- [ ] **Step 1: Write the failing test**

Append to `event-adapter/internal/responder/responder_test.go`:

```go
func TestHandlePermanentPathErrorRepliesWith400(t *testing.T) {
	permErr := fmt.Errorf("dispatcher: resolve path: %w", pathtemplate.ErrPermanent)
	d := fakeDispatcher{err: permErr}
	met := &fakeMetrics{}
	r := newResponder(d, met)
	m, out := capture()
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	status, ok := reply["httpstatus"].(float64)
	if !ok || status != 400 {
		t.Fatalf("httpstatus = %v, want 400", reply["httpstatus"])
	}
	if met.dispatchErr != 1 {
		t.Fatalf("dispatchErr metric = %d, want 1", met.dispatchErr)
	}
}
```

Add `"fmt"` and `pathtemplate "event-adapter/internal/pathtemplate"` to the imports.

- [ ] **Step 2: Run test, expect fail**

```bash
go test ./internal/responder/ -run TestHandlePermanentPathErrorRepliesWith400 -count=1 -v
```

Expected: `httpstatus = 502, want 400` — the current responder treats every non-timeout dispatcher error as a 502 Bad Gateway.

- [ ] **Step 3: Add the `ErrPermanent` branch to `responder.go`**

In `event-adapter/internal/responder/responder.go`, find the dispatch-error block in `handle()`:

```go
if derr != nil {
	r.metrics.RequestDispatchError(ctx, route.Name)
	status := http.StatusBadGateway
	if errors.Is(derr, context.DeadlineExceeded) {
		status = http.StatusGatewayTimeout
	}
	reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, status, "application/json", errorBody(derr.Error()), "")
	...
}
```

Add an `ErrPermanent` branch alongside the existing `DeadlineExceeded` branch:

```go
if derr != nil {
	r.metrics.RequestDispatchError(ctx, route.Name)
	status := http.StatusBadGateway
	switch {
	case errors.Is(derr, pathtemplate.ErrPermanent):
		status = http.StatusBadRequest
	case errors.Is(derr, context.DeadlineExceeded):
		status = http.StatusGatewayTimeout
	}
	reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, status, "application/json", errorBody(derr.Error()), "")
	...
}
```

Add `pathtemplate "event-adapter/internal/pathtemplate"` to the imports.

- [ ] **Step 4: Run all responder tests, expect pass**

```bash
go test ./internal/responder/ -count=1 -v
```

Expected: all tests pass, including the new one and existing 502/504 cases.

- [ ] **Step 5: Sanity — module-wide test sweep**

```bash
go test ./... -count=1
```

Expected: every package passes. This catches any consumer of dispatcher errors we may have missed.

- [ ] **Step 6: Commit**

```bash
git add event-adapter/internal/responder/responder.go event-adapter/internal/responder/responder_test.go
git commit -m "feat(event-adapter/responder): reply 400 for permanent path-template errors (#18)"
```

---

## Task 7: Mock-app `echoPath` option + e2e round-trip

**Goal:** Mock-app gains a new `echoPath: true` option on its `response:` block — when true, the response body becomes `{"path":"<the request URL path>"}` instead of the configured `body`. The e2e test publishes an event with `data.taskId` and asserts the published response event's data contains the expected resolved path.

**Files:**
- Modify: `event-adapter/cmd/mock-app/main.go`
- Modify: `event-adapter/test/e2e/mock-app.yaml`
- Modify: `event-adapter/test/e2e/routes.yaml`
- Create: `event-adapter/test/e2e/fixtures/path-template.json`
- Modify: `event-adapter/test/e2e/e2e_test.go`

- [ ] **Step 1: Add the `echoPath` field to mock-app**

In `event-adapter/cmd/mock-app/main.go`, update `HandlerResponse`:

```go
type HandlerResponse struct {
	Status      int    `yaml:"status"`
	ContentType string `yaml:"contentType"`
	Body        string `yaml:"body"`
	Location    string `yaml:"location"`
	EchoPath    bool   `yaml:"echoPath"`
}
```

In `makeHandler`, find the body-writing block:

```go
if h.Response.Body != "" {
	_, _ = fmt.Fprint(w, h.Response.Body)
}
```

Replace it with:

```go
switch {
case h.Response.EchoPath:
	_, _ = fmt.Fprintf(w, `{"path":"%s"}`, r.URL.Path)
case h.Response.Body != "":
	_, _ = fmt.Fprint(w, h.Response.Body)
}
```

When `echoPath: true`, the body is the JSON-encoded path. When false (default), the configured `body` is used. Backward compatible.

- [ ] **Step 2: Build mock-app to verify compile**

```bash
go build ./cmd/mock-app/
```

Expected: clean exit. No unit tests for mock-app.

- [ ] **Step 3: Add the templated handler to `mock-app.yaml`**

Append to `event-adapter/test/e2e/mock-app.yaml`:

```yaml
  - method: POST
    path: /api/tasks/{taskId}/complete
    requireHeaders:
      - X-Workspace-Actor-Id
    response:
      status: 200
      contentType: application/json
      echoPath: true
```

`{taskId}` here is a Go 1.22 `http.ServeMux` wildcard — it accepts any value and the test asserts the echoed path contains the resolved value.

- [ ] **Step 4: Add the JetStream route + a new response subject**

In `event-adapter/test/e2e/routes.yaml`, append to the JetStream `routes:` block:

```yaml
  - name: task-template
    match:
      type: com.workspace.task.template
    subject: t.tenant-a.app.task.event.created
    dispatch:
      method: POST
      path: /api/tasks/{taskId}/complete
    response:
      type: com.workspace.task.template.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed.template
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
```

Then in `event-adapter/test/e2e/docker-compose.yaml`, find the `nats-setup` service's `--subjects` argument and add the new response subject `t.tenant-a.app.task.event.processed.template` to the comma-separated list.

- [ ] **Step 5: Create the fixture**

Create `event-adapter/test/e2e/fixtures/path-template.json`:

```json
{
  "specversion": "1.0",
  "id": "pt-1",
  "source": "workspace/task",
  "type": "com.workspace.task.template",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1"
  },
  "data": {
    "taskId": "e2e-task-1"
  }
}
```

- [ ] **Step 6: Add the e2e test**

Append to `event-adapter/test/e2e/e2e_test.go`:

```go
func TestPathTemplateResolvesFromEventData(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ensureEmptyStream(t, js)

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed.template")
	if err != nil {
		t.Fatalf("subscribe template response subject: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	fixture, err := os.ReadFile("fixtures/path-template.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	if _, err := js.Publish("t.tenant-a.app.task.event.created", fixture); err != nil {
		t.Fatalf("publish input event: %v", err)
	}

	msg, err := sub.NextMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("waiting for response: %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	status, ok := response["httpstatus"].(float64)
	if !ok || status != 200 {
		t.Fatalf("httpstatus = %v, want 200", response["httpstatus"])
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatalf("response.data is not an object: %v", response["data"])
	}
	if data["path"] != "/api/tasks/e2e-task-1/complete" {
		t.Fatalf("echoed path = %v, want /api/tasks/e2e-task-1/complete", data["path"])
	}
}
```

Also extend `ensureEmptyStream`'s `Subjects` list with `t.tenant-a.app.task.event.processed.template`.

- [ ] **Step 7: Bring up the rebuilt stack and run the test**

From `event-adapter/test/e2e/`:

```bash
docker compose up --build -d --wait
```

Then from `event-adapter/`:

```bash
go test ./test/e2e/ -tags=e2e -run TestPathTemplateResolvesFromEventData -count=1 -v
```

Expected: PASS.

- [ ] **Step 8: Full e2e sweep**

```bash
go test ./test/e2e/ -tags=e2e -count=1
```

Expected: all e2e tests pass (existing ones + new one).

- [ ] **Step 9: Commit**

```bash
git add event-adapter/cmd/mock-app/main.go event-adapter/test/e2e/mock-app.yaml event-adapter/test/e2e/routes.yaml event-adapter/test/e2e/docker-compose.yaml event-adapter/test/e2e/fixtures/path-template.json event-adapter/test/e2e/e2e_test.go
git commit -m "test(event-adapter/e2e): assert path template resolves from event data (#18)"
```

---

## Task 8: PRD updates

**Goal:** Document path templating in `prd/event-adapter/prd.md` so the platform docs reflect the new behavior.

**Files:**
- Modify: `prd/event-adapter/prd.md`

- [ ] **Step 1: Add path-template documentation to the route configuration section**

In `prd/event-adapter/prd.md`, locate the section that describes route configuration (search for `dispatch.path` or for a yaml block showing `dispatch:`). Immediately after the description of `dispatch.path`, add a new paragraph and example:

> `dispatch.path` supports `{fieldName}` template tokens that are resolved at dispatch time against the top-level fields of the incoming CloudEvent's `data` payload. Token names must match `[a-zA-Z][a-zA-Z0-9_]*`. Values are URL-path-escaped. Multiple tokens are supported, and the same token may appear more than once.
>
> Example:
>
> ```yaml
> dispatch:
>   method: PUT
>   path: /api/tasks/{taskId}/complete
> ```
>
> With `data.taskId = "task-42"`, the sidecar dispatches `PUT /api/tasks/task-42/complete`.
>
> If a referenced field is absent from `data`, or `data` is not a JSON object, the event is treated as a permanent failure: the sidecar publishes it to the route DLQ immediately, with no retries.

- [ ] **Step 2: Commit**

```bash
git add prd/event-adapter/prd.md
git commit -m "docs(event-adapter/prd): document path templates (#18)"
```

---

## Task 9: App developer guide updates

**Goal:** Add a short example to `prd/event-adapter/app-developer-guide.md` so handler authors know how to write a route that consumes path tokens.

**Files:**
- Modify: `prd/event-adapter/app-developer-guide.md`

- [ ] **Step 1: Add a path-template example near the existing routing examples**

In `prd/event-adapter/app-developer-guide.md`, locate the section that shows routing examples (likely under a "Routing" or "Configuration" heading). Add a new sub-section:

> ### Routes with dynamic path segments
>
> If your handler lives at a path with a dynamic segment — for example `PUT /api/tasks/{taskId}/complete` — declare the template in your route config:
>
> ```yaml
> dispatch:
>   method: PUT
>   path: /api/tasks/{taskId}/complete
> ```
>
> The sidecar resolves `{taskId}` against the top-level `data.taskId` field of every incoming CloudEvent before dispatching. Your handler receives the resolved URL (e.g. `/api/tasks/task-42/complete`) as a normal HTTP request — no special header parsing required.
>
> If the event omits the referenced field, the sidecar sends the event to your route's DLQ subject. There are no retries — the event data does not change between attempts.

- [ ] **Step 2: Commit**

```bash
git add prd/event-adapter/app-developer-guide.md
git commit -m "docs(event-adapter): app guide example for templated dispatch paths (#18)"
```

---

## Self-review notes (for the implementer)

Before opening the PR, run this final pass:

- [ ] **Full test suite green:** `go test ./... -count=1` and `go test ./test/e2e/ -tags=e2e -count=1`
- [ ] **Linter clean:** repo's linter (if `.golangci.yml` present, run it)
- [ ] **No leftover TODOs in the diff:** `git diff main -- '*.go' | grep -i todo` should be empty
- [ ] **All commits referenced #18:** `git log main.. --oneline | grep -v '#18'` should be empty
- [ ] **Static path still cheap:** confirm with a brief inspection of `pathtemplate.Resolve` that it returns `path` immediately when `tokenNames(path)` returns an empty slice — no JSON parsing of `ev.Data()` should happen on the static-path fast path

## Out of scope (do not implement)

- Nested field access (`{user.id}`). Top-level only.
- CloudEvent envelope access (`{ce.id}`, `{ext.tenantId}`).
- Query-string-specific escaping rules. Templates work in query strings but use the same `url.PathEscape`.
- Default values when a field is missing (`{taskId|default}`).
- Tokens in HTTP headers, body, or response config. v1 covers `dispatch.path` only.
- Mock-app `echoPath` for non-3xx response bodies beyond what Task 7 needs.
