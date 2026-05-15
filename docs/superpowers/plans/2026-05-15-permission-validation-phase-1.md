# Permission Validation Phase 1 — Sidecar Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the Phase 1 `permission-validation` sidecar in Go 1.25 — an Envoy `ext_proc` gRPC handler that extracts `Authorization` + `X-Auth-Context` from intercepted requests, calls the Permission Checking Service (PCS), and returns `CONTINUE` (allow) or `ImmediateResponse(403)` (deny / fail-closed) — plus a `validate-routes` CLI that turns the Phase 1 route-config YAML into Envoy static config.

**Architecture:** Single Go module `permission-validation` with two binaries: `cmd/permission-validation` (the sidecar gRPC server) and `cmd/validate-routes` (the YAML validator + Envoy translator). Internal packages: `internal/header` (extract + parse), `internal/pcs` (HTTP client to PCS), `internal/metrics` (OpenTelemetry meter + counters/histograms), `internal/extproc` (gRPC `ExternalProcessor` server + handler), `internal/config` (route YAML schema + translator). End-to-end tests live under `test/e2e` and use Docker Compose to bring up real Envoy, a fake PCS, and a fake app backend.

**Tech Stack:** Go 1.25, `google.golang.org/grpc`, `github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3`, `go.opentelemetry.io/otel/metric` (OTel API) + `go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc` (OTLP gRPC exporter), `gopkg.in/yaml.v3`, `github.com/stretchr/testify`. End-to-end harness uses real Envoy (image `envoyproxy/envoy:v1.31-latest`) via Docker Compose.

**Design references** (already approved — do not modify):

- [phase-1-architecture.md](../../../prd/permission-validation/phase-1-architecture.md) — components, data flow, invariants.
- [phase-1-topology-decision.md](../../../prd/permission-validation/phase-1-topology-decision.md) — why Envoy `ext_proc` (Option B).
- [phase-1-request-contract.md](../../../prd/permission-validation/phase-1-request-contract.md) — client→sidecar and sidecar→PCS wire contracts, rejection cases.
- [phase-1-context-header-format.md](../../../prd/permission-validation/phase-1-context-header-format.md) — `X-Auth-Context` grammar and rejection labels.
- [phase-1-route-config-schema.md](../../../prd/permission-validation/phase-1-route-config-schema.md) — YAML schema for protected/skipped routes.
- [phase-1-user-stories.md](../../../prd/permission-validation/phase-1-user-stories.md) — PV1-001 … PV1-012.

---

## File Structure

All paths are relative to `/home/cjamhe01385/workspace/permission-validation/` (the new Go module root, sibling of `prd/` and `docs/`).

```
permission-validation/
├── go.mod                                  # module permission-validation; go 1.25
├── go.sum
├── .gitignore                              # bin/, vendor/, *.test
├── cmd/
│   ├── permission-validation/
│   │   └── main.go                         # sidecar binary: flags → wire OTel + extproc + pcs → grpc.Serve
│   └── validate-routes/
│       └── main.go                         # CLI: validate <file>, translate <file> -o <envoy.yaml>
├── internal/
│   ├── header/
│   │   ├── extract.go                      # ExtractAuth, ExtractContext from net/http.Header
│   │   ├── extract_test.go
│   │   ├── parse.go                        # ParseContextHeader → ParsedContext or *ParseError
│   │   └── parse_test.go
│   ├── pcs/
│   │   ├── client.go                       # HTTP client; Check(ctx, req, ssoToken) (Decision, error)
│   │   └── client_test.go
│   ├── metrics/
│   │   ├── metrics.go                      # New(meter) *Metrics; counters + histograms with label attrs
│   │   └── metrics_test.go
│   ├── extproc/
│   │   ├── server.go                       # ExternalProcessor gRPC server; one Process stream per request
│   │   ├── server_test.go
│   │   ├── handler.go                      # Decide(ctx, hdr Headers) Outcome — wires header → parse → pcs
│   │   ├── handler_test.go
│   │   └── response.go                     # Helpers to build CONTINUE / ImmediateResponse(403) replies
│   └── config/
│       ├── schema.go                       # YAML types (RouteConfig, RouteRule); UnmarshalYAML hooks
│       ├── schema_test.go
│       ├── validate.go                     # Validate(*RouteConfig) []ValidationError
│       ├── validate_test.go
│       ├── translate.go                    # Translate(*RouteConfig) → Envoy static-config YAML bytes
│       └── translate_test.go
├── testdata/
│   ├── routes/
│   │   ├── valid-minimal.yaml
│   │   ├── valid-comprehensive.yaml
│   │   ├── invalid-wrong-version.yaml
│   │   ├── invalid-empty-routes.yaml
│   │   ├── invalid-bad-method.yaml
│   │   └── invalid-bad-behavior.yaml
│   └── envoy/
│       └── envoy-static.tmpl.yaml          # Go text/template used by translate.go
├── test/e2e/
│   ├── docker-compose.yaml                 # envoy + sidecar + fake-pcs + fake-backend
│   ├── envoy.yaml                          # static Envoy config produced by validate-routes
│   ├── routes.yaml                         # sample app route config used to generate envoy.yaml
│   ├── fakes/
│   │   ├── pcs/main.go                     # programmable fake PCS over HTTP
│   │   └── backend/main.go                 # records every received request
│   ├── e2e_test.go                         # //go:build e2e — drives docker compose, asserts outcomes
│   └── README.md                           # how to run; intentionally NOT for user docs
└── examples/
    └── onboarding/
        ├── routes.yaml                     # sample app route config (PV1-012)
        ├── client-request.sh               # curl snippets producing valid + invalid headers
        └── README.md                       # PV1-012 onboarding doc
```

Each file has one responsibility. Tests sit next to the code they cover, except for cross-cutting end-to-end tests under `test/e2e`.

---

## Phase 0 Map — User Story to Task

| User Story | Tasks | Notes |
|---|---|---|
| PV1-004 (route config schema) | Task 8, 9, 10 | Design is done; we implement the validator + Envoy translator + CLI. |
| PV1-005 (route matching) | Task 9, 14 | Under Option B, matching happens in Envoy; we generate the Envoy route table from YAML. |
| PV1-006 (header extraction) | Task 3 | `internal/header/extract.go`. |
| PV1-007 (context-header parse) | Task 2 | `internal/header/parse.go` with reason labels from PV1-003. |
| PV1-008 (PCS request) | Task 4 | `internal/pcs/client.go`. |
| PV1-009 (enforce) | Task 6, 7 | `internal/extproc/handler.go` orchestrates the outcome; `server.go` translates to ext_proc reply. |
| PV1-010 (metrics) | Task 5 | OTel meter wired into all of the above; `internal/metrics/metrics.go`. |
| PV1-011 (integration tests) | Task 13, 14, 15 | Fakes + Docker Compose + e2e Go tests. |
| PV1-012 (onboarding example) | Task 16 | `examples/onboarding/`. |

---

## Task 1: Bootstrap the `permission-validation` Go module

**Files:**
- Create: `permission-validation/go.mod`
- Create: `permission-validation/.gitignore`
- Create: `permission-validation/cmd/permission-validation/main.go`
- Create: `permission-validation/cmd/permission-validation/main_test.go`

- [ ] **Step 1: Create the module directory and `go.mod`**

```bash
mkdir -p permission-validation/cmd/permission-validation
cd permission-validation
go mod init permission-validation
```

Edit `permission-validation/go.mod` to pin Go 1.25 explicitly:

```
module permission-validation

go 1.25
```

- [ ] **Step 2: Add `.gitignore`**

```
# Go build artifacts
bin/
*.test
*.out
coverage.txt

# Editor / OS
.idea/
.vscode/
.DS_Store
```

- [ ] **Step 3: Write a placeholder `main.go` with a unit-testable entrypoint**

Create `cmd/permission-validation/main.go`:

```go
package main

import (
	"context"
	"fmt"
	"io"
	"os"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "permission-validation:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	fmt.Fprintln(stdout, "permission-validation: not yet implemented")
	return nil
}
```

- [ ] **Step 4: Write a smoke test for `run`**

Create `cmd/permission-validation/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunPrintsBanner(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "permission-validation") {
		t.Fatalf("expected banner in stdout, got %q", stdout.String())
	}
}
```

- [ ] **Step 5: Run the test**

```bash
cd permission-validation && go test ./cmd/permission-validation/...
```

Expected: `ok permission-validation/cmd/permission-validation`.

- [ ] **Step 6: Commit**

```bash
git add permission-validation/go.mod permission-validation/.gitignore permission-validation/cmd/permission-validation/
git commit -m "feat(permission-validation): bootstrap go module skeleton"
```

---

## Task 2: Context-header parser (PV1-007)

The parser is the only line of defense against malformed values reaching PCS. Rules are normative — see [phase-1-context-header-format.md §4](../../../prd/permission-validation/phase-1-context-header-format.md#4-validation-rules-and-rejection).

**Files:**
- Create: `permission-validation/internal/header/parse.go`
- Create: `permission-validation/internal/header/parse_test.go`

- [ ] **Step 1: Write the failing test for valid input**

Create `internal/header/parse_test.go`:

```go
package header

import (
	"strings"
	"testing"
)

func TestParseContextHeader_Valid(t *testing.T) {
	got, err := ParseContextHeader("doc-42:document:edit")
	if err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
	if got.ObjectID != "doc-42" || got.ObjectType != "document" || got.Action != "edit" {
		t.Fatalf("unexpected parse result: %#v", got)
	}
}

func TestParseContextHeader_Rejections(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantReason string
	}{
		{"wrong segment count - too few", "doc-42:document", "wrong_segment_count"},
		{"wrong segment count - too many", "a:b:c:d", "wrong_segment_count"},
		{"empty leading segment", ":document:edit", "empty_segment"},
		{"empty middle segment", "doc-42::edit", "empty_segment"},
		{"empty trailing segment", "doc-42:document:", "empty_segment"},
		{"leading whitespace", " doc-42:document:edit", "whitespace"},
		{"trailing whitespace", "doc-42:document:edit ", "whitespace"},
		{"interior whitespace", "doc-42:doc type:edit", "whitespace"},
		{"control character", "doc-42:document:edit\x01", "control_char"},
		{"non-printable", "doc-42:document:edit\xff", "non_printable"},
		{"over length", strings.Repeat("a", 1024-2) + ":b:c", "over_length"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseContextHeader(c.input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}
			if pe.Reason != c.wantReason {
				t.Fatalf("reason: got %q want %q", pe.Reason, c.wantReason)
			}
		})
	}
}
```

- [ ] **Step 2: Run it; confirm it fails**

```bash
go test ./internal/header/...
```

Expected: FAIL — undefined `ParseContextHeader`, `ParsedContext`, `ParseError`.

- [ ] **Step 3: Implement the parser**

Create `internal/header/parse.go`:

```go
package header

import (
	"unicode/utf8"
)

const MaxContextHeaderLen = 1024

// ParsedContext is the result of splitting X-Auth-Context.
// Field semantics come from phase-1-request-contract.md §3.1.
type ParsedContext struct {
	ObjectID   string
	ObjectType string
	Action     string
}

// ParseError carries the metric reason label defined in phase-1-context-header-format.md §4.
type ParseError struct {
	Reason string
}

func (e *ParseError) Error() string { return "context header parse failure: " + e.Reason }

// ParseContextHeader applies the rules from phase-1-context-header-format.md.
// It never returns a partial result on error.
func ParseContextHeader(v string) (ParsedContext, error) {
	if len(v) > MaxContextHeaderLen {
		return ParsedContext{}, &ParseError{Reason: "over_length"}
	}
	if !utf8.ValidString(v) {
		return ParsedContext{}, &ParseError{Reason: "non_printable"}
	}
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b > 0x7E {
			return ParsedContext{}, &ParseError{Reason: "non_printable"}
		}
		if b < 0x20 || b == 0x7F {
			return ParsedContext{}, &ParseError{Reason: "control_char"}
		}
	}

	segs := splitExact(v, ':', 3)
	if segs == nil {
		return ParsedContext{}, &ParseError{Reason: "wrong_segment_count"}
	}
	for _, s := range segs {
		if s == "" {
			return ParsedContext{}, &ParseError{Reason: "empty_segment"}
		}
	}
	for _, s := range segs {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == ' ' || c == '\t' {
				return ParsedContext{}, &ParseError{Reason: "whitespace"}
			}
		}
	}
	return ParsedContext{ObjectID: segs[0], ObjectType: segs[1], Action: segs[2]}, nil
}

// splitExact returns nil when v does not have exactly n segments separated by sep.
func splitExact(v string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(v); i++ {
		if v[i] == sep {
			if len(out) == n-1 {
				return nil
			}
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	out = append(out, v[start:])
	if len(out) != n {
		return nil
	}
	return out
}
```

- [ ] **Step 4: Run tests; confirm they pass**

```bash
go test ./internal/header/... -v
```

Expected: all `TestParseContextHeader_*` PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/header/parse.go internal/header/parse_test.go
git commit -m "feat(header): parse X-Auth-Context per phase-1-context-header-format spec"
```

---

## Task 3: Header extractor (PV1-006)

Pulls SSO token and `X-Auth-Context` from request metadata. Both must be present and well-formed; missing values produce labeled errors per [phase-1-request-contract.md §5](../../../prd/permission-validation/phase-1-request-contract.md#5-rejection-cases).

**Files:**
- Create: `permission-validation/internal/header/extract.go`
- Create: `permission-validation/internal/header/extract_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/header/extract_test.go`:

```go
package header

import (
	"testing"
)

func TestExtractAuth(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		wantToken  string
		wantReason string
	}{
		{"valid bearer", map[string]string{"authorization": "Bearer abc.def.ghi"}, "abc.def.ghi", ""},
		{"missing", map[string]string{}, "", "missing_authz"},
		{"wrong scheme", map[string]string{"authorization": "Basic xyz"}, "", "malformed_authz"},
		{"empty token", map[string]string{"authorization": "Bearer "}, "", "malformed_authz"},
		{"lowercase scheme rejected", map[string]string{"authorization": "bearer abc"}, "", "malformed_authz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, err := ExtractAuth(c.headers)
			if c.wantReason == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tok != c.wantToken {
					t.Fatalf("token: got %q want %q", tok, c.wantToken)
				}
				return
			}
			he, ok := err.(*HeaderError)
			if !ok {
				t.Fatalf("expected *HeaderError, got %T", err)
			}
			if he.Reason != c.wantReason {
				t.Fatalf("reason: got %q want %q", he.Reason, c.wantReason)
			}
		})
	}
}

func TestExtractContext(t *testing.T) {
	if v, err := ExtractContext(map[string]string{"x-auth-context": "doc-42:document:edit"}); err != nil || v != "doc-42:document:edit" {
		t.Fatalf("got (%q, %v)", v, err)
	}
	_, err := ExtractContext(map[string]string{})
	if err == nil {
		t.Fatal("expected missing_ctx error")
	}
	if he := err.(*HeaderError); he.Reason != "missing_ctx" {
		t.Fatalf("reason: got %q want missing_ctx", he.Reason)
	}
}
```

- [ ] **Step 2: Run it; confirm it fails**

```bash
go test ./internal/header/... -run Extract -v
```

Expected: FAIL — undefined `ExtractAuth`, `ExtractContext`, `HeaderError`.

- [ ] **Step 3: Implement extraction**

Create `internal/header/extract.go`:

```go
package header

import "strings"

// HeaderError carries the metric reason label defined in phase-1-request-contract.md §5.
type HeaderError struct{ Reason string }

func (e *HeaderError) Error() string { return "header invalid: " + e.Reason }

// ExtractAuth returns the bearer token from the lowercase `authorization` header.
// Envoy lower-cases header names on the wire (HTTP/2), so callers should pass a
// map keyed by lowercase header names.
func ExtractAuth(h map[string]string) (string, error) {
	v, ok := h["authorization"]
	if !ok || v == "" {
		return "", &HeaderError{Reason: "missing_authz"}
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(v, prefix) {
		return "", &HeaderError{Reason: "malformed_authz"}
	}
	tok := v[len(prefix):]
	if tok == "" {
		return "", &HeaderError{Reason: "malformed_authz"}
	}
	return tok, nil
}

// ExtractContext returns the raw X-Auth-Context value, or a missing_ctx HeaderError.
// Format validation happens in ParseContextHeader.
func ExtractContext(h map[string]string) (string, error) {
	v, ok := h["x-auth-context"]
	if !ok || v == "" {
		return "", &HeaderError{Reason: "missing_ctx"}
	}
	return v, nil
}
```

- [ ] **Step 4: Run tests; confirm they pass**

```bash
go test ./internal/header/... -v
```

Expected: all extract tests PASS, parser tests still PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/header/extract.go internal/header/extract_test.go
git commit -m "feat(header): extract Authorization and X-Auth-Context with labeled errors"
```

---

## Task 4: PCS HTTP client (PV1-008)

Builds the PCS request body from a parsed context and forwards the SSO token unchanged. Treats any non-200 status (or transport error / timeout) as a "decision = error" outcome for the caller to interpret as deny per [phase-1-architecture.md §3](../../../prd/permission-validation/phase-1-architecture.md#3-key-invariants).

**Files:**
- Create: `permission-validation/internal/pcs/client.go`
- Create: `permission-validation/internal/pcs/client_test.go`

- [ ] **Step 1: Add dependencies for tests**

```bash
cd permission-validation
go get github.com/stretchr/testify@latest
```

- [ ] **Step 2: Write the failing test**

Create `internal/pcs/client_test.go`:

```go
package pcs

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClient_Allow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/permission-check/v1/check", r.URL.Path)
		require.Equal(t, "POST", r.Method)
		require.Equal(t, "Bearer sso-tok", r.Header.Get("Authorization"))
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		require.Equal(t, "req-1", r.Header.Get("X-Request-Id"))

		var body struct {
			ObjectID   string `json:"objectId"`
			ObjectType string `json:"objectType"`
			Permission string `json:"permission"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		require.Equal(t, "doc-1", body.ObjectID)
		require.Equal(t, "document", body.ObjectType)
		require.Equal(t, "edit", body.Permission)
		_, _ = io.WriteString(w, `{"allowed": true}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{
		ObjectID: "doc-1", ObjectType: "document", Permission: "edit",
		SSOToken: "sso-tok", RequestID: "req-1",
	})
	require.NoError(t, err)
	require.Equal(t, DecisionAllow, dec)
}

func TestClient_Deny(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"allowed": false}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	dec, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.NoError(t, err)
	require.Equal(t, DecisionDeny, dec)
}

func TestClient_5xxIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", 503)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 500*time.Millisecond)
	_, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
}

func TestClient_TimeoutIsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(50 * time.Millisecond)
		_, _ = io.WriteString(w, `{"allowed": true}`)
	}))
	defer srv.Close()

	c := NewClient(srv.URL, 5*time.Millisecond)
	_, err := c.Check(context.Background(), CheckRequest{ObjectID: "x", ObjectType: "y", Permission: "z", SSOToken: "tok"})
	require.Error(t, err)
}
```

- [ ] **Step 3: Run it; confirm it fails**

```bash
go test ./internal/pcs/... -v
```

Expected: FAIL — undefined `Client`, `NewClient`, `Check`, `CheckRequest`, `Decision*`.

- [ ] **Step 4: Implement the client**

Create `internal/pcs/client.go`:

```go
package pcs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Decision is the resolved PCS outcome.
type Decision int

const (
	DecisionUnknown Decision = iota
	DecisionAllow
	DecisionDeny
)

// CheckRequest is the input to Check; fields map 1:1 onto phase-1-request-contract.md §3.
type CheckRequest struct {
	ObjectID   string
	ObjectType string
	Permission string
	SSOToken   string
	RequestID  string // optional
}

// Client calls the Permission Checking Service.
type Client struct {
	endpoint string
	http     *http.Client
}

// NewClient returns a Client targeting endpoint+"/permission-check/v1/check".
// timeout bounds the per-call HTTP timeout; callers may also cancel via ctx.
func NewClient(endpoint string, timeout time.Duration) *Client {
	return &Client{
		endpoint: endpoint,
		http:     &http.Client{Timeout: timeout},
	}
}

type checkBody struct {
	ObjectID   string `json:"objectId"`
	ObjectType string `json:"objectType"`
	Permission string `json:"permission"`
}

type checkResp struct {
	Allowed bool `json:"allowed"`
}

// Check performs POST /permission-check/v1/check.
// Returns (DecisionAllow|DecisionDeny, nil) on a 2xx with a parsable body.
// Returns (DecisionUnknown, err) on transport error, timeout, non-2xx, or JSON failure.
// Callers treat any error as fail-closed (return 403) per PV1-009.
func (c *Client) Check(ctx context.Context, req CheckRequest) (Decision, error) {
	payload, err := json.Marshal(checkBody{ObjectID: req.ObjectID, ObjectType: req.ObjectType, Permission: req.Permission})
	if err != nil {
		return DecisionUnknown, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/permission-check/v1/check", bytes.NewReader(payload))
	if err != nil {
		return DecisionUnknown, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.SSOToken)
	if req.RequestID != "" {
		httpReq.Header.Set("X-Request-Id", req.RequestID)
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return DecisionUnknown, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return DecisionUnknown, err
	}
	if resp.StatusCode/100 != 2 {
		return DecisionUnknown, fmt.Errorf("pcs: status %d body=%q", resp.StatusCode, truncate(body, 256))
	}
	var cr checkResp
	if err := json.Unmarshal(body, &cr); err != nil {
		return DecisionUnknown, fmt.Errorf("pcs: decode response: %w", err)
	}
	if cr.Allowed {
		return DecisionAllow, nil
	}
	return DecisionDeny, nil
}

func truncate(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}
```

- [ ] **Step 5: Run tests; confirm they pass**

```bash
go test ./internal/pcs/... -v
```

Expected: 4 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/pcs/ go.mod go.sum
git commit -m "feat(pcs): HTTP client for /permission-check/v1/check with fail-closed errors"
```

---

## Task 5: OpenTelemetry metrics (PV1-010)

Metrics fall into three buckets: decision outcomes, header/parse failures, and latencies. Reason labels mirror the spec exactly so SREs can read a histogram or counter without consulting code.

**Files:**
- Create: `permission-validation/internal/metrics/metrics.go`
- Create: `permission-validation/internal/metrics/metrics_test.go`

- [ ] **Step 1: Add OTel dependencies**

```bash
cd permission-validation
go get go.opentelemetry.io/otel/metric@latest
go get go.opentelemetry.io/otel/sdk/metric@latest
```

- [ ] **Step 2: Write the failing test using OTel's manual reader**

Create `internal/metrics/metrics_test.go`:

```go
package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func setup(t *testing.T) (*Metrics, *metric.ManualReader) {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	m := New(mp.Meter("test"))
	return m, reader
}

func collect(t *testing.T, r *metric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, r.Collect(context.Background(), &rm))
	return rm
}

func sumByLabel(t *testing.T, rm metricdata.ResourceMetrics, instrument, labelKey string) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != instrument {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %s not int64 Sum", instrument)
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value(labelKey)
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestDecisionsCounter(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.Decision(ctx, "allow")
	m.Decision(ctx, "allow")
	m.Decision(ctx, "deny")
	m.Decision(ctx, "error")

	rm := collect(t, r)
	got := sumByLabel(t, rm, "pv.decisions.total", "outcome")
	require.Equal(t, map[string]int64{"allow": 2, "deny": 1, "error": 1}, got)
}

func TestHeaderInvalidCounter(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.HeaderInvalid(ctx, "missing_authz")
	m.HeaderInvalid(ctx, "missing_ctx")
	m.HeaderInvalid(ctx, "missing_ctx")

	rm := collect(t, r)
	got := sumByLabel(t, rm, "pv.header_invalid.total", "reason")
	require.Equal(t, map[string]int64{"missing_authz": 1, "missing_ctx": 2}, got)
}

func TestCtxParseFailureCounter(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.CtxParseFailure(ctx, "wrong_segment_count")
	m.CtxParseFailure(ctx, "over_length")

	rm := collect(t, r)
	got := sumByLabel(t, rm, "pv.ctx_parse_failure.total", "reason")
	require.Equal(t, map[string]int64{"wrong_segment_count": 1, "over_length": 1}, got)
}

func TestLatencyHistograms(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.SidecarLatency(ctx, 3*time.Millisecond)
	m.PCSLatency(ctx, 2*time.Millisecond)

	rm := collect(t, r)
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			seen[mt.Name] = true
		}
	}
	require.True(t, seen["pv.sidecar.latency"], "missing pv.sidecar.latency")
	require.True(t, seen["pv.pcs.latency"], "missing pv.pcs.latency")
}
```

- [ ] **Step 3: Run it; confirm it fails**

```bash
go test ./internal/metrics/... -v
```

Expected: FAIL — undefined `New`, `Metrics`, `Decision`, etc.

- [ ] **Step 4: Implement the metrics package**

Create `internal/metrics/metrics.go`:

```go
package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the Phase 1 SRE instruments. Names map to phase-1-request-contract.md §5
// and phase-1-context-header-format.md §4.
type Metrics struct {
	decisions       metric.Int64Counter
	headerInvalid   metric.Int64Counter
	ctxParseFailure metric.Int64Counter
	sidecarLatency  metric.Float64Histogram
	pcsLatency      metric.Float64Histogram
}

// New builds a Metrics from an OTel Meter. Panic-free; instrument creation errors are
// wrapped into a single panic at startup because a missing meter is a deploy-time bug.
func New(meter metric.Meter) *Metrics {
	mustC := func(name, desc string) metric.Int64Counter {
		c, err := meter.Int64Counter(name, metric.WithDescription(desc))
		if err != nil {
			panic(err)
		}
		return c
	}
	mustH := func(name, desc string) metric.Float64Histogram {
		h, err := meter.Float64Histogram(name, metric.WithDescription(desc), metric.WithUnit("ms"))
		if err != nil {
			panic(err)
		}
		return h
	}
	return &Metrics{
		decisions:       mustC("pv.decisions.total", "Decision outcome counts (allow/deny/error)"),
		headerInvalid:   mustC("pv.header_invalid.total", "Header-presence and well-formedness failures"),
		ctxParseFailure: mustC("pv.ctx_parse_failure.total", "X-Auth-Context parse failures"),
		sidecarLatency:  mustH("pv.sidecar.latency", "End-to-end sidecar handling latency"),
		pcsLatency:      mustH("pv.pcs.latency", "PCS HTTP call latency"),
	}
}

// Decision records one outcome. outcome ∈ {"allow","deny","error"}.
func (m *Metrics) Decision(ctx context.Context, outcome string) {
	m.decisions.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// HeaderInvalid records one rejection. reason ∈ {"missing_authz","missing_ctx","malformed_authz"}.
func (m *Metrics) HeaderInvalid(ctx context.Context, reason string) {
	m.headerInvalid.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// CtxParseFailure records one parse rejection. reason from phase-1-context-header-format.md §4.
func (m *Metrics) CtxParseFailure(ctx context.Context, reason string) {
	m.ctxParseFailure.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// SidecarLatency records the wall-clock time the sidecar held the request.
func (m *Metrics) SidecarLatency(ctx context.Context, d time.Duration) {
	m.sidecarLatency.Record(ctx, float64(d.Microseconds())/1000.0)
}

// PCSLatency records the time spent waiting on PCS.
func (m *Metrics) PCSLatency(ctx context.Context, d time.Duration) {
	m.pcsLatency.Record(ctx, float64(d.Microseconds())/1000.0)
}
```

- [ ] **Step 5: Run tests; confirm they pass**

```bash
go test ./internal/metrics/... -v
```

Expected: 4 PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/metrics/ go.mod go.sum
git commit -m "feat(metrics): OpenTelemetry counters and latency histograms for PV1-010"
```

---

## Task 6: Decision-enforcer handler (PV1-006, PV1-007, PV1-008, PV1-009)

`handler.Decide` is the orchestration core. Given a flat map of lowercased request headers, it returns an `Outcome` of `Allow`, `Deny`, or `Reject(reason)`. It records metrics inline and propagates the PCS latency separately. The ext_proc server (Task 7) turns the `Outcome` into a wire-level reply.

**Files:**
- Create: `permission-validation/internal/extproc/handler.go`
- Create: `permission-validation/internal/extproc/handler_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/extproc/handler_test.go`:

```go
package extproc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"

	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

type stubPCS struct {
	decision pcs.Decision
	err      error
	gotReq   pcs.CheckRequest
}

func (s *stubPCS) Check(ctx context.Context, req pcs.CheckRequest) (pcs.Decision, error) {
	s.gotReq = req
	return s.decision, s.err
}

func newHandler(t *testing.T, p PCS) *Handler {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	return New(p, metrics.New(mp.Meter("test")))
}

func TestDecide_Allow(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow}
	h := newHandler(t, p)
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:edit",
		"x-request-id":   "req-1",
	})
	require.Equal(t, OutcomeAllow, out.Kind)
	require.Equal(t, "doc-1", p.gotReq.ObjectID)
	require.Equal(t, "document", p.gotReq.ObjectType)
	require.Equal(t, "edit", p.gotReq.Permission)
	require.Equal(t, "sso-tok", p.gotReq.SSOToken)
	require.Equal(t, "req-1", p.gotReq.RequestID)
}

func TestDecide_Deny(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionDeny}
	h := newHandler(t, p)
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:edit",
	})
	require.Equal(t, OutcomeDeny, out.Kind)
}

func TestDecide_PCSError_FailClosed(t *testing.T) {
	p := &stubPCS{err: errors.New("boom")}
	h := newHandler(t, p)
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:edit",
	})
	require.Equal(t, OutcomeRejectError, out.Kind)
}

func TestDecide_MissingAuth(t *testing.T) {
	h := newHandler(t, &stubPCS{})
	out := h.Decide(context.Background(), map[string]string{
		"x-auth-context": "doc-1:document:edit",
	})
	require.Equal(t, OutcomeRejectHeader, out.Kind)
	require.Equal(t, "missing_authz", out.Reason)
}

func TestDecide_MissingContext(t *testing.T) {
	h := newHandler(t, &stubPCS{})
	out := h.Decide(context.Background(), map[string]string{
		"authorization": "Bearer sso-tok",
	})
	require.Equal(t, OutcomeRejectHeader, out.Kind)
	require.Equal(t, "missing_ctx", out.Reason)
}

func TestDecide_MalformedContext(t *testing.T) {
	h := newHandler(t, &stubPCS{})
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document", // wrong segment count
	})
	require.Equal(t, OutcomeRejectParse, out.Kind)
	require.Equal(t, "wrong_segment_count", out.Reason)
}

func TestDecide_RecordsSidecarLatency(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow}
	h := newHandler(t, p)
	start := time.Now()
	_ = h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer t",
		"x-auth-context": "a:b:c",
	})
	require.WithinDuration(t, time.Now(), start, 50*time.Millisecond)
}
```

- [ ] **Step 2: Run it; confirm it fails**

```bash
go test ./internal/extproc/... -v
```

Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement the handler**

Create `internal/extproc/handler.go`:

```go
package extproc

import (
	"context"
	"time"

	"permission-validation/internal/header"
	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

// PCS is the dependency interface for the permission checking service.
type PCS interface {
	Check(ctx context.Context, req pcs.CheckRequest) (pcs.Decision, error)
}

// OutcomeKind enumerates the four wire-level outcomes the sidecar produces.
type OutcomeKind int

const (
	OutcomeAllow OutcomeKind = iota
	OutcomeDeny
	OutcomeRejectHeader
	OutcomeRejectParse
	OutcomeRejectError
)

// Outcome is what handler.Decide returns. Reason is set only for reject kinds and
// carries the metric label.
type Outcome struct {
	Kind   OutcomeKind
	Reason string
}

// Handler is the orchestration core: extract → parse → PCS → emit metrics → return Outcome.
type Handler struct {
	pcs PCS
	m   *metrics.Metrics
}

// New constructs a Handler. The metrics object is required.
func New(p PCS, m *metrics.Metrics) *Handler {
	return &Handler{pcs: p, m: m}
}

// Decide consumes a lowercased header map (Envoy normalizes header casing on HTTP/2)
// and returns the wire outcome.
func (h *Handler) Decide(ctx context.Context, hdrs map[string]string) Outcome {
	start := time.Now()
	defer func() { h.m.SidecarLatency(ctx, time.Since(start)) }()

	tok, err := header.ExtractAuth(hdrs)
	if err != nil {
		reason := err.(*header.HeaderError).Reason
		h.m.HeaderInvalid(ctx, reason)
		return Outcome{Kind: OutcomeRejectHeader, Reason: reason}
	}
	ctxRaw, err := header.ExtractContext(hdrs)
	if err != nil {
		reason := err.(*header.HeaderError).Reason
		h.m.HeaderInvalid(ctx, reason)
		return Outcome{Kind: OutcomeRejectHeader, Reason: reason}
	}
	parsed, err := header.ParseContextHeader(ctxRaw)
	if err != nil {
		reason := err.(*header.ParseError).Reason
		h.m.CtxParseFailure(ctx, reason)
		return Outcome{Kind: OutcomeRejectParse, Reason: reason}
	}

	pcsStart := time.Now()
	dec, err := h.pcs.Check(ctx, pcs.CheckRequest{
		ObjectID:   parsed.ObjectID,
		ObjectType: parsed.ObjectType,
		Permission: parsed.Action,
		SSOToken:   tok,
		RequestID:  hdrs["x-request-id"],
	})
	h.m.PCSLatency(ctx, time.Since(pcsStart))
	if err != nil {
		h.m.Decision(ctx, "error")
		return Outcome{Kind: OutcomeRejectError, Reason: "pcs_error"}
	}
	if dec == pcs.DecisionAllow {
		h.m.Decision(ctx, "allow")
		return Outcome{Kind: OutcomeAllow}
	}
	h.m.Decision(ctx, "deny")
	return Outcome{Kind: OutcomeDeny}
}
```

- [ ] **Step 4: Run tests; confirm they pass**

```bash
go test ./internal/extproc/... -v
```

Expected: 7 PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/extproc/handler.go internal/extproc/handler_test.go
git commit -m "feat(extproc): orchestrate header/parse/pcs into a single decision Outcome"
```

---

## Task 7: ext_proc gRPC server (PV1-009 wire layer)

The server implements `envoy_service_ext_proc_v3.ExternalProcessorServer`. Envoy opens one bidirectional stream per HTTP transaction. Phase 1 only consumes `RequestHeaders`; everything else (response phase, trailers) is acknowledged with `CONTINUE` for forward-compat but never read.

**Files:**
- Create: `permission-validation/internal/extproc/response.go`
- Create: `permission-validation/internal/extproc/server.go`
- Create: `permission-validation/internal/extproc/server_test.go`

- [ ] **Step 1: Add go-control-plane and grpc dependencies**

```bash
cd permission-validation
go get github.com/envoyproxy/go-control-plane@latest
go get google.golang.org/grpc@latest
```

- [ ] **Step 2: Implement reply helpers first (no tests yet; covered by server_test)**

Create `internal/extproc/response.go`:

```go
package extproc

import (
	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/envoyproxy/go-control-plane/envoy/type/v3"
)

// continueReply tells Envoy to keep the request unchanged and forward to the upstream.
func continueReply() *ext_proc_v3.ProcessingResponse {
	return &ext_proc_v3.ProcessingResponse{
		Response: &ext_proc_v3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HeadersResponse{
				Response: &ext_proc_v3.CommonResponse{
					Status: ext_proc_v3.CommonResponse_CONTINUE,
				},
			},
		},
	}
}

// rejectReply terminates the request with a 403 and a short reason body.
// reasonCode is included in a response header so SREs can correlate.
func rejectReply(reasonCode string) *ext_proc_v3.ProcessingResponse {
	return &ext_proc_v3.ProcessingResponse{
		Response: &ext_proc_v3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &ext_proc_v3.ImmediateResponse{
				Status: &v3.HttpStatus{Code: v3.StatusCode_Forbidden},
				Headers: &ext_proc_v3.HeaderMutation{
					SetHeaders: []*core_v3.HeaderValueOption{{
						Header: &core_v3.HeaderValue{
							Key:      "x-pv-reject-reason",
							RawValue: []byte(reasonCode),
						},
					}},
				},
				Body: []byte("forbidden\n"),
			},
		},
	}
}
```

- [ ] **Step 3: Write the failing server test**

Create `internal/extproc/server_test.go`:

```go
package extproc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

type fixedPCS struct{ d pcs.Decision; err error }

func (f *fixedPCS) Check(_ context.Context, _ pcs.CheckRequest) (pcs.Decision, error) {
	return f.d, f.err
}

func startServer(t *testing.T, p PCS) (ext_proc_v3.ExternalProcessorClient, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	h := New(p, metrics.New(mp.Meter("test")))
	RegisterServer(gs, h)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	return ext_proc_v3.NewExternalProcessorClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
	}
}

func sendHeaders(t *testing.T, c ext_proc_v3.ExternalProcessorClient, hdrs map[string]string) *ext_proc_v3.ProcessingResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := c.Process(ctx)
	require.NoError(t, err)

	hv := make([]*core_v3.HeaderValue, 0, len(hdrs))
	for k, v := range hdrs {
		hv = append(hv, &core_v3.HeaderValue{Key: k, RawValue: []byte(v)})
	}
	require.NoError(t, stream.Send(&ext_proc_v3.ProcessingRequest{
		Request: &ext_proc_v3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HttpHeaders{Headers: &core_v3.HeaderMap{Headers: hv}},
		},
	}))
	resp, err := stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	return resp
}

func TestServer_Allow(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()
	r := sendHeaders(t, c, map[string]string{
		"authorization":  "Bearer t",
		"x-auth-context": "a:b:c",
	})
	require.NotNil(t, r.GetRequestHeaders())
	require.Equal(t, ext_proc_v3.CommonResponse_CONTINUE, r.GetRequestHeaders().Response.Status)
}

func TestServer_Deny(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionDeny})
	defer stop()
	r := sendHeaders(t, c, map[string]string{
		"authorization":  "Bearer t",
		"x-auth-context": "a:b:c",
	})
	imm := r.GetImmediateResponse()
	require.NotNil(t, imm)
	require.EqualValues(t, 403, imm.Status.Code)
}

func TestServer_MissingHeader(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()
	r := sendHeaders(t, c, map[string]string{}) // no auth, no ctx
	imm := r.GetImmediateResponse()
	require.NotNil(t, imm)
	require.EqualValues(t, 403, imm.Status.Code)
	var reason string
	for _, h := range imm.Headers.SetHeaders {
		if h.Header.Key == "x-pv-reject-reason" {
			reason = string(h.Header.RawValue)
		}
	}
	require.Equal(t, "missing_authz", reason)
}
```

- [ ] **Step 4: Run it; confirm it fails**

```bash
go test ./internal/extproc/... -run TestServer -v
```

Expected: FAIL — undefined `RegisterServer`.

- [ ] **Step 5: Implement the server**

Create `internal/extproc/server.go`:

```go
package extproc

import (
	"context"
	"errors"
	"io"

	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
)

// Server is the gRPC wrapper around Handler.
type Server struct {
	ext_proc_v3.UnimplementedExternalProcessorServer
	h *Handler
}

// RegisterServer mounts an ExternalProcessor service on gs.
func RegisterServer(gs *grpc.Server, h *Handler) {
	ext_proc_v3.RegisterExternalProcessorServer(gs, &Server{h: h})
}

// Process handles one HTTP transaction. Phase 1 reads RequestHeaders, replies once,
// then acknowledges any further messages with CONTINUE so Envoy is free to advance
// through response phases (forward-compat with Phase 1.5).
func (s *Server) Process(stream ext_proc_v3.ExternalProcessor_ProcessServer) error {
	decided := false
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch v := msg.Request.(type) {
		case *ext_proc_v3.ProcessingRequest_RequestHeaders:
			if decided {
				if err := stream.Send(continueReply()); err != nil {
					return err
				}
				continue
			}
			decided = true
			hdrs := flattenHeaders(v.RequestHeaders.GetHeaders())
			out := s.h.Decide(stream.Context(), hdrs)
			reply := outcomeToReply(out)
			if err := stream.Send(reply); err != nil {
				return err
			}
			// If the reply is ImmediateResponse, Envoy will close the stream. CONTINUE
			// keeps it open for downstream phases; we ack them below.
		default:
			if err := stream.Send(continueReply()); err != nil {
				return err
			}
		}
		_ = context.Canceled // keep linter quiet about unused import patterns
	}
}

func outcomeToReply(o Outcome) *ext_proc_v3.ProcessingResponse {
	switch o.Kind {
	case OutcomeAllow:
		return continueReply()
	case OutcomeDeny:
		return rejectReply("deny")
	case OutcomeRejectHeader, OutcomeRejectParse, OutcomeRejectError:
		return rejectReply(o.Reason)
	default:
		return rejectReply("unknown")
	}
}

func flattenHeaders(hm *core_v3.HeaderMap) map[string]string {
	if hm == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(hm.Headers))
	for _, h := range hm.Headers {
		// Envoy may put the value in Value (string) or RawValue (bytes); prefer RawValue.
		if len(h.RawValue) > 0 {
			out[h.Key] = string(h.RawValue)
		} else {
			out[h.Key] = h.Value
		}
	}
	return out
}
```

- [ ] **Step 6: Run tests; confirm they pass**

```bash
go test ./internal/extproc/... -v
```

Expected: handler tests + 3 server tests PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/extproc/server.go internal/extproc/response.go internal/extproc/server_test.go go.mod go.sum
git commit -m "feat(extproc): gRPC ExternalProcessor server for request_headers"
```

---

## Task 8: Route-config YAML schema + validator (PV1-004)

Implements the validator side of PV1-004. The translator (Task 9) and CLI (Task 10) build on this.

**Files:**
- Create: `permission-validation/internal/config/schema.go`
- Create: `permission-validation/internal/config/validate.go`
- Create: `permission-validation/internal/config/validate_test.go`
- Create: `permission-validation/testdata/routes/valid-minimal.yaml`
- Create: `permission-validation/testdata/routes/valid-comprehensive.yaml`
- Create: `permission-validation/testdata/routes/invalid-wrong-version.yaml`
- Create: `permission-validation/testdata/routes/invalid-empty-routes.yaml`
- Create: `permission-validation/testdata/routes/invalid-bad-method.yaml`
- Create: `permission-validation/testdata/routes/invalid-bad-behavior.yaml`

- [ ] **Step 1: Add YAML dependency**

```bash
go get gopkg.in/yaml.v3@latest
```

- [ ] **Step 2: Create testdata fixtures**

Create `testdata/routes/valid-minimal.yaml`:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /api/orders/*
    behavior: protected
```

Create `testdata/routes/valid-comprehensive.yaml`:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /metrics
    behavior: skipped
  - method: GET
    path: /assets/**
    behavior: skipped
  - method: GET
    path: /favicon.ico
    behavior: skipped
  - method: GET
    path: /api/orders/*
    behavior: protected
  - method: POST
    path: /api/orders
    behavior: protected
  - method: "*"
    path: /api/admin/**
    behavior: protected
```

Create `testdata/routes/invalid-wrong-version.yaml`:

```yaml
version: v0
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /api
    behavior: protected
```

Create `testdata/routes/invalid-empty-routes.yaml`:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes: []
```

Create `testdata/routes/invalid-bad-method.yaml`:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: SUBSCRIBE
    path: /api
    behavior: protected
```

Create `testdata/routes/invalid-bad-behavior.yaml`:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /api
    behavior: maybe
```

- [ ] **Step 3: Write the failing test**

Create `internal/config/validate_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadFile(t *testing.T, name string) *RouteConfig {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "routes", name))
	require.NoError(t, err)
	rc, err := Parse(b)
	require.NoError(t, err)
	return rc
}

func TestValidate_MinimalOK(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	require.Empty(t, Validate(rc))
}

func TestValidate_ComprehensiveOK(t *testing.T) {
	rc := loadFile(t, "valid-comprehensive.yaml")
	require.Empty(t, Validate(rc))
}

func TestValidate_WrongVersion(t *testing.T) {
	rc := loadFile(t, "invalid-wrong-version.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "version")
}

func TestValidate_EmptyRoutes(t *testing.T) {
	rc := loadFile(t, "invalid-empty-routes.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "routes")
}

func TestValidate_BadMethod(t *testing.T) {
	rc := loadFile(t, "invalid-bad-method.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "method")
}

func TestValidate_BadBehavior(t *testing.T) {
	rc := loadFile(t, "invalid-bad-behavior.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "behavior")
}

func TestValidate_BadDefault(t *testing.T) {
	rc := &RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "protected",
		Routes: []RouteRule{{Method: "GET", Path: "/x", Behavior: "protected"}},
	}
	errs := Validate(rc)
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), "defaultBehavior")
}

func TestValidate_PathMustStartWithSlash(t *testing.T) {
	rc := &RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []RouteRule{{Method: "GET", Path: "api/x", Behavior: "protected"}},
	}
	errs := Validate(rc)
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), "path")
}
```

- [ ] **Step 4: Run; confirm fails**

```bash
go test ./internal/config/...
```

Expected: FAIL — undefined `Parse`, `Validate`, `RouteConfig`, `RouteRule`.

- [ ] **Step 5: Implement the schema types**

Create `internal/config/schema.go`:

```go
package config

import (
	"fmt"

	"gopkg.in/yaml.v3"
)

// RouteConfig is the YAML schema described in phase-1-route-config-schema.md §2.
type RouteConfig struct {
	Version         string      `yaml:"version"`
	AppID           string      `yaml:"appId"`
	DefaultBehavior string      `yaml:"defaultBehavior"`
	Routes          []RouteRule `yaml:"routes"`
}

// RouteRule is one entry in the routes list.
type RouteRule struct {
	Method   string `yaml:"method"`
	Path     string `yaml:"path"`
	Behavior string `yaml:"behavior"`
}

// Parse decodes the YAML bytes into a RouteConfig. Decode errors are returned;
// semantic validation lives in Validate.
func Parse(b []byte) (*RouteConfig, error) {
	rc := &RouteConfig{}
	if err := yaml.Unmarshal(b, rc); err != nil {
		return nil, fmt.Errorf("config: yaml decode: %w", err)
	}
	return rc, nil
}
```

- [ ] **Step 6: Implement validation**

Create `internal/config/validate.go`:

```go
package config

import (
	"fmt"
	"strings"
)

// ValidationError is a single failure during Validate.
type ValidationError struct {
	Path string
	Msg  string
}

func (v *ValidationError) Error() string {
	if v.Path == "" {
		return v.Msg
	}
	return v.Path + ": " + v.Msg
}

var (
	allowedMethods   = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "*": true}
	allowedBehavior  = map[string]bool{"protected": true, "skipped": true}
	allowedDefault   = map[string]bool{"deny": true, "skipped": true}
)

// Validate enforces phase-1-route-config-schema.md §2 / §4 rules. Returns all errors found.
func Validate(rc *RouteConfig) []error {
	var errs []error
	if rc.Version != "v1" {
		errs = append(errs, &ValidationError{Path: "version", Msg: fmt.Sprintf("must be %q, got %q", "v1", rc.Version)})
	}
	if rc.AppID == "" {
		errs = append(errs, &ValidationError{Path: "appId", Msg: "is required"})
	}
	if !allowedDefault[rc.DefaultBehavior] {
		errs = append(errs, &ValidationError{Path: "defaultBehavior", Msg: fmt.Sprintf("must be deny or skipped, got %q", rc.DefaultBehavior)})
	}
	if len(rc.Routes) == 0 {
		errs = append(errs, &ValidationError{Path: "routes", Msg: "must be a non-empty list"})
	}
	for i, r := range rc.Routes {
		prefix := fmt.Sprintf("routes[%d]", i)
		if !allowedMethods[r.Method] {
			errs = append(errs, &ValidationError{Path: prefix + ".method", Msg: fmt.Sprintf("unsupported method %q", r.Method)})
		}
		if r.Path == "" {
			errs = append(errs, &ValidationError{Path: prefix + ".path", Msg: "must be non-empty"})
		} else if !strings.HasPrefix(r.Path, "/") {
			errs = append(errs, &ValidationError{Path: prefix + ".path", Msg: fmt.Sprintf("must start with '/', got %q", r.Path)})
		}
		if !allowedBehavior[r.Behavior] {
			errs = append(errs, &ValidationError{Path: prefix + ".behavior", Msg: fmt.Sprintf("must be protected or skipped, got %q", r.Behavior)})
		}
	}
	return errs
}
```

- [ ] **Step 7: Run; confirm pass**

```bash
go test ./internal/config/... -v
```

Expected: 8 PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/config/schema.go internal/config/validate.go internal/config/validate_test.go testdata/routes/ go.mod go.sum
git commit -m "feat(config): YAML route-config schema and validator (PV1-004)"
```

---

## Task 9: YAML → Envoy static-config translator (PV1-005)

Generates Envoy 1.31 static `bootstrap.yaml` matching the Phase 1 topology decision: HTTP listener → router with per-route ext_proc filter, `ExtProcPerRoute.disabled: true` on skipped routes. Gitignore-style globs are translated to Envoy `path_match_policy`/`prefix`/`safe_regex` as documented in §2.1 of the schema spec.

**Files:**
- Create: `permission-validation/testdata/envoy/envoy-static.tmpl.yaml`
- Create: `permission-validation/internal/config/translate.go`
- Create: `permission-validation/internal/config/translate_test.go`

- [ ] **Step 1: Write the failing test**

Create `internal/config/translate_test.go`:

```go
package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTranslate_MinimalProducesValidYAML(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	require.Empty(t, Validate(rc))
	got, err := Translate(rc, TranslateOptions{
		SidecarHost: "127.0.0.1", SidecarPort: 18080,
		AppBackendHost: "127.0.0.1", AppBackendPort: 8080,
		FailureModeAllow: false,
	})
	require.NoError(t, err)

	// Sanity: it's valid YAML.
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(got, &parsed))

	s := string(got)
	require.Contains(t, s, "ext_proc")
	require.Contains(t, s, "failure_mode_allow: false")
	require.Contains(t, s, "/health")
	require.Contains(t, s, "/api/orders")
}

func TestTranslate_SkippedHasDisabled(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := Translate(rc, TranslateOptions{
		SidecarHost: "sidecar", SidecarPort: 50051,
		AppBackendHost: "backend", AppBackendPort: 8080,
	})
	require.NoError(t, err)
	s := string(got)
	healthIdx := strings.Index(s, "/health")
	require.GreaterOrEqual(t, healthIdx, 0)
	// The disabled override appears in the per-route section for /health.
	require.Contains(t, s[healthIdx:], "disabled: true")
}

func TestTranslate_DefaultDenyEmitsFallbackRoute(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	require.Equal(t, "deny", rc.DefaultBehavior)
	got, err := Translate(rc, TranslateOptions{SidecarHost: "s", SidecarPort: 1, AppBackendHost: "b", AppBackendPort: 1})
	require.NoError(t, err)
	require.Contains(t, string(got), "direct_response")
	require.Contains(t, string(got), "status: 403")
}
```

- [ ] **Step 2: Run; confirm fails**

```bash
go test ./internal/config/... -run Translate
```

Expected: FAIL — undefined `Translate`, `TranslateOptions`.

- [ ] **Step 3: Author the template**

Create `testdata/envoy/envoy-static.tmpl.yaml`:

```yaml
admin:
  address:
    socket_address: { address: 0.0.0.0, port_value: 9901 }

static_resources:
  listeners:
    - name: ingress
      address:
        socket_address: { address: 0.0.0.0, port_value: 8000 }
      filter_chains:
        - filters:
            - name: envoy.filters.network.http_connection_manager
              typed_config:
                "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
                stat_prefix: ingress_http
                route_config:
                  name: app_routes
                  virtual_hosts:
                    - name: app
                      domains: ["*"]
                      routes:
                        {{- range $i, $r := .Routes }}
                        - match:
                            {{- if eq $r.PathKind "exact" }}
                            path: {{ printf "%q" $r.Path }}
                            {{- else if eq $r.PathKind "prefix" }}
                            prefix: {{ printf "%q" $r.Path }}
                            {{- else }}
                            safe_regex:
                              regex: {{ printf "%q" $r.Regex }}
                            {{- end }}
                            {{- if ne $r.Method "*" }}
                            headers:
                              - name: ":method"
                                string_match: { exact: {{ printf "%q" $r.Method }} }
                            {{- end }}
                          route: { cluster: app_backend }
                          typed_per_filter_config:
                            envoy.filters.http.ext_proc:
                              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
                              {{- if eq $r.Behavior "skipped" }}
                              disabled: true
                              {{- else }}
                              overrides: {}
                              {{- end }}
                        {{- end }}
                        {{- if eq .DefaultBehavior "deny" }}
                        - match: { prefix: "/" }
                          direct_response:
                            status: 403
                            body: { inline_string: "forbidden\n" }
                        {{- else }}
                        - match: { prefix: "/" }
                          route: { cluster: app_backend }
                          typed_per_filter_config:
                            envoy.filters.http.ext_proc:
                              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
                              disabled: true
                        {{- end }}
                http_filters:
                  - name: envoy.filters.http.ext_proc
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
                      grpc_service:
                        envoy_grpc: { cluster_name: pv_sidecar }
                      failure_mode_allow: {{ .FailureModeAllow }}
                      processing_mode:
                        request_header_mode: SEND
                        response_header_mode: SKIP
                        request_body_mode: NONE
                        response_body_mode: NONE
                        request_trailer_mode: SKIP
                        response_trailer_mode: SKIP
                  - name: envoy.filters.http.router
                    typed_config:
                      "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router

  clusters:
    - name: app_backend
      type: STRICT_DNS
      connect_timeout: 1s
      load_assignment:
        cluster_name: app_backend
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address: { address: {{ .AppBackendHost }}, port_value: {{ .AppBackendPort }} }
    - name: pv_sidecar
      type: STRICT_DNS
      connect_timeout: 1s
      typed_extension_protocol_options:
        envoy.extensions.upstreams.http.v3.HttpProtocolOptions:
          "@type": type.googleapis.com/envoy.extensions.upstreams.http.v3.HttpProtocolOptions
          explicit_http_config:
            http2_protocol_options: {}
      load_assignment:
        cluster_name: pv_sidecar
        endpoints:
          - lb_endpoints:
              - endpoint:
                  address:
                    socket_address: { address: {{ .SidecarHost }}, port_value: {{ .SidecarPort }} }
```

- [ ] **Step 4: Implement the translator**

Create `internal/config/translate.go`:

```go
package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed ../../testdata/envoy/envoy-static.tmpl.yaml
var envoyTemplate string

// TranslateOptions are environment-specific values that the YAML schema does not carry.
type TranslateOptions struct {
	SidecarHost      string
	SidecarPort      int
	AppBackendHost   string
	AppBackendPort   int
	FailureModeAllow bool // must stay false in Phase 1; exposed for tests
}

type routeView struct {
	Method   string
	Behavior string
	Path     string
	Regex    string
	PathKind string // "exact" | "prefix" | "regex"
}

type translateView struct {
	Routes           []routeView
	DefaultBehavior  string
	SidecarHost      string
	SidecarPort      int
	AppBackendHost   string
	AppBackendPort   int
	FailureModeAllow bool
}

// Translate renders the embedded Envoy template using rc + opts.
func Translate(rc *RouteConfig, opts TranslateOptions) ([]byte, error) {
	tv := translateView{
		DefaultBehavior:  rc.DefaultBehavior,
		SidecarHost:      opts.SidecarHost,
		SidecarPort:      opts.SidecarPort,
		AppBackendHost:   opts.AppBackendHost,
		AppBackendPort:   opts.AppBackendPort,
		FailureModeAllow: opts.FailureModeAllow,
	}
	for _, r := range rc.Routes {
		rv, err := routeToView(r)
		if err != nil {
			return nil, err
		}
		tv.Routes = append(tv.Routes, rv)
	}
	tmpl, err := template.New("envoy").Parse(envoyTemplate)
	if err != nil {
		return nil, fmt.Errorf("translate: parse template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, tv); err != nil {
		return nil, fmt.Errorf("translate: execute template: %w", err)
	}
	return out.Bytes(), nil
}

// routeToView resolves the path glob into an Envoy matcher kind.
//
//	literal             → exact
//	literal + trailing /*  → prefix (one segment only is best-effort approximated by regex)
//	literal + trailing /** → prefix
//	anything containing * or ** elsewhere → safe_regex
func routeToView(r RouteRule) (routeView, error) {
	rv := routeView{Method: r.Method, Behavior: r.Behavior, Path: r.Path}
	hasStar := strings.Contains(r.Path, "*")
	switch {
	case !hasStar:
		rv.PathKind = "exact"
	case strings.HasSuffix(r.Path, "/**") && strings.Count(r.Path, "*") == 2:
		rv.PathKind = "prefix"
		rv.Path = strings.TrimSuffix(r.Path, "/**")
	default:
		rv.PathKind = "regex"
		rv.Regex = globToRegex(r.Path)
	}
	return rv, nil
}

// globToRegex converts a gitignore-style glob (§2.1) to an Envoy safe_regex (RE2).
//   * → one path segment ([^/]+)
//   ** → zero or more segments (.*)
//   literal characters → escaped
func globToRegex(p string) string {
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(p) {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case p[i] == '*':
			b.WriteString("[^/]+")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}
```

- [ ] **Step 5: Run tests; confirm pass**

```bash
go test ./internal/config/... -v
```

Expected: all config tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/config/translate.go internal/config/translate_test.go testdata/envoy/
git commit -m "feat(config): translate YAML route config to Envoy static bootstrap"
```

---

## Task 10: `validate-routes` CLI

Two subcommands: `validate <file>` (exit 0 if valid) and `translate <file> -o <envoy.yaml>` (writes Envoy YAML, exits 0 on success).

**Files:**
- Create: `permission-validation/cmd/validate-routes/main.go`
- Create: `permission-validation/cmd/validate-routes/main_test.go`

- [ ] **Step 1: Write the failing test**

Create `cmd/validate-routes/main_test.go`:

```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate_ValidExits0(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"validate", "../../testdata/routes/valid-minimal.yaml"},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
}

func TestValidate_InvalidExits1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"validate", "../../testdata/routes/invalid-bad-method.yaml"},
		&stdout, &stderr)
	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "method")
}

func TestTranslate_WritesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envoy.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"-o", out, "--sidecar-host", "127.0.0.1", "--sidecar-port", "50051",
			"--backend-host", "127.0.0.1", "--backend-port", "8080"},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	require.True(t, strings.Contains(string(b), "ext_proc"))
}
```

- [ ] **Step 2: Run; confirm fails**

```bash
go test ./cmd/validate-routes/...
```

Expected: FAIL — `run` not defined.

- [ ] **Step 3: Implement the CLI**

Create `cmd/validate-routes/main.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"permission-validation/internal/config"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: validate-routes (validate|translate) <file> [flags]")
		return 2
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:], stderr)
	case "translate":
		return runTranslate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		return 2
	}
}

func runValidate(args []string, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: validate-routes validate <file>")
		return 2
	}
	rc, err := readConfig(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	errs := config.Validate(rc)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, e)
		}
		return 1
	}
	return 0
}

func runTranslate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	out := fs.String("o", "", "output file (defaults to stdout)")
	sidecarHost := fs.String("sidecar-host", "127.0.0.1", "")
	sidecarPort := fs.Int("sidecar-port", 50051, "")
	backendHost := fs.String("backend-host", "127.0.0.1", "")
	backendPort := fs.Int("backend-port", 8080, "")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() < 1 {
		fmt.Fprintln(stderr, "usage: validate-routes translate <file> [-o output] [--sidecar-host h] [--sidecar-port n] [--backend-host h] [--backend-port n]")
		return 2
	}
	rc, err := readConfig(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if errs := config.Validate(rc); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, e)
		}
		return 1
	}
	b, err := config.Translate(rc, config.TranslateOptions{
		SidecarHost: *sidecarHost, SidecarPort: *sidecarPort,
		AppBackendHost: *backendHost, AppBackendPort: *backendPort,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *out == "" {
		_, _ = stdout.Write(b)
		return 0
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func readConfig(path string) (*config.RouteConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return config.Parse(b)
}
```

- [ ] **Step 4: Run; confirm pass**

```bash
go test ./cmd/validate-routes/... -v
```

Expected: 3 PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/validate-routes/
git commit -m "feat(validate-routes): CLI for schema validation and Envoy translation"
```

---

## Task 11: Wire `cmd/permission-validation/main.go`

Replaces the placeholder banner with the full startup: parse flags, build OTLP meter provider, build PCS client, register ext_proc server, listen, handle SIGTERM.

**Files:**
- Modify: `permission-validation/cmd/permission-validation/main.go`
- Modify: `permission-validation/cmd/permission-validation/main_test.go`

- [ ] **Step 1: Add OTLP exporter dependency**

```bash
go get go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc@latest
```

- [ ] **Step 2: Update the smoke test to assert flag parsing**

Replace `cmd/permission-validation/main_test.go` with:

```go
package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestRun_HelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected --help to exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String()+stderr.String(), "permission-validation") {
		t.Fatalf("expected --help to mention permission-validation; got %q / %q", stdout.String(), stderr.String())
	}
}

func TestRun_BadFlagExitsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--no-such-flag"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit on bad flag")
	}
}

func TestRun_ServesAndShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{
			"--listen", "127.0.0.1:0",
			"--pcs-endpoint", "http://127.0.0.1:1",
			"--otel-disabled",
		}, &stdout, &stderr)
	}()
	// Let it start.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected clean shutdown, got %d; stderr=%s", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within 2s")
	}
}
```

- [ ] **Step 3: Run; confirm fails**

```bash
go test ./cmd/permission-validation/...
```

Expected: FAIL — current `run` ignores all flags.

- [ ] **Step 4: Implement the full main**

Replace `cmd/permission-validation/main.go` with:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc"

	"permission-validation/internal/extproc"
	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("permission-validation", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "0.0.0.0:50051", "gRPC listen address")
	pcsEndpoint := fs.String("pcs-endpoint", "http://permission-checking:8080", "base URL for PCS")
	pcsTimeout := fs.Duration("pcs-timeout", 250*time.Millisecond, "per-call PCS timeout")
	otelEndpoint := fs.String("otel-endpoint", "127.0.0.1:4317", "OTLP/gRPC metrics endpoint")
	otelDisabled := fs.Bool("otel-disabled", false, "disable OTLP export (uses no-op meter)")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "permission-validation — Phase 1 Envoy ext_proc sidecar\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already printed; map --help to 0.
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	mp, shutdownMeter, err := buildMeterProvider(ctx, *otelEndpoint, *otelDisabled)
	if err != nil {
		fmt.Fprintln(stderr, "metrics:", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdownMeter(shutdownCtx)
	}()

	m := metrics.New(mp.Meter("permission-validation"))
	pcsClient := pcs.NewClient(*pcsEndpoint, *pcsTimeout)
	h := extproc.New(pcsClient, m)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "listen:", err)
		return 1
	}
	gs := grpc.NewServer()
	extproc.RegisterServer(gs, h)
	fmt.Fprintf(stdout, "permission-validation listening on %s\n", lis.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- gs.Serve(lis) }()

	select {
	case <-ctx.Done():
		gs.GracefulStop()
		return 0
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintln(stderr, "serve:", err)
			return 1
		}
		return 0
	}
}

func buildMeterProvider(ctx context.Context, endpoint string, disabled bool) (*metric.MeterProvider, func(context.Context) error, error) {
	if disabled {
		mp := metric.NewMeterProvider()
		return mp, mp.Shutdown, nil
	}
	exp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("otlp: %w", err)
	}
	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(exp, metric.WithInterval(15*time.Second))),
	)
	return mp, mp.Shutdown, nil
}
```

- [ ] **Step 5: Run; confirm pass**

```bash
go test ./cmd/permission-validation/... -v
```

Expected: 3 PASS.

- [ ] **Step 6: Commit**

```bash
git add cmd/permission-validation/ go.mod go.sum
git commit -m "feat(sidecar): wire flags, OTLP exporter, and ext_proc server in main"
```

---

## Task 12: Fake PCS server for e2e

Programmable HTTP server: for a given `(objectId, objectType, permission)` triple it returns `allowed: true|false`. Anything not pre-registered returns 503 so tests catch typos. Records every received request for assertion.

**Files:**
- Create: `permission-validation/test/e2e/fakes/pcs/main.go`

- [ ] **Step 1: Implement the fake**

Create `test/e2e/fakes/pcs/main.go`:

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
)

type rule struct {
	Allowed bool `json:"allowed"`
}

type fixture struct {
	mu    sync.Mutex
	rules map[string]rule
	calls []map[string]string
}

func (f *fixture) check(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req struct {
		ObjectID   string `json:"objectId"`
		ObjectType string `json:"objectType"`
		Permission string `json:"permission"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "bad json", 400)
		return
	}
	auth := r.Header.Get("Authorization")
	f.mu.Lock()
	f.calls = append(f.calls, map[string]string{
		"objectId":      req.ObjectID,
		"objectType":    req.ObjectType,
		"permission":    req.Permission,
		"authorization": auth,
	})
	key := req.ObjectID + "|" + req.ObjectType + "|" + req.Permission
	r2, ok := f.rules[key]
	f.mu.Unlock()
	if !ok {
		http.Error(w, "no rule for "+key, http.StatusServiceUnavailable)
		return
	}
	_ = json.NewEncoder(w).Encode(r2)
}

func (f *fixture) loadFromEnv() {
	// PCS_RULES="doc-1|document|edit=true,doc-2|document|edit=false"
	for _, kv := range strings.Split(os.Getenv("PCS_RULES"), ",") {
		kv = strings.TrimSpace(kv)
		if kv == "" {
			continue
		}
		eq := strings.LastIndex(kv, "=")
		if eq < 0 {
			continue
		}
		key, val := kv[:eq], kv[eq+1:]
		f.rules[key] = rule{Allowed: val == "true"}
	}
}

func main() {
	addr := flag.String("listen", "0.0.0.0:9000", "")
	flag.Parse()

	f := &fixture{rules: map[string]rule{}}
	f.loadFromEnv()

	mux := http.NewServeMux()
	mux.HandleFunc("/permission-check/v1/check", f.check)
	mux.HandleFunc("/_admin/rules", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", 405)
			return
		}
		var in map[string]bool
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		f.mu.Lock()
		for k, v := range in {
			f.rules[k] = rule{Allowed: v}
		}
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/_admin/calls", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.calls)
	})
	mux.HandleFunc("/_admin/reset", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.calls = nil
		f.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})

	log.Printf("fake-pcs: listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Confirm it builds**

```bash
go build ./test/e2e/fakes/pcs/...
```

Expected: no output.

- [ ] **Step 3: Commit**

```bash
git add test/e2e/fakes/pcs/
git commit -m "test(e2e): programmable fake PCS server for integration tests"
```

---

## Task 13: Fake app backend for e2e

Echoes the request path + headers it received. Records every received request so tests can assert "rejected requests never reached the backend" (PV1-011 acceptance criterion).

**Files:**
- Create: `permission-validation/test/e2e/fakes/backend/main.go`

- [ ] **Step 1: Implement the backend**

Create `test/e2e/fakes/backend/main.go`:

```go
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
)

type calls struct {
	mu sync.Mutex
	c  []map[string]any
}

func (c *calls) record(r *http.Request) {
	hdrs := map[string]string{}
	for k, vs := range r.Header {
		hdrs[k] = vs[0]
	}
	c.mu.Lock()
	c.c = append(c.c, map[string]any{
		"method":  r.Method,
		"path":    r.URL.Path,
		"headers": hdrs,
	})
	c.mu.Unlock()
}

func main() {
	addr := flag.String("listen", "0.0.0.0:8080", "")
	flag.Parse()

	c := &calls{}

	mux := http.NewServeMux()
	mux.HandleFunc("/_admin/calls", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		defer c.mu.Unlock()
		_ = json.NewEncoder(w).Encode(c.c)
	})
	mux.HandleFunc("/_admin/reset", func(w http.ResponseWriter, _ *http.Request) {
		c.mu.Lock()
		c.c = nil
		c.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		c.record(r)
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintf(w, "ok %s %s\n", r.Method, r.URL.Path)
	})

	log.Printf("fake-backend: listening on %s", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
```

- [ ] **Step 2: Confirm it builds**

```bash
go build ./test/e2e/fakes/backend/...
```

- [ ] **Step 3: Commit**

```bash
git add test/e2e/fakes/backend/
git commit -m "test(e2e): fake app backend that records every received request"
```

---

## Task 14: Docker Compose e2e harness + generated Envoy config

Generates the Envoy config from a known route config via `validate-routes translate`, then brings up Envoy + sidecar + fake-pcs + fake-backend.

**Files:**
- Create: `permission-validation/test/e2e/routes.yaml`
- Create: `permission-validation/test/e2e/Dockerfile.sidecar`
- Create: `permission-validation/test/e2e/Dockerfile.fake-pcs`
- Create: `permission-validation/test/e2e/Dockerfile.fake-backend`
- Create: `permission-validation/test/e2e/docker-compose.yaml`
- Create: `permission-validation/test/e2e/Makefile`

- [ ] **Step 1: Create the sample route config used by e2e**

Create `test/e2e/routes.yaml`:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /api/orders/*
    behavior: protected
  - method: POST
    path: /api/orders
    behavior: protected
```

- [ ] **Step 2: Sidecar Dockerfile**

Create `test/e2e/Dockerfile.sidecar`:

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/permission-validation ./cmd/permission-validation

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/permission-validation /permission-validation
ENTRYPOINT ["/permission-validation"]
```

- [ ] **Step 3: Fake PCS / backend Dockerfiles**

Create `test/e2e/Dockerfile.fake-pcs`:

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/fake-pcs ./test/e2e/fakes/pcs

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/fake-pcs /fake-pcs
ENTRYPOINT ["/fake-pcs"]
```

Create `test/e2e/Dockerfile.fake-backend`:

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/fake-backend ./test/e2e/fakes/backend

FROM gcr.io/distroless/static-debian12
COPY --from=build /out/fake-backend /fake-backend
ENTRYPOINT ["/fake-backend"]
```

- [ ] **Step 4: Docker Compose file**

Create `test/e2e/docker-compose.yaml`:

```yaml
services:
  fake-pcs:
    build: { context: ../.., dockerfile: test/e2e/Dockerfile.fake-pcs }
    command: ["--listen", "0.0.0.0:9000"]
    ports: ["9000:9000"]
  fake-backend:
    build: { context: ../.., dockerfile: test/e2e/Dockerfile.fake-backend }
    command: ["--listen", "0.0.0.0:8080"]
    ports: ["8080:8080"]
  sidecar:
    build: { context: ../.., dockerfile: test/e2e/Dockerfile.sidecar }
    command:
      - "--listen=0.0.0.0:50051"
      - "--pcs-endpoint=http://fake-pcs:9000"
      - "--pcs-timeout=250ms"
      - "--otel-disabled"
    depends_on: [fake-pcs]
    ports: ["50051:50051"]
  envoy:
    image: envoyproxy/envoy:v1.31-latest
    command: ["-c", "/etc/envoy/envoy.yaml", "--log-level", "info"]
    volumes:
      - ./envoy.yaml:/etc/envoy/envoy.yaml:ro
    depends_on: [sidecar, fake-backend]
    ports: ["8000:8000", "9901:9901"]
```

- [ ] **Step 5: Makefile that regenerates envoy.yaml**

Create `test/e2e/Makefile`:

```makefile
.PHONY: envoy.yaml up down clean

envoy.yaml: routes.yaml ../../cmd/validate-routes/main.go
	cd ../.. && go run ./cmd/validate-routes translate test/e2e/routes.yaml \
		-o test/e2e/envoy.yaml \
		--sidecar-host sidecar --sidecar-port 50051 \
		--backend-host fake-backend --backend-port 8080

up: envoy.yaml
	docker compose -f docker-compose.yaml up --build -d

down:
	docker compose -f docker-compose.yaml down -v

clean:
	rm -f envoy.yaml
```

- [ ] **Step 6: Generate envoy.yaml and confirm it parses**

```bash
make -C test/e2e envoy.yaml
```

Expected: `test/e2e/envoy.yaml` exists.

```bash
python3 -c "import yaml; yaml.safe_load(open('test/e2e/envoy.yaml'))"
```

Expected: no output (valid YAML). If `python3`/`yaml` is unavailable, fall back to `go run` with an inline `yaml.Unmarshal` smoke script committed under `test/e2e/scripts/yaml-check.go` instead — but plan defaults to the simpler check.

- [ ] **Step 7: Commit**

```bash
git add test/e2e/routes.yaml test/e2e/Dockerfile.* test/e2e/docker-compose.yaml test/e2e/Makefile test/e2e/envoy.yaml
git commit -m "test(e2e): docker-compose harness with envoy, sidecar, fake pcs, fake backend"
```

---

## Task 15: End-to-end Go tests (PV1-011)

Build-tagged `e2e` tests that assume the Compose stack is up. They reset PCS+backend fixtures, drive HTTP requests through Envoy on `:8000`, and assert outcomes.

**Files:**
- Create: `permission-validation/test/e2e/e2e_test.go`

- [ ] **Step 1: Write the e2e tests**

Create `test/e2e/e2e_test.go`:

```go
//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	envoyURL   = "http://127.0.0.1:8000"
	pcsURL     = "http://127.0.0.1:9000"
	backendURL = "http://127.0.0.1:8080"
)

func waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(envoyURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("envoy not ready after 20s")
}

func resetFixtures(t *testing.T) {
	t.Helper()
	for _, u := range []string{pcsURL + "/_admin/reset", backendURL + "/_admin/reset"} {
		req, _ := http.NewRequest("POST", u, nil)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err, u)
		_ = resp.Body.Close()
	}
}

func setRule(t *testing.T, key string, allowed bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]bool{key: allowed})
	resp, err := http.Post(pcsURL+"/_admin/rules", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	_ = resp.Body.Close()
}

func backendCallCount(t *testing.T) int {
	t.Helper()
	resp, err := http.Get(backendURL + "/_admin/calls")
	require.NoError(t, err)
	defer resp.Body.Close()
	var calls []any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&calls))
	return len(calls)
}

func do(t *testing.T, method, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, envoyURL+path, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMain(m *testing.M) {
	// e2e build tag is the gate; CI brings the stack up via `make -C test/e2e up`.
	if os.Getenv("E2E_SKIP_WAIT") != "1" {
		// Just-in-case readiness probe.
		t := &testing.T{}
		waitReady(t)
	}
	os.Exit(m.Run())
}

func TestE2E_GrantedReachesBackend(t *testing.T) {
	resetFixtures(t)
	setRule(t, "doc-1|document|view", true)
	before := backendCallCount(t)
	code, body := do(t, "GET", "/api/orders/123", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "doc-1:document:view",
	})
	require.Equal(t, 200, code, "body=%s", body)
	require.Equal(t, before+1, backendCallCount(t))
}

func TestE2E_DeniedReturns403(t *testing.T) {
	resetFixtures(t)
	setRule(t, "doc-2|document|edit", false)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/2", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "doc-2:document:edit",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t), "denied request must not reach backend")
}

func TestE2E_MissingAuthRejected(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/1", map[string]string{
		"X-Auth-Context": "doc-1:document:view",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_MalformedContextRejected(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/1", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "doc-1:document",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_OverLengthContextRejected(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	tooLong := strings.Repeat("a", 1024) + ":document:view"
	code, _ := do(t, "GET", "/api/orders/1", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": tooLong,
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_PCSErrorFailClosed(t *testing.T) {
	resetFixtures(t)
	// No rule registered → fake-pcs returns 503.
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/no-rule", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "no-rule:document:view",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_SkippedRouteBypassesSidecar(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/health", nil)
	require.Equal(t, 200, code)
	require.Equal(t, before+1, backendCallCount(t))
}
```

- [ ] **Step 2: Bring up the stack**

```bash
make -C test/e2e up
```

Expected: 4 containers running. Inspect with `docker compose -f test/e2e/docker-compose.yaml ps`.

- [ ] **Step 3: Run the e2e tests**

```bash
go test -tags=e2e ./test/e2e/... -v -count=1
```

Expected: 7 PASS. If a test fails, inspect with `docker compose -f test/e2e/docker-compose.yaml logs envoy sidecar fake-pcs fake-backend`.

- [ ] **Step 4: Bring the stack down**

```bash
make -C test/e2e down
```

- [ ] **Step 5: Commit**

```bash
git add test/e2e/e2e_test.go
git commit -m "test(e2e): granted/denied/missing/malformed/pcs-error/skip end-to-end cases"
```

---

## Task 16: Onboarding example (PV1-012)

Self-contained example app showing the route YAML, sample client requests, and the sidecar→PCS payload it produces. Lives outside `test/e2e` because it's documentation, not a test asset.

**Files:**
- Create: `permission-validation/examples/onboarding/routes.yaml`
- Create: `permission-validation/examples/onboarding/client-request.sh`
- Create: `permission-validation/examples/onboarding/README.md`

- [ ] **Step 1: Onboarding route config**

Create `examples/onboarding/routes.yaml`:

```yaml
# Phase 1 onboarding example: orders-app
# Adopted by copying this file into your app repo and running:
#   validate-routes validate examples/onboarding/routes.yaml
#   validate-routes translate examples/onboarding/routes.yaml -o envoy.yaml \
#       --sidecar-host sidecar --sidecar-port 50051 \
#       --backend-host orders-app --backend-port 8080
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  # --- skipped (no validation) ---
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /metrics
    behavior: skipped
  - method: GET
    path: /assets/**
    behavior: skipped

  # --- protected (validated via PCS) ---
  - method: GET
    path: /api/orders/*
    behavior: protected
  - method: POST
    path: /api/orders
    behavior: protected
  - method: "*"
    path: /api/admin/**
    behavior: protected
```

- [ ] **Step 2: Sample client requests**

Create `examples/onboarding/client-request.sh`:

```bash
#!/usr/bin/env bash
# Phase 1 onboarding: example client calls through the sidecar.
# Trust model: the client is trusted to declare a truthful (objectId, objectType).
# PCS still decides whether the SSO user holds the requested permission on that object.
# A client substituting an objectId they don't have permission on is harmless (PCS denies).
# A client substituting an objectId they DO have permission on while the app backend
# operates on a different object referenced in the URL is the Phase 1 residual risk.
set -euo pipefail

ENVOY=${ENVOY:-http://127.0.0.1:8000}
SSO=${SSO:-"Bearer your-sso-token"}

echo "# granted: user-1 viewing doc-1"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "Authorization: $SSO" \
  -H "X-Auth-Context: doc-1:document:view"

echo
echo "# denied: user-1 trying admin-delete on doc-1 (PCS will reject if they lack the perm)"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "Authorization: $SSO" \
  -H "X-Auth-Context: doc-1:document:admin-delete"

echo
echo "# rejected at sidecar (malformed: too few segments)"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "Authorization: $SSO" \
  -H "X-Auth-Context: doc-1:document"

echo
echo "# rejected at sidecar (missing Authorization)"
curl -sS -i "$ENVOY/api/orders/1" \
  -H "X-Auth-Context: doc-1:document:view"
```

- [ ] **Step 3: Onboarding README**

Create `examples/onboarding/README.md`:

```markdown
# Phase 1 Onboarding — orders-app example

This example shows the minimum a new application team needs to onboard the
permission-validation sidecar in Phase 1.

## Files

- `routes.yaml` — protected and skipped routes for your app.
- `client-request.sh` — `curl` snippets that produce a granted call, a denied
  call, a malformed-header call, and a missing-header call.

## Trust model (must read)

Phase 1 trusts the client to declare a truthful `(objectId, objectType)` in
`X-Auth-Context`. The `action` segment is **user intent**, not proof of
permission. The Permission Checking Service (PCS) is the sole authority on
whether the SSO user holds the requested permission on `(objectId, objectType)`.

A client substituting an `objectId` they do not have permission on is harmless
(PCS denies). A client substituting an `objectId` they *do* have permission on,
while the application backend operates on a different object referenced in the
URL or body, is the accepted Phase 1 residual risk. Cross-checking the URL /
body against the header is out of Phase 1 scope.

## Wire format

```
Authorization: Bearer <SSO token>
X-Auth-Context: <objectId>:<objectType>:<action>
```

Rules (rejection labels in parentheses):

- Exactly three non-empty segments separated by `:` (`wrong_segment_count`, `empty_segment`).
- No `:` inside any segment (parses as `wrong_segment_count`).
- No whitespace anywhere (`whitespace`).
- No control characters or non-ASCII bytes (`control_char`, `non_printable`).
- Header value ≤ 1024 bytes (`over_length`).

## What the sidecar sends to PCS

```
POST http://permission-checking/permission-check/v1/check
Content-Type: application/json
Authorization: Bearer <SSO token, forwarded verbatim>

{
  "objectId":   "<from segment 1 of X-Auth-Context>",
  "objectType": "<from segment 2>",
  "permission": "<from segment 3>"
}
```

## Common rejection cases

| Client sent | Sidecar response | Backend reached? |
|---|---|---|
| `X-Auth-Context: doc-1:document:view`, valid SSO, PCS allows | `200` (backend response) | yes |
| `X-Auth-Context: doc-1:document:admin-delete`, valid SSO, PCS denies | `403 Forbidden` | no |
| Missing `Authorization` | `403 Forbidden` | no |
| Missing `X-Auth-Context` | `403 Forbidden` | no |
| `X-Auth-Context: doc-1:document` (too few segments) | `403 Forbidden` | no |
| `X-Auth-Context: doc-1::view` (empty segment) | `403 Forbidden` | no |
| `X-Auth-Context: doc-1:document:view ` (trailing space) | `403 Forbidden` | no |
| PCS timeout or 5xx | `403 Forbidden` (fail-closed) | no |

## Adopt in your repo

1. Copy `routes.yaml` next to your app source.
2. Validate locally: `validate-routes validate routes.yaml`.
3. Have the platform CI run the same `validate-routes` step.
4. Tell every client that produces requests to your app to send `Authorization`
   and `X-Auth-Context` per the wire format above.
```

- [ ] **Step 4: Commit**

```bash
chmod +x examples/onboarding/client-request.sh
git add examples/onboarding/
git commit -m "docs(examples): Phase 1 onboarding example (PV1-012)"
```

---

## Task 17: Final tidy — module-wide checks

- [ ] **Step 1: Run the full test suite (excluding e2e)**

```bash
go test ./...
```

Expected: all PASS.

- [ ] **Step 2: Run `go vet`**

```bash
go vet ./...
```

Expected: no output.

- [ ] **Step 3: Run `go build ./...`**

```bash
go build ./...
```

Expected: no output.

- [ ] **Step 4: Confirm `gofmt`-clean**

```bash
test -z "$(gofmt -l .)"
```

Expected: exit 0 (no output). If anything is listed, run `gofmt -w` on those files and re-stage.

- [ ] **Step 5: Final commit (only if anything changed)**

```bash
git status
# If anything changed:
# git add -p
# git commit -m "chore: gofmt and tidy after task 16"
```

---

## Self-Review

**Spec coverage (PV1-001 … PV1-012):**

- PV1-001 (topology decision) — design only; already approved.
- PV1-002 (request contract) — design only.
- PV1-003 (context header format) — design only; encoded as Task 2 parser rules.
- PV1-004 (route config schema) — Task 8 (validator) + Task 9 (translator) + Task 10 (CLI).
- PV1-005 (route matching) — moved to Envoy under Option B; covered by Task 9 (translator) + Task 14 (envoy-up-and-running) + Task 15 (skip-bypass e2e test).
- PV1-006 (extract required headers) — Task 3, with `missing_authz`/`missing_ctx`/`malformed_authz` reason labels exercised in Task 7 (server) and Task 15 (e2e).
- PV1-007 (parse + validate context header) — Task 2 with all six reason labels.
- PV1-008 (build PCS request) — Task 4.
- PV1-009 (enforce decision) — Task 6 (orchestration) + Task 7 (wire), including fail-closed on PCS error.
- PV1-010 (metrics) — Task 5; instrument names: `pv.decisions.total{outcome}`, `pv.header_invalid.total{reason}`, `pv.ctx_parse_failure.total{reason}`, `pv.sidecar.latency`, `pv.pcs.latency`. Exported via OTLP/gRPC in Task 11.
- PV1-011 (integration tests) — Task 12 (fake PCS), Task 13 (fake backend), Task 14 (Compose), Task 15 (Go tests). Covers granted, denied, missing header, malformed context, over-length context, PCS error, skipped route.
- PV1-012 (onboarding example) — Task 16.

**Placeholder scan:** no `TBD`/`TODO`/`fill in`/"similar to" patterns. Every code step contains the actual code an engineer would commit.

**Type consistency:** `Outcome.Kind` uses the same `OutcomeAllow/OutcomeDeny/OutcomeRejectHeader/OutcomeRejectParse/OutcomeRejectError` constants in Task 6's handler and Task 7's `outcomeToReply`. `pcs.Decision` (`DecisionUnknown`, `DecisionAllow`, `DecisionDeny`) is consumed in both Task 4 and Task 6. Metric names in Task 5 match the assertions in Task 5's test and are referenced unchanged in Task 6's handler calls.

**Spec-derived risks consciously deferred to Phase 1.5 / Phase 2 (do not implement here):**

- URL / body cross-check against `X-Auth-Context.objectId` — Phase 1 trusts the client.
- Response-phase Tap for the Phase 1.5 WAL invariant — Task 7's `processing_mode.response_header_mode: SKIP` is the Phase 1 setting; flipping it to `SEND` is a Phase 1.5 task.
- Decision caching and event-driven invalidation.
- xDS-based Envoy config delivery (Phase 1 uses static config).
- Fail-open behavior.

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-05-15-permission-validation-phase-1.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration.

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
