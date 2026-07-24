# Event Adapter Direct Request Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add opt-in publisher-directed method/path dispatch for synchronous NATS requests, with exact-route precedence, safe loopback request targets, generic replies, and DELETE support across all dispatch modes.

**Architecture:** Extract `dispatchmethod` and `dispatchpath` as CloudEvent control metadata, validate them in a new focused `internal/requesttarget` package, and let the responder synthesize a normal `config.RequestRouteConfig` only when exact type matching fails and direct dispatch is enabled. Reuse the existing dispatcher and reply builder so headers, cookies, timeouts, HTTP response handling, correlation, and worker-pool behavior stay on one path.

**Tech Stack:** Go 1.25, CloudEvents SDK for Go, NATS request-reply, `net/http`, `net/url`, YAML v3, table-driven Go tests, Docker Compose e2e tests.

---

## File and Responsibility Map

- Modify `event-adapter/internal/config/schema.go`: define the
  `requests.directDispatch` schema.
- Modify `event-adapter/internal/config/validate.go`: validate direct-only
  responder configs and use the shared HTTP method validator.
- Modify `event-adapter/internal/config/schema_test.go`: prove strict YAML
  parsing for the new block.
- Modify `event-adapter/internal/config/validate_test.go`: prove config
  enablement, timeout, prefix, route coexistence, and DELETE behavior.
- Modify `event-adapter/internal/cloudevent/event.go`: extract and strip
  `dispatchmethod` and `dispatchpath`.
- Modify `event-adapter/internal/cloudevent/event_test.go`: prove extraction,
  absence, type validation, and non-leakage.
- Create `event-adapter/internal/requesttarget/target.go`: normalize allowed
  methods and validate/canonicalize publisher-selected local request targets.
- Create `event-adapter/internal/requesttarget/target_test.go`: exhaustively
  cover method, URL, escaping, traversal, query, and prefix safety.
- Modify `event-adapter/internal/cloudevent/response.go`: define the generic
  direct reply type.
- Modify `event-adapter/internal/cloudevent/response_test.go`: prove generic
  direct replies preserve the existing envelope contract.
- Modify `event-adapter/internal/responder/responder.go`: implement exact-route
  precedence and direct fallback.
- Modify `event-adapter/internal/responder/responder_test.go`: prove selection,
  error mapping, dispatch config, reply metadata, and bounded metric labels.
- Modify `event-adapter/test/e2e/routes.yaml`: enable direct dispatch alongside
  existing exact routes.
- Modify `event-adapter/test/e2e/mock-app.yaml`: add a DELETE endpoint.
- Create `event-adapter/test/e2e/fixtures/direct-delete.json`: provide an
  unmatched request type with a resolved dispatch target.
- Modify `event-adapter/test/e2e/e2e_test.go`: prove a route-free synchronous
  DELETE round trip.
- Modify `event-adapter/AGENTS.md`, `event-adapter/README.md`,
  `prd/event-adapter/prd.md`, and
  `prd/event-adapter/app-developer-guide.md`: document the new contract,
  precedence, safety boundary, replies, errors, and DELETE support.

### Task 1: Share the HTTP Method Allowlist and Add DELETE

**Files:**
- Create: `event-adapter/internal/requesttarget/target.go`
- Create: `event-adapter/internal/requesttarget/target_test.go`
- Modify: `event-adapter/internal/config/validate.go`
- Modify: `event-adapter/internal/config/validate_test.go`
- Modify: `event-adapter/internal/dispatcher/dispatcher_test.go`

- [ ] **Step 1: Write failing tests for method normalization and DELETE config**

Create `internal/requesttarget/target_test.go` with the initial method tests:

```go
package requesttarget

import (
	"net/http"
	"testing"
)

func TestNormalizeMethodAcceptsSupportedMethods(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"get", "POST", "Put", "patch", "delete"} {
		in := in
		t.Run(in, func(t *testing.T) {
			t.Parallel()
			got, err := NormalizeMethod(in)
			if err != nil {
				t.Fatalf("NormalizeMethod(%q): %v", in, err)
			}
			want := http.CanonicalHeaderKey(in)
			if in == "get" {
				want = http.MethodGet
			}
			switch in {
			case "POST":
				want = http.MethodPost
			case "Put":
				want = http.MethodPut
			case "patch":
				want = http.MethodPatch
			case "delete":
				want = http.MethodDelete
			}
			if got != want {
				t.Fatalf("NormalizeMethod(%q) = %q, want %q", in, got, want)
			}
		})
	}
}

func TestNormalizeMethodRejectsUnsupportedMethod(t *testing.T) {
	t.Parallel()
	if _, err := NormalizeMethod("OPTIONS"); err == nil {
		t.Fatal("expected OPTIONS to be rejected")
	}
}
```

Add this test to `internal/config/validate_test.go`:

```go
func TestValidateAcceptsDeleteDispatchMethod(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Method = http.MethodDelete
	cfg.Requests = baseRequests()
	cfg.Requests.Routes[0].Dispatch.Method = http.MethodDelete
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected DELETE for event and request routes, got %v", errs)
	}
}
```

- [ ] **Step 2: Run the focused tests and verify they fail**

Run from `event-adapter/`:

```bash
go test ./internal/requesttarget ./internal/config -run 'TestNormalizeMethod|TestValidateAcceptsDelete' -count=1
```

Expected: FAIL because `NormalizeMethod` is undefined and DELETE is rejected by
`validateDispatch`.

- [ ] **Step 3: Implement the shared method validator**

Create `internal/requesttarget/target.go`:

```go
package requesttarget

import (
	"fmt"
	"net/http"
	"strings"
)

var supportedMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

func NormalizeMethod(raw string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(raw))
	if _, ok := supportedMethods[method]; !ok {
		return "", fmt.Errorf("unsupported dispatch method %q", raw)
	}
	return method, nil
}
```

In `internal/config/validate.go`, import `event-adapter/internal/requesttarget`
and replace the local method comparison:

```go
if _, err := requesttarget.NormalizeMethod(d.Method); err != nil {
	errs = append(errs, ValidationError{
		Path: prefix + ".dispatch.method",
		Msg:  "must be GET, POST, PUT, PATCH, or DELETE",
	})
}
```

- [ ] **Step 4: Add a dispatcher regression test for DELETE request bodies**

Add to `internal/dispatcher/dispatcher_test.go`:

```go
func TestDispatchDeleteSendsCloudEventData(t *testing.T) {
	var gotMethod string
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		var err error
		gotBody, err = io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"delete-1","source":"test","type":"test.delete","data":{"reason":"cleanup"}}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = New(srv.URL, srv.Client()).Dispatch(context.Background(), config.DispatchConfig{
		Method: http.MethodDelete,
		Path:   "/orders/ord-456",
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Fatalf("method = %q, want DELETE", gotMethod)
	}
	if string(gotBody) != `{"reason":"cleanup"}` {
		t.Fatalf("body = %s", gotBody)
	}
}
```

- [ ] **Step 5: Run focused tests and format**

Run:

```bash
gofmt -w internal/requesttarget/target.go internal/requesttarget/target_test.go internal/config/validate.go internal/config/validate_test.go internal/dispatcher/dispatcher_test.go
go test ./internal/requesttarget ./internal/config ./internal/dispatcher -count=1
```

Expected: all three packages PASS.

- [ ] **Step 6: Commit**

```bash
git add event-adapter/internal/requesttarget/target.go event-adapter/internal/requesttarget/target_test.go event-adapter/internal/config/validate.go event-adapter/internal/config/validate_test.go event-adapter/internal/dispatcher/dispatcher_test.go
git commit -m "feat(event-adapter): support DELETE dispatch"
```

### Task 2: Extract Direct-Dispatch CloudEvent Metadata

**Files:**
- Modify: `event-adapter/internal/cloudevent/event.go`
- Modify: `event-adapter/internal/cloudevent/event_test.go`

- [ ] **Step 1: Write failing metadata extraction tests**

Add to `internal/cloudevent/event_test.go`:

```go
func TestParseExtractsAndStripsDirectDispatchMetadata(t *testing.T) {
	raw := []byte(`{"specversion":"1.0","id":"req-direct","source":"client","type":"orders.delete","dispatchmethod":"DELETE","dispatchpath":"/orders/ord-456?hard=true","data":{}}`)
	ev, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.DispatchMethod != "DELETE" {
		t.Fatalf("DispatchMethod = %q", ev.DispatchMethod)
	}
	if ev.DispatchPath != "/orders/ord-456?hard=true" {
		t.Fatalf("DispatchPath = %q", ev.DispatchPath)
	}
	if _, ok := ev.Extensions()["dispatchmethod"]; ok {
		t.Fatal("dispatchmethod leaked into CloudEvent extensions")
	}
	if _, ok := ev.Extensions()["dispatchpath"]; ok {
		t.Fatal("dispatchpath leaked into CloudEvent extensions")
	}
}

func TestParseDirectDispatchMetadataAbsentIsEmpty(t *testing.T) {
	ev, err := Parse([]byte(`{"specversion":"1.0","id":"req-static","source":"client","type":"orders.get","data":{}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ev.DispatchMethod != "" || ev.DispatchPath != "" {
		t.Fatalf("unexpected direct metadata: method=%q path=%q", ev.DispatchMethod, ev.DispatchPath)
	}
}

func TestParseRejectsNonStringDirectDispatchMetadata(t *testing.T) {
	for _, raw := range []string{
		`{"specversion":"1.0","id":"x","source":"c","type":"t","dispatchmethod":123,"data":{}}`,
		`{"specversion":"1.0","id":"x","source":"c","type":"t","dispatchpath":{},"data":{}}`,
	} {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Fatalf("expected parse error for %s", raw)
		}
	}
}
```

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/cloudevent -run 'TestParse.*DirectDispatch' -count=1
```

Expected: FAIL because `Event` has no direct-dispatch fields.

- [ ] **Step 3: Implement string metadata extraction**

Extend `Event` in `internal/cloudevent/event.go`:

```go
type Event struct {
	*ce.Event
	DispatchHeaders    map[string]string
	DispatchCookies    map[string]string
	DispatchPathParams map[string]string
	DispatchMethod     string
	DispatchPath       string
}
```

Before marshaling `probe` back into the CloudEvents SDK, extract both fields:

```go
dispatchMethod, err := parseDispatchString("dispatchmethod", probe["dispatchmethod"])
if err != nil {
	return nil, err
}
delete(probe, "dispatchmethod")
dispatchPath, err := parseDispatchString("dispatchpath", probe["dispatchpath"])
if err != nil {
	return nil, err
}
delete(probe, "dispatchpath")
```

Set them in the returned `Event` and add:

```go
func parseDispatchString(name string, raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fmt.Errorf("cloudevent: %s must be a string: %w", name, err)
	}
	return value, nil
}
```

- [ ] **Step 4: Run tests and commit**

Run:

```bash
gofmt -w internal/cloudevent/event.go internal/cloudevent/event_test.go
go test ./internal/cloudevent -count=1
```

Expected: PASS.

Commit:

```bash
git add event-adapter/internal/cloudevent/event.go event-adapter/internal/cloudevent/event_test.go
git commit -m "feat(event-adapter): parse direct dispatch metadata"
```

### Task 3: Validate and Canonicalize Publisher Request Targets

**Files:**
- Modify: `event-adapter/internal/requesttarget/target.go`
- Modify: `event-adapter/internal/requesttarget/target_test.go`

- [ ] **Step 1: Write the failing target validation matrix**

Append to `internal/requesttarget/target_test.go`:

```go
func TestResolveAcceptsAndCanonicalizesTarget(t *testing.T) {
	got, err := Resolve("delete", "/orders//ord-456?hard=true", []string{"/orders/"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Method != http.MethodDelete {
		t.Fatalf("Method = %q", got.Method)
	}
	if got.Path != "/orders/ord-456?hard=true" {
		t.Fatalf("Path = %q", got.Path)
	}
}

func TestResolveRejectsUnsafeTargets(t *testing.T) {
	tests := map[string]string{
		"full URL":          "http://localhost/admin",
		"network path":      "//localhost/admin",
		"fragment":          "/orders/1#fragment",
		"literal traversal": "/orders/../admin",
		"encoded traversal": "/orders/%2e%2e/admin",
		"encoded slash":     "/orders%2fadmin",
		"encoded backslash": "/orders%5cadmin",
		"nested escaping":   "/orders/%252e%252e/admin",
		"backslash":         `/orders\\admin`,
		"bad escape":        "/orders/%zz",
	}
	for name, raw := range tests {
		raw := raw
		t.Run(name, func(t *testing.T) {
			if _, err := Resolve(http.MethodPost, raw, nil); err == nil {
				t.Fatalf("Resolve(%q) succeeded", raw)
			}
		})
	}
}

func TestResolveEnforcesPrefixBoundary(t *testing.T) {
	for _, raw := range []string{"/orders", "/orders/ord-1"} {
		if _, err := Resolve("GET", raw, []string{"/orders/"}); err != nil {
			t.Fatalf("Resolve(%q): %v", raw, err)
		}
	}
	for _, raw := range []string{"/orders-admin", "/admin"} {
		if _, err := Resolve("GET", raw, []string{"/orders/"}); err == nil {
			t.Fatalf("Resolve(%q) should fail prefix check", raw)
		}
	}
}

func TestValidatePrefix(t *testing.T) {
	for _, prefix := range []string{"/", "/orders", "/orders/"} {
		if err := ValidatePrefix(prefix); err != nil {
			t.Fatalf("ValidatePrefix(%q): %v", prefix, err)
		}
	}
	for _, prefix := range []string{"orders", "//orders", "/orders?x=1", "/orders/../admin"} {
		if err := ValidatePrefix(prefix); err == nil {
			t.Fatalf("ValidatePrefix(%q) succeeded", prefix)
		}
	}
}
```

- [ ] **Step 2: Run the matrix and verify it fails**

Run:

```bash
go test ./internal/requesttarget -run 'TestResolve|TestValidatePrefix' -count=1
```

Expected: FAIL because `Target`, `Resolve`, and `ValidatePrefix` are undefined.

- [ ] **Step 3: Implement request-target validation**

Extend `internal/requesttarget/target.go`:

```go
package requesttarget

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
)

var supportedMethods = map[string]struct{}{
	http.MethodGet:    {},
	http.MethodPost:   {},
	http.MethodPut:    {},
	http.MethodPatch:  {},
	http.MethodDelete: {},
}

type Target struct {
	Method string
	Path   string
}

func NormalizeMethod(raw string) (string, error) {
	method := strings.ToUpper(strings.TrimSpace(raw))
	if _, ok := supportedMethods[method]; !ok {
		return "", fmt.Errorf("unsupported dispatch method %q", raw)
	}
	return method, nil
}

func Resolve(rawMethod, rawTarget string, allowedPrefixes []string) (Target, error) {
	method, err := NormalizeMethod(rawMethod)
	if err != nil {
		return Target{}, err
	}
	canonicalPath, rawQuery, err := parsePath(rawTarget, true)
	if err != nil {
		return Target{}, err
	}
	if len(allowedPrefixes) > 0 {
		allowed := false
		for _, rawPrefix := range allowedPrefixes {
			prefix, _, prefixErr := parsePath(rawPrefix, false)
			if prefixErr != nil {
				return Target{}, fmt.Errorf("invalid allowed prefix %q: %w", rawPrefix, prefixErr)
			}
			if canonicalPath == prefix || prefix == "/" ||
				strings.HasPrefix(canonicalPath, prefix+"/") {
				allowed = true
				break
			}
		}
		if !allowed {
			return Target{}, fmt.Errorf("dispatch path %q is outside allowed prefixes", canonicalPath)
		}
	}
	u := &url.URL{Path: canonicalPath, RawQuery: rawQuery}
	return Target{Method: method, Path: u.RequestURI()}, nil
}

func ValidatePrefix(raw string) error {
	_, _, err := parsePath(raw, false)
	return err
}

func parsePath(raw string, allowQuery bool) (string, string, error) {
	if raw == "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") {
		return "", "", fmt.Errorf("must be an absolute-path reference beginning with one slash")
	}
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse request target: %w", err)
	}
	if u.IsAbs() || u.Host != "" || u.Opaque != "" || u.Fragment != "" {
		return "", "", fmt.Errorf("scheme, host, opaque target, and fragment are forbidden")
	}
	if !allowQuery && u.RawQuery != "" {
		return "", "", fmt.Errorf("query is forbidden in an allowed path prefix")
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return "", "", fmt.Errorf("encoded path separators are forbidden")
	}
	decoded, err := url.PathUnescape(u.EscapedPath())
	if err != nil {
		return "", "", fmt.Errorf("unescape path: %w", err)
	}
	if second, secondErr := url.PathUnescape(decoded); secondErr == nil && second != decoded {
		return "", "", fmt.Errorf("nested path escaping is forbidden")
	}
	if strings.Contains(decoded, `\`) {
		return "", "", fmt.Errorf("backslashes are forbidden")
	}
	for _, r := range decoded {
		if unicode.IsControl(r) {
			return "", "", fmt.Errorf("control characters are forbidden")
		}
	}
	for _, segment := range strings.Split(decoded, "/") {
		if segment == "." || segment == ".." {
			return "", "", fmt.Errorf("path traversal is forbidden")
		}
	}
	return path.Clean(decoded), u.RawQuery, nil
}
```

- [ ] **Step 4: Run the package tests and commit**

Run:

```bash
gofmt -w internal/requesttarget/target.go internal/requesttarget/target_test.go
go test ./internal/requesttarget -count=1
```

Expected: PASS.

Commit:

```bash
git add event-adapter/internal/requesttarget/target.go event-adapter/internal/requesttarget/target_test.go
git commit -m "feat(event-adapter): validate direct request targets"
```

### Task 4: Add and Validate Direct-Dispatch Configuration

**Files:**
- Modify: `event-adapter/internal/config/schema.go`
- Modify: `event-adapter/internal/config/schema_test.go`
- Modify: `event-adapter/internal/config/validate.go`
- Modify: `event-adapter/internal/config/validate_test.go`

- [ ] **Step 1: Write failing schema and validation tests**

Add to `internal/config/schema_test.go`:

```go
func TestParseDirectDispatchRequestsBlock(t *testing.T) {
	raw := []byte(`
app: {id: order-service, httpBaseURL: http://127.0.0.1:8080}
nats: {url: nats://127.0.0.1:4222}
requests:
  subject: q.orders
  queueGroup: order-responders
  workerPoolSize: 8
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes: [/orders/]
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := cfg.Requests.DirectDispatch
	if !got.Enabled || got.Timeout != 3*time.Second {
		t.Fatalf("directDispatch = %+v", got)
	}
	if len(got.AllowedPathPrefixes) != 1 || got.AllowedPathPrefixes[0] != "/orders/" {
		t.Fatalf("allowedPathPrefixes = %#v", got.AllowedPathPrefixes)
	}
}
```

Add to `internal/config/validate_test.go`:

```go
func directOnlyConfig() *Config {
	return &Config{
		App:  AppConfig{ID: "order-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: &RequestsConfig{
			Subject: "q.orders", QueueGroup: "orders", WorkerPoolSize: 4,
			DirectDispatch: DirectDispatchConfig{
				Enabled: true, Timeout: 3 * time.Second,
				AllowedPathPrefixes: []string{"/orders/"},
			},
		},
		Observability: ObservabilityConfig{Environment: "testing"},
	}
}

func TestValidateAcceptsDirectOnlyResponder(t *testing.T) {
	if errs := Validate(directOnlyConfig()); len(errs) != 0 {
		t.Fatalf("Validate: %v", errs)
	}
}

func TestValidateDirectDispatchRequirements(t *testing.T) {
	tests := map[string]func(*Config){
		"no routes and disabled": func(c *Config) { c.Requests.DirectDispatch.Enabled = false },
		"missing timeout": func(c *Config) { c.Requests.DirectDispatch.Timeout = 0 },
		"unsafe prefix": func(c *Config) {
			c.Requests.DirectDispatch.AllowedPathPrefixes = []string{"/orders/../admin"}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := directOnlyConfig()
			mutate(cfg)
			if errs := Validate(cfg); len(errs) == 0 {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateAllowsStaticAndDirectRoutesTogether(t *testing.T) {
	cfg := directOnlyConfig()
	cfg.Requests.Routes = baseRequests().Routes
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("Validate: %v", errs)
	}
}
```

- [ ] **Step 2: Run focused tests and verify they fail**

Run:

```bash
go test ./internal/config -run 'TestParseDirect|TestValidate.*Direct|TestValidateAllowsStaticAndDirect' -count=1
```

Expected: FAIL because the schema and validation do not exist and empty routes
are rejected.

- [ ] **Step 3: Add the schema**

In `internal/config/schema.go`:

```go
type RequestsConfig struct {
	Subject        string                 `yaml:"subject"`
	QueueGroup     string                 `yaml:"queueGroup"`
	WorkerPoolSize int                    `yaml:"workerPoolSize"`
	DirectDispatch DirectDispatchConfig   `yaml:"directDispatch"`
	Routes         []RequestRouteConfig   `yaml:"routes"`
}

type DirectDispatchConfig struct {
	Enabled             bool          `yaml:"enabled"`
	Timeout             time.Duration `yaml:"timeout"`
	AllowedPathPrefixes []string      `yaml:"allowedPathPrefixes"`
}
```

- [ ] **Step 4: Implement configuration validation**

Replace the unconditional empty-route error in `validateRequests` with:

```go
if len(rc.Routes) == 0 && !rc.DirectDispatch.Enabled {
	errs = append(errs, ValidationError{
		Path: "requests",
		Msg:  "must configure routes or enable directDispatch",
	})
}
if rc.DirectDispatch.Enabled {
	if rc.DirectDispatch.Timeout <= 0 {
		errs = append(errs, ValidationError{
			Path: "requests.directDispatch.timeout",
			Msg:  "must be positive",
		})
	}
	for i, prefix := range rc.DirectDispatch.AllowedPathPrefixes {
		if err := requesttarget.ValidatePrefix(prefix); err != nil {
			errs = append(errs, ValidationError{
				Path: fmt.Sprintf("requests.directDispatch.allowedPathPrefixes[%d]", i),
				Msg:  err.Error(),
			})
		}
	}
}
```

Keep all existing per-route validation when routes are also present.

- [ ] **Step 5: Run config tests and commit**

Run:

```bash
gofmt -w internal/config/schema.go internal/config/schema_test.go internal/config/validate.go internal/config/validate_test.go
go test ./internal/config -count=1
```

Expected: PASS.

Commit:

```bash
git add event-adapter/internal/config/schema.go event-adapter/internal/config/schema_test.go event-adapter/internal/config/validate.go event-adapter/internal/config/validate_test.go
git commit -m "feat(event-adapter): configure direct request dispatch"
```

### Task 5: Build Generic Direct Replies

**Files:**
- Modify: `event-adapter/internal/cloudevent/response.go`
- Modify: `event-adapter/internal/cloudevent/response_test.go`

- [ ] **Step 1: Write a failing generic reply test**

Add to `internal/cloudevent/response_test.go`:

```go
func TestBuildDirectReplyUsesGenericEnvelope(t *testing.T) {
	in, err := Parse([]byte(`{"specversion":"1.0","id":"req-direct","source":"client","type":"orders.delete","correlationid":"corr-1","data":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	out, err := BuildReply(in, DirectReplyConfig("order-service"), DirectRouteName,
		http.StatusNoContent, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	if out.Type() != DirectReplyType || out.Source() != "order-service" {
		t.Fatalf("type/source = %q/%q", out.Type(), out.Source())
	}
	if out.Extensions()["causationid"] != "req-direct" {
		t.Fatalf("causationid = %v", out.Extensions()["causationid"])
	}
	if out.Extensions()["correlationid"] != "corr-1" {
		t.Fatalf("correlationid = %v", out.Extensions()["correlationid"])
	}
}
```

Add `net/http` to that test file's import block.

- [ ] **Step 2: Run the test and verify it fails**

Run:

```bash
go test ./internal/cloudevent -run TestBuildDirectReply -count=1
```

Expected: FAIL because the direct reply constants/helper do not exist.

- [ ] **Step 3: Add bounded direct reply metadata**

In `internal/cloudevent/response.go`, add:

```go
const (
	ErrorReplyType  = "io.eventadapter.error.reply"
	DirectReplyType = "io.eventadapter.direct.reply"
	DirectRouteName = "direct"
)

func DirectReplyConfig(source string) config.ReplyConfig {
	return config.ReplyConfig{Source: source, Type: DirectReplyType}
}
```

Leave `BuildReply` unchanged; the new helper deliberately feeds the existing
reply construction path.

- [ ] **Step 4: Run tests and commit**

Run:

```bash
gofmt -w internal/cloudevent/response.go internal/cloudevent/response_test.go
go test ./internal/cloudevent -count=1
```

Expected: PASS.

Commit:

```bash
git add event-adapter/internal/cloudevent/response.go event-adapter/internal/cloudevent/response_test.go
git commit -m "feat(event-adapter): add generic direct replies"
```

### Task 6: Add Exact-Route Precedence and Direct Fallback

**Files:**
- Modify: `event-adapter/internal/responder/responder.go`
- Modify: `event-adapter/internal/responder/responder_test.go`

- [ ] **Step 1: Make the fake dispatcher capture selected dispatch config**

Change `fakeDispatcher` in `internal/responder/responder_test.go` without
changing existing value-based test call sites:

```go
type fakeDispatcher struct {
	res dispatcher.Result
	err error
	got *config.DispatchConfig
}

func (f fakeDispatcher) Dispatch(_ context.Context, dc config.DispatchConfig, _ *clevent.Event) (dispatcher.Result, error) {
	if f.got != nil {
		*f.got = dc
	}
	return f.res, f.err
}
```

- [ ] **Step 2: Write failing selection and failure tests**

Add helpers and tests:

```go
func newDirectResponder(d Dispatcher, m Metrics, routes []config.RequestRouteConfig) *Responder {
	matcher, err := router.NewRequests(routes)
	if err != nil {
		panic(err)
	}
	return New(matcher, d, m, "order-service", &config.RequestsConfig{
		Subject: "q.orders", QueueGroup: "orders", WorkerPoolSize: 2,
		DirectDispatch: config.DirectDispatchConfig{
			Enabled: true, Timeout: 3 * time.Second,
			AllowedPathPrefixes: []string{"/orders/"},
		},
		Routes: routes,
	}, io.Discard)
}

func directMessage(eventType, method, path string) (natsjs.RequestMsg, *[]byte) {
	var out []byte
	body := fmt.Sprintf(`{"specversion":"1.0","id":"req-direct","source":"client","type":%q,"dispatchmethod":%q,"dispatchpath":%q,"correlationid":"corr-1","data":{"reason":"cleanup"}}`,
		eventType, method, path)
	return natsjs.RequestMsg{
		ReplyTo: "_INBOX.direct",
		Data:    []byte(body),
		Respond: func(b []byte) error { out = b; return nil },
	}, &out
}

func TestHandleUsesDirectFallbackForUnmatchedType(t *testing.T) {
	var selected config.DispatchConfig
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 204}, got: &selected}
	met := &fakeMetrics{}
	r := newDirectResponder(d, met, nil)
	m, out := directMessage("orders.delete", "delete", "/orders/ord-456?hard=true")
	r.handle(context.Background(), m)

	got := selected
	if got.Method != http.MethodDelete || got.Path != "/orders/ord-456?hard=true" ||
		got.Timeout != 3*time.Second {
		t.Fatalf("dispatch config = %+v", got)
	}
	reply := decode(t, *out)
	if reply["type"] != clevent.DirectReplyType || reply["source"] != "order-service" {
		t.Fatalf("direct reply = %v", reply)
	}
	if reply["causationid"] != "req-direct" || reply["correlationid"] != "corr-1" {
		t.Fatalf("direct reply identity = %v", reply)
	}
	if met.lastRoute != clevent.DirectRouteName {
		t.Fatalf("route metric label = %q", met.lastRoute)
	}
}

func TestHandleExactRouteTakesPrecedence(t *testing.T) {
	route := config.RequestRouteConfig{
		Name: "controlled",
		Match: config.RequestMatchConfig{Type: "orders.controlled"},
		Dispatch: config.DispatchConfig{
			Method: http.MethodPost, Path: "/controlled", Timeout: time.Second,
		},
		Reply: config.ReplyConfig{Source: "controlled", Type: "controlled.reply"},
	}
	var selected config.DispatchConfig
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 200}, got: &selected}
	r := newDirectResponder(d, &fakeMetrics{}, []config.RequestRouteConfig{route})
	m, _ := directMessage("orders.controlled", "OPTIONS", "http://example.com/admin")
	r.handle(context.Background(), m)

	got := selected
	if got.Method != http.MethodPost || got.Path != "/controlled" {
		t.Fatalf("publisher metadata overrode exact route: %+v", got)
	}
}

func TestHandleInvalidDirectTargetReplies400WithoutDispatch(t *testing.T) {
	tests := []struct {
		name, method, path string
	}{
		{name: "missing method", path: "/orders/ord-456"},
		{name: "missing path", method: "DELETE"},
		{name: "unsupported method", method: "OPTIONS", path: "/orders/ord-456"},
		{name: "outside prefix", method: "DELETE", path: "/admin"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var selected config.DispatchConfig
			d := fakeDispatcher{got: &selected}
			met := &fakeMetrics{}
			r := newDirectResponder(d, met, nil)
			m, out := directMessage("orders.delete", tc.method, tc.path)
			r.handle(context.Background(), m)

			reply := decode(t, *out)
			if reply["httpstatus"].(float64) != 400 {
				t.Fatalf("httpstatus = %v", reply["httpstatus"])
			}
			if selected.Method != "" {
				t.Fatal("backend was called for invalid target")
			}
			if met.invalid != 1 || met.invalidReason != "invalid_dispatch_target" {
				t.Fatalf("invalid metric = %d reason = %q", met.invalid, met.invalidReason)
			}
		})
	}
}

func TestHandleUnmatchedRouteWithDirectDisabledStillReplies404(t *testing.T) {
	matcher, _ := router.NewRequests(nil)
	var selected config.DispatchConfig
	d := fakeDispatcher{got: &selected}
	r := New(matcher, d, &fakeMetrics{}, "order-service", &config.RequestsConfig{
		Subject: "q.orders", QueueGroup: "orders", WorkerPoolSize: 1,
	}, io.Discard)
	m, out := directMessage("orders.delete", "DELETE", "/orders/ord-456")
	r.handle(context.Background(), m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 404 {
		t.Fatalf("httpstatus = %v", reply["httpstatus"])
	}
}
```

Add `net/http` and `event-adapter/internal/router` to the test imports. Replace
`fakeMetrics` with this complete bounded-label-aware fake:

```go
type fakeMetrics struct {
	mu                                              sync.Mutex
	received, dispatchErr, noReply, invalid, panics int
	lastRoute, invalidReason                        string
}

func (f *fakeMetrics) RequestReceived(_ context.Context, route string) {
	f.mu.Lock()
	f.received++
	f.lastRoute = route
	f.mu.Unlock()
}

func (f *fakeMetrics) RequestReplyLatency(context.Context, string, time.Duration) {}

func (f *fakeMetrics) RequestDispatchError(context.Context, string) {
	f.mu.Lock()
	f.dispatchErr++
	f.mu.Unlock()
}

func (f *fakeMetrics) RequestNoReply(context.Context) {
	f.mu.Lock()
	f.noReply++
	f.mu.Unlock()
}

func (f *fakeMetrics) InvalidRequestEvent(_ context.Context, reason string) {
	f.mu.Lock()
	f.invalid++
	f.invalidReason = reason
	f.mu.Unlock()
}

func (f *fakeMetrics) PanicRecovered(context.Context, string) {
	f.mu.Lock()
	f.panics++
	f.mu.Unlock()
}
```

- [ ] **Step 3: Run responder tests and verify they fail**

Run:

```bash
go test ./internal/responder -run 'TestHandle(UsesDirect|ExactRoute|InvalidDirect|UnmatchedRoute)' -count=1
```

Expected: FAIL because unmatched requests still return `404`.

- [ ] **Step 4: Implement direct route selection**

Import `event-adapter/internal/requesttarget` and replace the current route miss
branch with:

```go
route, ok := r.matcher.Match(ev)
if !ok {
	if !r.cfg.DirectDispatch.Enabled {
		r.metrics.InvalidRequestEvent(ctx, "no_route")
		r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusNotFound, "no matching route"))
		return
	}
	target, targetErr := requesttarget.Resolve(
		ev.DispatchMethod,
		ev.DispatchPath,
		r.cfg.DirectDispatch.AllowedPathPrefixes,
	)
	if targetErr != nil {
		r.metrics.InvalidRequestEvent(ctx, "invalid_dispatch_target")
		r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusBadRequest, targetErr.Error()))
		return
	}
	route = config.RequestRouteConfig{
		Name: clevent.DirectRouteName,
		Dispatch: config.DispatchConfig{
			Method:  target.Method,
			Path:    target.Path,
			Timeout: r.cfg.DirectDispatch.Timeout,
		},
		Reply: clevent.DirectReplyConfig(r.appID),
	}
}
```

Leave the existing dispatch, timeout/502/504 mapping, normal HTTP response
handling, and `BuildReply` calls shared below this branch.

- [ ] **Step 5: Run all responder and router tests**

Run:

```bash
gofmt -w internal/responder/responder.go internal/responder/responder_test.go
go test ./internal/responder ./internal/router -count=1
```

Expected: PASS, including all pre-existing static-route tests.

- [ ] **Step 6: Commit**

```bash
git add event-adapter/internal/responder/responder.go event-adapter/internal/responder/responder_test.go
git commit -m "feat(event-adapter): dispatch unmatched requests directly"
```

### Task 7: Add Route-Free DELETE End-to-End Coverage

**Files:**
- Modify: `event-adapter/test/e2e/routes.yaml`
- Modify: `event-adapter/test/e2e/mock-app.yaml`
- Create: `event-adapter/test/e2e/fixtures/direct-delete.json`
- Modify: `event-adapter/test/e2e/e2e_test.go`

- [ ] **Step 1: Add a failing e2e test and fixture**

Create `test/e2e/fixtures/direct-delete.json`:

```json
{
  "specversion": "1.0",
  "id": "req-direct-delete-1",
  "source": "workspace/orders-client",
  "type": "com.workspace.orders.delete.request",
  "datacontenttype": "application/json",
  "correlationid": "corr-direct-delete-1",
  "dispatchmethod": "DELETE",
  "dispatchpath": "/requests/orders/ord-456?hard=true",
  "data": {
    "reason": "cleanup"
  }
}
```

Add to `test/e2e/e2e_test.go`:

```go
func TestRequestReplyDirectDelete(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	fixture, err := os.ReadFile("fixtures/direct-delete.json")
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
	if reply["type"] != "io.eventadapter.direct.reply" {
		t.Fatalf("type = %v", reply["type"])
	}
	if reply["source"] != "task-service" {
		t.Fatalf("source = %v", reply["source"])
	}
	if reply["httpstatus"].(float64) != 200 {
		t.Fatalf("httpstatus = %v", reply["httpstatus"])
	}
	if reply["causationid"] != "req-direct-delete-1" ||
		reply["correlationid"] != "corr-direct-delete-1" {
		t.Fatalf("reply identity = %v", reply)
	}
	data := reply["data"].(map[string]any)
	if data["deleted"] != "ord-456" || data["hard"] != true {
		t.Fatalf("data = %v", data)
	}
}
```

- [ ] **Step 2: Run the e2e test and verify it fails**

Run from `event-adapter/test/e2e/`:

```bash
docker compose up --build -d
cd ../..
go test -tags=e2e ./test/e2e/... -run TestRequestReplyDirectDelete -v -count=1
```

Expected: FAIL with a `404` reply because direct dispatch is not enabled in the
e2e config and the request type has no exact route.

- [ ] **Step 3: Enable direct dispatch and add the mock DELETE handler**

Under `requests` in `test/e2e/routes.yaml`, add:

```yaml
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes:
      - /requests/orders/
```

Add to `test/e2e/mock-app.yaml`:

```yaml
  - method: DELETE
    path: /requests/orders/{orderId}
    response:
      status: 200
      contentType: application/json
      body: '{"deleted":"ord-456","hard":true}'
```

The handler match proves that the resolved path reached the DELETE endpoint;
query preservation is covered directly in `internal/requesttarget` and
responder unit tests.

- [ ] **Step 4: Rebuild and run request-reply e2e coverage**

Run:

```bash
cd test/e2e
docker compose up --build -d
cd ../..
go test -tags=e2e ./test/e2e/... -run 'TestRequestReply(Presign|RedirectCarriesHTTPLocation|DirectDelete)' -v -count=1
```

Expected: all three request-reply tests PASS.

Stop the environment:

```bash
cd test/e2e
docker compose down
```

- [ ] **Step 5: Commit**

```bash
git add event-adapter/test/e2e/routes.yaml event-adapter/test/e2e/mock-app.yaml event-adapter/test/e2e/fixtures/direct-delete.json event-adapter/test/e2e/e2e_test.go
git commit -m "test(event-adapter): cover direct DELETE requests"
```

### Task 8: Update Contracts, Developer Guidance, and Run Full Verification

**Files:**
- Modify: `event-adapter/AGENTS.md`
- Modify: `event-adapter/README.md`
- Modify: `prd/event-adapter/prd.md`
- Modify: `prd/event-adapter/app-developer-guide.md`

- [ ] **Step 1: Update the module config reference**

In `event-adapter/AGENTS.md`, add the direct mode example:

```yaml
requests:
  subject: q.tenant-a.app.orders.request
  queueGroup: order-responders
  workerPoolSize: 16
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes: [/orders/]
```

Document beside it:

```text
Exact request routes take precedence. Only unmatched synchronous requests use
dispatchmethod and the publisher's fully resolved dispatchpath. Direct replies
use type io.eventadapter.direct.reply and source app.id. JetStream never uses
publisher-directed targets.
```

- [ ] **Step 2: Update the README and PRD contracts**

In `event-adapter/README.md` and PRD section 17, document this request:

```json
{
  "specversion": "1.0",
  "id": "req-123",
  "source": "checkout-service",
  "type": "com.workspace.orders.delete.request",
  "dispatchmethod": "DELETE",
  "dispatchpath": "/orders/ord-456?hard=true",
  "data": {}
}
```

State all normative behavior explicitly:

- direct dispatch is opt-in and request-reply-only
- exact type route wins
- the publisher supplies a fully resolved relative path
- the base URL always comes from loopback-only `app.httpBaseURL`
- allowed methods are GET, POST, PUT, PATCH, and DELETE
- invalid direct targets return 400 without a backend call
- direct disabled plus no exact route returns 404
- normal direct replies use `io.eventadapter.direct.reply`, `app.id`, and no
  subject
- correlation, causation, status, location, headers, cookies, GET body
  suppression, 502, and 504 keep their existing behavior
- static JetStream routes may use DELETE, but JetStream cannot use
  publisher-selected targets

- [ ] **Step 3: Update the app developer guide**

Add a “Direct synchronous dispatch” subsection to
`prd/event-adapter/app-developer-guide.md` containing the config and request
examples above, plus this decision guide:

```text
Use an exact request route when the operator must control the backend method,
path, static headers, forwarded-header allowlist, reply type, or reply source.
Use direct dispatch for authorized publishers when the backend operation set is
large and the service can safely expose the configured path prefixes.
```

Update every stale method list in the guide from POST/PUT/PATCH or
GET/POST/PUT/PATCH to GET/POST/PUT/PATCH/DELETE.

- [ ] **Step 4: Run documentation and source consistency scans**

Run from the repository root:

```bash
rg -n 'must be `POST`, `PUT`, or `PATCH`|GET, POST, PUT, and PATCH' event-adapter prd/event-adapter
rg -n 'dispatchmethod|dispatchpath|directDispatch|io.eventadapter.direct.reply' event-adapter prd/event-adapter
```

Expected: the first scan finds no stale allowlist; the second finds the new
schema, implementation, tests, and documentation.

- [ ] **Step 5: Run the standard module check**

Run from `event-adapter/`:

```bash
go build ./...
go vet ./...
go test ./...
test -z "$(gofmt -l .)"
```

Expected: all commands exit 0 and `gofmt -l` prints nothing.

- [ ] **Step 6: Run the complete e2e suite**

Run:

```bash
cd test/e2e
docker compose up --build -d
cd ../..
go test -tags=e2e ./test/e2e/... -v -count=1
cd test/e2e
docker compose down
```

Expected: all e2e tests PASS and the Compose environment is removed.

- [ ] **Step 7: Commit documentation**

```bash
git add event-adapter/AGENTS.md event-adapter/README.md prd/event-adapter/prd.md prd/event-adapter/app-developer-guide.md
git commit -m "docs(event-adapter): document direct request dispatch"
```

- [ ] **Step 8: Verify the final commit range**

Run from the repository root:

```bash
git status --short
git log --oneline --decorate -8
git diff origin/main...HEAD --check
```

Expected: only pre-existing unrelated files remain untracked, the feature
commits are visible, and the diff check exits 0.
