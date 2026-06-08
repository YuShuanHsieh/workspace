# event-adapter: Redirect Location Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stop the event-adapter's dispatcher from silently following HTTP 3xx redirects, capture the `Location` header on 3xx responses, and surface it as an `httplocation` CloudEvent extension on both response events (JetStream path) and reply events (request-reply path).

**Architecture:** Single source of truth is `dispatcher.Result.Location`, populated only for 3xx responses. Both `BuildResponse` (JetStream) and `BuildReply` (request-reply) gain a `location string` parameter and emit the `httplocation` extension when non-empty. `processor.go` and `responder.go` thread `res.Location` to their respective builders. Default `http.Client` gains `CheckRedirect → http.ErrUseLastResponse` so redirects are not followed transparently.

**Tech Stack:** Go 1.25, cloudevents-sdk-go v2, nats.go, docker compose (e2e), YAML (mock-app config).

**Spec reference:** `docs/superpowers/specs/2026-06-08-event-adapter-redirect-location-design.md`

---

## File structure

| File | Responsibility | Action |
|---|---|---|
| `event-adapter/internal/dispatcher/dispatcher.go` | HTTP transport. New: stop following redirects on default client; new `Result.Location` field; populate on 3xx only. | Modify |
| `event-adapter/internal/dispatcher/dispatcher_test.go` | Unit tests for the above. | Modify (add tests) |
| `event-adapter/internal/cloudevent/response.go` | CloudEvent builders. `BuildResponse` + `BuildReply` gain `location string` param; emit `httplocation` extension when non-empty. | Modify |
| `event-adapter/internal/cloudevent/response_test.go` | Unit tests for both builders. Update existing 5-arg calls to 6-arg. Add tests asserting `httplocation` extension behavior. | Modify |
| `event-adapter/internal/processor/processor.go` | JetStream path. Pass `res.Location` to `BuildResponse`. | Modify (1 line) |
| `event-adapter/internal/processor/processor_test.go` | Add a test for 3xx + Location end-to-end through Process. | Modify (add tests) |
| `event-adapter/internal/responder/responder.go` | Request-reply path. Pass `res.Location` to `BuildReply` on success; `""` on dispatch-error path. | Modify (2 lines) |
| `event-adapter/internal/responder/responder_test.go` | Add a test for 3xx + Location through `handle`. | Modify (add tests) |
| `event-adapter/cmd/mock-app/main.go` | Extend existing `Response` config with a `Location` field. When set, the handler writes the `Location` header before the status line. | Modify |
| `event-adapter/test/e2e/mock-app.yaml` | Add a new handler returning 307 with a Location, for e2e proof. | Modify |
| `event-adapter/test/e2e/routes.yaml` | Add a new route to the JetStream config + a new request-reply route so both paths can be exercised. | Modify |
| `event-adapter/test/e2e/fixtures/redirect-jetstream.json` | New JetStream fixture event targeting the redirect handler. | Create |
| `event-adapter/test/e2e/fixtures/redirect-reqreply.json` | New request-reply fixture event targeting the redirect handler. | Create |
| `event-adapter/test/e2e/e2e_test.go` | Two new tests: JetStream redirect round-trip; request-reply redirect round-trip. | Modify (add tests) |
| `prd/event-adapter/prd.md` | Document `httplocation` extension in section 8 (response event format) and section 17 (request-reply reply event format). | Modify |
| `prd/event-adapter/app-developer-guide.md` | Add consumer-side guidance about 3xx + `httplocation`. | Modify |

---

## Task 1: Dispatcher — `Location` field + 3xx-only capture

**Goal:** Add `Location` to `dispatcher.Result` and populate it only when status is in `[300, 400)`. No behavior change yet for redirect-following (Task 2 handles that).

**Files:**
- Test: `event-adapter/internal/dispatcher/dispatcher_test.go`
- Modify: `event-adapter/internal/dispatcher/dispatcher.go`

- [ ] **Step 1: Write the failing tests**

Append to `event-adapter/internal/dispatcher/dispatcher_test.go`:

```go
func TestDispatchPopulatesLocationOn3xx(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("Location", "/new-path")
		return &http.Response{
			StatusCode: 307,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString("")),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-loc","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.StatusCode != 307 {
		t.Fatalf("StatusCode = %d, want 307", res.StatusCode)
	}
	if res.Location != "/new-path" {
		t.Fatalf("Location = %q, want /new-path", res.Location)
	}
}

func TestDispatchLeavesLocationEmptyWhen3xxHasNoLocationHeader(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 304,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewBufferString("")),
		}, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-nl","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Location != "" {
		t.Fatalf("Location = %q, want empty (no Location header on 304)", res.Location)
	}
}

func TestDispatchLeavesLocationEmptyOnNon3xxEvenIfLocationHeaderPresent(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("Location", "/should-be-ignored")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-200loc","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Location != "" {
		t.Fatalf("Location = %q on 200 response, want empty (3xx-only rule)", res.Location)
	}
}
```

- [ ] **Step 2: Run tests, expect compile failure on `res.Location`**

Run from `event-adapter/`:

```bash
go test ./internal/dispatcher/ -run TestDispatchPopulatesLocationOn3xx -count=1
```

Expected: `./dispatcher_test.go:NN:NN: res.Location undefined (type Result has no field or method Location)`. This confirms the test is reaching the assertion and the production code is missing the field.

- [ ] **Step 3: Implement — add field and populate**

Modify `event-adapter/internal/dispatcher/dispatcher.go`. Update the `Result` struct and the final `return` of `Dispatch()`:

```go
type Result struct {
	StatusCode  int
	ContentType string
	Body        []byte
	Location    string
}
```

Replace the final return at the end of `Dispatch()` (the line currently returning `Result{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: respBody}, nil`) with:

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

- [ ] **Step 4: Run all dispatcher tests, expect pass**

```bash
go test ./internal/dispatcher/ -count=1 -v
```

Expected: all previously-passing tests still pass; the three new tests pass.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/dispatcher/dispatcher.go event-adapter/internal/dispatcher/dispatcher_test.go
git commit -m "feat(event-adapter/dispatcher): capture Location header on 3xx responses (#11)"
```

---

## Task 2: Dispatcher — default client stops following redirects

**Goal:** When `dispatcher.New(...)` is called with `nil` client, install a `CheckRedirect` that returns `http.ErrUseLastResponse`. Tests that supply their own client are unaffected. This task uses `httptest.NewServer` (not the `roundTripFunc` mock) so we exercise the real `http.Client` redirect machinery.

**Files:**
- Test: `event-adapter/internal/dispatcher/dispatcher_test.go`
- Modify: `event-adapter/internal/dispatcher/dispatcher.go`

- [ ] **Step 1: Write the failing test**

Append to `event-adapter/internal/dispatcher/dispatcher_test.go`. Add imports as needed (`net/http/httptest`, `sync/atomic`):

```go
func TestDispatchDefaultClientDoesNotFollowRedirects(t *testing.T) {
	var targetHits int64

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/landed")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-noredir","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	// Pass nil client so dispatcher uses its default.
	d := New(origin.URL, nil)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/", Timeout: 2 * time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("StatusCode = %d, want 307 (must not have followed)", res.StatusCode)
	}
	if got := atomic.LoadInt64(&targetHits); got != 0 {
		t.Fatalf("targetHits = %d, want 0 (default client must not follow redirects)", got)
	}
	if res.Location != target.URL+"/landed" {
		t.Fatalf("Location = %q, want %q", res.Location, target.URL+"/landed")
	}
}
```

- [ ] **Step 2: Run test, expect failure (target was hit)**

```bash
go test ./internal/dispatcher/ -run TestDispatchDefaultClientDoesNotFollowRedirects -count=1 -v
```

Expected: `targetHits = 1, want 0` — the current default `http.Client` follows the redirect. This confirms the test bites before the fix.

- [ ] **Step 3: Implement — install `CheckRedirect` on the default path**

In `event-adapter/internal/dispatcher/dispatcher.go`, replace the body of `New()`:

```go
func New(baseURL string, client *http.Client) *Dispatcher {
	if client == nil {
		client = &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}
	}
	return &Dispatcher{baseURL: strings.TrimRight(baseURL, "/"), client: client}
}
```

- [ ] **Step 4: Run all dispatcher tests, expect pass**

```bash
go test ./internal/dispatcher/ -count=1 -v
```

Expected: all tests pass, including the new one.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/dispatcher/dispatcher.go event-adapter/internal/dispatcher/dispatcher_test.go
git commit -m "feat(event-adapter/dispatcher): default client stops following redirects (#11)"
```

---

## Task 3: cloudevent — `BuildResponse` accepts `location` and emits `httplocation`; processor wires it

**Goal:** `BuildResponse` gains a `location string` parameter and emits `httplocation` extension when non-empty. Existing callers and tests are updated atomically in the same commit so `main` remains buildable at every step.

**Files:**
- Test: `event-adapter/internal/cloudevent/response_test.go`
- Test: `event-adapter/internal/processor/processor_test.go`
- Modify: `event-adapter/internal/cloudevent/response.go`
- Modify: `event-adapter/internal/processor/processor.go`

- [ ] **Step 1: Write the failing tests (cloudevent)**

Append to `event-adapter/internal/cloudevent/response_test.go`:

```go
func TestBuildResponseSetsHTTPLocationWhenNonEmpty(t *testing.T) {
	in := newInputEvent(t, "evt-loc-1")
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "x.processed", Source: "task-service", Subject: "out"},
	}

	out, err := BuildResponse(in, route, 307, "application/json", []byte(""), "/new-path")
	if err != nil {
		t.Fatalf("BuildResponse: %v", err)
	}
	got, ok := out.Extensions()["httplocation"]
	if !ok {
		t.Fatalf("expected httplocation extension to be set")
	}
	if got != "/new-path" {
		t.Fatalf("httplocation = %v, want /new-path", got)
	}
}

func TestBuildResponseOmitsHTTPLocationWhenEmpty(t *testing.T) {
	in := newInputEvent(t, "evt-loc-2")
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "x.processed", Source: "task-service", Subject: "out"},
	}

	out, err := BuildResponse(in, route, 200, "application/json", []byte(`{"ok":true}`), "")
	if err != nil {
		t.Fatalf("BuildResponse: %v", err)
	}
	if _, present := out.Extensions()["httplocation"]; present {
		t.Fatalf("httplocation extension must not be set when location is empty")
	}
}
```

If `newInputEvent` does not yet exist in this test file, also add this helper at the bottom of the file:

```go
func newInputEvent(t *testing.T, id string) *Event {
	t.Helper()
	raw := []byte(`{"specversion":"1.0","id":"` + id + `","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	in, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse input event: %v", err)
	}
	return in
}
```

If the existing tests already use a similar helper, reuse it instead — search for `Parse(` in `response_test.go` and follow the local convention.

- [ ] **Step 2: Run tests, expect compile failure (wrong arg count)**

```bash
go test ./internal/cloudevent/ -count=1 -v
```

Expected: `not enough arguments in call to BuildResponse` on the new tests AND on the existing `TestBuildResponseUsesDeterministicIDAndCausation` / `TestBuildResponseStampsErrorStatus`. We will fix all of them atomically below.

- [ ] **Step 3: Implement — update `BuildResponse` signature and add `httplocation`**

Modify `event-adapter/internal/cloudevent/response.go`. Change the `BuildResponse` signature and add the extension emission near the other `SetExtension` calls:

```go
func BuildResponse(in *Event, route config.RouteConfig, status int, contentType string, body []byte, location string) (*ce.Event, error) {
	if in == nil {
		return nil, fmt.Errorf("response: incoming event is nil")
	}
	out := ce.New()
	out.SetID(deterministicID(in.ID(), route.Name, route.Response.Type, route.Response.Subject))
	out.SetType(route.Response.Type)
	out.SetSource(route.Response.Source)
	out.SetSubject(route.Response.Subject)
	out.SetTime(time.Now().UTC())
	if route.Response.DataSchema != "" {
		out.SetDataSchema(route.Response.DataSchema)
	}
	if err := setHTTPData(&out, contentType, body); err != nil {
		return nil, fmt.Errorf("response: %w", err)
	}
	out.SetExtension("httpstatus", int32(status))
	out.SetExtension("causationid", in.ID())
	if corr, ok := in.Extensions()["correlationid"]; ok {
		out.SetExtension("correlationid", corr)
	}
	if location != "" {
		out.SetExtension("httplocation", location)
	}
	return &out, nil
}
```

- [ ] **Step 4: Update existing `BuildResponse` callers and tests**

In `event-adapter/internal/cloudevent/response_test.go`, update the two existing `BuildResponse` call sites in `TestBuildResponseUsesDeterministicIDAndCausation` and `TestBuildResponseStampsErrorStatus` to pass `""` as the final argument. Example for the first:

```go
a, err := BuildResponse(wrapped, route, 200, "application/json", []byte(`{"ok":true}`), "")
```

(Repeat for the second `BuildResponse` call in the same test and for the call in `TestBuildResponseStampsErrorStatus`.)

In `event-adapter/internal/processor/processor.go`, update the single call site (currently at line 71):

```go
resp, buildErr := clevent.BuildResponse(ev, route, res.StatusCode, res.ContentType, res.Body, res.Location)
```

- [ ] **Step 5: Add a processor test for end-to-end Location wiring**

Append to `event-adapter/internal/processor/processor_test.go`:

```go
func TestProcessor3xxWithLocationPublishesHTTPLocation(t *testing.T) {
	// Wire a Process call where Dispatcher returns 307 + Location and assert
	// the published response event carries the httplocation extension.
	disp := &fakeProcessorDispatcher{res: dispatcher.Result{
		StatusCode:  307,
		ContentType: "",
		Body:        []byte(""),
		Location:    "/new-path",
	}}
	pub := &fakeProcessorPublisher{}
	p := New(disp, pub)

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-p-3xx","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	route := config.RouteConfig{
		Name:     "task-created",
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second},
		Response: config.ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.x.processed"},
		Retry:    config.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	}

	msg := &fakeMessageHandle{}
	if err := p.Process(context.Background(), "t.x.created", ev, route, msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if pub.lastResponse == nil {
		t.Fatal("expected publisher to receive a response event")
	}
	got, ok := pub.lastResponse.Extensions()["httplocation"]
	if !ok {
		t.Fatal("httplocation extension missing on published response event")
	}
	if got != "/new-path" {
		t.Fatalf("httplocation = %v, want /new-path", got)
	}
}
```

If `fakeProcessorDispatcher`, `fakeProcessorPublisher`, and `fakeMessageHandle` do not already exist in the test file, follow the patterns used by existing tests in `processor_test.go` for these fakes — copy and rename, do not invent new shapes. The fakes only need to record their last call.

- [ ] **Step 6: Run cloudevent + processor tests, expect pass**

```bash
go test ./internal/cloudevent/ ./internal/processor/ -count=1 -v
```

Expected: all tests pass, including the new ones.

- [ ] **Step 7: Commit**

```bash
git add event-adapter/internal/cloudevent/response.go event-adapter/internal/cloudevent/response_test.go event-adapter/internal/processor/processor.go event-adapter/internal/processor/processor_test.go
git commit -m "feat(event-adapter): emit httplocation on response events for 3xx (#11)"
```

---

## Task 4: cloudevent — `BuildReply` accepts `location`; responder wires it

**Goal:** Symmetric to Task 3 for the request-reply path. `BuildReply` gains the same parameter; `responder.go` passes `res.Location` on the success path and `""` on the dispatch-error path (no app response available).

**Files:**
- Test: `event-adapter/internal/cloudevent/response_test.go`
- Test: `event-adapter/internal/responder/responder_test.go`
- Modify: `event-adapter/internal/cloudevent/response.go`
- Modify: `event-adapter/internal/responder/responder.go`

- [ ] **Step 1: Write the failing tests**

Append to `event-adapter/internal/cloudevent/response_test.go`:

```go
func TestBuildReplySetsHTTPLocationWhenNonEmpty(t *testing.T) {
	in := newInputEvent(t, "req-loc-1")
	reply := config.ReplyConfig{Type: "x.reply", Source: "upload-service"}

	out, err := BuildReply(in, reply, "upload-presign", 307, "application/json", []byte(""), "/elsewhere")
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	got, ok := out.Extensions()["httplocation"]
	if !ok {
		t.Fatal("expected httplocation extension on reply")
	}
	if got != "/elsewhere" {
		t.Fatalf("httplocation = %v, want /elsewhere", got)
	}
}

func TestBuildReplyOmitsHTTPLocationWhenEmpty(t *testing.T) {
	in := newInputEvent(t, "req-loc-2")
	reply := config.ReplyConfig{Type: "x.reply", Source: "upload-service"}

	out, err := BuildReply(in, reply, "upload-presign", 200, "application/json", []byte(`{"ok":true}`), "")
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	if _, present := out.Extensions()["httplocation"]; present {
		t.Fatal("httplocation extension must be absent when location is empty")
	}
}
```

Append to `event-adapter/internal/responder/responder_test.go`:

```go
func TestHandle3xxWithLocationRepliesWithHTTPLocation(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{
		StatusCode:  307,
		ContentType: "",
		Body:        []byte(""),
		Location:    "/moved",
	}}
	r := newResponder(d, &fakeMetrics{})
	m, out := capture()
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 307 {
		t.Fatalf("httpstatus = %v, want 307", reply["httpstatus"])
	}
	got, ok := reply["httplocation"].(string)
	if !ok {
		t.Fatalf("httplocation missing from reply: %v", reply)
	}
	if got != "/moved" {
		t.Fatalf("httplocation = %q, want /moved", got)
	}
}
```

- [ ] **Step 2: Run, expect compile failure on `BuildReply` arg count**

```bash
go test ./internal/cloudevent/ ./internal/responder/ -count=1 -v
```

Expected: `not enough arguments in call to BuildReply` on the new cloudevent tests and inside `responder.go` (existing call sites use the old signature).

- [ ] **Step 3: Implement — update `BuildReply` signature and add `httplocation`**

Modify `event-adapter/internal/cloudevent/response.go`:

```go
func BuildReply(in *Event, reply config.ReplyConfig, routeName string, status int, contentType string, body []byte, location string) (*ce.Event, error) {
	if in == nil {
		return nil, fmt.Errorf("reply: incoming event is nil")
	}
	out := ce.New()
	out.SetID(deterministicID(in.ID(), routeName, reply.Type))
	out.SetType(reply.Type)
	out.SetSource(reply.Source)
	out.SetTime(time.Now().UTC())
	if reply.DataSchema != "" {
		out.SetDataSchema(reply.DataSchema)
	}
	if err := setHTTPData(&out, contentType, body); err != nil {
		return nil, fmt.Errorf("reply: %w", err)
	}
	out.SetExtension("httpstatus", int32(status))
	out.SetExtension("causationid", in.ID())
	if corr, ok := in.Extensions()["correlationid"]; ok {
		out.SetExtension("correlationid", corr)
	}
	if location != "" {
		out.SetExtension("httplocation", location)
	}
	return &out, nil
}
```

- [ ] **Step 4: Update `responder.go` call sites**

In `event-adapter/internal/responder/responder.go`, the two `BuildReply` call sites inside `handle()`:

Dispatch-error path (currently calls `BuildReply(ev, route.Reply, route.Name, status, "application/json", errorBody(derr.Error()))`):

```go
reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, status, "application/json", errorBody(derr.Error()), "")
```

Success path (currently calls `BuildReply(ev, route.Reply, route.Name, res.StatusCode, res.ContentType, res.Body)`):

```go
reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, res.StatusCode, res.ContentType, res.Body, res.Location)
```

- [ ] **Step 5: Run cloudevent + responder tests, expect pass**

```bash
go test ./internal/cloudevent/ ./internal/responder/ -count=1 -v
```

Expected: all tests pass, including the new ones.

- [ ] **Step 6: Sanity — full unit test sweep, expect pass**

```bash
go test ./... -count=1
```

Expected: every package passes. Catches any other consumer of `BuildResponse` or `BuildReply` we might have missed.

- [ ] **Step 7: Commit**

```bash
git add event-adapter/internal/cloudevent/response.go event-adapter/internal/cloudevent/response_test.go event-adapter/internal/responder/responder.go event-adapter/internal/responder/responder_test.go
git commit -m "feat(event-adapter): emit httplocation on request-reply replies for 3xx (#11)"
```

---

## Task 5: mock-app — support a `Location` field on the handler `response` block

**Goal:** Extend the existing `Response` struct with `Location string` so a handler can declare a redirect for e2e tests. No new `respondWith` block — extend what is already there.

**Files:**
- Modify: `event-adapter/cmd/mock-app/main.go`

- [ ] **Step 1: Add the field**

In `event-adapter/cmd/mock-app/main.go`, update `HandlerResponse`:

```go
type HandlerResponse struct {
	Status      int    `yaml:"status"`
	ContentType string `yaml:"contentType"`
	Body        string `yaml:"body"`
	Location    string `yaml:"location"`
}
```

- [ ] **Step 2: Write the Location header when present**

In `event-adapter/cmd/mock-app/main.go`, inside `makeHandler`, just before the `WriteHeader` call, add:

```go
if h.Response.Location != "" {
	w.Header().Set("Location", h.Response.Location)
}
```

The change goes between the `if h.Response.ContentType != "" { ... }` block and the `status := cmp.Or(...)` line so the header is set before status. Keep the existing `WriteHeader` call as-is.

- [ ] **Step 3: Build the binary to confirm it compiles**

```bash
go build ./cmd/mock-app/
```

Expected: clean exit, no output. (No unit tests exist for mock-app; e2e in Task 6/7 exercises the new behavior.)

- [ ] **Step 4: Commit**

```bash
git add event-adapter/cmd/mock-app/main.go
git commit -m "feat(event-adapter/mock-app): support Location header in handler response (#11)"
```

---

## Task 6: e2e — JetStream redirect round-trip

**Goal:** Add an e2e test asserting that when the JetStream-path handler returns 307 + Location, the response CloudEvent on the response subject carries `httpstatus: 307` and `httplocation`, and the mock-app's redirect target is never invoked.

**Files:**
- Modify: `event-adapter/test/e2e/mock-app.yaml`
- Modify: `event-adapter/test/e2e/routes.yaml`
- Create: `event-adapter/test/e2e/fixtures/redirect-jetstream.json`
- Modify: `event-adapter/test/e2e/e2e_test.go`

- [ ] **Step 1: Add a redirect handler to mock-app.yaml**

Append to `event-adapter/test/e2e/mock-app.yaml`:

```yaml
  - method: POST
    path: /events/redirect-me
    requireHeaders:
      - X-Workspace-Actor-Id
    response:
      status: 307
      location: /events/post-redirect
  - method: POST
    path: /events/post-redirect
    response:
      status: 200
      contentType: application/json
      body: '{"reached":"target"}'
```

The second handler exists only so that if the adapter ever started following redirects again, the test would observe the wrong status code. We never expect it to be hit.

- [ ] **Step 2: Add a JetStream route + response subject for the redirect handler**

In `event-adapter/test/e2e/routes.yaml`, add a new entry to the existing `routes:` block (preserving all current routes). Match what the existing routes look like — type, subject, response subject — and target `/events/redirect-me`. Concretely:

```yaml
  - name: task-created-redirect
    match:
      type: com.workspace.task.created.redirect
    subject: t.tenant-a.app.task.event.created
    dispatch:
      method: POST
      path: /events/redirect-me
    response:
      type: com.workspace.task.created.redirect.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed.redirect
```

If `routes.yaml` uses different field names or layout, follow the file's current convention rather than this snippet verbatim. Both the input subject and the response subject must be on the existing `workspace-events` JetStream stream (see `test/e2e/docker-compose.yaml` `nats-setup`). If the response subject is new, also add it to the stream's `--subjects` list in the docker-compose nats-setup command.

- [ ] **Step 3: Create the fixture**

Create `event-adapter/test/e2e/fixtures/redirect-jetstream.json`:

```json
{
  "specversion": "1.0",
  "id": "redir-jet-1",
  "source": "workspace/task",
  "type": "com.workspace.task.created.redirect",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1"
  },
  "data": {
    "taskId": "redir-1"
  }
}
```

- [ ] **Step 4: Add the e2e test**

Append to `event-adapter/test/e2e/e2e_test.go`:

```go
func TestJetStreamRedirectPublishesHTTPLocation(t *testing.T) {
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

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed.redirect")
	if err != nil {
		t.Fatalf("subscribe redirect response subject: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	fixture, err := os.ReadFile("fixtures/redirect-jetstream.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	if _, err := js.Publish("t.tenant-a.app.task.event.created", fixture); err != nil {
		t.Fatalf("publish input event: %v", err)
	}

	msg, err := sub.NextMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("waiting for redirect response: %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	status, ok := response["httpstatus"].(float64)
	if !ok || status != 307 {
		t.Fatalf("httpstatus = %v, want 307", response["httpstatus"])
	}
	loc, ok := response["httplocation"].(string)
	if !ok {
		t.Fatalf("httplocation missing from response event: %v", response)
	}
	if loc != "/events/post-redirect" {
		t.Fatalf("httplocation = %q, want /events/post-redirect", loc)
	}
}
```

`ensureEmptyStream` from the existing test file may need its `Subjects` list extended to include `t.tenant-a.app.task.event.processed.redirect` so the helper does not drop it on rerun. Add the new subject to that slice in the existing `ensureEmptyStream` helper.

- [ ] **Step 5: Run the e2e test against the live stack**

From `event-adapter/test/e2e/`:

```bash
docker compose up --build -d --wait
cd ../..  # back to event-adapter/
go test ./test/e2e/ -tags=e2e -run TestJetStreamRedirectPublishesHTTPLocation -count=1 -v
```

Expected: PASS.

- [ ] **Step 6: Confirm the redirect target was never hit (mock-app logs)**

```bash
docker compose -f test/e2e/docker-compose.yaml logs mock-app | grep "/events/post-redirect" || echo "OK: target never invoked"
```

Expected: `OK: target never invoked`. If grep matches, the dispatcher followed the redirect — fix Task 2 first.

- [ ] **Step 7: Commit**

```bash
git add event-adapter/test/e2e/mock-app.yaml event-adapter/test/e2e/routes.yaml event-adapter/test/e2e/fixtures/redirect-jetstream.json event-adapter/test/e2e/e2e_test.go
git commit -m "test(event-adapter/e2e): assert JetStream 3xx response surfaces httplocation (#11)"
```

---

## Task 7: e2e — request-reply redirect round-trip

**Goal:** Add an e2e test asserting that when the request-reply-path handler returns 307 + Location, the reply CloudEvent on the requester's inbox carries `httpstatus: 307` and `httplocation`.

**Files:**
- Modify: `event-adapter/test/e2e/routes.yaml` (add a request route only — handler reuse from Task 6)
- Create: `event-adapter/test/e2e/fixtures/redirect-reqreply.json`
- Modify: `event-adapter/test/e2e/e2e_test.go`

- [ ] **Step 1: Add a request-reply route for the redirect handler**

In `event-adapter/test/e2e/routes.yaml`, under the existing `requests:` block (or wherever request-reply routes live — follow the existing `upload-presign` route's shape exactly), add:

```yaml
  routes:
    - name: redirect-reqreply
      match:
        type: com.workspace.redirect.request
      dispatch:
        method: POST
        path: /events/redirect-me
        timeout: 2s
      reply:
        source: task-service
        type: com.workspace.redirect.reply
```

If the request-reply subject is configured under `requests.subject:`, the existing one (`q.tenant-a.app.uploads.request` in `e2e_test.go:150`) handles all request types — match by `type` not by subject. Reuse the existing subject; do not add a new one unless the e2e harness clearly distinguishes per-route subjects.

- [ ] **Step 2: Create the fixture**

Create `event-adapter/test/e2e/fixtures/redirect-reqreply.json`:

```json
{
  "specversion": "1.0",
  "id": "req-redir-1",
  "source": "frontend/web",
  "type": "com.workspace.redirect.request",
  "datacontenttype": "application/json",
  "dispatchheaders": {
    "X-Workspace-Actor-Id": "user-1"
  },
  "data": {
    "what": "wherever"
  }
}
```

- [ ] **Step 3: Add the e2e test**

Append to `event-adapter/test/e2e/e2e_test.go`:

```go
func TestRequestReplyRedirectCarriesHTTPLocation(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	fixture, err := os.ReadFile("fixtures/redirect-reqreply.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	msg, err := nc.Request("q.tenant-a.app.uploads.request", fixture, 15*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var reply map[string]any
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply["type"] != "com.workspace.redirect.reply" {
		t.Fatalf("type = %v, want com.workspace.redirect.reply", reply["type"])
	}
	status, ok := reply["httpstatus"].(float64)
	if !ok || status != 307 {
		t.Fatalf("httpstatus = %v, want 307", reply["httpstatus"])
	}
	loc, ok := reply["httplocation"].(string)
	if !ok {
		t.Fatalf("httplocation missing from reply: %v", reply)
	}
	if loc != "/events/post-redirect" {
		t.Fatalf("httplocation = %q, want /events/post-redirect", loc)
	}
}
```

If the existing request-reply test (`TestRequestReplyPresign`) uses a different subject string, copy it verbatim — the request subject must match what the responder is configured to subscribe to.

- [ ] **Step 4: Rebuild and rerun e2e**

From `event-adapter/test/e2e/`:

```bash
docker compose up --build -d --wait
cd ../..
go test ./test/e2e/ -tags=e2e -run TestRequestReplyRedirectCarriesHTTPLocation -count=1 -v
```

Expected: PASS.

- [ ] **Step 5: Full e2e sweep, expect pass**

```bash
go test ./test/e2e/ -tags=e2e -count=1
```

Expected: all e2e tests pass, including the new ones from Task 6 and 7 plus the pre-existing ones.

- [ ] **Step 6: Commit**

```bash
git add event-adapter/test/e2e/routes.yaml event-adapter/test/e2e/fixtures/redirect-reqreply.json event-adapter/test/e2e/e2e_test.go
git commit -m "test(event-adapter/e2e): assert request-reply 3xx reply surfaces httplocation (#11)"
```

---

## Task 8: PRD — document `httplocation` extension

**Goal:** Add a one-line entry for `httplocation` in both the JetStream response event format section (section 8) and the request-reply reply event format section (section 17).

**Files:**
- Modify: `prd/event-adapter/prd.md`

- [ ] **Step 1: Find the `httpstatus` lines and add `httplocation` next to each**

In `prd/event-adapter/prd.md`, locate the bullet around line 252 in section 8 that reads:

> - A `httpstatus` extension carrying the HTTP status code returned by the app. Consumers use this to distinguish success (`2xx`/`3xx`) from error (`4xx`/`5xx`).

Add a new bullet immediately below it:

> - A `httplocation` extension carrying the value of the HTTP `Location` response header. Populated only when the app returns a `3xx` status; absent otherwise. The sidecar does not follow redirects — consumers receive the redirect intent and decide how to act on it.

In section 17 (request-reply), locate the equivalent reply-event-format section. The current text around line 410 reads:

> 6. The responder builds a reply CloudEvent (response body as `data`, HTTP status in `httpstatus`, `causationid` = request id, `correlationid` passed through) and sends it on the caller's reply inbox.

Replace this bullet with:

> 6. The responder builds a reply CloudEvent (response body as `data`, HTTP status in `httpstatus`, `httplocation` carrying the `Location` header when the app returns a `3xx`, `causationid` = request id, `correlationid` passed through) and sends it on the caller's reply inbox.

- [ ] **Step 2: Commit**

```bash
git add prd/event-adapter/prd.md
git commit -m "docs(event-adapter/prd): document httplocation extension on response and reply (#11)"
```

---

## Task 9: App developer guide — consumer-side guidance on 3xx + `httplocation`

**Goal:** Add a short paragraph after the existing `httpstatus` guidance explaining how to use 3xx + `Location` from the app side.

**Files:**
- Modify: `prd/event-adapter/app-developer-guide.md`

- [ ] **Step 1: Add guidance after the `httpstatus` paragraph**

In `prd/event-adapter/app-developer-guide.md`, locate the paragraph around line 132 that reads:

> The sidecar publishes a response event for every HTTP response (success or error) and carries the status code in the `httpstatus` CloudEvent extension. The sidecar does not retry on `4xx` or `5xx` — if you need a retry on a transient failure, do it inside the handler before returning. Only network-class failures (timeout, connection refused, TLS error) are retried by the sidecar.

Add immediately after it:

> If your handler returns a `3xx` redirect status with a `Location` header, the sidecar publishes the response event (or reply, for request-reply routes) with both `httpstatus` and an `httplocation` extension carrying the header value. The sidecar does not follow the redirect — consumers see the redirect intent and decide what to do with it. This applies to all `3xx` codes (301, 302, 303, 307, 308); if you return `3xx` without a `Location` header, only `httpstatus` is set.

- [ ] **Step 2: Commit**

```bash
git add prd/event-adapter/app-developer-guide.md
git commit -m "docs(event-adapter): app guide for returning 3xx + Location (#11)"
```

---

## Self-review notes (for the implementer)

Before opening the PR, run this final pass:

- [ ] **Full test suite green:** `go test ./... -count=1` and `go test ./test/e2e/ -tags=e2e -count=1`
- [ ] **Linter clean:** whatever the repo's linter is (check `.golangci.yml` if present)
- [ ] **No leftover TODOs in the diff:** `git diff main -- '*.go' | grep -i todo` should be empty
- [ ] **All commits referenced #11:** `git log main.. --oneline | grep -v '#11'` should be empty
- [ ] **Spec is the source of truth:** if implementation differs from the spec (e.g. an edge case discovered during coding), update the spec in the same PR with a brief note explaining why

## Out of scope (do not implement)

- Validation, sanitization, or normalization of the `Location` value.
- Capturing the `Location` header on non-3xx responses.
- Supporting `Location` on `BuildErrorReply` (no app response exists at that point).
- A configuration flag to opt out of stopping redirects.
- Forwarding any other 3xx-related response headers (e.g. `Retry-After`).
