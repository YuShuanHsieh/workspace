# event-adapter Request-Reply Responder Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a NATS request-reply responder to the event-adapter sidecar so a backend app can answer request-reply calls over its existing loopback HTTP handlers, with no NATS code of its own.

**Architecture:** A new `responder` package subscribes to a core-NATS subject (queue group), parses the request CloudEvent, matches it by `type`, dispatches it to the local app over HTTP (the existing dispatcher, refactored to take a `DispatchConfig`), and replies on the request's inbox with a CloudEvent built from the HTTP response. It runs concurrently with the existing JetStream consumer; either path may be configured independently. No ack/Nak/retry/DLQ on the request path — failures return an error reply the caller may retry.

**Tech Stack:** Go 1.25, `github.com/nats-io/nats.go` (core NATS `QueueSubscribe`/`Respond`), `github.com/cloudevents/sdk-go/v2`, OpenTelemetry metrics, `gopkg.in/yaml.v3`.

**Spec:** `docs/superpowers/specs/2026-06-07-event-adapter-req-reply-design.md`

---

## File Structure

All paths are under `event-adapter/`.

| File | Responsibility | Change |
|---|---|---|
| `internal/config/schema.go` | Config structs | Add `RequestsConfig`, `RequestRouteConfig`, `ReplyConfig`; add `Requests *RequestsConfig` to `Config` |
| `internal/config/validate.go` | Config validation | Extract `validateDispatch`; gate JetStream validation on routes; add request validation; require ≥1 path |
| `internal/dispatcher/dispatcher.go` | HTTP dispatch | Change `Dispatch` to take `config.DispatchConfig`; `setPublisherHeaders` takes `DispatchConfig` |
| `internal/processor/processor.go` | Event processing | Update `Dispatcher` interface + call site to pass `route.Dispatch` |
| `internal/cloudevent/response.go` | Reply/response builders | Add `BuildReply`, `BuildErrorReply`; extract shared data helper |
| `internal/router/matcher.go` | Route matching | Add `RequestMatcher` + `NewRequests` |
| `internal/metrics/metrics.go` | Metrics | Add 5 request-reply counters + methods |
| `internal/natsjs/client.go` | NATS client | Add `RequestMsg` + `SubscribeRequests` |
| `internal/responder/responder.go` | Request-reply loop (new) | New package: worker pool, parse→match→dispatch→reply |
| `cmd/event-adapter/main.go` | Wiring | Start responder and/or consumer based on config |
| `test/e2e/mock-app.yaml` | e2e mock app | Add `/requests/upload-presign` handler |
| `test/e2e/routes.yaml` | e2e adapter config | Add `requests:` block |
| `test/e2e/fixtures/upload-presign.json` | e2e fixture (new) | Request CloudEvent |
| `test/e2e/e2e_test.go` | e2e test | Add request-reply round-trip test |
| `test/e2e/README.md` | e2e docs | Document the request-reply test |

---

## Task 1: Config structs for request-reply

**Files:**
- Modify: `internal/config/schema.go`
- Test: `internal/config/schema_test.go` (create if absent)

- [ ] **Step 1: Write the failing test**

Create/append in `internal/config/schema_test.go`:

```go
package config

import (
	"testing"
	"time"
)

func TestParseRequestsBlock(t *testing.T) {
	raw := []byte(`
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: upload-responders
  workerPoolSize: 8
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request
      dispatch:
        method: POST
        path: /requests/upload-presign
        timeout: 3s
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Requests == nil {
		t.Fatal("Requests is nil")
	}
	if cfg.Requests.Subject != "q.tenant-a.app.uploads.request" {
		t.Errorf("subject = %q", cfg.Requests.Subject)
	}
	if cfg.Requests.QueueGroup != "upload-responders" {
		t.Errorf("queueGroup = %q", cfg.Requests.QueueGroup)
	}
	if cfg.Requests.WorkerPoolSize != 8 {
		t.Errorf("workerPoolSize = %d", cfg.Requests.WorkerPoolSize)
	}
	if len(cfg.Requests.Routes) != 1 {
		t.Fatalf("routes len = %d", len(cfg.Requests.Routes))
	}
	r := cfg.Requests.Routes[0]
	if r.Match.Type != "com.workspace.uploads.presign.request" {
		t.Errorf("match.type = %q", r.Match.Type)
	}
	if r.Dispatch.Timeout != 3*time.Second {
		t.Errorf("dispatch.timeout = %v", r.Dispatch.Timeout)
	}
	if r.Reply.Type != "com.workspace.uploads.presign.reply" || r.Reply.Source != "upload-service" {
		t.Errorf("reply = %+v", r.Reply)
	}
}

func TestParseRequestRouteRejectsResponseKey(t *testing.T) {
	// KnownFields(true) means response/retry/dlq under a request route is a parse error.
	raw := []byte(`
app:
  id: x
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: s
  queueGroup: g
  workerPoolSize: 1
  routes:
    - name: r
      match: {type: t}
      dispatch: {method: POST, path: /x, timeout: 1s}
      reply: {source: s, type: t.reply}
      retry: {maxAttempts: 3}
`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected parse error for retry key on request route, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd event-adapter && go test ./internal/config/ -run TestParseRequest -v`
Expected: FAIL — `cfg.Requests` undefined (compile error).

- [ ] **Step 3: Add the structs**

In `internal/config/schema.go`, add `Requests` to `Config`:

```go
type Config struct {
	App      AppConfig       `yaml:"app"`
	NATS     NATSConfig      `yaml:"nats"`
	Routes   []RouteConfig   `yaml:"routes"`
	Requests *RequestsConfig `yaml:"requests"`
}
```

Then add these types at the end of the file (before `func Parse`):

```go
type RequestsConfig struct {
	Subject        string               `yaml:"subject"`
	QueueGroup     string               `yaml:"queueGroup"`
	WorkerPoolSize int                  `yaml:"workerPoolSize"`
	Routes         []RequestRouteConfig `yaml:"routes"`
}

type RequestRouteConfig struct {
	Name     string         `yaml:"name"`
	Match    MatchConfig    `yaml:"match"`
	Dispatch DispatchConfig `yaml:"dispatch"`
	Reply    ReplyConfig    `yaml:"reply"`
}

type ReplyConfig struct {
	Source     string `yaml:"source"`
	Type       string `yaml:"type"`
	DataSchema string `yaml:"dataschema"`
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd event-adapter && go test ./internal/config/ -run TestParseRequest -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/config/schema.go event-adapter/internal/config/schema_test.go
git commit -m "feat(config): add request-reply config structs"
```

---

## Task 2: Config validation — gate JetStream, validate requests, require ≥1 path

**Files:**
- Modify: `internal/config/validate.go`
- Test: `internal/config/validate_test.go` (append; create if absent)

- [ ] **Step 1: Write the failing test**

Append to `internal/config/validate_test.go`:

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func baseRequests() *RequestsConfig {
	return &RequestsConfig{
		Subject:        "q.tenant-a.app.uploads.request",
		QueueGroup:     "upload-responders",
		WorkerPoolSize: 4,
		Routes: []RequestRouteConfig{{
			Name:     "upload-presign",
			Match:    MatchConfig{Type: "com.workspace.uploads.presign.request"},
			Dispatch: DispatchConfig{Method: "POST", Path: "/requests/upload-presign", Timeout: time.Second},
			Reply:    ReplyConfig{Source: "upload-service", Type: "com.workspace.uploads.presign.reply"},
		}},
	}
}

func TestValidatePureResponder(t *testing.T) {
	cfg := &Config{
		App:      AppConfig{ID: "upload-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: baseRequests(),
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRejectsNoPath(t *testing.T) {
	cfg := &Config{
		App:  AppConfig{ID: "x", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{URL: "nats://127.0.0.1:4222"},
	}
	errs := Validate(cfg)
	if !hasErr(errs, "at least one of routes or requests") {
		t.Fatalf("expected at-least-one-path error, got %v", errs)
	}
}

func TestValidateRequestRouteFields(t *testing.T) {
	reqs := baseRequests()
	reqs.Routes[0].Reply.Type = ""
	reqs.Subject = ""
	cfg := &Config{
		App:      AppConfig{ID: "x", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: reqs,
	}
	errs := Validate(cfg)
	if !hasErr(errs, "requests.subject") {
		t.Errorf("expected requests.subject error, got %v", errs)
	}
	if !hasErr(errs, "reply.type") {
		t.Errorf("expected reply.type error, got %v", errs)
	}
}

func TestValidateDuplicateRequestType(t *testing.T) {
	reqs := baseRequests()
	dup := reqs.Routes[0]
	dup.Name = "second"
	reqs.Routes = append(reqs.Routes, dup)
	cfg := &Config{
		App:      AppConfig{ID: "x", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: reqs,
	}
	if !hasErr(Validate(cfg), "duplicate match type") {
		t.Fatalf("expected duplicate type error")
	}
}

func hasErr(errs []error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}
```

> If `validate_test.go` already defines a `hasErr` helper, reuse it and drop the duplicate here.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd event-adapter && go test ./internal/config/ -run TestValidate -v`
Expected: FAIL — pure-responder config currently errors on missing `nats.stream`, `routes`, etc.

- [ ] **Step 3: Rewrite `Validate` to gate paths**

Replace the body of `Validate` in `internal/config/validate.go` from the `if cfg.NATS.URL == "" {` block through the route loop (lines ~65-116) with this. Keep the `app.id`, `app.httpBaseURL`, and final `return errs` exactly as they are:

```go
	if cfg.NATS.URL == "" {
		errs = append(errs, ValidationError{Path: "nats.url", Msg: "is required"})
	}

	jetStreamEnabled := len(cfg.Routes) > 0
	requestsEnabled := cfg.Requests != nil

	if !jetStreamEnabled && !requestsEnabled {
		errs = append(errs, ValidationError{Path: "routes", Msg: "at least one of routes or requests must be configured"})
	}

	if jetStreamEnabled {
		errs = append(errs, validateJetStream(cfg)...)
	}
	if requestsEnabled {
		errs = append(errs, validateRequests(cfg.Requests)...)
	}
```

Add `validateJetStream` (this is the existing NATS+routes validation, moved verbatim into its own function). Insert after `Validate`:

```go
func validateJetStream(cfg *Config) []error {
	var errs []error
	if cfg.NATS.Stream == "" {
		errs = append(errs, ValidationError{Path: "nats.stream", Msg: "is required"})
	}
	if cfg.NATS.DurableConsumer == "" {
		errs = append(errs, ValidationError{Path: "nats.durableConsumer", Msg: "is required"})
	}
	if cfg.NATS.FilterSubject == "" {
		errs = append(errs, ValidationError{Path: "nats.filterSubject", Msg: "is required"})
	}
	if cfg.NATS.WorkerPoolSize <= 0 {
		errs = append(errs, ValidationError{Path: "nats.workerPoolSize", Msg: "must be positive"})
	}
	if cfg.NATS.FetchBatch <= 0 {
		errs = append(errs, ValidationError{Path: "nats.fetchBatch", Msg: "must be positive"})
	}
	if cfg.NATS.AckWait <= 0 {
		errs = append(errs, ValidationError{Path: "nats.ackWait", Msg: "must be positive"})
	}
	if cfg.NATS.MaxDeliver <= 0 {
		errs = append(errs, ValidationError{Path: "nats.maxDeliver", Msg: "must be positive"})
	}
	if cfg.NATS.MaxAckPending <= 0 {
		errs = append(errs, ValidationError{Path: "nats.maxAckPending", Msg: "must be positive"})
	}
	if cfg.NATS.FetchBatch > 0 && cfg.NATS.WorkerPoolSize > 0 && cfg.NATS.FetchBatch > cfg.NATS.WorkerPoolSize {
		errs = append(errs, ValidationError{Path: "nats.fetchBatch", Msg: "must not exceed nats.workerPoolSize"})
	}
	if cfg.NATS.WorkerPoolSize > 0 && cfg.NATS.MaxAckPending > 0 && cfg.NATS.WorkerPoolSize > cfg.NATS.MaxAckPending {
		errs = append(errs, ValidationError{Path: "nats.workerPoolSize", Msg: "must not exceed nats.maxAckPending"})
	}
	if cfg.NATS.DefaultDLQSubject == "" {
		errs = append(errs, ValidationError{Path: "nats.defaultDLQSubject", Msg: "is required"})
	}
	seen := make(map[string]int, len(cfg.Routes))
	for i, r := range cfg.Routes {
		errs = append(errs, validateRoute(fmt.Sprintf("routes[%d]", i), r)...)
		if j, ok := seen[r.Match.Type]; ok {
			errs = append(errs, ValidationError{
				Path: fmt.Sprintf("routes[%d].match", i),
				Msg:  fmt.Sprintf("duplicate match type already defined at routes[%d]", j),
			})
		} else {
			seen[r.Match.Type] = i
		}
	}
	return errs
}

func validateRequests(rc *RequestsConfig) []error {
	var errs []error
	if rc.Subject == "" {
		errs = append(errs, ValidationError{Path: "requests.subject", Msg: "is required"})
	}
	if rc.QueueGroup == "" {
		errs = append(errs, ValidationError{Path: "requests.queueGroup", Msg: "is required"})
	}
	if rc.WorkerPoolSize <= 0 {
		errs = append(errs, ValidationError{Path: "requests.workerPoolSize", Msg: "must be positive"})
	}
	if len(rc.Routes) == 0 {
		errs = append(errs, ValidationError{Path: "requests.routes", Msg: "must contain at least one route"})
	}
	seen := make(map[string]int, len(rc.Routes))
	for i, r := range rc.Routes {
		prefix := fmt.Sprintf("requests.routes[%d]", i)
		if r.Name == "" {
			errs = append(errs, ValidationError{Path: prefix + ".name", Msg: "is required"})
		}
		if r.Match.Type == "" {
			errs = append(errs, ValidationError{Path: prefix + ".match.type", Msg: "is required"})
		}
		errs = append(errs, validateDispatch(prefix, r.Dispatch)...)
		if r.Reply.Type == "" {
			errs = append(errs, ValidationError{Path: prefix + ".reply.type", Msg: "is required"})
		}
		if r.Reply.Source == "" {
			errs = append(errs, ValidationError{Path: prefix + ".reply.source", Msg: "is required"})
		}
		if j, ok := seen[r.Match.Type]; ok {
			errs = append(errs, ValidationError{Path: prefix + ".match", Msg: fmt.Sprintf("duplicate match type already defined at requests.routes[%d]", j)})
		} else {
			seen[r.Match.Type] = i
		}
	}
	return errs
}
```

- [ ] **Step 4: Extract `validateDispatch` and reuse it in `validateRoute`**

In `validateRoute`, replace the dispatch-method/path/timeout/headers/forwardHeaders checks (the block from `if r.Dispatch.Method != ...` through the `forwardHeaders` loop) with a single call, then add the helper:

```go
func validateRoute(prefix string, r RouteConfig) []error {
	var errs []error
	if r.Name == "" {
		errs = append(errs, ValidationError{Path: prefix + ".name", Msg: "is required"})
	}
	if r.Match.Type == "" {
		errs = append(errs, ValidationError{Path: prefix + ".match.type", Msg: "is required"})
	}
	errs = append(errs, validateDispatch(prefix, r.Dispatch)...)
	if r.Response.Type == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.type", Msg: "is required"})
	}
	if r.Response.Source == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.source", Msg: "is required"})
	}
	if r.Response.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.subject", Msg: "is required"})
	}
	if r.Retry.MaxAttempts <= 0 {
		errs = append(errs, ValidationError{Path: prefix + ".retry.maxAttempts", Msg: "must be positive"})
	}
	if r.Retry.InitialBackoff <= 0 || r.Retry.MaxBackoff <= 0 || r.Retry.InitialBackoff > r.Retry.MaxBackoff {
		errs = append(errs, ValidationError{Path: prefix + ".retry", Msg: "initialBackoff and maxBackoff must be positive and ordered"})
	}
	if r.DLQ.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".dlq.subject", Msg: "is required"})
	}
	return errs
}

func validateDispatch(prefix string, d DispatchConfig) []error {
	var errs []error
	if d.Method != http.MethodPost && d.Method != http.MethodPut && d.Method != http.MethodPatch {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.method", Msg: "must be POST, PUT, or PATCH"})
	}
	if !strings.HasPrefix(d.Path, "/") {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.path", Msg: "must start with /"})
	}
	if d.Timeout <= 0 {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.timeout", Msg: "must be positive"})
	}
	for name := range d.Headers {
		if reservedHeaders[strings.ToLower(name)] {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.headers." + name, Msg: "reserved header cannot be overridden"})
		}
	}
	for _, name := range d.ForwardHeaders {
		if name == "" {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.forwardHeaders", Msg: "header names must be non-empty"})
			continue
		}
		if reservedHeaders[strings.ToLower(name)] {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.forwardHeaders." + name, Msg: "reserved header cannot be forwarded from publisher"})
		}
	}
	return errs
}
```

Remove the now-unused `if len(cfg.Routes) == 0` check and the old inline route loop from `Validate` (they moved into `validateJetStream`).

- [ ] **Step 5: Run all config tests**

Run: `cd event-adapter && go test ./internal/config/ -v`
Expected: PASS, including the pre-existing tests.

- [ ] **Step 6: Commit**

```bash
git add event-adapter/internal/config/validate.go event-adapter/internal/config/validate_test.go
git commit -m "feat(config): gate jetstream validation, validate requests, require one path"
```

---

## Task 3: Refactor dispatcher to take `DispatchConfig`

**Files:**
- Modify: `internal/dispatcher/dispatcher.go`
- Modify: `internal/processor/processor.go:19-21,63`
- Test: `internal/dispatcher/dispatcher_test.go` (update call sites)

- [ ] **Step 1: Update the dispatcher signature**

In `internal/dispatcher/dispatcher.go`, change `Dispatch` and `setPublisherHeaders` to take `config.DispatchConfig`:

```go
func (d *Dispatcher) Dispatch(ctx context.Context, dc config.DispatchConfig, ev *clevent.Event) (Result, error) {
	if ev == nil || ev.Event == nil {
		return Result{}, ErrNilEvent
	}
	body, err := clevent.JSONDataBytes(ev)
	if err != nil {
		return Result{}, err
	}
	u, err := url.JoinPath(d.baseURL, dc.Path)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: build url: %w", err)
	}
	if dc.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dc.Timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, dc.Method, u, bytes.NewReader(body))
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: create request: %w", err)
	}
	setCloudEventHeaders(req, ev)
	setPublisherHeaders(req, dc, ev)
	setPublisherCookies(req, ev)
	for k, v := range dc.Headers {
		req.Header.Set(k, v)
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: http call: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return Result{}, fmt.Errorf("dispatcher: read response: %w", err)
	}
	return Result{StatusCode: resp.StatusCode, ContentType: resp.Header.Get("Content-Type"), Body: respBody}, nil
}
```

Change `setPublisherHeaders` signature and its two `route.Dispatch.ForwardHeaders` references to use `dc.ForwardHeaders`:

```go
func setPublisherHeaders(req *http.Request, dc config.DispatchConfig, ev *clevent.Event) {
	if len(ev.DispatchHeaders) == 0 {
		return
	}
	if len(dc.ForwardHeaders) == 0 {
		for name, value := range ev.DispatchHeaders {
			if config.IsReservedHeader(name) {
				continue
			}
			req.Header.Set(name, value)
		}
		return
	}
	allowed := map[string]string{}
	for _, name := range dc.ForwardHeaders {
		allowed[strings.ToLower(name)] = name
	}
	for name, value := range ev.DispatchHeaders {
		canonical, ok := allowed[strings.ToLower(name)]
		if !ok {
			continue
		}
		if config.IsReservedHeader(canonical) {
			continue
		}
		req.Header.Set(canonical, value)
	}
}
```

- [ ] **Step 2: Update the processor interface and call site**

In `internal/processor/processor.go`, change the `Dispatcher` interface (lines 19-21):

```go
type Dispatcher interface {
	Dispatch(context.Context, config.DispatchConfig, *clevent.Event) (dispatcher.Result, error)
}
```

And the call site (line 63):

```go
	res, dispatchErr := p.dispatcher.Dispatch(ctx, route.Dispatch, ev)
```

- [ ] **Step 3: Update dispatcher tests**

In `internal/dispatcher/dispatcher_test.go`, change every `d.Dispatch(ctx, route, ev)` call to pass `route.Dispatch` instead of `route` (the test routes are `config.RouteConfig` values — pass their `.Dispatch` field). Do the same for any `setPublisherHeaders` direct test calls.

- [ ] **Step 4: Run dispatcher + processor tests to verify they pass**

Run: `cd event-adapter && go test ./internal/dispatcher/ ./internal/processor/ -v`
Expected: PASS. If `processor_test.go` has a mock dispatcher implementing the old interface, update its method signature to `Dispatch(context.Context, config.DispatchConfig, *clevent.Event)`.

- [ ] **Step 5: Build the whole module**

Run: `cd event-adapter && go build ./...`
Expected: no errors.

- [ ] **Step 6: Commit**

```bash
git add event-adapter/internal/dispatcher/ event-adapter/internal/processor/
git commit -m "refactor(dispatcher): accept DispatchConfig so request path can reuse it"
```

---

## Task 4: `BuildReply` and `BuildErrorReply`

**Files:**
- Modify: `internal/cloudevent/response.go`
- Test: `internal/cloudevent/response_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/cloudevent/response_test.go`:

```go
func TestBuildReplySuccess(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"req-1","source":"client","type":"com.x.request","datacontenttype":"application/json","data":{"a":1},"correlationid":"corr-9"}`)
	reply := config.ReplyConfig{Source: "upload-service", Type: "com.x.reply"}
	out, err := BuildReply(in, reply, "upload-presign", 200, "application/json", []byte(`{"url":"https://s3/put"}`))
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	if out.Type() != "com.x.reply" {
		t.Errorf("type = %q", out.Type())
	}
	if out.Source() != "upload-service" {
		t.Errorf("source = %q", out.Source())
	}
	if out.Subject() != "" {
		t.Errorf("reply must have no subject, got %q", out.Subject())
	}
	if got := out.Extensions()["httpstatus"]; got != int32(200) {
		t.Errorf("httpstatus = %v", got)
	}
	if got := out.Extensions()["causationid"]; got != "req-1" {
		t.Errorf("causationid = %v", got)
	}
	if got := out.Extensions()["correlationid"]; got != "corr-9" {
		t.Errorf("correlationid = %v", got)
	}
}

func TestBuildErrorReply(t *testing.T) {
	out := BuildErrorReply("upload-service", 400, "bad cloudevent")
	if out.Type() != ErrorReplyType {
		t.Errorf("type = %q, want %q", out.Type(), ErrorReplyType)
	}
	if out.Source() != "upload-service" {
		t.Errorf("source = %q", out.Source())
	}
	if got := out.Extensions()["httpstatus"]; got != int32(400) {
		t.Errorf("httpstatus = %v", got)
	}
	var data map[string]string
	if err := out.DataAs(&data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data["error"] != "bad cloudevent" {
		t.Errorf("error body = %v", data)
	}
}
```

> If `mustEvent` does not already exist in this test file, add a helper that wraps `Parse([]byte(s))` and `t.Fatal`s on error, returning the `*Event`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd event-adapter && go test ./internal/cloudevent/ -run 'TestBuildReply|TestBuildErrorReply' -v`
Expected: FAIL — `BuildReply` / `BuildErrorReply` / `ErrorReplyType` undefined.

- [ ] **Step 3: Implement the builders**

In `internal/cloudevent/response.go`, refactor the data-setting out of `BuildResponse` into a shared helper, and add the reply builders. Replace the data block inside `BuildResponse` (lines ~29-41) with `if err := setHTTPData(&out, contentType, body); err != nil { return nil, err }`, then append:

```go
// ErrorReplyType is the CloudEvent type used for replies the sidecar generates
// itself (parse failures, no matching route) rather than from an app response.
const ErrorReplyType = "io.eventadapter.error.reply"

// BuildReply builds a request-reply response CloudEvent from the app's HTTP
// response. Unlike BuildResponse it sets no subject — the reply travels on the
// request's inbox.
func BuildReply(in *Event, reply config.ReplyConfig, routeName string, status int, contentType string, body []byte) (*ce.Event, error) {
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
		return nil, err
	}
	out.SetExtension("httpstatus", int32(status))
	out.SetExtension("causationid", in.ID())
	if corr, ok := in.Extensions()["correlationid"]; ok {
		out.SetExtension("correlationid", corr)
	}
	return &out, nil
}

// BuildErrorReply builds a self-generated error reply when there is no app
// response to wrap (malformed request, no matching route).
func BuildErrorReply(source string, status int, message string) *ce.Event {
	out := ce.New()
	out.SetID(deterministicID(message, source, ErrorReplyType))
	out.SetType(ErrorReplyType)
	out.SetSource(source)
	out.SetTime(time.Now().UTC())
	_ = out.SetData("application/json", map[string]string{"error": message})
	out.SetExtension("httpstatus", int32(status))
	return &out
}

func setHTTPData(out *ce.Event, contentType string, body []byte) error {
	if contentType == "" {
		contentType = "application/json"
	}
	data := any(string(body))
	if strings.Contains(strings.ToLower(contentType), "json") && len(body) > 0 {
		var raw any
		if err := json.Unmarshal(body, &raw); err == nil {
			data = raw
		}
	}
	if err := out.SetData(contentType, data); err != nil {
		return fmt.Errorf("set data: %w", err)
	}
	return nil
}

func deterministicID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return "evt_" + hex.EncodeToString(sum[:])
}
```

Update `BuildResponse` to use `deterministicID(in.ID(), route.Name, route.Response.Type, route.Response.Subject)` and delete the old `deterministicResponseID` function.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd event-adapter && go test ./internal/cloudevent/ -v`
Expected: PASS (new and existing tests).

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/cloudevent/response.go event-adapter/internal/cloudevent/response_test.go
git commit -m "feat(cloudevent): add BuildReply and BuildErrorReply"
```

---

## Task 5: Request matcher

**Files:**
- Modify: `internal/router/matcher.go`
- Test: `internal/router/matcher_test.go` (append)

- [ ] **Step 1: Write the failing test**

Append to `internal/router/matcher_test.go`:

```go
func TestRequestMatcher(t *testing.T) {
	routes := []config.RequestRouteConfig{{
		Name:  "upload-presign",
		Match: config.MatchConfig{Type: "com.workspace.uploads.presign.request"},
	}}
	m, err := NewRequests(routes)
	if err != nil {
		t.Fatalf("NewRequests: %v", err)
	}
	ev := mustEvent(t, `{"specversion":"1.0","id":"1","source":"c","type":"com.workspace.uploads.presign.request","data":{}}`)
	r, ok := m.Match(ev)
	if !ok || r.Name != "upload-presign" {
		t.Fatalf("match = %+v ok=%v", r, ok)
	}
	miss := mustEvent(t, `{"specversion":"1.0","id":"2","source":"c","type":"other","data":{}}`)
	if _, ok := m.Match(miss); ok {
		t.Fatal("expected no match for unknown type")
	}
}

func TestNewRequestsRejectsDuplicateType(t *testing.T) {
	routes := []config.RequestRouteConfig{
		{Name: "a", Match: config.MatchConfig{Type: "t"}},
		{Name: "b", Match: config.MatchConfig{Type: "t"}},
	}
	if _, err := NewRequests(routes); err == nil {
		t.Fatal("expected duplicate-type error")
	}
}
```

> Reuse the existing `mustEvent` helper in this test package; if none exists, add one that parses a JSON string into `*clevent.Event`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd event-adapter && go test ./internal/router/ -run TestRequest -v`
Expected: FAIL — `NewRequests` undefined.

- [ ] **Step 3: Implement `RequestMatcher`**

Append to `internal/router/matcher.go`:

```go
type RequestMatcher struct {
	index map[string]config.RequestRouteConfig
}

func NewRequests(routes []config.RequestRouteConfig) (*RequestMatcher, error) {
	index := make(map[string]config.RequestRouteConfig, len(routes))
	for _, r := range routes {
		if existing, ok := index[r.Match.Type]; ok {
			return nil, fmt.Errorf("duplicate request match type %q for routes %q and %q", r.Match.Type, existing.Name, r.Name)
		}
		index[r.Match.Type] = r
	}
	return &RequestMatcher{index: index}, nil
}

func (m *RequestMatcher) Match(ev *clevent.Event) (config.RequestRouteConfig, bool) {
	if ev == nil {
		return config.RequestRouteConfig{}, false
	}
	r, ok := m.index[ev.Type()]
	return r, ok
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd event-adapter && go test ./internal/router/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/router/matcher.go event-adapter/internal/router/matcher_test.go
git commit -m "feat(router): add request matcher"
```

---

## Task 6: Request-reply metrics

**Files:**
- Modify: `internal/metrics/metrics.go`
- Test: none (counters are exercised via the responder tests in Task 8)

- [ ] **Step 1: Add counter fields**

In `internal/metrics/metrics.go`, add to the `Metrics` struct:

```go
	requestsReceived    metric.Int64Counter
	requestReplyLatency metric.Float64Histogram
	requestDispatchErr  metric.Int64Counter
	requestNoReply      metric.Int64Counter
	invalidRequests     metric.Int64Counter
```

- [ ] **Step 2: Initialize them in `New`**

In `New`, after the existing histogram `h`, add a second histogram and the counters:

```go
	rh, err := meter.Float64Histogram("cts.request.reply_latency", metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}
```

and in the returned `&Metrics{...}` literal add:

```go
		requestsReceived:    mustC("cts.requests.received"),
		requestReplyLatency: rh,
		requestDispatchErr:  mustC("cts.requests.dispatch_errors"),
		requestNoReply:      mustC("cts.requests.no_reply"),
		invalidRequests:     mustC("cts.requests.invalid"),
```

- [ ] **Step 3: Add the methods**

Append to `internal/metrics/metrics.go`:

```go
func (m *Metrics) RequestReceived(ctx context.Context, route string) {
	m.requestsReceived.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RequestReplyLatency(ctx context.Context, route string, d time.Duration) {
	m.requestReplyLatency.Record(ctx, float64(d.Microseconds())/1000, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RequestDispatchError(ctx context.Context, route string) {
	m.requestDispatchErr.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RequestNoReply(ctx context.Context) {
	m.requestNoReply.Add(ctx, 1)
}

func (m *Metrics) InvalidRequestEvent(ctx context.Context, reason string) {
	m.invalidRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}
```

- [ ] **Step 4: Build to verify**

Run: `cd event-adapter && go build ./internal/metrics/`
Expected: no errors.

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/metrics/metrics.go
git commit -m "feat(metrics): add request-reply counters"
```

---

## Task 7: NATS request subscription

**Files:**
- Modify: `internal/natsjs/client.go`
- Test: none directly (core NATS needs a live server; covered by e2e in Task 10). Build-only here.

- [ ] **Step 1: Add `RequestMsg` and `SubscribeRequests`**

Append to `internal/natsjs/client.go`:

```go
// RequestMsg is a core-NATS request delivered to the responder. Respond is a
// function field (not a method) so responder unit tests can capture the reply
// bytes without a live NATS connection.
type RequestMsg struct {
	Subject string
	ReplyTo string
	Data    []byte
	Respond func([]byte) error
}

// SubscribeRequests subscribes to subject within queue group queue and invokes h
// for each request. Uses core NATS (no JetStream): request-reply is transient.
func (c *Client) SubscribeRequests(subject, queue string, h func(RequestMsg)) (*nats.Subscription, error) {
	sub, err := c.nc.QueueSubscribe(subject, queue, func(m *nats.Msg) {
		h(RequestMsg{
			Subject: m.Subject,
			ReplyTo: m.Reply,
			Data:    m.Data,
			Respond: m.Respond,
		})
	})
	if err != nil {
		return nil, fmt.Errorf("nats: subscribe requests %q: %w", subject, err)
	}
	return sub, nil
}
```

- [ ] **Step 2: Build to verify**

Run: `cd event-adapter && go build ./internal/natsjs/`
Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add event-adapter/internal/natsjs/client.go
git commit -m "feat(natsjs): add core-NATS request subscription"
```

---

## Task 8: Responder package

**Files:**
- Create: `internal/responder/responder.go`
- Test: `internal/responder/responder_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/responder/responder_test.go`:

```go
package responder

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/natsjs"
)

type fakeDispatcher struct {
	res dispatcher.Result
	err error
}

func (f fakeDispatcher) Dispatch(_ context.Context, _ config.DispatchConfig, _ *clevent.Event) (dispatcher.Result, error) {
	return f.res, f.err
}

type fakeMetrics struct{ received, dispatchErr, noReply, invalid int }

func (f *fakeMetrics) RequestReceived(context.Context, string)               { f.received++ }
func (f *fakeMetrics) RequestReplyLatency(context.Context, string, time.Duration) {}
func (f *fakeMetrics) RequestDispatchError(context.Context, string)          { f.dispatchErr++ }
func (f *fakeMetrics) RequestNoReply(context.Context)                        { f.noReply++ }
func (f *fakeMetrics) InvalidRequestEvent(context.Context, string)           { f.invalid++ }

func newResponder(d Dispatcher, m Metrics) *Responder {
	matcher, _ := newTestMatcher()
	return New(matcher, d, m, "upload-service", &config.RequestsConfig{
		Subject: "s", QueueGroup: "g", WorkerPoolSize: 2,
		Routes: []config.RequestRouteConfig{{
			Name:     "upload-presign",
			Match:    config.MatchConfig{Type: "com.x.request"},
			Dispatch: config.DispatchConfig{Method: "POST", Path: "/r", Timeout: time.Second},
			Reply:    config.ReplyConfig{Source: "upload-service", Type: "com.x.reply"},
		}},
	}, io.Discard)
}

func capture() (*natsjs.RequestMsg, *[]byte) {
	var out []byte
	m := &natsjs.RequestMsg{
		ReplyTo: "_INBOX.1",
		Data:    []byte(`{"specversion":"1.0","id":"req-1","source":"c","type":"com.x.request","data":{"k":1}}`),
		Respond: func(b []byte) error { out = b; return nil },
	}
	return m, &out
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	return v
}

func TestHandleSuccess(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"url":"https://s3/put"}`)}}
	met := &fakeMetrics{}
	r := newResponder(d, met)
	m, out := capture()
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	if reply["type"] != "com.x.reply" {
		t.Errorf("type = %v", reply["type"])
	}
	if reply["httpstatus"].(float64) != 200 {
		t.Errorf("httpstatus = %v", reply["httpstatus"])
	}
	if reply["causationid"] != "req-1" {
		t.Errorf("causationid = %v", reply["causationid"])
	}
	if met.received != 1 {
		t.Errorf("received = %d", met.received)
	}
}

func TestHandleAppRejectionIsNormalReply(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 400, ContentType: "application/json", Body: []byte(`{"error":"bad type"}`)}}
	r := newResponder(d, &fakeMetrics{})
	m, out := capture()
	r.handle(context.Background(), *m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 400 {
		t.Errorf("httpstatus = %v, want 400 forwarded", reply["httpstatus"])
	}
	if reply["type"] != "com.x.reply" {
		t.Errorf("type = %v, want normal reply type", reply["type"])
	}
}

func TestHandleDispatchErrorReplies502(t *testing.T) {
	d := fakeDispatcher{err: errors.New("connection refused")}
	met := &fakeMetrics{}
	r := newResponder(d, met)
	m, out := capture()
	r.handle(context.Background(), *m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 502 {
		t.Errorf("httpstatus = %v, want 502", reply["httpstatus"])
	}
	if met.dispatchErr != 1 {
		t.Errorf("dispatchErr = %d", met.dispatchErr)
	}
}

func TestHandleTimeoutReplies504(t *testing.T) {
	d := fakeDispatcher{err: context.DeadlineExceeded}
	r := newResponder(d, &fakeMetrics{})
	m, out := capture()
	r.handle(context.Background(), *m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 504 {
		t.Errorf("httpstatus = %v, want 504", reply["httpstatus"])
	}
}

func TestHandleParseErrorReplies400(t *testing.T) {
	r := newResponder(fakeDispatcher{}, &fakeMetrics{})
	out := []byte(nil)
	m := natsjs.RequestMsg{ReplyTo: "_INBOX.1", Data: []byte(`not json`), Respond: func(b []byte) error { out = b; return nil }}
	r.handle(context.Background(), m)
	reply := decode(t, out)
	if reply["httpstatus"].(float64) != 400 {
		t.Errorf("httpstatus = %v, want 400", reply["httpstatus"])
	}
	if reply["type"] != clevent.ErrorReplyType {
		t.Errorf("type = %v, want error reply type", reply["type"])
	}
}

func TestHandleNoReplyToIsDropped(t *testing.T) {
	met := &fakeMetrics{}
	r := newResponder(fakeDispatcher{}, met)
	responded := false
	m := natsjs.RequestMsg{ReplyTo: "", Data: []byte(`{}`), Respond: func([]byte) error { responded = true; return nil }}
	r.handle(context.Background(), m)
	if responded {
		t.Error("must not respond when ReplyTo is empty")
	}
	if met.noReply != 1 {
		t.Errorf("noReply = %d", met.noReply)
	}
}
```

Also create a tiny test helper file `internal/responder/matcher_test_helper_test.go`:

```go
package responder

import (
	"event-adapter/internal/config"
	"event-adapter/internal/router"
)

func newTestMatcher() (*router.RequestMatcher, error) {
	return router.NewRequests([]config.RequestRouteConfig{{
		Name:  "upload-presign",
		Match: config.MatchConfig{Type: "com.x.request"},
	}})
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd event-adapter && go test ./internal/responder/ -v`
Expected: FAIL — package/`New`/`handle` undefined.

- [ ] **Step 3: Implement the responder**

Create `internal/responder/responder.go`:

```go
package responder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/nats-io/nats.go"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/natsjs"
)

type Dispatcher interface {
	Dispatch(context.Context, config.DispatchConfig, *clevent.Event) (dispatcher.Result, error)
}

type Matcher interface {
	Match(ev *clevent.Event) (config.RequestRouteConfig, bool)
}

type Metrics interface {
	RequestReceived(ctx context.Context, route string)
	RequestReplyLatency(ctx context.Context, route string, d time.Duration)
	RequestDispatchError(ctx context.Context, route string)
	RequestNoReply(ctx context.Context)
	InvalidRequestEvent(ctx context.Context, reason string)
}

// Subscriber is satisfied by *natsjs.Client.
type Subscriber interface {
	SubscribeRequests(subject, queue string, h func(natsjs.RequestMsg)) (*nats.Subscription, error)
}

type Responder struct {
	matcher Matcher
	disp    Dispatcher
	metrics Metrics
	appID   string
	cfg     *config.RequestsConfig
	stderr  io.Writer
}

func New(matcher Matcher, disp Dispatcher, metrics Metrics, appID string, cfg *config.RequestsConfig, stderr io.Writer) *Responder {
	if stderr == nil {
		stderr = io.Discard
	}
	return &Responder{matcher: matcher, disp: disp, metrics: metrics, appID: appID, cfg: cfg, stderr: stderr}
}

// Run subscribes and processes requests on a bounded worker pool until ctx is
// cancelled, then drains the subscription and waits for in-flight work.
func (r *Responder) Run(ctx context.Context, sub Subscriber) error {
	jobs := make(chan natsjs.RequestMsg, r.cfg.WorkerPoolSize)
	var wg sync.WaitGroup
	wg.Add(r.cfg.WorkerPoolSize)
	for i := 0; i < r.cfg.WorkerPoolSize; i++ {
		go func() {
			defer wg.Done()
			for m := range jobs {
				r.handle(ctx, m)
			}
		}()
	}

	subscription, err := sub.SubscribeRequests(r.cfg.Subject, r.cfg.QueueGroup, func(m natsjs.RequestMsg) {
		select {
		case jobs <- m:
		case <-ctx.Done():
		}
	})
	if err != nil {
		close(jobs)
		wg.Wait()
		return err
	}

	<-ctx.Done()
	// Drain stops new callbacks and waits for pending ones to finish, so no
	// goroutine sends to jobs after this returns — safe to close.
	_ = subscription.Drain()
	close(jobs)
	wg.Wait()
	return nil
}

func (r *Responder) handle(ctx context.Context, m natsjs.RequestMsg) {
	if m.Respond == nil || m.ReplyTo == "" {
		r.metrics.RequestNoReply(ctx)
		return
	}
	ev, err := clevent.Parse(m.Data)
	if err != nil {
		r.metrics.InvalidRequestEvent(ctx, "parse_error")
		r.respond(m, clevent.BuildErrorReply(r.appID, http.StatusBadRequest, err.Error()))
		return
	}
	route, ok := r.matcher.Match(ev)
	if !ok {
		r.metrics.InvalidRequestEvent(ctx, "no_route")
		r.respond(m, clevent.BuildErrorReply(r.appID, http.StatusNotFound, "no matching route"))
		return
	}
	r.metrics.RequestReceived(ctx, route.Name)
	start := time.Now()
	defer func() { r.metrics.RequestReplyLatency(ctx, route.Name, time.Since(start)) }()

	res, derr := r.disp.Dispatch(ctx, route.Dispatch, ev)
	if derr != nil {
		r.metrics.RequestDispatchError(ctx, route.Name)
		status := http.StatusBadGateway
		if errors.Is(derr, context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		reply, _ := clevent.BuildReply(ev, route.Reply, route.Name, status, "application/json", errorBody(derr.Error()))
		r.respond(m, reply)
		return
	}
	reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, res.StatusCode, res.ContentType, res.Body)
	if berr != nil {
		r.respond(m, clevent.BuildErrorReply(r.appID, http.StatusInternalServerError, berr.Error()))
		return
	}
	r.respond(m, reply)
}

func (r *Responder) respond(m natsjs.RequestMsg, ev *ce.Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(r.stderr, "responder: marshal reply: %v\n", err)
		return
	}
	if err := m.Respond(b); err != nil {
		fmt.Fprintf(r.stderr, "responder: respond: %v\n", err)
	}
}

func errorBody(message string) []byte {
	b, _ := json.Marshal(map[string]string{"error": message})
	return b
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd event-adapter && go test ./internal/responder/ -v`
Expected: PASS (all subtests).

- [ ] **Step 5: Commit**

```bash
git add event-adapter/internal/responder/
git commit -m "feat(responder): request-reply responder with bounded worker pool"
```

---

## Task 9: Wire the responder into main

**Files:**
- Modify: `cmd/event-adapter/main.go`
- Test: none (covered by e2e in Task 10). Build-only.

- [ ] **Step 1: Update `run` to start both paths**

In `cmd/event-adapter/main.go`, add `"sync"` and `"event-adapter/internal/responder"` to the imports, then replace the section from `matcher, err := router.New(...)` through `cons.Run(ctx)` (lines ~68-84) with:

```go
	mp := metric.NewMeterProvider()
	m := metrics.New(mp.Meter("event-adapter"))
	httpDispatcher := dispatcher.New(cfg.App.HTTPBaseURL, nil)

	var wg sync.WaitGroup

	if len(cfg.Routes) > 0 {
		matcher, err := router.New(cfg.Routes)
		if err != nil {
			fmt.Fprintf(stderr, "build router: %v\n", err)
			return 1
		}
		proc := processor.New(httpDispatcher, js)
		sub, err := js.SubscribeWildcard(cfg.NATS)
		if err != nil {
			fmt.Fprintf(stderr, "subscribe %s: %v\n", cfg.NATS.FilterSubject, err)
			return 1
		}
		cons := consumer.New(sub, proc, matcher, js, m, *cfg, cfg.NATS.FetchBatch, cfg.NATS.WorkerPoolSize, stderr)
		fmt.Fprintf(stdout, "event-adapter consuming %q with %d workers (batch %d)\n", cfg.NATS.FilterSubject, cfg.NATS.WorkerPoolSize, cfg.NATS.FetchBatch)
		wg.Add(1)
		go func() {
			defer wg.Done()
			cons.Run(ctx)
		}()
	}

	if cfg.Requests != nil {
		rmatcher, err := router.NewRequests(cfg.Requests.Routes)
		if err != nil {
			fmt.Fprintf(stderr, "build request router: %v\n", err)
			return 1
		}
		resp := responder.New(rmatcher, httpDispatcher, m, cfg.App.ID, cfg.Requests, stderr)
		fmt.Fprintf(stdout, "event-adapter responding to %q (queue %q) with %d workers\n", cfg.Requests.Subject, cfg.Requests.QueueGroup, cfg.Requests.WorkerPoolSize)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := resp.Run(ctx, js); err != nil {
				fmt.Fprintf(stderr, "responder: %v\n", err)
			}
		}()
	}

	wg.Wait()
	return 0
```

- [ ] **Step 2: Build the binary**

Run: `cd event-adapter && go build ./cmd/event-adapter/`
Expected: no errors. (If `router`/`processor`/`consumer` imports become unused in some build configuration they won't — both paths reference them conditionally at runtime, imports stay used.)

- [ ] **Step 3: Run the full unit suite**

Run: `cd event-adapter && go test ./...`
Expected: PASS for all non-e2e packages (e2e has a build tag and is skipped).

- [ ] **Step 4: Commit**

```bash
git add event-adapter/cmd/event-adapter/main.go
git commit -m "feat(event-adapter): run responder and consumer concurrently per config"
```

---

## Task 10: End-to-end request-reply test

**Files:**
- Modify: `test/e2e/mock-app.yaml`
- Modify: `test/e2e/routes.yaml`
- Create: `test/e2e/fixtures/upload-presign.json`
- Modify: `test/e2e/e2e_test.go`
- Modify: `test/e2e/README.md`

- [ ] **Step 1: Add a mock-app handler**

Append to `test/e2e/mock-app.yaml` under `handlers:` (a presign-shaped fixed response):

```yaml
  - method: POST
    path: /requests/upload-presign
    response:
      status: 200
      contentType: application/json
      body: '{"uploadId":"up-1","url":"https://example-bucket.s3/put?sig=abc"}'
```

- [ ] **Step 2: Add the `requests` block to the adapter config**

Append to `test/e2e/routes.yaml`:

```yaml
requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: upload-responders
  workerPoolSize: 8
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request
      dispatch:
        method: POST
        path: /requests/upload-presign
        timeout: 3s
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
```

- [ ] **Step 3: Add the request fixture**

Create `test/e2e/fixtures/upload-presign.json`:

```json
{
  "specversion": "1.0",
  "id": "req-presign-1",
  "source": "workspace/uploads-client",
  "type": "com.workspace.uploads.presign.request",
  "datacontenttype": "application/json",
  "data": {"filename": "photo.jpg", "contentType": "image/jpeg", "size": 20480}
}
```

- [ ] **Step 4: Write the e2e test**

Append to `test/e2e/e2e_test.go`:

```go
func TestRequestReplyPresign(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	fixture, err := os.ReadFile("fixtures/upload-presign.json")
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
	if reply["type"] != "com.workspace.uploads.presign.reply" {
		t.Errorf("type = %v", reply["type"])
	}
	if reply["causationid"] != "req-presign-1" {
		t.Errorf("causationid = %v", reply["causationid"])
	}
	if reply["httpstatus"].(float64) != 200 {
		t.Errorf("httpstatus = %v", reply["httpstatus"])
	}
	data, _ := reply["data"].(map[string]any)
	if data["uploadId"] != "up-1" {
		t.Errorf("data.uploadId = %v, want up-1", data["uploadId"])
	}
}
```

- [ ] **Step 5: Document the test**

Add a short section to `test/e2e/README.md` after the existing test descriptions:

```markdown
## TestRequestReplyPresign

Exercises the request-reply responder. Sends a `com.workspace.uploads.presign.request`
CloudEvent via `nats request` to `q.tenant-a.app.uploads.request`; the adapter
dispatches to the mock-app `/requests/upload-presign` handler and replies on the
request inbox. Asserts the reply CloudEvent type, `causationid`, `httpstatus=200`,
and the presigned `uploadId` from the mock response.
```

- [ ] **Step 6: Run the e2e suite**

```bash
cd event-adapter/test/e2e
docker compose up --build -d
go test -tags e2e -run TestRequestReplyPresign -v ./...
docker compose down
```
Expected: PASS. (Bring the stack down when finished.)

- [ ] **Step 7: Commit**

```bash
git add event-adapter/test/e2e/
git commit -m "test(e2e): request-reply presign round-trip"
```

---

## Self-Review Notes (verification checklist for the implementer)

After all tasks, confirm:

1. `cd event-adapter && go build ./... && go test ./...` is green (unit suite).
2. `go vet ./...` is clean.
3. Spec coverage: responder (T7-9), config block + validation (T1-2), unified dispatch core (T3), reply/error format incl. app-4xx-as-reply and 502/504 (T4, T8), request matcher (T5), metrics (T6), concurrency/drain (T8), e2e (T10), at-least-one-path + backward compat (T2, T9).
4. Type consistency: `Dispatch(ctx, config.DispatchConfig, *clevent.Event)` is used identically in `dispatcher`, `processor`, and `responder`; `BuildReply(in, ReplyConfig, routeName, status, contentType, body)` signature matches every call site; `RequestMsg.Respond` is a func field everywhere.
```
