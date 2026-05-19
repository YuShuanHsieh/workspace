# Istio EnvoyFilter Target Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an Istio render target to `validate-routes translate` so app teams running under a service mesh (istio-injected pods) can adopt the permission-validation sidecar without owning the Envoy bootstrap — by generating an `EnvoyFilter` CRD that patches the mesh-owned Envoy — and move route-level `protected`/`skipped` decisions into the pv sidecar process via a new `--routes-file` flag, with `routes.yaml` still the single source of truth.

**Architecture:** Two parallel changes converging on the same `routes.yaml`. (1) A new `internal/routes` package compiles routes into method+path matchers with a `Lookup(method, path) → Decision` API; the `extproc.Handler` accepts an optional routes table and short-circuits skipped/default-deny/default-skipped requests before invoking header parsing or PCS. (2) A new `internal/config.TranslateIstio` function renders an `EnvoyFilter` CRD (three configPatches: STATIC cluster, INSERT_BEFORE ext_proc filter, MERGE probe-path bypass) from an embedded `envoy-filter.tmpl.yaml`; the `validate-routes translate` CLI grows a `--target={static|istio}` flag with strict flag-matrix validation so wrong-target flags are rejected at the CLI rather than silently shipped.

**Tech Stack:** Go 1.25, `text/template` with `//go:embed`, `gopkg.in/yaml.v3`, `github.com/stretchr/testify/require`, existing `permission-validation/internal/config` package (`Parse`, `Validate`, `GlobToRegex` — newly exported in Task 1).

**Design references** (already approved — do not modify):

- [2026-05-18-istio-envoyfilter-target-design.md](../specs/2026-05-18-istio-envoyfilter-target-design.md) — the design spec this plan implements.
- [phase-1-topology-decision.md](../../../prd/permission-validation/phase-1-topology-decision.md) — why xDS is deferred; this work is the stepping stone.
- Existing static-target renderer: `permission-validation/internal/config/translate.go`, `permission-validation/internal/config/envoy-static.tmpl.yaml`.

---

## File Structure

All paths relative to `/home/cjamhe01385/workspace/`.

```
permission-validation/
├── cmd/
│   ├── permission-validation/
│   │   ├── main.go                         # MODIFY: add --routes-file flag
│   │   └── main_test.go                    # MODIFY: cover --routes-file
│   └── validate-routes/
│       ├── main.go                         # MODIFY: add --target + Istio flags, matrix validation, dispatch
│       └── main_test.go                    # MODIFY: cover target/flag matrix
├── internal/
│   ├── config/
│   │   ├── envoy-filter.tmpl.yaml          # CREATE: embedded EnvoyFilter template
│   │   ├── translate.go                    # MODIFY: export GlobToRegex (rename globToRegex)
│   │   ├── translate_istio.go              # CREATE: IstioOptions + TranslateIstio
│   │   └── translate_istio_test.go         # CREATE: translator unit tests
│   ├── routes/
│   │   ├── match.go                        # CREATE: Decision, Table, Compile, Lookup
│   │   └── match_test.go                   # CREATE: matcher tests
│   └── extproc/
│       ├── handler.go                      # MODIFY: accept *routes.Table, short-circuit before header parse
│       └── handler_test.go                 # MODIFY: 3 new short-circuit cases + update newHandler helper
├── examples/onboarding/
│   ├── envoyfilter.yaml                    # CREATE: committed Istio target render
│   └── README.md                           # MODIFY: "Adopt in an Istio-injected pod" section
├── README.md                               # MODIFY: --target=istio + --routes-file docs
└── test/e2e/
    └── README.md                           # MODIFY: one-line "Istio target out of scope" note
```

**Responsibilities:**
- `internal/routes/match.go` — pure compile + lookup; no I/O; imports `internal/config` for `RouteConfig` types and `GlobToRegex`. Returns a tri-valued `Decision`: `Allow` (skip route), `Deny` (no match + default deny), `Protected` (matched protected → caller falls through to PCS).
- `internal/extproc/handler.go` — gains an optional `*routes.Table` field; when set, runs short-circuit BEFORE header extraction so skipped routes never see `Authorization`/`X-Auth-Context` requirements. When `nil`, behaves exactly as Phase 1.
- `internal/config/translate_istio.go` — independent of `internal/config/translate.go`; new `IstioOptions` struct, new template, new render function. No shared "target" field on either options struct (spec §4.2).
- `cmd/validate-routes/main.go` — `runTranslate` parses `--target`, validates the flag matrix against `flag.FlagSet.Visit` (detects "explicitly set" vs "default"), dispatches to the right renderer.

---

## Sequencing

Eight tasks, executed in order. Tasks 1–3 build the sidecar's route short-circuit. Tasks 4–6 build the Istio renderer + CLI. Task 7 produces the committed example. Task 8 finishes the docs.

Each task ends with a commit. Run `go test ./... && go vet ./... && test -z "$(gofmt -l .)"` before committing each task.

---

### Task 1: Export GlobToRegex and add `internal/routes` package

**Files:**
- Modify: `permission-validation/internal/config/translate.go`
- Create: `permission-validation/internal/routes/match.go`
- Create: `permission-validation/internal/routes/match_test.go`

- [ ] **Step 1: Rename `globToRegex` to `GlobToRegex` in `internal/config/translate.go`**

Apply two edits inside `translate.go`:

1. The function declaration on line 114: `func globToRegex(p string) string {` → `func GlobToRegex(p string) string {`.
2. The single call site on line 106: `rv.Regex = globToRegex(r.Path)` → `rv.Regex = GlobToRegex(r.Path)`.

Update the doc comment on line 111 from `// globToRegex converts ...` to `// GlobToRegex converts ...`.

- [ ] **Step 2: Verify the existing tests still pass**

Run: `cd permission-validation && go build ./... && go test ./internal/config/...`
Expected: PASS (the rename is internal to one package; no external callers exist yet).

- [ ] **Step 3: Write the failing matcher test file**

Create `permission-validation/internal/routes/match_test.go`:

```go
package routes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"permission-validation/internal/config"
)

func tableFrom(t *testing.T, defaultBehavior string, rules ...config.RouteRule) *Table {
	t.Helper()
	rc := &config.RouteConfig{
		Version:         "v1",
		AppID:           "test-app",
		DefaultBehavior: defaultBehavior,
		Routes:          rules,
	}
	tbl, err := Compile(rc)
	require.NoError(t, err)
	return tbl
}

func TestCompile_NilConfigReturnsError(t *testing.T) {
	_, err := Compile(nil)
	require.Error(t, err)
}

func TestLookup_ExactMethodAndPath_Skipped(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/health", Behavior: "skipped"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/health"))
}

func TestLookup_ExactMethodAndPath_Protected(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "POST", Path: "/api/orders", Behavior: "protected"},
	)
	require.Equal(t, DecisionProtected, tbl.Lookup("POST", "/api/orders"))
}

func TestLookup_WildcardMethodMatches(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "*", Path: "/api/admin/**", Behavior: "protected"},
	)
	require.Equal(t, DecisionProtected, tbl.Lookup("DELETE", "/api/admin/users/42"))
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/api/admin/users/42"))
}

func TestLookup_MethodMismatchSkipsRule(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "POST", Path: "/api/orders", Behavior: "protected"},
	)
	// GET /api/orders does not match POST rule; falls through to default-deny.
	require.Equal(t, DecisionDeny, tbl.Lookup("GET", "/api/orders"))
}

func TestLookup_SingleStarMatchesOneSegment(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/api/orders/123"))
	// Two segments past /api/orders → does not match single-star.
	require.Equal(t, DecisionDeny, tbl.Lookup("GET", "/api/orders/123/items"))
}

func TestLookup_DoubleStarMatchesAnySuffix(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/assets/**", Behavior: "skipped"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/assets/img/logo.png"))
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/assets/"))
}

func TestLookup_FirstMatchWins(t *testing.T) {
	// Two overlapping rules: skipped wins because it's listed first.
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/api/orders/health", Behavior: "skipped"},
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/api/orders/health"))
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/api/orders/42"))
}

func TestLookup_NoMatch_DefaultDeny(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionDeny, tbl.Lookup("GET", "/unrelated"))
}

func TestLookup_NoMatch_DefaultSkipped(t *testing.T) {
	tbl := tableFrom(t, "skipped",
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/unrelated"))
}

func TestLookup_NilTable_TreatedAsProtected(t *testing.T) {
	var tbl *Table
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/anything"))
}
```

- [ ] **Step 4: Run the test file to verify it fails**

Run: `cd permission-validation && go test ./internal/routes/...`
Expected: FAIL — `package routes` has no `Compile`, `Table`, `DecisionAllow`, etc.

- [ ] **Step 5: Implement `internal/routes/match.go`**

Create `permission-validation/internal/routes/match.go`:

```go
// Package routes compiles a permission-validation route table and answers
// short-circuit lookups by method + request path. It is the in-sidecar
// counterpart to the per-route filter config the static Envoy target embeds
// in envoy.yaml: when the Istio target is used, route-level skip / default
// decisions move into this package so the EnvoyFilter does not need them.
package routes

import (
	"errors"
	"fmt"
	"regexp"

	"permission-validation/internal/config"
)

// Decision is the outcome of a route Lookup.
type Decision int

const (
	// DecisionAllow means the request should be forwarded without PCS validation
	// (matched a skipped route, or no match + default skipped).
	DecisionAllow Decision = iota
	// DecisionDeny means the request should be rejected at the sidecar
	// (no match + default deny).
	DecisionDeny
	// DecisionProtected means the caller should fall through to the existing
	// header-parse + PCS-call path (matched a protected route).
	DecisionProtected
)

type compiledRoute struct {
	method   string // exact method or "*"
	pattern  *regexp.Regexp
	behavior string // "protected" | "skipped"
}

// Table is a compiled, immutable route lookup. Use Compile to construct one;
// Lookup is safe for concurrent use.
type Table struct {
	routes          []compiledRoute
	defaultBehavior string
}

// Compile builds a Table from a validated RouteConfig. Routes are evaluated in
// file order: the first matching rule wins.
func Compile(rc *config.RouteConfig) (*Table, error) {
	if rc == nil {
		return nil, errors.New("routes: nil route config")
	}
	t := &Table{defaultBehavior: rc.DefaultBehavior}
	for i, r := range rc.Routes {
		re, err := regexp.Compile(config.GlobToRegex(r.Path))
		if err != nil {
			return nil, fmt.Errorf("routes[%d]: compile %q: %w", i, r.Path, err)
		}
		t.routes = append(t.routes, compiledRoute{
			method:   r.Method,
			pattern:  re,
			behavior: r.Behavior,
		})
	}
	return t, nil
}

// Lookup returns the decision for (method, path). A nil receiver returns
// DecisionProtected so callers fall through to the existing PCS path.
func (t *Table) Lookup(method, path string) Decision {
	if t == nil {
		return DecisionProtected
	}
	for _, r := range t.routes {
		if r.method != "*" && r.method != method {
			continue
		}
		if !r.pattern.MatchString(path) {
			continue
		}
		if r.behavior == "skipped" {
			return DecisionAllow
		}
		return DecisionProtected
	}
	if t.defaultBehavior == "skipped" {
		return DecisionAllow
	}
	return DecisionDeny
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `cd permission-validation && go test ./internal/routes/... -v`
Expected: PASS — all 11 tests green.

- [ ] **Step 7: Run the full suite + lint**

Run: `cd permission-validation && go test ./... && go vet ./... && test -z "$(gofmt -l .)"`
Expected: clean exit.

- [ ] **Step 8: Commit**

```bash
git add permission-validation/internal/routes/ permission-validation/internal/config/translate.go
git commit -m "$(cat <<'EOF'
feat(routes): add compiled route matcher with Lookup() API

Why: prerequisite for moving route-level skip/default decisions into the
sidecar process when the Istio render target is used (the EnvoyFilter cannot
carry per-route ext_proc disabled config without diverging from routes.yaml).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Wire `*routes.Table` into `extproc.Handler` with short-circuit

**Files:**
- Modify: `permission-validation/internal/extproc/handler.go`
- Modify: `permission-validation/internal/extproc/handler_test.go`
- Modify: `permission-validation/internal/extproc/server_test.go` (one helper line — see Step 5)

- [ ] **Step 1: Update existing handler test helper to take a nil routes arg (failing test setup)**

Edit `permission-validation/internal/extproc/handler_test.go`. Update the `newHandler` helper at line 27:

```go
func newHandler(t *testing.T, p PCS) *Handler {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	return New(p, metrics.New(mp.Meter("test")), nil)
}
```

Add an overload helper directly below for tests that need a routes table:

```go
func newHandlerWithRoutes(t *testing.T, p PCS, tbl *routes.Table) *Handler {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	return New(p, metrics.New(mp.Meter("test")), tbl)
}
```

Also update the three `New(nil, ...)` / `New(&stubPCS{...}, nil)` calls inside `TestDecide_NilDependenciesFailClosed` (lines 115–117) to pass a third `nil` argument:

```go
{name: "nil pcs", h: New(nil, metrics.New(metric.NewMeterProvider().Meter("test")), nil)},
{name: "nil metrics", h: New(&stubPCS{decision: pcs.DecisionAllow}, nil, nil)},
```

(The third entry `{name: "nil handler", h: nil}` stays unchanged.)

Add the import: `"permission-validation/internal/routes"` to the import block at the top.

- [ ] **Step 2: Write the three new failing tests**

Append to `permission-validation/internal/extproc/handler_test.go`:

```go
func TestDecide_SkippedRouteShortCircuitsAllow(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionDeny} // would deny if asked
	tbl, err := routes.Compile(&config.RouteConfig{
		Version: "v1", AppID: "test", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/health", Behavior: "skipped"},
		},
	})
	require.NoError(t, err)
	h := newHandlerWithRoutes(t, p, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/health",
	})
	require.Equal(t, OutcomeAllow, out.Kind)
	require.Equal(t, "", p.gotReq.ObjectID, "PCS must not be invoked for skipped routes")
}

func TestDecide_DefaultDenyShortCircuitsDeny(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow} // would allow if asked
	tbl, err := routes.Compile(&config.RouteConfig{
		Version: "v1", AppID: "test", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
		},
	})
	require.NoError(t, err)
	h := newHandlerWithRoutes(t, p, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/unrelated",
	})
	require.Equal(t, OutcomeDeny, out.Kind)
	require.Equal(t, "", p.gotReq.ObjectID, "PCS must not be invoked for default-deny no-match")
}

func TestDecide_DefaultSkippedShortCircuitsAllow(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionDeny}
	tbl, err := routes.Compile(&config.RouteConfig{
		Version: "v1", AppID: "test", DefaultBehavior: "skipped",
		Routes: []config.RouteRule{
			{Method: "POST", Path: "/api/orders", Behavior: "protected"},
		},
	})
	require.NoError(t, err)
	h := newHandlerWithRoutes(t, p, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/anything-not-protected",
	})
	require.Equal(t, OutcomeAllow, out.Kind)
	require.Equal(t, "", p.gotReq.ObjectID, "PCS must not be invoked for default-skipped no-match")
}

func TestDecide_ProtectedRouteFallsThroughToPCS(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow}
	tbl, err := routes.Compile(&config.RouteConfig{
		Version: "v1", AppID: "test", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "POST", Path: "/api/orders", Behavior: "protected"},
		},
	})
	require.NoError(t, err)
	h := newHandlerWithRoutes(t, p, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method":        "POST",
		":path":          "/api/orders",
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:create",
	})
	require.Equal(t, OutcomeAllow, out.Kind)
	require.Equal(t, "doc-1", p.gotReq.ObjectID, "protected route must invoke PCS with parsed context")
}

func TestDecide_PathWithQueryStringMatchesRoute(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow}
	tbl, err := routes.Compile(&config.RouteConfig{
		Version: "v1", AppID: "test", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/health", Behavior: "skipped"},
		},
	})
	require.NoError(t, err)
	h := newHandlerWithRoutes(t, p, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/health?verbose=1",
	})
	require.Equal(t, OutcomeAllow, out.Kind)
}
```

Add the `config` import: `"permission-validation/internal/config"` to the import block.

- [ ] **Step 3: Run the new tests to verify they fail**

Run: `cd permission-validation && go test ./internal/extproc/... -run 'TestDecide_(SkippedRouteShortCircuitsAllow|DefaultDenyShortCircuitsDeny|DefaultSkippedShortCircuitsAllow|ProtectedRouteFallsThroughToPCS|PathWithQueryStringMatchesRoute)' -v`
Expected: FAIL on the new tests because `New` still has the old 2-arg signature.

- [ ] **Step 4: Update `Handler` struct, `New` signature, and `Decide` short-circuit in `internal/extproc/handler.go`**

Replace the entire body of `permission-validation/internal/extproc/handler.go` with:

```go
package extproc

import (
	"context"
	"errors"
	"strings"
	"time"

	"permission-validation/internal/header"
	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
	"permission-validation/internal/routes"
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

// Handler is the orchestration core: route-lookup → extract → parse → PCS → emit
// metrics → return Outcome.
type Handler struct {
	pcs   PCS
	m     *metrics.Metrics
	table *routes.Table // nil = Phase 1 behavior (route decisions live in Envoy)
}

// New constructs a Handler. The metrics object is required. A nil routes Table
// preserves Phase 1 behavior; pass a non-nil Table to enable in-sidecar
// short-circuit of skipped routes and the catch-all (used by the Istio target).
func New(p PCS, m *metrics.Metrics, t *routes.Table) *Handler {
	return &Handler{pcs: p, m: m, table: t}
}

// Decide consumes a lowercased header map (Envoy normalizes header casing on HTTP/2)
// and returns the wire outcome.
func (h *Handler) Decide(ctx context.Context, hdrs map[string]string) Outcome {
	if h == nil || h.m == nil || h.pcs == nil {
		return Outcome{Kind: OutcomeRejectError, Reason: "internal_error"}
	}
	start := time.Now()
	defer func() { h.m.SidecarLatency(ctx, time.Since(start)) }()

	if h.table != nil {
		method := hdrs[":method"]
		path := hdrs[":path"]
		if i := strings.IndexByte(path, '?'); i >= 0 {
			path = path[:i]
		}
		switch h.table.Lookup(method, path) {
		case routes.DecisionAllow:
			h.m.Decision(ctx, "allow")
			return Outcome{Kind: OutcomeAllow}
		case routes.DecisionDeny:
			h.m.Decision(ctx, "deny")
			return Outcome{Kind: OutcomeDeny}
		}
		// DecisionProtected → fall through to header + PCS path.
	}

	tok, err := header.ExtractAuth(hdrs)
	if err != nil {
		var he *header.HeaderError
		if !errors.As(err, &he) {
			he = &header.HeaderError{Reason: "internal_error"}
		}
		h.m.HeaderInvalid(ctx, he.Reason)
		return Outcome{Kind: OutcomeRejectHeader, Reason: he.Reason}
	}
	ctxRaw, err := header.ExtractContext(hdrs)
	if err != nil {
		var he *header.HeaderError
		if !errors.As(err, &he) {
			he = &header.HeaderError{Reason: "internal_error"}
		}
		h.m.HeaderInvalid(ctx, he.Reason)
		return Outcome{Kind: OutcomeRejectHeader, Reason: he.Reason}
	}
	parsed, err := header.ParseContextHeader(ctxRaw)
	if err != nil {
		var pe *header.ParseError
		if !errors.As(err, &pe) {
			pe = &header.ParseError{Reason: "internal_error"}
		}
		h.m.CtxParseFailure(ctx, pe.Reason)
		return Outcome{Kind: OutcomeRejectParse, Reason: pe.Reason}
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

- [ ] **Step 5: Update `server_test.go`'s `startServer` helper to pass nil routes**

Edit `permission-validation/internal/extproc/server_test.go`. Replace the `h := New(p, metrics.New(mp.Meter("test")))` line (around line 37) with:

```go
h := New(p, metrics.New(mp.Meter("test")), nil)
```

- [ ] **Step 6: Run the full extproc + routes tests**

Run: `cd permission-validation && go test ./internal/extproc/... ./internal/routes/... -v`
Expected: PASS — all new and existing tests green.

- [ ] **Step 7: Commit**

```bash
git add permission-validation/internal/extproc/
git commit -m "$(cat <<'EOF'
feat(extproc): short-circuit skipped routes via optional routes.Table

When the sidecar is configured with a routes table (Istio target), route-level
skip / default decisions happen before any header parsing or PCS call. nil
table preserves Phase 1 behavior where Envoy's per-route filter config bypasses
the sidecar entirely.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Add `--routes-file` flag to the `permission-validation` sidecar

**Files:**
- Modify: `permission-validation/cmd/permission-validation/main.go`
- Modify: `permission-validation/cmd/permission-validation/main_test.go`

- [ ] **Step 1: Write failing tests for the new flag**

Append to `permission-validation/cmd/permission-validation/main_test.go`:

```go
func TestRun_RoutesFile_ParsesAndStarts(t *testing.T) {
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.yaml")
	require.NoError(t, os.WriteFile(routes, []byte(
		"version: v1\nappId: test\ndefaultBehavior: deny\nroutes:\n  - method: GET\n    path: /health\n    behavior: skipped\n"),
		0o644))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{
			"--listen", "127.0.0.1:0",
			"--pcs-endpoint", "http://127.0.0.1:1",
			"--otel-disabled",
			"--routes-file", routes,
		}, &stdout, &stderr)
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		require.Equal(t, 0, code, "stderr=%s", stderr.String())
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within 2s")
	}
}

func TestRun_RoutesFile_MissingFile_ExitsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--listen", "127.0.0.1:0",
		"--pcs-endpoint", "http://127.0.0.1:1",
		"--otel-disabled",
		"--routes-file", "/nonexistent/routes.yaml",
	}, &stdout, &stderr)
	require.NotEqual(t, 0, code)
	require.Contains(t, stderr.String(), "routes-file")
}

func TestRun_RoutesFile_InvalidYAML_ExitsNonZero(t *testing.T) {
	dir := t.TempDir()
	routes := filepath.Join(dir, "routes.yaml")
	require.NoError(t, os.WriteFile(routes, []byte("version: v2\n"), 0o644)) // bad version, missing fields

	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--listen", "127.0.0.1:0",
		"--pcs-endpoint", "http://127.0.0.1:1",
		"--otel-disabled",
		"--routes-file", routes,
	}, &stdout, &stderr)
	require.NotEqual(t, 0, code)
}
```

Add these imports if not present: `"os"`, `"path/filepath"`, `"github.com/stretchr/testify/require"`.

- [ ] **Step 2: Run the tests to confirm they fail**

Run: `cd permission-validation && go test ./cmd/permission-validation/... -run 'TestRun_RoutesFile' -v`
Expected: FAIL — `--routes-file` flag does not exist.

- [ ] **Step 3: Add the `--routes-file` flag and table wiring in `main.go`**

Edit `permission-validation/cmd/permission-validation/main.go`. Apply three changes:

1. Add `"permission-validation/internal/config"` and `"permission-validation/internal/routes"` to the import block.

2. Add the flag definition right after the `otelDisabled` line (around line 36):

```go
routesFile := fs.String("routes-file", "", "optional path to routes.yaml; when set, the sidecar short-circuits skipped routes and the default catch-all without invoking PCS")
```

3. After `pcsClient := pcs.NewClient(...)` and before `h := extproc.New(pcsClient, m)`, replace the handler construction with:

```go
var routesTable *routes.Table
if *routesFile != "" {
	b, err := os.ReadFile(*routesFile)
	if err != nil {
		fmt.Fprintln(stderr, "routes-file:", err)
		return 1
	}
	rc, perr := config.Parse(b)
	if perr != nil {
		fmt.Fprintln(stderr, "routes-file:", perr)
		return 1
	}
	if verrs := config.Validate(rc); len(verrs) > 0 {
		for _, e := range verrs {
			fmt.Fprintln(stderr, "routes-file:", e)
		}
		return 1
	}
	tbl, cerr := routes.Compile(rc)
	if cerr != nil {
		fmt.Fprintln(stderr, "routes-file:", cerr)
		return 1
	}
	routesTable = tbl
}
h := extproc.New(pcsClient, m, routesTable)
```

- [ ] **Step 4: Run the cmd tests + lint**

Run: `cd permission-validation && go test ./cmd/permission-validation/... -v && go vet ./... && test -z "$(gofmt -l .)"`
Expected: PASS, clean.

- [ ] **Step 5: Commit**

```bash
git add permission-validation/cmd/permission-validation/
git commit -m "$(cat <<'EOF'
feat(sidecar): add --routes-file flag to short-circuit skipped routes

When set, the sidecar parses and validates routes.yaml at startup (fail-fast)
and compiles it into a routes.Table consulted before header parse + PCS call.
Empty value preserves Phase 1 behavior.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Create the EnvoyFilter YAML template

**Files:**
- Create: `permission-validation/internal/config/envoy-filter.tmpl.yaml`

- [ ] **Step 1: Write the template**

Create `permission-validation/internal/config/envoy-filter.tmpl.yaml`:

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: {{ .Name }}
  namespace: {{ .Namespace }}
spec:
  workloadSelector:
    labels:
      {{- range .WorkloadLabels }}
      {{ .Key }}: {{ printf "%q" .Value }}
      {{- end }}
  configPatches:

    - applyTo: CLUSTER
      match:
        context: SIDECAR_INBOUND
      patch:
        operation: ADD
        value:
          name: pv_sidecar
          type: STATIC
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
                        socket_address: { address: 127.0.0.1, port_value: {{ .SidecarPort }} }

    - applyTo: HTTP_FILTER
      match:
        context: SIDECAR_INBOUND
        listener:
          filterChain:
            filter:
              name: envoy.filters.network.http_connection_manager
              subFilter:
                name: envoy.filters.http.router
      patch:
        operation: INSERT_BEFORE
        value:
          name: envoy.filters.http.ext_proc
          typed_config:
            "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
            grpc_service:
              envoy_grpc: { cluster_name: pv_sidecar }
            failure_mode_allow: false
            processing_mode:
              request_header_mode: SEND
              response_header_mode: SKIP
              request_body_mode: NONE
              response_body_mode: NONE
              request_trailer_mode: SKIP
              response_trailer_mode: SKIP

    - applyTo: VIRTUAL_HOST
      match:
        context: SIDECAR_INBOUND
      patch:
        operation: MERGE
        value:
          routes:
            {{- range .ProbePaths }}
            - match: { path: {{ printf "%q" . }} }
              typed_per_filter_config:
                envoy.filters.http.ext_proc:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
                  disabled: true
            {{- end }}
```

- [ ] **Step 2: No tests run yet — proceed to Task 5 which exercises this template**

The template is only valuable through `TranslateIstio`; defer the assertion-driven tests to the next task. No commit yet — combine with Task 5.

---

### Task 5: Implement `TranslateIstio` in `internal/config`

**Files:**
- Create: `permission-validation/internal/config/translate_istio.go`
- Create: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Write the failing translator test file**

Create `permission-validation/internal/config/translate_istio_test.go`:

```go
package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func parseYAML(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var out map[string]any
	require.NoError(t, yaml.Unmarshal(b, &out))
	return out
}

func TestTranslateIstio_MinimalProducesValidYAML(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)

	parsed := parseYAML(t, got)
	require.Equal(t, "networking.istio.io/v1alpha3", parsed["apiVersion"])
	require.Equal(t, "EnvoyFilter", parsed["kind"])

	md := parsed["metadata"].(map[string]any)
	require.Equal(t, "permission-validation-orders-app", md["name"])
	require.Equal(t, "orders", md["namespace"])

	spec := parsed["spec"].(map[string]any)
	labels := spec["workloadSelector"].(map[string]any)["labels"].(map[string]any)
	require.Equal(t, "orders-app", labels["app"])
}

func TestTranslateIstio_NameDefaultsFromAppID(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml") // appId: orders-app
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)
	parsed := parseYAML(t, got)
	md := parsed["metadata"].(map[string]any)
	require.Equal(t, "permission-validation-orders-app", md["name"])
}

func TestTranslateIstio_NameOverride(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		Name:           "custom-name",
	})
	require.NoError(t, err)
	parsed := parseYAML(t, got)
	md := parsed["metadata"].(map[string]any)
	require.Equal(t, "custom-name", md["name"])
}

func TestTranslateIstio_ProbePathsDefault(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)
	s := string(got)
	require.Contains(t, s, `path: "/healthz"`)
	require.Contains(t, s, `path: "/readyz"`)
	require.Contains(t, s, `path: "/livez"`)
}

func TestTranslateIstio_ProbePathsOverride(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		ProbePaths:     []string{"/health", "/ready"},
	})
	require.NoError(t, err)
	s := string(got)
	require.Contains(t, s, `path: "/health"`)
	require.Contains(t, s, `path: "/ready"`)
	require.NotContains(t, s, `path: "/healthz"`)
	require.NotContains(t, s, `path: "/livez"`)
}

func TestTranslateIstio_WorkloadLabelsRendered(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace: "orders",
		WorkloadLabels: map[string]string{
			"app":     "orders-app",
			"version": "v1",
		},
	})
	require.NoError(t, err)
	parsed := parseYAML(t, got)
	labels := parsed["spec"].(map[string]any)["workloadSelector"].(map[string]any)["labels"].(map[string]any)
	require.Equal(t, "orders-app", labels["app"])
	require.Equal(t, "v1", labels["version"])
}

func TestTranslateIstio_RoutesNotInOutput(t *testing.T) {
	// routes.yaml routes (e.g. /api/orders/*) must NOT appear in the EnvoyFilter —
	// only the probe paths do. This is the contract that the sidecar's routes.Table
	// owns route-level decisions when the Istio target is used.
	rc := loadFile(t, "valid-comprehensive.yaml") // has /api/orders/*, /api/admin/**, /metrics, /assets/**, etc.
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)
	s := string(got)
	require.NotContains(t, s, "/api/orders")
	require.NotContains(t, s, "/api/admin")
	require.NotContains(t, s, "/metrics")
	require.NotContains(t, s, "/assets")
	require.NotContains(t, s, "/favicon.ico")
}

func TestTranslateIstio_RejectsEmptyNamespace(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	_, err := TranslateIstio(rc, IstioOptions{
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "namespace")
}

func TestTranslateIstio_RejectsEmptyWorkloadLabels(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	_, err := TranslateIstio(rc, IstioOptions{
		Namespace: "orders",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "workload label")
}

func TestTranslateIstio_ContextIsSidecarInbound(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)
	parsed := parseYAML(t, got)
	patches := parsed["spec"].(map[string]any)["configPatches"].([]any)
	require.Len(t, patches, 3, "must emit exactly three configPatches")
	for i, p := range patches {
		match := p.(map[string]any)["match"].(map[string]any)
		require.Equal(t, "SIDECAR_INBOUND", match["context"], "patch %d", i)
	}
	// Counts of patch kinds.
	var addCluster, insertFilter, mergeVhost int
	for _, p := range patches {
		pm := p.(map[string]any)
		applyTo := pm["applyTo"].(string)
		op := pm["patch"].(map[string]any)["operation"].(string)
		switch {
		case applyTo == "CLUSTER" && op == "ADD":
			addCluster++
		case applyTo == "HTTP_FILTER" && op == "INSERT_BEFORE":
			insertFilter++
		case applyTo == "VIRTUAL_HOST" && op == "MERGE":
			mergeVhost++
		}
	}
	require.Equal(t, 1, addCluster)
	require.Equal(t, 1, insertFilter)
	require.Equal(t, 1, mergeVhost)
}

func TestTranslateIstio_FailureModeAllowFalse(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)
	require.True(t, strings.Contains(string(got), "failure_mode_allow: false"),
		"phase-1 fail-closed posture must be preserved")
}

func TestTranslateIstio_NilConfigReturnsError(t *testing.T) {
	_, err := TranslateIstio(nil, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil route config")
}

func TestTranslateIstio_SidecarPortOverride(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		SidecarPort:    50099,
	})
	require.NoError(t, err)
	require.Contains(t, string(got), "port_value: 50099")
}

func TestTranslateIstio_DefaultSidecarPort50051(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := TranslateIstio(rc, IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
	})
	require.NoError(t, err)
	require.Contains(t, string(got), "port_value: 50051")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd permission-validation && go test ./internal/config/... -run 'TestTranslateIstio' -v`
Expected: FAIL — `TranslateIstio` and `IstioOptions` do not exist.

- [ ] **Step 3: Implement `TranslateIstio` and `IstioOptions`**

Create `permission-validation/internal/config/translate_istio.go`:

```go
package config

import (
	"bytes"
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"text/template"
)

//go:embed envoy-filter.tmpl.yaml
var envoyFilterTemplate string

// IstioOptions are environment-specific values for the EnvoyFilter render.
// Namespace and WorkloadLabels are required; Name, ProbePaths, and SidecarPort
// default if unset.
type IstioOptions struct {
	// Namespace is the Kubernetes namespace metadata.namespace. Required.
	Namespace string
	// WorkloadLabels populates spec.workloadSelector.labels. Required, ≥1 entry.
	WorkloadLabels map[string]string
	// Name is metadata.name. Defaults to "permission-validation-<appId>".
	Name string
	// ProbePaths are the exact-match paths whose ext_proc filter is disabled
	// (the liveness/readiness carve-out). Defaults to /healthz, /readyz, /livez.
	ProbePaths []string
	// SidecarPort is the pv sidecar's gRPC port on 127.0.0.1. Defaults to 50051.
	SidecarPort int
}

var defaultProbePaths = []string{"/healthz", "/readyz", "/livez"}

type labelKV struct{ Key, Value string }

type istioView struct {
	Name           string
	Namespace      string
	WorkloadLabels []labelKV
	SidecarPort    int
	ProbePaths     []string
}

// TranslateIstio renders an Istio EnvoyFilter CRD that patches the mesh-owned
// Envoy with the ext_proc HTTP filter, a static pv_sidecar cluster, and a
// probe-path bypass. Route-level decisions (skipped routes, default behavior)
// are NOT embedded — they live in the sidecar's routes.Table at runtime.
func TranslateIstio(rc *RouteConfig, opts IstioOptions) ([]byte, error) {
	if rc == nil {
		return nil, errors.New("translate-istio: nil route config")
	}
	if opts.Namespace == "" {
		return nil, errors.New("translate-istio: namespace is required")
	}
	if len(opts.WorkloadLabels) == 0 {
		return nil, errors.New("translate-istio: at least one workload label is required")
	}

	name := opts.Name
	if name == "" {
		name = "permission-validation-" + rc.AppID
	}
	probes := opts.ProbePaths
	if len(probes) == 0 {
		probes = defaultProbePaths
	}
	port := opts.SidecarPort
	if port == 0 {
		port = 50051
	}

	labels := make([]labelKV, 0, len(opts.WorkloadLabels))
	for k, v := range opts.WorkloadLabels {
		labels = append(labels, labelKV{Key: k, Value: v})
	}
	sort.Slice(labels, func(i, j int) bool { return labels[i].Key < labels[j].Key })

	view := istioView{
		Name:           name,
		Namespace:      opts.Namespace,
		WorkloadLabels: labels,
		SidecarPort:    port,
		ProbePaths:     probes,
	}
	tmpl, err := template.New("envoyfilter").Parse(envoyFilterTemplate)
	if err != nil {
		return nil, fmt.Errorf("translate-istio: parse template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, view); err != nil {
		return nil, fmt.Errorf("translate-istio: execute template: %w", err)
	}
	return out.Bytes(), nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd permission-validation && go test ./internal/config/... -v`
Expected: PASS — all old config tests + 14 new TranslateIstio tests.

- [ ] **Step 5: Run full suite + lint**

Run: `cd permission-validation && go test ./... && go vet ./... && test -z "$(gofmt -l .)"`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add permission-validation/internal/config/envoy-filter.tmpl.yaml permission-validation/internal/config/translate_istio.go permission-validation/internal/config/translate_istio_test.go
git commit -m "$(cat <<'EOF'
feat(config): add TranslateIstio renderer for EnvoyFilter target

Renders a three-patch EnvoyFilter CRD (STATIC pv_sidecar cluster,
INSERT_BEFORE ext_proc HTTP filter, VIRTUAL_HOST MERGE for probe-path bypass)
all scoped to SIDECAR_INBOUND. Route-level skip/default decisions are NOT
embedded — they live in the sidecar's routes.Table.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Add `--target` flag and Istio flags to `validate-routes translate`

**Files:**
- Modify: `permission-validation/cmd/validate-routes/main.go`
- Modify: `permission-validation/cmd/validate-routes/main_test.go`

- [ ] **Step 1: Write failing CLI tests**

Append to `permission-validation/cmd/validate-routes/main_test.go`:

```go
func TestTranslate_TargetIstio_WritesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envoyfilter.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "istio",
			"--namespace", "orders",
			"--workload-label", "app=orders-app",
			"-o", out},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(b)
	require.Contains(t, s, "kind: EnvoyFilter")
	require.Contains(t, s, "namespace: orders")
	require.Contains(t, s, "permission-validation-orders-app")
	require.Contains(t, s, "SIDECAR_INBOUND")
}

func TestTranslate_TargetIstio_RequiresNamespace(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "istio",
			"--workload-label", "app=orders-app"},
		&bytes.Buffer{}, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "namespace")
}

func TestTranslate_TargetIstio_RequiresWorkloadLabel(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "istio",
			"--namespace", "orders"},
		&bytes.Buffer{}, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "workload-label")
}

func TestTranslate_TargetIstio_RejectsStaticFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "backend-host",
			args: []string{"--backend-host", "x"},
			want: "backend-host",
		},
		{
			name: "backend-port",
			args: []string{"--backend-port", "9999"},
			want: "backend-port",
		},
		{
			name: "admin-host",
			args: []string{"--admin-host", "0.0.0.0"},
			want: "admin-host",
		},
		{
			name: "access-log",
			args: []string{"--access-log"},
			want: "access-log",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			args := append([]string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--target", "istio",
				"--namespace", "orders",
				"--workload-label", "app=orders-app"}, tc.args...)
			code := run(context.Background(), args, &bytes.Buffer{}, &stderr)
			require.Equal(t, 2, code)
			require.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestTranslate_TargetStatic_RejectsIstioFlags(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "namespace", args: []string{"--namespace", "x"}, want: "namespace"},
		{name: "workload-label", args: []string{"--workload-label", "a=b"}, want: "workload-label"},
		{name: "name", args: []string{"--name", "x"}, want: "name"},
		{name: "probe-paths", args: []string{"--probe-paths", "/x"}, want: "probe-paths"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			args := append([]string{"translate", "../../testdata/routes/valid-minimal.yaml"}, tc.args...)
			code := run(context.Background(), args, &bytes.Buffer{}, &stderr)
			require.Equal(t, 2, code)
			require.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestTranslate_TargetInvalid(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "nginx"},
		&bytes.Buffer{}, &stderr)
	require.Equal(t, 2, code)
	s := stderr.String()
	require.Contains(t, s, "target")
	require.Contains(t, s, "static")
	require.Contains(t, s, "istio")
}

func TestTranslate_TargetIstio_MultipleWorkloadLabels(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envoyfilter.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "istio",
			"--namespace", "orders",
			"--workload-label", "app=orders-app",
			"--workload-label", "version=v1",
			"-o", out},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
	b, _ := os.ReadFile(out)
	s := string(b)
	require.Contains(t, s, `app: "orders-app"`)
	require.Contains(t, s, `version: "v1"`)
}

func TestTranslate_TargetIstio_ProbePathsOverride(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envoyfilter.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "istio",
			"--namespace", "orders",
			"--workload-label", "app=orders-app",
			"--probe-paths", "/health,/ready",
			"-o", out},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
	b, _ := os.ReadFile(out)
	s := string(b)
	require.Contains(t, s, `path: "/health"`)
	require.Contains(t, s, `path: "/ready"`)
	require.NotContains(t, s, `path: "/healthz"`)
}

func TestTranslate_TargetIstio_InvalidWorkloadLabel(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"--target", "istio",
			"--namespace", "orders",
			"--workload-label", "no-equals-sign"},
		&bytes.Buffer{}, &stderr)
	require.Equal(t, 2, code)
	require.Contains(t, stderr.String(), "workload-label")
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd permission-validation && go test ./cmd/validate-routes/... -run 'TestTranslate_Target' -v`
Expected: FAIL — `--target` flag does not exist.

- [ ] **Step 3: Replace `runTranslate` in `cmd/validate-routes/main.go`**

Replace the entire `runTranslate` function in `permission-validation/cmd/validate-routes/main.go` with:

```go
type stringList []string

func (s *stringList) String() string {
	if s == nil {
		return ""
	}
	return strings.Join(*s, ",")
}

func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func runTranslate(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: validate-routes translate <file> [--target static|istio] [flags]")
		return 2
	}
	file := args[0]
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(stderr)

	out := fs.String("o", "", "output file (defaults to stdout)")
	target := fs.String("target", "static", "envoy config target: static or istio")

	// Static-target flags.
	sidecarHost := fs.String("sidecar-host", "127.0.0.1", "(static, istio: ignored) sidecar gRPC host Envoy will dial")
	sidecarPort := fs.Int("sidecar-port", 50051, "sidecar gRPC port")
	backendHost := fs.String("backend-host", "127.0.0.1", "(static only) application backend host")
	backendPort := fs.Int("backend-port", 8080, "(static only) application backend port")
	adminHost := fs.String("admin-host", "127.0.0.1", "(static only) Envoy admin listener bind address (port 9901)")
	accessLog := fs.Bool("access-log", false, "(static only) emit Envoy access logs to stdout")

	// Istio-target flags.
	namespace := fs.String("namespace", "", "(istio only) Kubernetes namespace for the EnvoyFilter")
	name := fs.String("name", "", "(istio only) metadata.name; defaults to permission-validation-<appId>")
	probePaths := fs.String("probe-paths", "", "(istio only) comma-separated probe paths to bypass; default /healthz,/readyz,/livez")
	var workloadLabels stringList
	fs.Var(&workloadLabels, "workload-label", "(istio only) workloadSelector label as key=value; repeatable")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	visited := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { visited[f.Name] = true })

	switch *target {
	case "static":
		// Reject Istio-only flags.
		var rejected []string
		for _, n := range []string{"namespace", "workload-label", "name", "probe-paths"} {
			if visited[n] {
				rejected = append(rejected, "--"+n)
			}
		}
		if len(rejected) > 0 {
			fmt.Fprintf(stderr, "--target=static rejects %s (these are Istio-only flags; see -h)\n", strings.Join(rejected, ", "))
			return 2
		}
	case "istio":
		// Reject static-only flags.
		var rejected []string
		for _, n := range []string{"backend-host", "backend-port", "admin-host", "access-log"} {
			if visited[n] {
				rejected = append(rejected, "--"+n)
			}
		}
		if len(rejected) > 0 {
			fmt.Fprintf(stderr, "--target=istio rejects %s (Istio routes to the app, not the renderer; admin/access-log are mesh-level concerns; see -h)\n", strings.Join(rejected, ", "))
			return 2
		}
		if *namespace == "" {
			fmt.Fprintln(stderr, "--target=istio requires --namespace")
			return 2
		}
		if len(workloadLabels) == 0 {
			fmt.Fprintln(stderr, "--target=istio requires at least one --workload-label key=value")
			return 2
		}
	default:
		fmt.Fprintf(stderr, "invalid --target %q (valid values: static, istio)\n", *target)
		return 2
	}

	if !validPort(*sidecarPort) {
		fmt.Fprintf(stderr, "sidecar-port must be in range 1..65535 (got %d)\n", *sidecarPort)
		return 2
	}
	if *target == "static" && !validPort(*backendPort) {
		fmt.Fprintf(stderr, "backend-port must be in range 1..65535 (got %d)\n", *backendPort)
		return 2
	}

	rc, err := readConfig(file)
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

	var b []byte
	switch *target {
	case "static":
		b, err = config.Translate(rc, config.TranslateOptions{
			SidecarHost: *sidecarHost, SidecarPort: *sidecarPort,
			AppBackendHost: *backendHost, AppBackendPort: *backendPort,
			AdminHost: *adminHost, AccessLogStdout: *accessLog,
		})
	case "istio":
		labels, perr := parseLabels(workloadLabels)
		if perr != nil {
			fmt.Fprintln(stderr, perr)
			return 2
		}
		var probes []string
		if *probePaths != "" {
			probes = splitAndTrim(*probePaths, ",")
		}
		b, err = config.TranslateIstio(rc, config.IstioOptions{
			Namespace:      *namespace,
			WorkloadLabels: labels,
			Name:           *name,
			ProbePaths:     probes,
			SidecarPort:    *sidecarPort,
		})
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}

	if *out == "" {
		n, werr := stdout.Write(b)
		if werr != nil {
			fmt.Fprintln(stderr, werr)
			return 1
		}
		if n != len(b) {
			fmt.Fprintf(stderr, "short write: wrote %d of %d bytes\n", n, len(b))
			return 1
		}
		return 0
	}
	if werr := os.WriteFile(*out, b, 0o644); werr != nil {
		fmt.Fprintln(stderr, werr)
		return 1
	}
	return 0
}

func parseLabels(kvs []string) (map[string]string, error) {
	m := make(map[string]string, len(kvs))
	for _, kv := range kvs {
		i := strings.IndexByte(kv, '=')
		if i <= 0 || i == len(kv)-1 {
			return nil, fmt.Errorf("--workload-label %q: must be key=value", kv)
		}
		m[kv[:i]] = kv[i+1:]
	}
	return m, nil
}

func splitAndTrim(s, sep string) []string {
	raw := strings.Split(s, sep)
	out := raw[:0]
	for _, x := range raw {
		x = strings.TrimSpace(x)
		if x != "" {
			out = append(out, x)
		}
	}
	return out
}
```

Add `"strings"` to the import block at the top of the file if not present (it isn't currently — check). Also keep the existing `validPort` and `readConfig` helpers at the bottom.

- [ ] **Step 4: Run the CLI tests + existing tests**

Run: `cd permission-validation && go test ./cmd/validate-routes/... -v`
Expected: PASS — new target tests + the existing `TestTranslate_*` tests that exercise the static path.

- [ ] **Step 5: Run full suite + lint**

Run: `cd permission-validation && go test ./... && go vet ./... && test -z "$(gofmt -l .)"`
Expected: clean.

- [ ] **Step 6: Commit**

```bash
git add permission-validation/cmd/validate-routes/
git commit -m "$(cat <<'EOF'
feat(cli): add --target=istio to validate-routes translate

Dispatches to config.TranslateIstio; rejects wrong-target flags via
flag.FlagSet.Visit so silent ignores cannot ship the wrong config to prod.
Static target behavior is preserved.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: Generate and commit the example `envoyfilter.yaml`

**Files:**
- Create: `permission-validation/examples/onboarding/envoyfilter.yaml`

- [ ] **Step 1: Render the example**

Run from the repo root:

```bash
cd permission-validation && go run ./cmd/validate-routes translate examples/onboarding/routes.yaml \
  --target istio \
  --namespace orders \
  --workload-label app=orders-app \
  -o examples/onboarding/envoyfilter.yaml
```

Expected: exit 0, no stderr.

- [ ] **Step 2: Verify the file is syntactically valid YAML**

Run: `cd permission-validation && go run ./cmd/validate-routes translate examples/onboarding/routes.yaml --target istio --namespace orders --workload-label app=orders-app | head -5`
Expected: starts with `apiVersion: networking.istio.io/v1alpha3`.

Also inspect: `head -10 permission-validation/examples/onboarding/envoyfilter.yaml`.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/examples/onboarding/envoyfilter.yaml
git commit -m "$(cat <<'EOF'
docs(examples): commit Istio target example envoyfilter.yaml

Rendered from examples/onboarding/routes.yaml via the new --target=istio. The
companion onboarding README change (next commit) documents the regeneration
command and the adoption flow.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: Update documentation

**Files:**
- Modify: `permission-validation/README.md`
- Modify: `permission-validation/examples/onboarding/README.md`
- Modify: `permission-validation/test/e2e/README.md`

- [ ] **Step 1: Update the module README**

In `permission-validation/README.md`, after the existing `## validate-routes CLI` section's `translate` example block (which currently ends with `--access-log`), insert a new subsection:

```markdown
### Render target: `--target`

`translate` supports two render targets:

- `--target=static` (default): a self-contained Envoy bootstrap, as in Phase 1. App teams who run their own Envoy mount it at `/etc/envoy/envoy.yaml`.
- `--target=istio`: an Istio `EnvoyFilter` CRD that patches the mesh-owned Envoy with the ext_proc HTTP filter, a static cluster pointing at the pv sidecar at `127.0.0.1:50051`, and a probe-path bypass for `/healthz`, `/readyz`, `/livez`. Apply with `kubectl apply -f envoyfilter.yaml`.

```sh
validate-routes translate routes.yaml \
  --target istio \
  --namespace orders \
  --workload-label app=orders-app \
  -o envoyfilter.yaml
```

`--target=istio` requires `--namespace` and ≥1 `--workload-label key=value` (repeatable). `--name` overrides the default `permission-validation-<appId>`. `--probe-paths` (comma-separated) overrides the defaults. `--backend-host`/`--backend-port`/`--admin-host`/`--access-log` are rejected (Istio owns those concerns).

When using the Istio target, run the sidecar with `--routes-file=<path>` so route-level `skipped` / default decisions are made in-process (the EnvoyFilter does not carry per-route ext_proc config).
```

In the same README, after the existing `## Repo layout` section, find the existing run-sidecar block (`./permission-validation \` …) and add this new subsection right after it:

```markdown
### Mesh-injected mode: `--routes-file`

When running under a service mesh (Istio target), pass `--routes-file` so the sidecar parses `routes.yaml` at startup and short-circuits skipped routes plus the default catch-all without invoking PCS:

```sh
./permission-validation \
  --listen 0.0.0.0:50051 \
  --pcs-endpoint http://permission-checking.internal:8080 \
  --routes-file /etc/pv/routes.yaml
```

With `--routes-file` unset, the sidecar behaves identically to Phase 1 (route decisions live in Envoy's per-route filter config).
```

- [ ] **Step 2: Update the onboarding README**

In `permission-validation/examples/onboarding/README.md`, after the existing `## Apply in production` section (which ends with "Reviewers inspect both `routes.yaml` (intent) and `envoy.yaml` (what Envoy will actually load) in the PR."), insert a new section:

```markdown
## Adopt in an Istio-injected pod

If your pod is injected by Istio (you don't own the Envoy bootstrap), use the `--target=istio` render. It produces an `EnvoyFilter` CRD that patches the mesh-owned Envoy with the three things the system needs: a static cluster pointing at the pv sidecar, the `ext_proc` HTTP filter, and a probe-path bypass.

1. Create a `ConfigMap` from `routes.yaml`:
   ```sh
   kubectl create configmap pv-routes --from-file=routes.yaml=./routes.yaml -n <ns>
   ```
2. Add the `permission-validation` container to the app `Deployment` with the ConfigMap mounted at `/etc/pv` and `--routes-file=/etc/pv/routes.yaml` in `args`. The container listens on `127.0.0.1:50051`.
3. Render and apply the `EnvoyFilter`:
   ```sh
   validate-routes translate routes.yaml \
     --target istio \
     --namespace <ns> \
     --workload-label app=<appId> \
     -o envoyfilter.yaml
   kubectl apply -f envoyfilter.yaml
   ```
4. Verify the filter is installed:
   ```sh
   istioctl proxy-config listener <pod> -n <ns> | grep ext_proc
   ```

A committed example render lives at [`envoyfilter.yaml`](envoyfilter.yaml). Regenerate it with the command above.

### Liveness / readiness probes

The `EnvoyFilter` exact-matches `/healthz`, `/readyz`, `/livez` and disables `ext_proc` on those routes so kubelet probes survive sidecar restarts. Override via `--probe-paths` (comma-separated, exact paths). **Probe paths are unauthenticated by design — do not route real traffic through them.**

### Native sidecars (optional)

On Kubernetes 1.28+ with Istio 1.20+ and `PILOT_ENABLE_NATIVE_SIDECARS=true`, both `istio-proxy` and `permission-validation` can run as `initContainers` with `restartPolicy: Always`. Not required for this work.
```

- [ ] **Step 3: Update the e2e README**

In `permission-validation/test/e2e/README.md`, find the "## What the suite covers" section. After the table of tests, add a new paragraph at the bottom (or under a new "## Out of scope" subsection if one does not exist):

```markdown
## Out of scope for the e2e suite

- The Istio target is covered by unit tests in `internal/config/translate_istio_test.go` and CLI tests in `cmd/validate-routes/main_test.go`. There is no in-CI mesh integration; app teams validate their generated EnvoyFilter against their real istiod via `kubectl apply --dry-run=server`.
```

- [ ] **Step 4: Run lint to confirm no whitespace damage**

Run: `cd permission-validation && test -z "$(gofmt -l .)"`
Expected: empty (docs changes don't touch Go files; this is just a safety belt).

- [ ] **Step 5: Commit**

```bash
git add permission-validation/README.md permission-validation/examples/onboarding/README.md permission-validation/test/e2e/README.md
git commit -m "$(cat <<'EOF'
docs: document --target=istio and --routes-file adoption flow

Module README gains a render-target section and a mesh-injected-mode section.
Onboarding README gains an "Adopt in an Istio-injected pod" checklist.
e2e README notes the Istio target is unit-tested only.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Final verification

After Task 8, run from the repo root:

- [ ] `cd permission-validation && go test ./...` — all unit tests pass.
- [ ] `cd permission-validation && go vet ./...` — clean.
- [ ] `cd permission-validation && test -z "$(gofmt -l .)"` — no unformatted files.
- [ ] `cd permission-validation && go build ./...` — both binaries compile.
- [ ] Regenerate the committed example and confirm it is byte-identical to the committed file:

  ```bash
  cd permission-validation && go run ./cmd/validate-routes translate examples/onboarding/routes.yaml \
    --target istio --namespace orders --workload-label app=orders-app \
    | diff - examples/onboarding/envoyfilter.yaml
  ```

  Expected: no output (zero diff).

- [ ] Spot-check the static target still works:

  ```bash
  cd permission-validation && go run ./cmd/validate-routes translate examples/onboarding/routes.yaml \
    --sidecar-host sidecar --sidecar-port 50051 \
    --backend-host orders-app --backend-port 8080 --access-log \
    | diff - examples/onboarding/envoy.yaml
  ```

  Expected: no output (the committed `envoy.yaml` was rendered with these flags).
