# dispatchcookies Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `dispatchcookies` field so publishers can forward per-event HTTP cookies through the event-adapter to the target app.

**Architecture:** Mirror the existing `dispatchheaders` mechanism end to end — parse and strip `dispatchcookies` in the cloudevent layer, attach the cookies to the outbound request in the dispatcher via `req.AddCookie`, and reserve the `Cookie` header so cookies travel through one channel only. Cookies are optional and forwarded as-is.

**Tech Stack:** Go, CloudEvents SDK (`github.com/cloudevents/sdk-go/v2`), `net/http`, NATS JetStream (e2e), standard `testing`.

**Design reference:** `docs/superpowers/specs/2026-06-04-event-adapter-dispatchcookies-design.md`

---

## File Structure

| File | Responsibility | Action |
|------|----------------|--------|
| `event-adapter/internal/cloudevent/event.go` | Parse + strip `dispatchcookies`, expose `DispatchCookies` | Modify |
| `event-adapter/internal/cloudevent/event_test.go` | Unit tests for cookie parse/strip | Modify |
| `event-adapter/internal/dispatcher/dispatcher.go` | Attach cookies via `setPublisherCookies` | Modify |
| `event-adapter/internal/dispatcher/dispatcher_test.go` | Unit test for cookie forwarding | Modify |
| `event-adapter/internal/config/validate.go` | Reserve `cookie` header | Modify |
| `event-adapter/internal/config/validate_test.go` | Unit test for reserved `Cookie` | Modify |
| `event-adapter/cmd/mock-app/main.go` | Support `requireCookies` in handler config | Modify |
| `event-adapter/test/e2e/mock-app.yaml` | Require a cookie on the task-created handler | Modify |
| `event-adapter/test/e2e/fixtures/task-created.json` | Add a `dispatchcookies` block | Modify |
| `prd/event-adapter/app-developer-guide.md` | Document `dispatchcookies` | Modify |
| `prd/event-adapter/prd.md` | Reference cookie forwarding | Modify |
| `event-adapter/examples/onboarding/README.md` | Short cookie example | Modify |

All commands assume the working directory is `event-adapter/` unless a path says otherwise.

---

## Task 1: Parse and strip `dispatchcookies` in the cloudevent layer

**Files:**
- Modify: `event-adapter/internal/cloudevent/event.go`
- Test: `event-adapter/internal/cloudevent/event_test.go`

- [ ] **Step 1: Write the failing test**

Append to `event-adapter/internal/cloudevent/event_test.go`:

```go
func TestParseExtractsAndStripsDispatchCookies(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-c1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchcookies":{"session":"abc123","csrf-token":"xyz789"},"data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.DispatchCookies["session"] != "abc123" || ev.DispatchCookies["csrf-token"] != "xyz789" {
		t.Fatalf("unexpected cookies: %#v", ev.DispatchCookies)
	}
	// dispatchcookies must be stripped from the envelope, never surfaced as an extension.
	if _, ok := ev.Extensions()["dispatchcookies"]; ok {
		t.Fatal("dispatchcookies leaked into CloudEvent extensions")
	}
}

func TestParseDispatchCookiesAbsentIsNil(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-c2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if ev.DispatchCookies != nil {
		t.Fatalf("expected nil cookies, got %#v", ev.DispatchCookies)
	}
}

func TestParseRejectsNonStringDispatchCookies(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"evt-c3","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchcookies":{"session":123},"data":{"taskId":"t1"}}`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected error for non-string-valued dispatchcookies")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./internal/cloudevent/ -run TestParse.*DispatchCookies -v`
Expected: FAIL — compile error `ev.DispatchCookies undefined` (field does not exist yet).

- [ ] **Step 3: Add the `DispatchCookies` field**

In `event-adapter/internal/cloudevent/event.go`, change the `Event` struct:

```go
type Event struct {
	*ce.Event
	DispatchHeaders map[string]string
	DispatchCookies map[string]string
}
```

- [ ] **Step 4: Extract, strip, and return cookies in `Parse()`**

In `Parse()`, immediately after the existing `delete(probe, "dispatchheaders")` line, add:

```go
	dispatchCookies, err := parseDispatchCookies(probe["dispatchcookies"])
	if err != nil {
		return nil, err
	}
	delete(probe, "dispatchcookies")
```

Then change the final return statement from:

```go
	return &Event{Event: &ev, DispatchHeaders: dispatchHeaders}, nil
```

to:

```go
	return &Event{Event: &ev, DispatchHeaders: dispatchHeaders, DispatchCookies: dispatchCookies}, nil
```

- [ ] **Step 5: Add the `parseDispatchCookies` helper**

At the end of `event-adapter/internal/cloudevent/event.go`, add:

```go
func parseDispatchCookies(raw json.RawMessage) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("cloudevent: dispatchcookies must be a string-valued object: %w", err)
	}
	return values, nil
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test ./internal/cloudevent/ -v`
Expected: PASS (all cloudevent tests, including the three new ones).

- [ ] **Step 7: Commit**

```bash
git add internal/cloudevent/event.go internal/cloudevent/event_test.go
git commit -m "feat(event-adapter): parse and strip dispatchcookies (#10)"
```

---

## Task 2: Forward cookies onto the outbound request in the dispatcher

**Files:**
- Modify: `event-adapter/internal/dispatcher/dispatcher.go`
- Test: `event-adapter/internal/dispatcher/dispatcher_test.go`

- [ ] **Step 1: Write the failing test**

Append to `event-adapter/internal/dispatcher/dispatcher_test.go`:

```go
func TestDispatchForwardsPublisherCookies(t *testing.T) {
	var gotSession, gotCSRF string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if c, err := r.Cookie("session"); err == nil {
			gotSession = c.Value
		}
		if c, err := r.Cookie("csrf-token"); err == nil {
			gotCSRF = c.Value
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-ck","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchcookies":{"session":"abc123","csrf-token":"xyz789"},"data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	if _, err := d.Dispatch(context.Background(), config.RouteConfig{
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second},
	}, ev); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotSession != "abc123" || gotCSRF != "xyz789" {
		t.Fatalf("cookies not forwarded: session=%q csrf=%q", gotSession, gotCSRF)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/dispatcher/ -run TestDispatchForwardsPublisherCookies -v`
Expected: FAIL — cookies are empty (`session="" csrf=""`) because nothing attaches them yet.

- [ ] **Step 3: Call `setPublisherCookies` in `Dispatch()`**

In `event-adapter/internal/dispatcher/dispatcher.go`, find these lines inside `Dispatch()`:

```go
	setCloudEventHeaders(req, ev)
	setPublisherHeaders(req, route, ev)
```

Add one line immediately after them:

```go
	setCloudEventHeaders(req, ev)
	setPublisherHeaders(req, route, ev)
	setPublisherCookies(req, ev)
```

- [ ] **Step 4: Add the `setPublisherCookies` function**

At the end of `event-adapter/internal/dispatcher/dispatcher.go`, add:

```go
func setPublisherCookies(req *http.Request, ev *clevent.Event) {
	for name, value := range ev.DispatchCookies {
		req.AddCookie(&http.Cookie{Name: name, Value: value})
	}
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./internal/dispatcher/ -run TestDispatchForwardsPublisherCookies -v`
Expected: PASS.

- [ ] **Step 6: Defensively skip `dispatchcookies` in the extensions loop**

In `setCloudEventHeaders`, find:

```go
	for name, value := range ev.Extensions() {
		if strings.EqualFold(name, "dispatchheaders") {
			continue
		}
		req.Header.Set("ce-"+strings.ToLower(name), fmt.Sprint(value))
	}
```

Change the condition to also skip `dispatchcookies`:

```go
	for name, value := range ev.Extensions() {
		if strings.EqualFold(name, "dispatchheaders") || strings.EqualFold(name, "dispatchcookies") {
			continue
		}
		req.Header.Set("ce-"+strings.ToLower(name), fmt.Sprint(value))
	}
```

- [ ] **Step 7: Run the full dispatcher suite to verify no regressions**

Run: `go test ./internal/dispatcher/ -v`
Expected: PASS (all dispatcher tests).

- [ ] **Step 8: Commit**

```bash
git add internal/dispatcher/dispatcher.go internal/dispatcher/dispatcher_test.go
git commit -m "feat(event-adapter): forward publisher dispatchcookies on outbound request (#10)"
```

---

## Task 3: Reserve the `Cookie` header

**Files:**
- Modify: `event-adapter/internal/config/validate.go`
- Test: `event-adapter/internal/config/validate_test.go`

- [ ] **Step 1: Write the failing test**

Append to `event-adapter/internal/config/validate_test.go`:

```go
func TestCookieIsReservedHeader(t *testing.T) {
	if !IsReservedHeader("Cookie") {
		t.Fatal("expected Cookie to be a reserved header")
	}
	if !IsReservedHeader("cookie") {
		t.Fatal("expected reserved-header check to be case-insensitive for cookie")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./internal/config/ -run TestCookieIsReservedHeader -v`
Expected: FAIL — `IsReservedHeader("Cookie")` returns false (not yet reserved).

- [ ] **Step 3: Add `cookie` to `reservedHeaders`**

In `event-adapter/internal/config/validate.go`, find the end of the `reservedHeaders` map:

```go
	"upgrade":             true,
}
```

Add a `cookie` entry:

```go
	"upgrade":             true,
	"cookie":              true,
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./internal/config/ -run TestCookieIsReservedHeader -v`
Expected: PASS.

- [ ] **Step 5: Run the full config suite to verify no regressions**

Run: `go test ./internal/config/ -v`
Expected: PASS (all config tests).

- [ ] **Step 6: Commit**

```bash
git add internal/config/validate.go internal/config/validate_test.go
git commit -m "feat(event-adapter): reserve Cookie header so cookies use dispatchcookies only (#10)"
```

---

## Task 4: Add `requireCookies` support to the mock-app

This is e2e test infrastructure. The mock-app gains a `requireCookies` list mirroring the existing `requireHeaders`, so the e2e test can prove a cookie reached the app (a missing cookie returns 400, which fails the dispatch and the test).

**Files:**
- Modify: `event-adapter/cmd/mock-app/main.go`

- [ ] **Step 1: Add the `RequireCookies` config field**

In `event-adapter/cmd/mock-app/main.go`, change the `Handler` struct:

```go
type Handler struct {
	Method         string          `yaml:"method"`
	Path           string          `yaml:"path"`
	RequireHeaders []string        `yaml:"requireHeaders"`
	RequireCookies []string        `yaml:"requireCookies"`
	Response       HandlerResponse `yaml:"response"`
}
```

- [ ] **Step 2: Enforce required cookies in the handler**

In `makeHandler`, find the existing required-header loop:

```go
		for _, name := range h.RequireHeaders {
			if r.Header.Get(name) == "" {
				msg := fmt.Sprintf("missing required header: %s", name)
				log.Printf("  ✗ %s", msg)
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
```

Add a parallel cookie loop immediately after it:

```go
		for _, name := range h.RequireCookies {
			if _, err := r.Cookie(name); err != nil {
				msg := fmt.Sprintf("missing required cookie: %s", name)
				log.Printf("  ✗ %s", msg)
				http.Error(w, msg, http.StatusBadRequest)
				return
			}
		}
```

- [ ] **Step 3: Verify the mock-app still builds**

Run: `go build ./cmd/mock-app/`
Expected: builds with no output (exit 0).

- [ ] **Step 4: Commit**

```bash
git add cmd/mock-app/main.go
git commit -m "test(event-adapter/e2e): add requireCookies enforcement to mock-app (#10)"
```

---

## Task 5: Exercise cookie forwarding end-to-end

Wire a required cookie into the e2e fixture and mock-app config. The existing `TestEventDispatchPublishesResponse` already publishes the fixture and waits for the response CloudEvent; with `requireCookies` set, that round-trip only succeeds if the cookie is forwarded.

**Files:**
- Modify: `event-adapter/test/e2e/fixtures/task-created.json`
- Modify: `event-adapter/test/e2e/mock-app.yaml`

- [ ] **Step 1: Add a `dispatchcookies` block to the fixture**

Replace the contents of `event-adapter/test/e2e/fixtures/task-created.json` with:

```json
{
  "specversion": "1.0",
  "id": "evt-manual-1",
  "source": "workspace/task",
  "type": "com.workspace.task.created",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1",
    "X-Workspace-Tenant-Id": "tenant-a"
  },
  "dispatchcookies": {
    "session": "sess-abc123"
  },
  "data": {"taskId": "task-1"}
}
```

- [ ] **Step 2: Require the cookie in the mock-app config**

Replace the contents of `event-adapter/test/e2e/mock-app.yaml` with:

```yaml
addr: 0.0.0.0:18080

handlers:
  - method: POST
    path: /events/task-created
    requireHeaders:
      - X-Workspace-Actor-Id
    requireCookies:
      - session
    response:
      status: 200
      contentType: application/json
      body: '{"ok":true}'
```

- [ ] **Step 3: Run the e2e suite (requires the docker stack)**

From `event-adapter/test/e2e/`:

```bash
docker compose up --build -d
go test -tags e2e ./... -run TestEventDispatchPublishesResponse -v
docker compose down
```

Expected: PASS — the response CloudEvent arrives, proving the `session` cookie was forwarded and accepted by the mock-app. (If the cookie were dropped, the mock-app would return 400, no response would be published, and the test would time out.)

- [ ] **Step 4: Commit**

```bash
git add test/e2e/fixtures/task-created.json test/e2e/mock-app.yaml
git commit -m "test(event-adapter/e2e): assert dispatchcookies survive the round-trip (#10)"
```

---

## Task 6: Document `dispatchcookies`

**Files:**
- Modify: `prd/event-adapter/app-developer-guide.md`
- Modify: `prd/event-adapter/prd.md`
- Modify: `event-adapter/examples/onboarding/README.md`

> Place additions a few lines away from any region PR #15 touches in these files to avoid a merge conflict. In `app-developer-guide.md`, PR #15 edits near line 195; add the cookie note as its own new bullet after the `dispatch.forwardHeaders` bullet (line ~203) rather than inside the line-195 region.

- [ ] **Step 1: Add a `dispatchcookies` note to the developer guide**

In `prd/event-adapter/app-developer-guide.md`, find the `dispatch.forwardHeaders` bullet (around line 203). Add a new bullet immediately after it:

```markdown
- `dispatchcookies` is an optional top-level CloudEvent field (a `name → value` object) for forwarding HTTP cookies to your app, e.g. session or CSRF tokens. The sidecar attaches each entry as a request cookie via `http.AddCookie`. Cookies are forwarded as-is with no per-route allowlist; cookie attributes (path, domain, secure, httponly) are not supported. The `Cookie` header is reserved, so cookies must be sent through `dispatchcookies`, not `dispatchheaders`.
```

- [ ] **Step 2: Reference cookie forwarding in the PRD**

In `prd/event-adapter/prd.md`, find the section that mentions `dispatchheaders`. Add a sentence alongside it:

```markdown
Publishers may also supply a `dispatchcookies` object (`name → value`) to forward HTTP cookies (e.g. session tokens) onto the outbound request. Cookies are forwarded as-is; the `Cookie` header is reserved so `dispatchcookies` is the only path.
```

- [ ] **Step 3: Add a cookie example to the onboarding README**

In `event-adapter/examples/onboarding/README.md`, find where the example event JSON / `dispatchheaders` is shown. Add a short note and example:

```markdown
To forward cookies (e.g. a session token), add a `dispatchcookies` object to the event:

```json
{
  "dispatchheaders": { "X-Workspace-Actor-Id": "user-1" },
  "dispatchcookies": { "session": "abc123" }
}
```

The sidecar attaches each entry as a `Cookie` on the request to your app.
```

- [ ] **Step 4: Commit**

```bash
git add prd/event-adapter/app-developer-guide.md prd/event-adapter/prd.md event-adapter/examples/onboarding/README.md
git commit -m "docs(event-adapter): document dispatchcookies (#10)"
```

---

## Final verification

- [ ] **Run the full unit suite, vet, and lint**

From `event-adapter/`:

```bash
go test ./...
go vet ./...
golangci-lint run
```

Expected: all pass, no warnings.

- [ ] **Confirm the e2e suite passes** (from Task 5, Step 3) if not already run in this session.
