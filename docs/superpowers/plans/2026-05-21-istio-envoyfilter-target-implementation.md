# Istio EnvoyFilter Render Target — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `validate-routes translate --target=istio` mode that emits an Istio `EnvoyFilter` CRD from the same `routes.yaml` the static target consumes, plus a matching `--routes-file` flag on the sidecar binary so route decisions move into the sidecar when the EnvoyFilter target is in use. Optionally wire the new generator into the kind-demo Option A so `/healthz` finally honours the skipped list.

**Architecture:** A new internal renderer `internal/config.TranslateIstio(rc, opts)` with its own embedded Go template `envoy-filter.tmpl.yaml`. A new internal package `internal/routes` providing `Compile(rc)` + `Lookup(method, path) → (behavior, matched)` used by the sidecar's `Decide()` orchestrator to short-circuit skipped/default-decision routes before any header parse or PCS call. The CLI gets a `--target={static|istio}` flag and a strict flag/target validation matrix. No xDS server, no new Kubernetes manifests beyond the EnvoyFilter itself, no changes to the existing static target's behaviour.

**Tech Stack:** Go 1.25, `gopkg.in/yaml.v3` (already a dep), `text/template` + `//go:embed` (stdlib), `github.com/stretchr/testify` (already a dep). No new external dependencies.

**Design references** (already approved):

- [`docs/superpowers/specs/2026-05-18-istio-envoyfilter-target-design.md`](../specs/2026-05-18-istio-envoyfilter-target-design.md) — the canonical spec this plan implements. 307 lines; no open questions.
- [`prd/permission-validation/phase-1-topology-decision.md`](../../../prd/permission-validation/phase-1-topology-decision.md) §6 — defers xDS to Phase 2; this work is a stepping stone.
- [`docs/superpowers/specs/2026-05-21-kind-demo-ext-proc-design.md`](../specs/2026-05-21-kind-demo-ext-proc-design.md) §11 — calls out the missing per-route skip in Option A; this plan's Phase 6 fixes it.

**Working directory:** `/Users/joe/ashwini-repos/workspace` on branch `kind-demo-ext_proc` (already 40 commits ahead of `origin/main` with the demos + e2e fixes). All new commits go on this same branch.

---

## File Structure

All paths are relative to the repo root `/Users/joe/ashwini-repos/workspace/`.

**Created by this plan:**

```text
permission-validation/
├── internal/
│   ├── config/
│   │   ├── envoy-filter.tmpl.yaml         # Phase 1 — new Go template
│   │   ├── translate_istio.go             # Phase 1 — TranslateIstio + IstioOptions
│   │   └── translate_istio_test.go        # Phase 1 — translator unit tests
│   └── routes/
│       ├── routes.go                      # Phase 3 — Compile + Lookup
│       └── routes_test.go                 # Phase 3 — matcher unit tests
└── examples/
    └── onboarding/
        └── envoyfilter.yaml               # Phase 5 — committed render of the example invocation
```

**Modified by this plan:**

```text
permission-validation/
├── cmd/
│   ├── validate-routes/
│   │   ├── main.go                        # Phase 2 — new --target flag + rejection matrix
│   │   └── main_test.go                   # Phase 2 — CLI tests for the flag matrix
│   └── permission-validation/
│       ├── main.go                        # Phase 4 — new --routes-file flag
│       └── main_test.go                   # Phase 4 — startup tests for --routes-file
├── internal/
│   └── extproc/
│       ├── handler.go                     # Phase 4 — Decide() short-circuit on route lookup
│       └── handler_test.go                # Phase 4 — three new short-circuit test cases
├── README.md                              # Phase 5 — docs for --target=istio and --routes-file
├── examples/
│   └── onboarding/
│       └── README.md                      # Phase 5 — "Adopt in an Istio-injected pod" section
└── test/
    └── e2e/
        └── README.md                      # Phase 5 — one-line note about Istio coverage scope

kind/
├── demo-ext-proc-istio/
│   ├── templates/
│   │   ├── envoyfilter.yaml               # Phase 6 — replace hand-written content with --target=istio output
│   │   ├── echo-app.yaml                  # Phase 6 — add routes.yaml ConfigMap mount + --routes-file flag
│   │   └── routes-cm.yaml                 # Phase 6 — new ConfigMap holding routes.yaml
│   └── README.md                          # Phase 6 — remove the "/healthz returns 403" caveat
└── setup-istio.sh                         # Phase 6 — generate EnvoyFilter via validate-routes
```

---

## Implementation Decision Log

Decisions made writing this plan that resolve small ambiguities in the spec.

1. **`IstioOptions` is a separate struct from `TranslateOptions`.** Spec §2 says "no shared 'target' field." We honour that — `Translate(rc, TranslateOptions)` stays untouched; `TranslateIstio(rc, IstioOptions)` is a new function. The CLI dispatches based on `--target` before either is called.
2. **Where `:method` and `:path` come from in the sidecar.** Envoy ext_proc emits HTTP/2 pseudo-headers (`:method`, `:path`) in the `request_headers` HeaderMap. `internal/extproc/server.go::flattenHeaders` already lowercases keys, so they arrive as `":method"` and `":path"` in the map the `Decide()` method consumes. The route matcher gets called with `hdrs[":method"]` and `hdrs[":path"]`. Tests stub these as map entries.
3. **`routes.yaml` glob semantics in `internal/routes`.** Reuse the regex compilation from `internal/config/translate.go::globToRegex` — extract it into the new `internal/routes` package so both targets share one definition of what `/api/orders/*` means. The static target keeps calling it via the new path.
4. **Probe paths in the EnvoyFilter are exact-match.** Spec §3.4 mandates exact paths (Envoy `match: { path: "..." }`), not prefix or regex. Otherwise a user route like `/healthzap` would be accidentally bypassed. The defaults are `/healthz,/readyz,/livez`.
5. **CLI flag rejection is hard exit-2, not warning.** Spec §4 calls for "rejected with error" — we exit non-zero with a stderr message naming the offending flag. Silent ignores ship the wrong config to prod.
6. **The sidecar's `--routes-file` is optional and additive.** Default empty preserves Phase 1 behaviour (route decisions stay in Envoy via the static target's per-route config). When set, the sidecar consults its compiled matcher BEFORE any header extraction or PCS call.
7. **Phase 6 (kind-demo integration) is in this plan but optional.** It validates the whole pipeline end-to-end, but isn't strictly required to declare the spec implemented. If something blocks the e2e, Phase 6 can be deferred and the rest still ships.

---

## Phase 1 — `internal/config.TranslateIstio` (the renderer)

### Task 1.1: `IstioOptions` struct + `Validate()` method (TDD)

**Files:**
- Create: `permission-validation/internal/config/translate_istio.go`
- Create: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Write the failing test**

Create `permission-validation/internal/config/translate_istio_test.go`:

```go
package config

import "testing"

func TestIstioOptions_Validate_RequiresNamespace(t *testing.T) {
	opts := IstioOptions{WorkloadLabels: map[string]string{"app": "x"}}
	if err := opts.Validate(); err == nil {
		t.Fatalf("expected error for missing namespace, got nil")
	}
}

func TestIstioOptions_Validate_RequiresOneWorkloadLabel(t *testing.T) {
	opts := IstioOptions{Namespace: "orders"}
	if err := opts.Validate(); err == nil {
		t.Fatalf("expected error for missing workload labels, got nil")
	}
}

func TestIstioOptions_Validate_HappyPath(t *testing.T) {
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	if err := opts.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestIstioOptions )
```
Expected: FAIL with "undefined: IstioOptions" or similar compile error.

- [ ] **Step 3: Write minimal implementation**

Create `permission-validation/internal/config/translate_istio.go`:

```go
package config

import "errors"

// IstioOptions are environment-specific values for the istio render target.
// They are intentionally NOT a superset of TranslateOptions — the two targets
// take disjoint flags and the CLI dispatches between them on --target.
type IstioOptions struct {
	// Namespace is metadata.namespace on the EnvoyFilter. Required.
	Namespace string
	// WorkloadLabels keys spec.workloadSelector.labels. Must have ≥1 entry.
	WorkloadLabels map[string]string
	// Name overrides metadata.name. Empty → permission-validation-<appId>.
	Name string
	// SidecarPort is the gRPC port the static cluster targets at 127.0.0.1.
	// Defaults to 50051 when zero.
	SidecarPort int
	// ProbePaths is the exact-match list of paths that bypass ext_proc at the
	// Envoy level (liveness/readiness probes). Empty → /healthz,/readyz,/livez.
	ProbePaths []string
}

// Validate returns an error if required fields are missing or malformed.
func (o IstioOptions) Validate() error {
	if o.Namespace == "" {
		return errors.New("IstioOptions.Namespace is required")
	}
	if len(o.WorkloadLabels) == 0 {
		return errors.New("IstioOptions.WorkloadLabels must have at least one entry")
	}
	return nil
}
```

- [ ] **Step 4: Run test, verify it passes**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestIstioOptions -v )
```
Expected: 3 tests, all PASS.

- [ ] **Step 5: Commit**

```bash
git add permission-validation/internal/config/translate_istio.go \
        permission-validation/internal/config/translate_istio_test.go
git commit -m "$(cat <<'EOF'
feat(config): IstioOptions struct + Validate() for the istio render target

Disjoint from TranslateOptions per spec §2 — no shared "target" field.
Namespace and ≥1 workload label are required; sidecar port defaults to
50051; probe paths default to /healthz,/readyz,/livez when empty.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.2: Template `envoy-filter.tmpl.yaml` + minimal `TranslateIstio` happy path

**Files:**
- Create: `permission-validation/internal/config/envoy-filter.tmpl.yaml`
- Modify: `permission-validation/internal/config/translate_istio.go`
- Modify: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Write the failing test**

Append to `permission-validation/internal/config/translate_istio_test.go`:

```go
import (
	"testing"
	"strings"

	"gopkg.in/yaml.v3"
)

func TestTranslateIstio_MinimalProducesValidYAML(t *testing.T) {
	rc := &RouteConfig{
		Version:         "v1",
		AppID:           "orders-app",
		DefaultBehavior: "deny",
		Routes:          []RouteRule{{Method: "GET", Path: "/api/orders", Behavior: "protected"}},
	}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}

	b, err := TranslateIstio(rc, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("rendered YAML did not parse: %v\n%s", err, b)
	}
	if got := doc["apiVersion"]; got != "networking.istio.io/v1alpha3" {
		t.Fatalf("apiVersion: got %v, want networking.istio.io/v1alpha3", got)
	}
	if got := doc["kind"]; got != "EnvoyFilter" {
		t.Fatalf("kind: got %v, want EnvoyFilter", got)
	}
	if !strings.Contains(string(b), "namespace: orders") {
		t.Fatalf("expected namespace: orders in output\n%s", b)
	}
}
```

Be sure to merge the new `import` block into any existing one at the top of the file (just `"testing"` from Task 1.1).

- [ ] **Step 2: Run test, verify it fails**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestTranslateIstio_Minimal )
```
Expected: FAIL with "undefined: TranslateIstio".

- [ ] **Step 3: Write the template**

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
{{- range $k, $v := .WorkloadLabels }}
      {{ $k }}: {{ $v }}
{{- end }}
  configPatches:

  # (a) STATIC cluster for the pv sibling container at 127.0.0.1.
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

  # (b) Insert ext_proc before the router in the inbound HCM.
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

  # (c) Probe-path carve-out: disable ext_proc for liveness/readiness paths.
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

- [ ] **Step 4: Implement `TranslateIstio`**

Append to `permission-validation/internal/config/translate_istio.go`:

```go
import (
	_ "embed"
	"bytes"
	"fmt"
	"text/template"
)

//go:embed envoy-filter.tmpl.yaml
var envoyFilterTemplate string

var defaultProbePaths = []string{"/healthz", "/readyz", "/livez"}

// TranslateIstio renders an Istio EnvoyFilter CRD from rc + opts.
// The rendered CRD is independent of the route list — protected/skipped
// decisions migrate into the sidecar (which reads routes.yaml at startup
// via --routes-file). The CRD installs the ext_proc filter, the static
// pv_sidecar cluster, and the probe-path carve-out only.
func TranslateIstio(rc *RouteConfig, opts IstioOptions) ([]byte, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("istio options: %w", err)
	}
	if opts.SidecarPort == 0 {
		opts.SidecarPort = 50051
	}
	if opts.Name == "" {
		opts.Name = "permission-validation-" + rc.AppID
	}
	if len(opts.ProbePaths) == 0 {
		opts.ProbePaths = append([]string(nil), defaultProbePaths...)
	}
	tmpl, err := template.New("envoy-filter").Parse(envoyFilterTemplate)
	if err != nil {
		return nil, fmt.Errorf("istio template parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, opts); err != nil {
		return nil, fmt.Errorf("istio template execute: %w", err)
	}
	return buf.Bytes(), nil
}
```

Merge the imports cleanly: the file now imports `errors`, `bytes`, `embed`, `fmt`, `text/template`.

- [ ] **Step 5: Run test, verify it passes**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestTranslateIstio_Minimal -v )
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add permission-validation/internal/config/envoy-filter.tmpl.yaml \
        permission-validation/internal/config/translate_istio.go \
        permission-validation/internal/config/translate_istio_test.go
git commit -m "$(cat <<'EOF'
feat(config): TranslateIstio + embedded envoy-filter.tmpl.yaml — minimal render

Three configPatches per spec §3.2: CLUSTER ADD (static pv_sidecar at
127.0.0.1:50051), HTTP_FILTER INSERT_BEFORE (ext_proc before router),
VIRTUAL_HOST MERGE (probe-path carve-out). All patches scoped to
context: SIDECAR_INBOUND, failure_mode_allow: false.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.3: Name defaults from `AppID`

**Files:**
- Modify: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Append failing test**

Append to `permission-validation/internal/config/translate_istio_test.go`:

```go
func TestTranslateIstio_NameDefaultsFromAppID(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	b, err := TranslateIstio(rc, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(b), "name: permission-validation-orders-app") {
		t.Fatalf("expected default name 'permission-validation-orders-app'; got:\n%s", b)
	}
}

func TestTranslateIstio_ExplicitNameOverridesDefault(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		Name:           "custom-filter-name",
	}
	b, _ := TranslateIstio(rc, opts)
	if !strings.Contains(string(b), "name: custom-filter-name") {
		t.Fatalf("expected explicit name; got:\n%s", b)
	}
}
```

- [ ] **Step 2: Run, verify both pass (no impl change needed — Task 1.2 already implemented this)**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestTranslateIstio_Name -v )
```
Expected: 2 tests PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/config/translate_istio_test.go
git commit -m "test(config): pin Name default behaviour for istio target

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 1.4: Probe paths — default and override

**Files:**
- Modify: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Append two tests**

Append to `permission-validation/internal/config/translate_istio_test.go`:

```go
func TestTranslateIstio_ProbePathsDefault(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	for _, p := range []string{`path: "/healthz"`, `path: "/readyz"`, `path: "/livez"`} {
		if !strings.Contains(s, p) {
			t.Fatalf("expected default probe-path entry %q; got:\n%s", p, b)
		}
	}
}

func TestTranslateIstio_ProbePathsOverride(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		ProbePaths:     []string{"/health", "/ready"},
	}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	if !strings.Contains(s, `path: "/health"`) || !strings.Contains(s, `path: "/ready"`) {
		t.Fatalf("expected /health and /ready probe paths; got:\n%s", b)
	}
	// Defaults must NOT appear when override given.
	for _, p := range []string{`path: "/healthz"`, `path: "/readyz"`, `path: "/livez"`} {
		if strings.Contains(s, p) {
			t.Fatalf("default probe path %q must not appear when override is given; got:\n%s", p, b)
		}
	}
}
```

- [ ] **Step 2: Run, verify both pass**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestTranslateIstio_ProbePaths -v )
```
Expected: 2 PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/config/translate_istio_test.go
git commit -m "test(config): pin probe-path default + override behaviour

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 1.5: Workload-label rendering (multi-label) + routes-not-in-output

**Files:**
- Modify: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Append two tests**

Append to `permission-validation/internal/config/translate_istio_test.go`:

```go
func TestTranslateIstio_WorkloadLabelsRendered(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{
		Namespace:      "ns",
		WorkloadLabels: map[string]string{"app": "x", "tier": "api"},
	}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	if !strings.Contains(s, "app: x") || !strings.Contains(s, "tier: api") {
		t.Fatalf("expected both labels rendered; got:\n%s", b)
	}
}

func TestTranslateIstio_RoutesNotInOutput(t *testing.T) {
	rc := &RouteConfig{
		Version: "v1", AppID: "orders-app", DefaultBehavior: "deny",
		Routes: []RouteRule{
			{Method: "GET", Path: "/api/orders/secret", Behavior: "protected"},
			{Method: "POST", Path: "/api/orders/admin", Behavior: "skipped"},
		},
	}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	// Routes from routes.yaml MUST NOT appear in the EnvoyFilter — they move into
	// the sidecar via --routes-file. Probe paths are the only routes in the CRD.
	if strings.Contains(s, "/api/orders/secret") || strings.Contains(s, "/api/orders/admin") {
		t.Fatalf("routes.yaml paths must not appear in the EnvoyFilter; got:\n%s", b)
	}
}

func TestTranslateIstio_ContextIsSidecarInbound(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "ns", WorkloadLabels: map[string]string{"app": "x"}}
	b, _ := TranslateIstio(rc, opts)
	// Every configPatches[].match.context MUST be SIDECAR_INBOUND.
	// We assert via simple string count: 3 patches × 1 context line each = 3.
	got := strings.Count(string(b), "context: SIDECAR_INBOUND")
	if got != 3 {
		t.Fatalf("expected 3 SIDECAR_INBOUND contexts; got %d in:\n%s", got, b)
	}
}
```

- [ ] **Step 2: Run, verify all pass**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestTranslateIstio -v )
```
Expected: all TranslateIstio tests PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/config/translate_istio_test.go
git commit -m "test(config): pin multi-label, routes-not-in-output, context safety

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 1.6: Negative tests — IstioOptions.Validate rejection paths

**Files:**
- Modify: `permission-validation/internal/config/translate_istio_test.go`

- [ ] **Step 1: Append two tests**

Append to `permission-validation/internal/config/translate_istio_test.go`:

```go
func TestTranslateIstio_RejectsEmptyNamespace(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{WorkloadLabels: map[string]string{"app": "x"}}
	if _, err := TranslateIstio(rc, opts); err == nil {
		t.Fatalf("expected error for empty Namespace")
	}
}

func TestTranslateIstio_RejectsEmptyWorkloadLabels(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "ns"}
	if _, err := TranslateIstio(rc, opts); err == nil {
		t.Fatalf("expected error for empty WorkloadLabels")
	}
}
```

- [ ] **Step 2: Run, verify both pass**

Run:
```bash
( cd permission-validation && go test ./internal/config/ -run TestTranslateIstio_Rejects -v )
```
Expected: 2 PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/config/translate_istio_test.go
git commit -m "test(config): pin error paths for empty namespace + empty labels

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 2 — CLI `--target=istio`

### Task 2.1: Add `--target` flag and dispatch boilerplate

**Files:**
- Modify: `permission-validation/cmd/validate-routes/main.go`

- [ ] **Step 1: Refactor `runTranslate` to dispatch on `--target`**

Replace `runTranslate` in `permission-validation/cmd/validate-routes/main.go` (currently lines 53-117) with:

```go
func runTranslate(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr,
			"usage: validate-routes translate <file> [--target=static|istio] [target-specific flags]")
		return 2
	}
	// First positional is the route-config file; flags follow it.
	file := args[0]
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output file (defaults to stdout)")
	target := fs.String("target", "static", "render target: static | istio")

	// Static-target flags. Default values stay backwards-compatible with
	// the Phase 1 invocation; runTranslate validates the matrix below.
	sidecarHost := fs.String("sidecar-host", "127.0.0.1", "sidecar gRPC host Envoy will dial (static only)")
	sidecarPort := fs.Int("sidecar-port", 50051, "sidecar gRPC port (both targets)")
	backendHost := fs.String("backend-host", "127.0.0.1", "application backend host (static only)")
	backendPort := fs.Int("backend-port", 8080, "application backend port (static only)")
	adminHost := fs.String("admin-host", "127.0.0.1", "Envoy admin bind (static only)")
	accessLog := fs.Bool("access-log", false, "emit Envoy access logs (static only)")

	// Istio-target flags.
	namespace := fs.String("namespace", "", "metadata.namespace on the EnvoyFilter (istio only, required)")
	var workloadLabels stringMap
	fs.Var(&workloadLabels, "workload-label", "repeatable key=value pairs for workloadSelector (istio only, ≥1 required)")
	filterName := fs.String("name", "", "EnvoyFilter metadata.name (istio only; defaults to permission-validation-<appId>)")
	probePaths := fs.String("probe-paths", "", "comma-separated exact paths to bypass ext_proc (istio only; defaults to /healthz,/readyz,/livez)")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !validPort(*sidecarPort) {
		fmt.Fprintf(stderr, "sidecar-port must be in range 1..65535 (got %d)\n", *sidecarPort)
		return 2
	}

	switch *target {
	case "static":
		// Reject istio-only flags.
		if *namespace != "" {
			fmt.Fprintln(stderr, "--namespace is only valid with --target=istio")
			return 2
		}
		if len(workloadLabels) > 0 {
			fmt.Fprintln(stderr, "--workload-label is only valid with --target=istio")
			return 2
		}
		if *filterName != "" {
			fmt.Fprintln(stderr, "--name is only valid with --target=istio")
			return 2
		}
		if *probePaths != "" {
			fmt.Fprintln(stderr, "--probe-paths is only valid with --target=istio")
			return 2
		}
		return runTranslateStatic(file, *out, *sidecarHost, *sidecarPort, *backendHost, *backendPort, *adminHost, *accessLog, stdout, stderr)
	case "istio":
		// Reject static-only flags (the ones a user explicitly set; we detect via fs.Visit).
		var rejected []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "backend-host", "backend-port", "admin-host", "access-log":
				rejected = append(rejected, "--"+f.Name)
			case "sidecar-host":
				rejected = append(rejected, "--sidecar-host (use --workload-label / pod-local 127.0.0.1 in istio mode)")
			}
		})
		if len(rejected) > 0 {
			fmt.Fprintf(stderr, "the following flags are not valid with --target=istio: %v\n", rejected)
			return 2
		}
		return runTranslateIstio(file, *out, *namespace, workloadLabels, *filterName, *probePaths, *sidecarPort, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "--target must be one of: static, istio (got %q)\n", *target)
		return 2
	}
}

// stringMap is a repeatable flag.Value that accumulates key=value pairs.
type stringMap map[string]string

func (m *stringMap) String() string {
	if m == nil || *m == nil {
		return ""
	}
	parts := make([]string, 0, len(*m))
	for k, v := range *m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (m *stringMap) Set(s string) error {
	i := strings.IndexByte(s, '=')
	if i <= 0 || i == len(s)-1 {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[s[:i]] = s[i+1:]
	return nil
}

func runTranslateStatic(file, out, sidecarHost string, sidecarPort int, backendHost string, backendPort int, adminHost string, accessLog bool, stdout, stderr io.Writer) int {
	if !validPort(backendPort) {
		fmt.Fprintf(stderr, "backend-port must be in range 1..65535 (got %d)\n", backendPort)
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
	b, err := config.Translate(rc, config.TranslateOptions{
		SidecarHost: sidecarHost, SidecarPort: sidecarPort,
		AppBackendHost: backendHost, AppBackendPort: backendPort,
		AdminHost: adminHost, AccessLogStdout: accessLog,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOut(out, b, stdout, stderr)
}

func runTranslateIstio(file, out, namespace string, workloadLabels stringMap, name, probePathsCSV string, sidecarPort int, stdout, stderr io.Writer) int {
	if namespace == "" {
		fmt.Fprintln(stderr, "--namespace is required with --target=istio")
		return 2
	}
	if len(workloadLabels) == 0 {
		fmt.Fprintln(stderr, "--workload-label is required with --target=istio (at least one)")
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
	opts := config.IstioOptions{
		Namespace:      namespace,
		WorkloadLabels: workloadLabels,
		Name:           name,
		SidecarPort:    sidecarPort,
	}
	if probePathsCSV != "" {
		opts.ProbePaths = strings.Split(probePathsCSV, ",")
	}
	b, err := config.TranslateIstio(rc, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOut(out, b, stdout, stderr)
}

func writeOut(out string, b []byte, stdout, stderr io.Writer) int {
	if out == "" {
		n, err := stdout.Write(b)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if n != len(b) {
			fmt.Fprintf(stderr, "short write: wrote %d of %d bytes\n", n, len(b))
			return 1
		}
		return 0
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
```

Add `"strings"` to the import block at the top.

- [ ] **Step 2: Run the existing CLI tests + build to confirm nothing existing is broken**

Run:
```bash
( cd permission-validation && go vet ./... && go test ./cmd/validate-routes/ -v && go build ./... )
```
Expected: vet clean, all existing CLI tests PASS, binary builds.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/cmd/validate-routes/main.go
git commit -m "$(cat <<'EOF'
feat(cli): --target=static|istio flag + dispatch + flag rejection matrix

Static target is unchanged in behaviour. Istio target requires
--namespace and ≥1 --workload-label. Cross-target flags reject hard
(exit 2) with a stderr message naming the offending flag.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.2: CLI test — happy path for `--target=istio`

**Files:**
- Modify: `permission-validation/cmd/validate-routes/main_test.go`

- [ ] **Step 1: Inspect existing test setup**

Run:
```bash
head -40 permission-validation/cmd/validate-routes/main_test.go
```
Note the existing pattern: tests invoke `run(...)` with `args` slices and assert on exit code + writer contents.

- [ ] **Step 2: Append the happy-path test**

Append to `permission-validation/cmd/validate-routes/main_test.go`:

```go
func TestTranslate_TargetIstio_WritesFile(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "envoyfilter.yaml")
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"translate", "../../testdata/routes/valid-minimal.yaml",
		"--target=istio",
		"--namespace=orders",
		"--workload-label=app=orders-app",
		"-o", out,
	}, &bytes.Buffer{}, &stderr)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("kind: EnvoyFilter")) {
		t.Fatalf("expected EnvoyFilter output; got:\n%s", b)
	}
	if !bytes.Contains(b, []byte("namespace: orders")) {
		t.Fatalf("expected namespace: orders; got:\n%s", b)
	}
}
```

Be sure to merge the new imports (`bytes`, `context`, `os`, `path/filepath`) into any existing import block.

- [ ] **Step 3: Run, verify it passes**

Run:
```bash
( cd permission-validation && go test ./cmd/validate-routes/ -run TestTranslate_TargetIstio_Writes -v )
```
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add permission-validation/cmd/validate-routes/main_test.go
git commit -m "test(cli): --target=istio happy path writes a valid EnvoyFilter file

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2.3: CLI tests — required flag rejection (istio target)

**Files:**
- Modify: `permission-validation/cmd/validate-routes/main_test.go`

- [ ] **Step 1: Append two tests**

Append to `permission-validation/cmd/validate-routes/main_test.go`:

```go
func TestTranslate_TargetIstio_RequiresNamespace(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"translate", "../../testdata/routes/valid-minimal.yaml",
		"--target=istio",
		"--workload-label=app=x",
	}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "namespace") {
		t.Fatalf("stderr should mention 'namespace'; got: %s", stderr.String())
	}
}

func TestTranslate_TargetIstio_RequiresWorkloadLabel(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"translate", "../../testdata/routes/valid-minimal.yaml",
		"--target=istio",
		"--namespace=orders",
	}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "workload-label") {
		t.Fatalf("stderr should mention 'workload-label'; got: %s", stderr.String())
	}
}
```

Merge `"strings"` into the imports if not already present.

- [ ] **Step 2: Run, verify both pass**

Run:
```bash
( cd permission-validation && go test ./cmd/validate-routes/ -run TestTranslate_TargetIstio_Requires -v )
```
Expected: 2 PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/cmd/validate-routes/main_test.go
git commit -m "test(cli): --target=istio requires --namespace and --workload-label

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 2.4: CLI tests — cross-target flag rejection

**Files:**
- Modify: `permission-validation/cmd/validate-routes/main_test.go`

- [ ] **Step 1: Append a table-driven test**

Append to `permission-validation/cmd/validate-routes/main_test.go`:

```go
func TestTranslate_FlagMatrixRejections(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		stderrIn string // substring expected in stderr
	}{
		{
			name: "istio_rejects_backend_host",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--target=istio", "--namespace=n", "--workload-label=app=x",
				"--backend-host=app",
			},
			stderrIn: "backend-host",
		},
		{
			name: "istio_rejects_admin_host",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--target=istio", "--namespace=n", "--workload-label=app=x",
				"--admin-host=0.0.0.0",
			},
			stderrIn: "admin-host",
		},
		{
			name: "istio_rejects_access_log",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--target=istio", "--namespace=n", "--workload-label=app=x",
				"--access-log",
			},
			stderrIn: "access-log",
		},
		{
			name: "static_rejects_namespace",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--namespace=oops",
			},
			stderrIn: "namespace",
		},
		{
			name: "static_rejects_workload_label",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--workload-label=app=x",
			},
			stderrIn: "workload-label",
		},
		{
			name: "static_rejects_probe_paths",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--probe-paths=/health",
			},
			stderrIn: "probe-paths",
		},
		{
			name: "invalid_target_value",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml",
				"--target=nginx",
			},
			stderrIn: "target must be one of",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, &bytes.Buffer{}, &stderr)
			if code != 2 {
				t.Fatalf("exit: got %d, want 2; stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), tc.stderrIn) {
				t.Fatalf("stderr missing %q; got: %s", tc.stderrIn, stderr.String())
			}
		})
	}
}
```

- [ ] **Step 2: Run, verify all pass**

Run:
```bash
( cd permission-validation && go test ./cmd/validate-routes/ -run TestTranslate_FlagMatrix -v )
```
Expected: 7 sub-tests PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/cmd/validate-routes/main_test.go
git commit -m "test(cli): table-driven flag matrix rejection for both targets

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 3 — `internal/routes` matcher package

### Task 3.1: Skeleton + first matcher test

**Files:**
- Create: `permission-validation/internal/routes/routes.go`
- Create: `permission-validation/internal/routes/routes_test.go`

- [ ] **Step 1: Write the failing test**

Create `permission-validation/internal/routes/routes_test.go`:

```go
package routes

import (
	"testing"

	"permission-validation/internal/config"
)

func TestLookup_ExactPathExactMethod_Match(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/api/orders", Behavior: "protected"},
		},
	}
	tbl, err := Compile(rc)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	behavior, matched := tbl.Lookup("GET", "/api/orders")
	if !matched || behavior != "protected" {
		t.Fatalf("got (%q, matched=%v); want (protected, true)", behavior, matched)
	}
}
```

- [ ] **Step 2: Run, verify it fails**

Run:
```bash
( cd permission-validation && go test ./internal/routes/ )
```
Expected: FAIL with "no Go files in ..." or "undefined: Compile".

- [ ] **Step 3: Write minimal implementation**

Create `permission-validation/internal/routes/routes.go`:

```go
package routes

import (
	"fmt"
	"regexp"
	"strings"

	"permission-validation/internal/config"
)

// Table is the compiled, ordered route table for a single RouteConfig.
// Lookup tries rules in file order; first match wins. On no match the
// default behaviour from RouteConfig.DefaultBehavior applies.
type Table struct {
	rules           []compiledRule
	defaultBehavior string
}

type compiledRule struct {
	method   string         // "GET", "POST", "*" — method matcher
	path     *regexp.Regexp // compiled from globToRegex
	behavior string         // "protected" or "skipped"
}

// Compile turns rc into a Table. Returns an error if any rule fails to compile.
func Compile(rc *config.RouteConfig) (*Table, error) {
	t := &Table{defaultBehavior: rc.DefaultBehavior}
	for i, r := range rc.Routes {
		re, err := regexp.Compile(globToRegex(r.Path))
		if err != nil {
			return nil, fmt.Errorf("routes[%d]: compile path %q: %w", i, r.Path, err)
		}
		t.rules = append(t.rules, compiledRule{
			method:   strings.ToUpper(r.Method),
			path:     re,
			behavior: r.Behavior,
		})
	}
	return t, nil
}

// Lookup returns the matched rule's behavior and matched=true if a rule
// matched, or (defaultBehavior, false) if none did.
func (t *Table) Lookup(method, path string) (behavior string, matched bool) {
	method = strings.ToUpper(method)
	for _, r := range t.rules {
		if r.method != "*" && r.method != method {
			continue
		}
		if r.path.MatchString(path) {
			return r.behavior, true
		}
	}
	return t.defaultBehavior, false
}

// globToRegex is a copy of the regex compiler from internal/config/translate.go.
// We duplicate it here intentionally to keep the routes package free of
// translate.go's other concerns (Envoy template variables, etc.). If the
// glob semantics diverge later, that's a deliberate decision visible in diff.
func globToRegex(p string) string {
	if p == "" {
		return "^$"
	}
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(p) {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case p[i] == '*':
			b.WriteString("[^/]*")
			i++
		case p[i] == '.', p[i] == '+', p[i] == '(', p[i] == ')', p[i] == '|',
			p[i] == '[', p[i] == ']', p[i] == '{', p[i] == '}', p[i] == '^',
			p[i] == '$', p[i] == '?', p[i] == '\\':
			b.WriteByte('\\')
			b.WriteByte(p[i])
			i++
		default:
			b.WriteByte(p[i])
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}
```

- [ ] **Step 4: Run, verify it passes**

Run:
```bash
( cd permission-validation && go test ./internal/routes/ -v )
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add permission-validation/internal/routes/routes.go \
        permission-validation/internal/routes/routes_test.go
git commit -m "$(cat <<'EOF'
feat(routes): new internal/routes package — Compile + Lookup

Used by the sidecar's Decide() orchestrator to short-circuit skipped
routes and the default-behavior catch-all before any header parse or
PCS call. Uses the same glob-to-regex semantics as the static target's
translator; duplicated locally so this package has no template/envoy
dependencies.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3.2: Path matching — exact / single-star / double-star

**Files:**
- Modify: `permission-validation/internal/routes/routes_test.go`

- [ ] **Step 1: Append table-driven tests**

Append to `permission-validation/internal/routes/routes_test.go`:

```go
func TestLookup_PathPatterns(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/exact", Behavior: "protected"},
			{Method: "GET", Path: "/wild/*", Behavior: "protected"},
			{Method: "GET", Path: "/deep/**", Behavior: "protected"},
		},
	}
	tbl, err := Compile(rc)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		path        string
		wantMatched bool
	}{
		{"/exact", true},
		{"/exactsuffix", false}, // exact must not prefix-match
		{"/wild/foo", true},
		{"/wild/foo/bar", false}, // single * does not cross slash
		{"/deep/anything/here", true},
		{"/unrelated", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			_, matched := tbl.Lookup("GET", c.path)
			if matched != c.wantMatched {
				t.Fatalf("path=%s: matched=%v, want %v", c.path, matched, c.wantMatched)
			}
		})
	}
}
```

- [ ] **Step 2: Run, verify all pass**

Run:
```bash
( cd permission-validation && go test ./internal/routes/ -run TestLookup_PathPatterns -v )
```
Expected: 6 sub-tests PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/routes/routes_test.go
git commit -m "test(routes): pin path-pattern matching — exact, *, **

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.3: Method matching — exact and wildcard

**Files:**
- Modify: `permission-validation/internal/routes/routes_test.go`

- [ ] **Step 1: Append tests**

Append to `permission-validation/internal/routes/routes_test.go`:

```go
func TestLookup_MethodMatching(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/get-only", Behavior: "protected"},
			{Method: "*", Path: "/any-method", Behavior: "protected"},
		},
	}
	tbl, _ := Compile(rc)

	if _, m := tbl.Lookup("GET", "/get-only"); !m {
		t.Fatalf("GET /get-only should match")
	}
	if _, m := tbl.Lookup("POST", "/get-only"); m {
		t.Fatalf("POST /get-only should NOT match a GET-only rule")
	}
	if _, m := tbl.Lookup("DELETE", "/any-method"); !m {
		t.Fatalf("DELETE /any-method should match a wildcard-method rule")
	}
	// Case-insensitive method match.
	if _, m := tbl.Lookup("get", "/get-only"); !m {
		t.Fatalf("lowercase 'get' should match GET rule")
	}
}
```

- [ ] **Step 2: Run, verify all pass**

Run:
```bash
( cd permission-validation && go test ./internal/routes/ -run TestLookup_MethodMatching -v )
```
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/routes/routes_test.go
git commit -m "test(routes): method matching — exact, *, case-insensitive

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.4: First-match-wins ordering + default fallback

**Files:**
- Modify: `permission-validation/internal/routes/routes_test.go`

- [ ] **Step 1: Append tests**

Append to `permission-validation/internal/routes/routes_test.go`:

```go
func TestLookup_FirstMatchWins(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/api/orders/admin", Behavior: "skipped"},
			{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
		},
	}
	tbl, _ := Compile(rc)

	if b, m := tbl.Lookup("GET", "/api/orders/admin"); !m || b != "skipped" {
		t.Fatalf("expected skipped on first-match-wins; got (%s, matched=%v)", b, m)
	}
	if b, m := tbl.Lookup("GET", "/api/orders/other"); !m || b != "protected" {
		t.Fatalf("expected protected for non-admin under /api/orders/*; got (%s, matched=%v)", b, m)
	}
}

func TestLookup_DefaultDeny(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes:  []config.RouteRule{{Method: "GET", Path: "/protected", Behavior: "protected"}},
	}
	tbl, _ := Compile(rc)
	b, m := tbl.Lookup("GET", "/nothing-matches")
	if m {
		t.Fatalf("expected no match")
	}
	if b != "deny" {
		t.Fatalf("expected default deny; got %q", b)
	}
}

func TestLookup_DefaultSkipped(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "skipped",
		Routes:  []config.RouteRule{{Method: "GET", Path: "/protected", Behavior: "protected"}},
	}
	tbl, _ := Compile(rc)
	b, m := tbl.Lookup("GET", "/nothing-matches")
	if m {
		t.Fatalf("expected no match")
	}
	if b != "skipped" {
		t.Fatalf("expected default skipped; got %q", b)
	}
}
```

- [ ] **Step 2: Run, verify all pass**

Run:
```bash
( cd permission-validation && go test ./internal/routes/ -v )
```
Expected: all `internal/routes` tests PASS.

- [ ] **Step 3: Commit**

```bash
git add permission-validation/internal/routes/routes_test.go
git commit -m "test(routes): first-match-wins ordering + default-deny/skipped fallback

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 4 — Sidecar `--routes-file` integration

### Task 4.1: Add `--routes-file` flag (no-op behaviour when empty)

**Files:**
- Modify: `permission-validation/cmd/permission-validation/main.go`

- [ ] **Step 1: Add the flag and a parsed Table to the run() function**

Modify `run()` in `permission-validation/cmd/permission-validation/main.go`. Right after the existing `otelDisabled` flag declaration (currently around line 36), add:

```go
	routesFile := fs.String("routes-file", "", "optional path to routes.yaml; when set, the sidecar short-circuits skipped routes and the default-behavior catch-all before any header parse or PCS call")
```

Then, after `fs.Parse(args)` completes and before the meter provider is built, add:

```go
	var routeTable *routes.Table
	if *routesFile != "" {
		b, err := os.ReadFile(*routesFile)
		if err != nil {
			fmt.Fprintln(stderr, "read routes file:", err)
			return 1
		}
		rc, err := config.Parse(b)
		if err != nil {
			fmt.Fprintln(stderr, "parse routes file:", err)
			return 1
		}
		if errs := config.Validate(rc); len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintln(stderr, e)
			}
			return 1
		}
		routeTable, err = routes.Compile(rc)
		if err != nil {
			fmt.Fprintln(stderr, "compile routes:", err)
			return 1
		}
	}
```

Update the imports — add `"permission-validation/internal/config"` and `"permission-validation/internal/routes"`.

Finally, update the call site that creates the handler so the table can be passed in. Replace:

```go
h := extproc.New(pcsClient, m)
```

with:

```go
h := extproc.New(pcsClient, m, routeTable)
```

(`extproc.New` signature is updated in Task 4.3 — Task 4.1's compile will fail there until then, which is intentional TDD red phase. We commit the wiring now so the next task's failure is purely on extproc.)

- [ ] **Step 2: Verify failure**

Run:
```bash
( cd permission-validation && go build ./... )
```
Expected: FAIL with "too many arguments in call to extproc.New" — we'll fix in Task 4.3.

- [ ] **Step 3: Stash the change before commit (it's the wiring half of a two-task TDD pair)**

Don't commit yet — Task 4.3 finalizes the handler signature; we'll commit both together. Confirm changes are staged for review:

```bash
git diff permission-validation/cmd/permission-validation/main.go | head -30
```

Move to Task 4.2 with the change in your working tree (uncommitted).

---

### Task 4.2: Sidecar startup error tests — invalid routes file

**Files:**
- Create or modify: `permission-validation/cmd/permission-validation/main_test.go`

- [ ] **Step 1: Inspect existing main_test.go (if any)**

Run:
```bash
ls permission-validation/cmd/permission-validation/main_test.go 2>/dev/null && head -20 permission-validation/cmd/permission-validation/main_test.go || echo "no test file yet"
```

- [ ] **Step 2: Append (or create) startup-failure tests**

Append the following to `permission-validation/cmd/permission-validation/main_test.go` (or create a new file with this content + the package declaration at top):

```go
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun_RoutesFile_MissingFile_ExitsNonZero(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--listen=127.0.0.1:0",
		"--routes-file=/nonexistent/routes.yaml",
		"--otel-disabled",
	}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "read routes file") {
		t.Fatalf("stderr should mention 'read routes file'; got: %s", stderr.String())
	}
}

func TestRun_RoutesFile_InvalidYAML_ExitsNonZero(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "routes.yaml")
	if err := os.WriteFile(bad, []byte("not: valid: yaml: { ["), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--listen=127.0.0.1:0",
		"--routes-file=" + bad,
		"--otel-disabled",
	}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0; stderr=%s", stderr.String())
	}
}
```

- [ ] **Step 3: Run, expect compile failure (extproc.New signature still wrong)**

Run:
```bash
( cd permission-validation && go test ./cmd/permission-validation/ )
```
Expected: compile error on extproc.New. Task 4.3 fixes it.

---

### Task 4.3: Update `extproc.Handler` to consult the route table

**Files:**
- Modify: `permission-validation/internal/extproc/handler.go`
- Modify: `permission-validation/internal/extproc/handler_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `permission-validation/internal/extproc/handler_test.go`:

```go
func TestDecide_SkippedRoute_AllowsWithoutPCS(t *testing.T) {
	mockPCS := &mockPCS{}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{{Method: "GET", Path: "/healthz", Behavior: "skipped"}},
	}
	tbl, _ := routes.Compile(rc)
	h := New(mockPCS, m, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/healthz",
	})
	if out.Kind != OutcomeAllow {
		t.Fatalf("expected allow on skipped route; got kind=%v", out.Kind)
	}
	if mockPCS.calls != 0 {
		t.Fatalf("PCS must not be called for skipped routes; got %d calls", mockPCS.calls)
	}
}

func TestDecide_DefaultDenyNoMatch_DeniesWithoutPCS(t *testing.T) {
	mockPCS := &mockPCS{}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes:  []config.RouteRule{{Method: "GET", Path: "/protected", Behavior: "protected"}},
	}
	tbl, _ := routes.Compile(rc)
	h := New(mockPCS, m, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/nothing-matches",
	})
	if out.Kind != OutcomeDeny {
		t.Fatalf("expected deny on default-deny catch-all; got kind=%v", out.Kind)
	}
	if mockPCS.calls != 0 {
		t.Fatalf("PCS must not be called for default-deny no-match; got %d calls", mockPCS.calls)
	}
}

func TestDecide_NilRouteTable_PreservesPhase1Behavior(t *testing.T) {
	// When route table is nil (--routes-file unset), the handler must behave
	// exactly as Phase 1 did — go straight to header parse + PCS, with no
	// route lookup whatsoever.
	mockPCS := &mockPCS{nextDecision: pcs.DecisionAllow}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	h := New(mockPCS, m, nil)

	out := h.Decide(context.Background(), map[string]string{
		":method":        "GET",
		":path":          "/whatever",
		"authorization":  "Bearer sometoken",
		"x-auth-context": "doc-1:document:read",
	})
	if out.Kind != OutcomeAllow {
		t.Fatalf("phase-1 path expected; got kind=%v reason=%s", out.Kind, out.Reason)
	}
	if mockPCS.calls != 1 {
		t.Fatalf("phase-1 path should call PCS exactly once; got %d", mockPCS.calls)
	}
}
```

Merge the new imports cleanly. The `mockPCS` type may already exist in the test file — adapt to its existing shape (add a `calls int` field and `nextDecision pcs.Decision` field if missing). If `mockPCS` does not exist yet, define it at the top of the file:

```go
type mockPCS struct {
	calls        int
	nextDecision pcs.Decision
}

func (m *mockPCS) Check(_ context.Context, _ pcs.CheckRequest) (pcs.Decision, error) {
	m.calls++
	return m.nextDecision, nil
}
```

The required imports are: `"context"`, `"testing"`, `"permission-validation/internal/config"`, `"permission-validation/internal/routes"`, `"permission-validation/internal/pcs"`, `"permission-validation/internal/metrics"`, `"go.opentelemetry.io/otel/sdk/metric"`.

- [ ] **Step 2: Run, verify they fail**

Run:
```bash
( cd permission-validation && go test ./internal/extproc/ -run TestDecide_SkippedRoute -v )
```
Expected: FAIL (handler signature still old, or Decide doesn't short-circuit yet).

- [ ] **Step 3: Update `Handler` to accept a `*routes.Table` and short-circuit on lookup**

In `permission-validation/internal/extproc/handler.go`, modify:

1. Add import: `"permission-validation/internal/routes"`
2. Add field to `Handler`:
   ```go
   type Handler struct {
       pcs    PCS
       m      *metrics.Metrics
       routes *routes.Table   // nil = Phase 1 behavior (no short-circuit)
   }
   ```
3. Update `New`:
   ```go
   func New(p PCS, m *metrics.Metrics, t *routes.Table) *Handler {
       return &Handler{pcs: p, m: m, routes: t}
   }
   ```
4. At the top of `Decide()` (immediately after the nil-check guard), add:
   ```go
   if h.routes != nil {
       method := hdrs[":method"]
       path := hdrs[":path"]
       behavior, _ := h.routes.Lookup(method, path)
       switch behavior {
       case "skipped":
           h.m.Decision(ctx, "allow")
           return Outcome{Kind: OutcomeAllow}
       case "deny":
           // Either an explicit deny rule (none in current schema; reserved)
           // OR default-deny catch-all from a no-match. Both mean: do not
           // call PCS, do not parse headers — reject immediately.
           h.m.Decision(ctx, "deny")
           return Outcome{Kind: OutcomeDeny}
       }
       // "protected" (matched rule or default-protected) → fall through to
       // existing header parse + PCS path.
   }
   ```

- [ ] **Step 4: Run, verify all extproc tests pass**

Run:
```bash
( cd permission-validation && go test ./internal/extproc/ -v )
```
Expected: all PASS, including the three new ones AND the existing Phase 1 tests.

- [ ] **Step 5: Now build + run the sidecar test from Task 4.2 to verify the full wiring**

Run:
```bash
( cd permission-validation && go vet ./... && go test ./... && go build ./... )
```
Expected: vet clean, all tests pass, all binaries build.

- [ ] **Step 6: Commit Tasks 4.1 + 4.2 + 4.3 together**

```bash
git add permission-validation/cmd/permission-validation/main.go \
        permission-validation/cmd/permission-validation/main_test.go \
        permission-validation/internal/extproc/handler.go \
        permission-validation/internal/extproc/handler_test.go
git commit -m "$(cat <<'EOF'
feat(sidecar): --routes-file + Decide() short-circuit for skipped/default-deny

When started with --routes-file <path>, the sidecar parses the file
via internal/config (fail-fast on error) and compiles a routes.Table.
At request time, before any header parse or PCS call, Decide()
consults the table:

  - matched skipped route → ALLOW, return
  - default-deny no-match → DENY, return (no PCS)
  - matched protected route → existing Phase 1 path (header parse, PCS)

Without --routes-file the handler behaves identically to Phase 1
(nil table = no short-circuit). Three new handler tests pin all
three paths; existing tests still pass.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4.4: Docker-compose wire-level test of `--routes-file`

**Why:** the unit tests in Task 4.3 hit `Handler.Decide` directly with stubbed headers. This task exercises the same code through a real gRPC ext_proc stream against a real sidecar binary started with `--routes-file`, with the existing `fake-pcs` recording call counts so we can assert "PCS was NOT called for skipped/default-deny." This is the lowest-cost full-stack verification of the new sidecar behaviour — no kind, no Istio, all docker-compose.

**Files:**
- Modify: `permission-validation/test/e2e/docker-compose.yaml`
- Modify: `permission-validation/test/e2e/e2e_test.go`

- [ ] **Step 1: Add a `sidecar-with-routes` service to docker-compose**

Append to `permission-validation/test/e2e/docker-compose.yaml`:

```yaml
  # Second sidecar instance, exercised by direct gRPC ext_proc client (no
  # Envoy in front) to verify --routes-file's short-circuit behaviour
  # without per-route Envoy filter config getting in the way.
  sidecar-with-routes:
    build: { context: ../.., dockerfile: test/e2e/Dockerfile.sidecar }
    command:
      - "--listen=0.0.0.0:50051"
      - "--pcs-endpoint=http://fake-pcs:9000"
      - "--pcs-timeout=250ms"
      - "--routes-file=/test-routes.yaml"
      - "--otel-disabled"
    volumes:
      - ./routes.yaml:/test-routes.yaml:ro
    depends_on: [fake-pcs]
    ports: ["50052:50051"]
```

The host port `50052:50051` is deliberately different from the existing `sidecar` service's `50051:50051` so both can run side-by-side and the tests can pick which to dial.

- [ ] **Step 2: Write the failing test**

Append to `permission-validation/test/e2e/e2e_test.go`:

```go
// extprocClient is a tiny helper that opens a gRPC Process stream to a
// permission-validation sidecar, sends a single RequestHeaders message,
// reads the first ProcessingResponse, and closes. Returns the outcome
// kind (extracted from the response shape) for assertions.
func extprocClient(t *testing.T, addr string, hdrs map[string]string) (status int, immediate bool) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	client := ext_proc_v3.NewExternalProcessorClient(conn)
	stream, err := client.Process(context.Background())
	if err != nil {
		t.Fatalf("open stream: %v", err)
	}
	hm := &core_v3.HeaderMap{}
	for k, v := range hdrs {
		hm.Headers = append(hm.Headers, &core_v3.HeaderValue{Key: k, RawValue: []byte(v)})
	}
	if err := stream.Send(&ext_proc_v3.ProcessingRequest{
		Request: &ext_proc_v3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HttpHeaders{Headers: hm},
		},
	}); err != nil {
		t.Fatalf("send: %v", err)
	}
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("recv: %v", err)
	}
	if ir, ok := resp.Response.(*ext_proc_v3.ProcessingResponse_ImmediateResponse); ok {
		return int(ir.ImmediateResponse.Status.Code), true
	}
	return 200, false // CONTINUE = allow path
}

func TestE2E_RoutesFile_SkippedRouteAllowsWithoutPCS(t *testing.T) {
	resetPCSCalls(t)
	status, immediate := extprocClient(t, "localhost:50052", map[string]string{
		":method": "GET",
		":path":   "/health",
	})
	if immediate || status != 200 {
		t.Fatalf("expected CONTINUE/200 on skipped route; got immediate=%v status=%d", immediate, status)
	}
	if got := pcsCallCount(t); got != 0 {
		t.Fatalf("PCS must not be called for skipped route; got %d calls", got)
	}
}

func TestE2E_RoutesFile_DefaultDenyNoMatch_403WithoutPCS(t *testing.T) {
	resetPCSCalls(t)
	status, immediate := extprocClient(t, "localhost:50052", map[string]string{
		":method": "GET",
		":path":   "/totally-unknown-path",
	})
	if !immediate || status != 403 {
		t.Fatalf("expected ImmediateResponse/403 on default-deny no-match; got immediate=%v status=%d", immediate, status)
	}
	if got := pcsCallCount(t); got != 0 {
		t.Fatalf("PCS must not be called for default-deny no-match; got %d calls", got)
	}
}

// resetPCSCalls posts to the fake-pcs admin endpoint to clear recorded calls.
func resetPCSCalls(t *testing.T) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, "http://localhost:9000/_admin/reset", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("reset pcs: %v", err)
	}
	resp.Body.Close()
}

// pcsCallCount fetches the recorded call list from fake-pcs and returns its length.
func pcsCallCount(t *testing.T) int {
	t.Helper()
	resp, err := http.Get("http://localhost:9000/_admin/calls")
	if err != nil {
		t.Fatalf("get calls: %v", err)
	}
	defer resp.Body.Close()
	var calls []map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&calls); err != nil {
		t.Fatalf("decode calls: %v", err)
	}
	return len(calls)
}
```

Merge the imports cleanly. The new imports are:
- `"encoding/json"`
- `"net/http"`
- `"context"`
- `"google.golang.org/grpc"`
- `"google.golang.org/grpc/credentials/insecure"`
- `core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"`
- `ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"`

Existing test setup in `TestMain` already brings the docker-compose stack up; just add `sidecar-with-routes` to the service list it waits on. If the existing `TestMain` uses a list/array of services, add `"sidecar-with-routes"` to it; otherwise, add a `dockerWait("sidecar-with-routes")` call alongside whatever pattern the existing code uses.

- [ ] **Step 3: Run the new tests, verify they pass**

Run from `permission-validation/test/e2e/`:

```bash
( cd permission-validation/test/e2e && make e2e ) 2>&1 | tail -40
```

(Or whatever the existing `Makefile` defines for running the docker-compose e2e suite.) Expected: both new tests PASS, all existing tests still PASS.

If `make e2e` does not exist or is broken, fall back to the manual sequence in `permission-validation/test/e2e/README.md` — typically:

```bash
cd permission-validation/test/e2e
docker compose up -d --build
go test -tags=e2e ./... -v -run TestE2E_RoutesFile
docker compose down
```

- [ ] **Step 4: Commit**

```bash
git add permission-validation/test/e2e/docker-compose.yaml \
        permission-validation/test/e2e/e2e_test.go
git commit -m "$(cat <<'EOF'
test(e2e): docker-compose wire-level test of --routes-file

Adds a second sidecar instance (`sidecar-with-routes`) wired to read
the existing test/e2e/routes.yaml via --routes-file. Two new tests
open a direct gRPC ext_proc stream against it and assert the
short-circuit paths:

  - skipped route (/health) → ImmediateResponse-less CONTINUE,
    0 calls to fake-pcs
  - default-deny no-match (/totally-unknown-path) → ImmediateResponse
    403, 0 calls to fake-pcs

Existing tests still go through Envoy via the original `sidecar`
service on port 50051; this addition uses port 50052 for the
routes-file-enabled instance, so they don't interfere.

Local dev loop for the --routes-file behaviour is now fully
docker-compose-based — no kind, no Istio required.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 5 — Docs + committed example

### Task 5.1: Update `permission-validation/README.md`

**Files:**
- Modify: `permission-validation/README.md`

- [ ] **Step 1: Open the file and find the "validate-routes CLI" section**

Run:
```bash
grep -n "validate-routes CLI" permission-validation/README.md
```

- [ ] **Step 2: Replace the "validate-routes CLI" subsection**

Find the section header and the block describing the static invocation. Replace the section body (keeping the header) with:

```markdown
## `validate-routes` CLI

Two subcommands. The first positional argument is the route-config file;
flags follow it.

```sh
validate-routes validate routes.yaml
```

Exits 0 if the file parses and validates, 1 otherwise; errors print to
stderr.

```sh
# Static target (Phase 1 — app team controls its own Envoy):
validate-routes translate routes.yaml \
  -o envoy.yaml \
  --sidecar-host sidecar --sidecar-port 50051 \
  --backend-host orders-app --backend-port 8080 \
  --access-log

# Istio target (Phase 1.5 — pv sidecar lives in an Istio-injected pod):
validate-routes translate routes.yaml \
  -o envoyfilter.yaml \
  --target=istio \
  --namespace orders \
  --workload-label app=orders-app
```

The static target renders an Envoy 1.31 static bootstrap (validates first;
errors abort output). The istio target renders an Istio `EnvoyFilter` CRD
with three patches scoped to `SIDECAR_INBOUND`: a STATIC cluster for the
pv sidecar at 127.0.0.1, the `ext_proc` HTTP filter, and a probe-path
carve-out for liveness/readiness paths.

Flag availability differs per target:

| Flag | `--target=static` | `--target=istio` |
|---|---|---|
| `--sidecar-host` | optional (default 127.0.0.1) | not allowed (always 127.0.0.1) |
| `--sidecar-port` | optional (default 50051) | optional (default 50051) |
| `--backend-host`, `--backend-port`, `--admin-host`, `--access-log` | as documented | **rejected with error** |
| `--namespace` | **rejected with error** | required |
| `--workload-label key=value` (repeatable) | **rejected with error** | required, ≥1 |
| `--name` | **rejected with error** | optional (defaults to `permission-validation-<appId>`) |
| `--probe-paths <a,b,c>` | **rejected with error** | optional (defaults to `/healthz,/readyz,/livez`) |
```

- [ ] **Step 3: Find the "Run the sidecar" section and add `--routes-file`**

Locate the section that documents the sidecar's flags (search for `--listen 0.0.0.0:50051`). Add a paragraph after the existing flag table:

```markdown
With the **istio target**, route decisions move into the sidecar. Pass
the same `routes.yaml` via `--routes-file`:

```sh
./permission-validation \
  --listen 0.0.0.0:50051 \
  --pcs-endpoint http://permission-checking.internal:8080 \
  --pcs-timeout 250ms \
  --routes-file /etc/pv/routes.yaml \
  --otel-disabled
```

The sidecar parses the file at startup (fail-fast on schema error) and
consults its compiled matcher before any header parse or PCS call:
skipped routes and the default-deny catch-all return ALLOW/DENY
immediately; protected matches fall through to the Phase 1 decision
path. Without `--routes-file`, the sidecar behaves exactly as Phase 1.
```

- [ ] **Step 4: Commit**

```bash
git add permission-validation/README.md
git commit -m "docs(pv): document --target=istio and --routes-file

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 5.2: Update `permission-validation/examples/onboarding/README.md`

**Files:**
- Modify: `permission-validation/examples/onboarding/README.md`

- [ ] **Step 1: Append a new "Adopt in an Istio-injected pod" section**

Append to `permission-validation/examples/onboarding/README.md`:

```markdown
## Adopt in an Istio-injected pod

Five-step onboarding when your app pod is already injected with `istio-proxy`:

1. **Create a ConfigMap holding `routes.yaml`:**

   ```sh
   kubectl create configmap pv-routes -n <ns> \
     --from-file=routes.yaml=./routes.yaml
   ```

2. **Add the `permission-validation` container to your Deployment** with
   the ConfigMap mounted at `/etc/pv` and `--routes-file=/etc/pv/routes.yaml`
   in its `args`. Example container snippet:

   ```yaml
   - name: pv-sidecar
     image: <registry>/permission-validation:<tag>
     args:
     - "--listen=127.0.0.1:50051"     # bind only on loopback — only istio-proxy needs to reach it
     - "--pcs-endpoint=http://permission-checking.internal:8080"
     - "--pcs-timeout=250ms"
     - "--routes-file=/etc/pv/routes.yaml"
     volumeMounts:
     - { name: pv-routes, mountPath: /etc/pv }
   ```

   And in the pod spec's `volumes`:

   ```yaml
   - name: pv-routes
     configMap: { name: pv-routes }
   ```

3. **Render the EnvoyFilter from your `routes.yaml`:**

   ```sh
   validate-routes translate routes.yaml \
     --target=istio \
     --namespace <ns> \
     --workload-label app=<appId> \
     -o envoyfilter.yaml
   ```

4. **Apply it:**

   ```sh
   kubectl apply -f envoyfilter.yaml
   ```

5. **Verify:**

   ```sh
   istioctl proxy-config listener <pod> -n <ns> | grep ext_proc
   ```

   You should see the `envoy.filters.http.ext_proc` filter inserted on the
   inbound listener.

### Liveness/readiness probes

`/healthz`, `/readyz`, and `/livez` are bypassed by default (see the
`VIRTUAL_HOST` patch in the generated EnvoyFilter). Override with
`--probe-paths /custom,/other` at render time. **Probe paths are
unauthenticated** — do not route real traffic through them.

### Native sidecars (Kubernetes 1.28+, Istio 1.20+)

When `PILOT_ENABLE_NATIVE_SIDECARS=true`, both `istio-proxy` and
`pv-sidecar` can become `initContainers` with `restartPolicy: Always`,
which improves startup ordering. Not required.
```

- [ ] **Step 2: Commit**

```bash
git add permission-validation/examples/onboarding/README.md
git commit -m "docs(onboarding): Adopt in an Istio-injected pod — 5-step checklist

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 5.3: Generate + commit `examples/onboarding/envoyfilter.yaml`

**Files:**
- Create: `permission-validation/examples/onboarding/envoyfilter.yaml`

- [ ] **Step 1: Inspect the existing routes.yaml in the onboarding example**

Run:
```bash
ls permission-validation/examples/onboarding/
cat permission-validation/examples/onboarding/routes.yaml 2>/dev/null | head -20
```

You should see a routes.yaml with `appId: orders-app` (or similar). If the file does not exist, stop and confirm with the controller — the example onboarding directory should have one already.

- [ ] **Step 2: Generate the EnvoyFilter using the new CLI**

Run from the `permission-validation/` directory:

```bash
cd permission-validation
go run ./cmd/validate-routes translate examples/onboarding/routes.yaml \
  --target=istio \
  --namespace orders \
  --workload-label app=orders-app \
  -o examples/onboarding/envoyfilter.yaml
cd -
```

- [ ] **Step 3: Verify the rendered file passes `kubectl --dry-run=client`**

If you have access to a kubectl install with Istio CRDs available, run:

```bash
kubectl apply --dry-run=client -f permission-validation/examples/onboarding/envoyfilter.yaml
```

Expected: `envoyfilter.networking.istio.io/permission-validation-orders-app created (dry run)`. If the CRD is not registered locally, fall back to confirming the YAML parses cleanly:

```bash
python3 -c "import yaml; yaml.safe_load(open('permission-validation/examples/onboarding/envoyfilter.yaml'))" && echo "yaml ok"
```

- [ ] **Step 4: Commit**

```bash
git add permission-validation/examples/onboarding/envoyfilter.yaml
git commit -m "$(cat <<'EOF'
docs(onboarding): committed envoyfilter.yaml — output of --target=istio

Rendered from examples/onboarding/routes.yaml with --namespace orders
and --workload-label app=orders-app. Reviewers see exactly what the
generator produces; regenerable byte-for-byte by re-running the CLI.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 5.4: One-line note in `test/e2e/README.md`

**Files:**
- Modify: `permission-validation/test/e2e/README.md`

- [ ] **Step 1: Find the "Out of scope" section (or end of file if no such section exists)**

Run:
```bash
grep -n "out of scope\|Out of Scope" permission-validation/test/e2e/README.md
```

- [ ] **Step 2: Add one line under "Out of scope" (or append a new section at the end)**

Append (or insert into the appropriate location):

```markdown
- The Istio render target (`validate-routes translate --target=istio`) is
  covered by unit tests in `internal/config/` and `cmd/validate-routes/` plus
  optional `kubectl apply --dry-run=server` lint against the user's own
  istiod. No in-CI mesh integration ships from this work.
```

- [ ] **Step 3: Commit**

```bash
git add permission-validation/test/e2e/README.md
git commit -m "docs(e2e): note that Istio target is unit-tested only

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 6 — Wire generated EnvoyFilter into the kind Option A demo

This phase replaces the hand-written `kind/demo-ext-proc-istio/templates/envoyfilter.yaml` with output from the new `--target=istio` CLI, and finally makes `/healthz` return 200 in Option A (closing the known caveat).

### Task 6.1: New `routes-cm.yaml` template (mount routes.yaml into the pod)

**Files:**
- Create: `kind/demo-ext-proc-istio/templates/routes-cm.yaml`

- [ ] **Step 1: Write the ConfigMap template**

Create `kind/demo-ext-proc-istio/templates/routes-cm.yaml`:

```yaml
# kind/demo-ext-proc-istio/templates/routes-cm.yaml
# ConfigMap holding kind/routes.yaml content. The sidecar reads it via
# --routes-file=/etc/pv/routes.yaml so route decisions move into the
# sidecar (matched skipped → ALLOW pre-PCS, default-deny → 403 pre-PCS).
{{- if not .Values.routesYaml }}
{{- fail "routesYaml is empty — pass via `helm install --set-file routesYaml=kind/routes.yaml`" }}
{{- end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: pv-routes
  namespace: {{ .Values.namespace }}
data:
  routes.yaml: |
{{ .Values.routesYaml | indent 4 }}
```

- [ ] **Step 2: Verify the fail-fast guard**

Run:
```bash
helm template istio kind/demo-ext-proc-istio/ --show-only templates/routes-cm.yaml 2>&1 | head -3
```
Expected: error containing "routesYaml is empty" (no other helm warning yet matters).

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/routes-cm.yaml
git commit -m "demo(istio): routes-cm ConfigMap template, populated via --set-file

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 6.2: Mount `pv-routes` ConfigMap into the sidecar + pass `--routes-file`

**Files:**
- Modify: `kind/demo-ext-proc-istio/templates/echo-app.yaml`

- [ ] **Step 1: Find the sidecar container in the template**

Run:
```bash
grep -n "sidecar\|args:\|volumes:" kind/demo-ext-proc-istio/templates/echo-app.yaml
```

- [ ] **Step 2: Add `--routes-file` to the sidecar args**

In `kind/demo-ext-proc-istio/templates/echo-app.yaml`, find the sidecar container's `args:` block and add `"--routes-file=/etc/pv/routes.yaml"` so it reads:

```yaml
      - name: sidecar
        image: {{ .Values.images.permissionValidation }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        args:
        - "--listen=0.0.0.0:50051"
        - "--pcs-endpoint=http://pcs:8080"
        - "--pcs-timeout=250ms"
        - "--routes-file=/etc/pv/routes.yaml"
        - "--otel-disabled"
        ports:
        - containerPort: 50051
          name: grpc-extproc
        readinessProbe:
          tcpSocket: { port: 50051 }
          periodSeconds: 2
        volumeMounts:
        - name: pv-routes
          mountPath: /etc/pv
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
```

(Add `volumeMounts:` if the sidecar container does not already have one.)

- [ ] **Step 3: Add the volume to the pod spec**

In the same template, find the pod-level `volumes:` block (or add one if missing — peer of `containers:`). Add:

```yaml
      volumes:
      - name: pv-routes
        configMap:
          name: pv-routes
```

- [ ] **Step 4: Verify the template still renders**

Run:
```bash
helm template istio kind/demo-ext-proc-istio/ \
  --set-file routesYaml=kind/routes.yaml \
  --show-only templates/echo-app.yaml | grep -E "routes-file|pv-routes" | head -10
```
Expected: see the `--routes-file=/etc/pv/routes.yaml` arg and the `pv-routes` volume + volumeMount lines.

- [ ] **Step 5: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/echo-app.yaml
git commit -m "demo(istio): mount pv-routes ConfigMap; sidecar reads --routes-file

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 6.3: Replace hand-written `envoyfilter.yaml` with generator output

**Files:**
- Modify: `kind/demo-ext-proc-istio/templates/envoyfilter.yaml`

- [ ] **Step 1: Inspect current contents (so you know what is being replaced)**

Run:
```bash
wc -l kind/demo-ext-proc-istio/templates/envoyfilter.yaml
head -5 kind/demo-ext-proc-istio/templates/envoyfilter.yaml
```

- [ ] **Step 2: Generate the EnvoyFilter from kind/routes.yaml**

Run from repo root:

```bash
cd permission-validation
go run ./cmd/validate-routes translate ../kind/routes.yaml \
  --target=istio \
  --namespace demo-istio \
  --workload-label app=echo-app \
  --name echo-ext-proc \
  -o /tmp/generated-envoyfilter.yaml
cd -
```

Inspect the output:

```bash
head -30 /tmp/generated-envoyfilter.yaml
```

- [ ] **Step 3: Replace the chart's hand-written template with the generated content**

```bash
cp /tmp/generated-envoyfilter.yaml kind/demo-ext-proc-istio/templates/envoyfilter.yaml
rm /tmp/generated-envoyfilter.yaml
```

This collapses the chart's Helm-templated EnvoyFilter into a literal one (no Helm `{{ }}` directives needed — the values were resolved at generation time). That is intentional: the EnvoyFilter is now derived from `kind/routes.yaml`, not from `kind/demo-ext-proc-istio/values.yaml`. Regenerable by re-running the command above.

Verify the template renders:

```bash
helm template istio kind/demo-ext-proc-istio/ \
  --set-file routesYaml=kind/routes.yaml \
  --show-only templates/envoyfilter.yaml | head -10
```
Expected: a literal EnvoyFilter (no template directives), starting with `apiVersion: networking.istio.io/v1alpha3`.

- [ ] **Step 4: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/envoyfilter.yaml
git commit -m "$(cat <<'EOF'
demo(istio): envoyfilter.yaml is now generator output from --target=istio

Single source of truth: kind/routes.yaml. The chart's template is a
literal copy of `validate-routes translate --target=istio` output —
regenerate by re-running:

  cd permission-validation
  go run ./cmd/validate-routes translate ../kind/routes.yaml \
    --target=istio --namespace demo-istio \
    --workload-label app=echo-app --name echo-ext-proc \
    -o ../kind/demo-ext-proc-istio/templates/envoyfilter.yaml

The `/healthz` skipped behavior is now honoured in Option A (the
VIRTUAL_HOST patch in the generator bypasses ext_proc for that path).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6.4: Update `setup-istio.sh` — pass `--set-file routesYaml`

**Files:**
- Modify: `kind/setup-istio.sh`

- [ ] **Step 1: Locate the helm install line**

Run:
```bash
grep -n "helm upgrade --install istio" kind/setup-istio.sh
```

- [ ] **Step 2: Update the helm install to pass routes.yaml**

Replace the line (search-and-replace in the editor):

```bash
helm upgrade --install istio "${CHART_DIR}" --namespace default --wait --timeout 180s
```

with:

```bash
helm upgrade --install istio "${CHART_DIR}" \
  --namespace default --wait --timeout 180s \
  --set-file routesYaml="${KIND_DIR}/routes.yaml"
```

- [ ] **Step 3: Update the `/healthz` assertion in the verification block — was 403, should now be 200**

Find the line:

```bash
expect_status 403 "http://127.0.0.1:8080/healthz" -H "Host: app.local"
```

Replace with:

```bash
expect_status 200 "http://127.0.0.1:8080/healthz" -H "Host: app.local"
```

And the comment block above it should change from:

```bash
# 4) ECHO HEALTHZ — Option A does not honour the routes.yaml skipped list
#    (no translate target); the request still goes through ext_proc, where
#    the sidecar rejects missing X-Auth-Context with 403. We assert 403 here
#    intentionally and call it out in DEMO.md.
```

to:

```bash
# 4) SKIPPED ROUTE — /healthz now bypasses the sidecar in Option A too,
#    courtesy of the VIRTUAL_HOST configPatch that --target=istio emits.
```

- [ ] **Step 4: bash -n check**

Run:
```bash
bash -n kind/setup-istio.sh && echo "syntax ok"
```

- [ ] **Step 5: Commit**

```bash
git add kind/setup-istio.sh
git commit -m "$(cat <<'EOF'
demo(istio): pass routes.yaml via --set-file; /healthz now asserted 200

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 6.5: Update Option A chart README — remove the /healthz caveat

**Files:**
- Modify: `kind/demo-ext-proc-istio/README.md`

- [ ] **Step 1: Find the "Known difference" section**

Run:
```bash
grep -n "Known difference\|/healthz" kind/demo-ext-proc-istio/README.md
```

- [ ] **Step 2: Replace the "Known difference vs. Option B" section**

Replace the existing section body with:

```markdown
## Skipped routes via VIRTUAL_HOST patch

`/healthz`, `/readyz`, and `/livez` bypass `ext_proc` at the Envoy level —
the EnvoyFilter's third `configPatch` (`applyTo: VIRTUAL_HOST`) marks
them with `ExtProcPerRoute.disabled: true`, so istio-proxy never opens
a gRPC stream to the sidecar for those paths. This means probes survive
sidecar restarts entirely.

`routes.yaml`'s `behavior: skipped` rules are honoured by the sidecar
itself (via `--routes-file`) for any other skipped routes you declare;
the EnvoyFilter only handles the probe paths.

To override the probe paths at generation time:

```sh
validate-routes translate routes.yaml --target=istio \
  --namespace demo-istio --workload-label app=echo-app \
  --probe-paths=/healthz,/custom-ready \
  -o envoyfilter.yaml
```
```

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/README.md
git commit -m "docs(demo/istio): /healthz now returns 200 via VIRTUAL_HOST patch

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 6.6: End-to-end verification — Option A with the new EnvoyFilter

**Files:**
- None — this is a verification task.

- [ ] **Step 1: Tear down any existing kind clusters**

Run:
```bash
./kind/teardown.sh ext-proc-istio-demo 2>/dev/null || true
./kind/teardown.sh ext-proc-plain-demo 2>/dev/null || true
kind get clusters
```
Expected: no kind clusters.

- [ ] **Step 2: Rebuild the `workspace/permission-validation:dev` image (new --routes-file flag)**

Run:
```bash
docker build -t workspace/permission-validation:dev \
  -f permission-validation/test/e2e/Dockerfile.sidecar permission-validation/
```
Expected: image build succeeds. Should be fast since base layers are cached.

- [ ] **Step 3: Run the full Option A setup**

Run:
```bash
SKIP_LOCAL_BUILD=1 ./kind/setup-istio.sh
```
Expected: script completes; the trailing banner says "All four canonical curls returned expected status codes." Notably the fourth curl (`/healthz`) now reports `ok 200`, not `ok 403`.

- [ ] **Step 4: Tear down**

Run:
```bash
./kind/teardown.sh ext-proc-istio-demo
```

- [ ] **Step 5: No commit** — verification only. If anything failed, fix forward in a new task and verify again.

---

### Task 6.7: Update the kind-demo spec (the §11 out-of-scope item is now done)

**Files:**
- Modify: `docs/superpowers/specs/2026-05-21-kind-demo-ext-proc-design.md`

- [ ] **Step 1: Find the §11 bullet about `translate --target=istio`**

Run:
```bash
grep -n "translate --target=istio\|Out of Scope" docs/superpowers/specs/2026-05-21-kind-demo-ext-proc-design.md
```

- [ ] **Step 2: Update the bullet from "out of scope" to "done"**

Find the bullet:

```markdown
- **A `translate --target=istio` mode in `validate-routes`.** Would let Option A derive its EnvoyFilter from `routes.yaml` automatically. ...
```

Replace with:

```markdown
- ~~**A `translate --target=istio` mode in `validate-routes`.**~~ **Done.** See [`docs/superpowers/plans/2026-05-21-istio-envoyfilter-target-implementation.md`](../plans/2026-05-21-istio-envoyfilter-target-implementation.md). Option A's `envoyfilter.yaml` is now generated from `routes.yaml`; `/healthz` returns 200 (the previous "known difference vs Option B" caveat is removed).
```

- [ ] **Step 3: Commit**

```bash
git add docs/superpowers/specs/2026-05-21-kind-demo-ext-proc-design.md
git commit -m "docs(spec): mark --target=istio out-of-scope item as done

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

After writing the full plan, checked against the spec:

1. **Spec coverage** — every section of `2026-05-18-istio-envoyfilter-target-design.md`:
   - §2 In-scope: `--target=static|istio` flag → Task 2.1; `--namespace`, `--workload-label`, `--name`, `--probe-paths` → Task 2.1 + tests in 2.3/2.4; `--routes-file` on sidecar → Task 4.1+4.3; `internal/routes` package → Phase 3; `internal/config.TranslateIstio` + `IstioOptions` → Phase 1; tests across §6.1/6.2/6.3 → Tasks 1.1–1.6, 2.2–2.4, 3.1–3.4, 4.3; docs → Phase 5; committed `envoyfilter.yaml` → Task 5.3.
   - §3.1 Runtime topology — described in plan's "What this builds" header + the kind-demo integration in Phase 6 actualizes it.
   - §3.2 Three EnvoyFilter patches — implemented in `envoy-filter.tmpl.yaml` (Task 1.2).
   - §3.3 Sidecar `--routes-file` behaviour — Tasks 4.1 + 4.3.
   - §3.4 Probe-path exact match, defaults — Task 1.4 (defaults + override tests), template (Task 1.2 uses `printf "%q"` for exact-string emission).
   - §4 Full flag matrix — covered by Task 2.1's switch statement; rejection cases tested in Task 2.4.
   - §5 Concrete EnvoyFilter sample — generated and committed in Task 5.3.
   - §6 Testing strategy — all three subsections covered (translator: 1.1–1.6; CLI: 2.2–2.4; sidecar: 3.1–3.4 + 4.3).
   - §6.4 E2E deferred — captured in Task 5.4.
   - §7 Docs — Tasks 5.1, 5.2, 5.3, 5.4.
   - §8 Acceptance criteria — all map to tasks above (build/vet/test cleanliness validated at the end of each phase).
   - §9 Open questions — none.
2. **Placeholder scan** — no TBD/TODO/"implement later"/handwaving in any task; every code step has complete code; every commit message is a HEREDOC with full body and Co-Authored-By.
3. **Type/name consistency** — `IstioOptions`, `TranslateIstio`, `routes.Table`, `routes.Compile`, `routes.Lookup`, `extproc.New(p, m, t)`, `pv_sidecar` cluster name — all stable across tasks. Flag names (`--target`, `--namespace`, `--workload-label`, `--name`, `--probe-paths`, `--routes-file`) consistent in plan tasks, README docs, and example invocations. EnvoyFilter `metadata.name` default formula `permission-validation-<appId>` consistent in both Task 1.3 and Task 5.3.

Plan is ready to execute.
