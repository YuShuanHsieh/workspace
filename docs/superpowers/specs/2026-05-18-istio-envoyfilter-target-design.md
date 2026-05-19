# Istio EnvoyFilter render target for `validate-routes`

**Status:** draft, pending review
**Date:** 2026-05-18
**Authors:** brainstormed with Claude
**Related:** `prd/permission-validation/phase-1-topology-decision.md` (defers xDS to Phase 2), `permission-validation/internal/config/translate.go`, `permission-validation/cmd/validate-routes/main.go`

## 1. Motivation

The Phase 1 implementation generates a static Envoy bootstrap (`envoy.yaml`). That works when the application team controls the Envoy process — they mount the generated file at `/etc/envoy/envoy.yaml` and start their own Envoy container. It does **not** work when Envoy is injected by a service mesh: the mesh control plane (istiod in Istio's case) owns the bootstrap, delivers config via xDS, and refuses to load a static file the app team provides.

The Phase 1 PRD acknowledged this gap. `phase-1-topology-decision.md:98` says:

> Envoy becomes a platform dependency … Operational ownership, version pinning, and config delivery (xDS or static) must be settled before pilot adoption. Mitigation: pin a single Envoy version for Phase 1 pilots; use static config; **defer xDS to Phase 2**.

This spec is a stepping stone toward that Phase 2 work. It does not introduce an xDS server. It introduces a render target — `validate-routes translate --target=istio` — that emits an Istio `EnvoyFilter` CRD instead of a static bootstrap. The CRD patches the mesh-owned Envoy with the three things our system needs: the `ext_proc` HTTP filter, a static cluster pointing at the pv sidecar, and a small probe-path bypass for liveness/readiness. Route-level `protected`/`skipped` decisions migrate into the pv sidecar process. `routes.yaml` remains the single source of truth.

## 2. Scope

### In scope

- New `--target={static|istio}` flag on the existing `validate-routes translate` subcommand. `static` (default) preserves Phase 1 behavior; `istio` renders an `EnvoyFilter`.
- New flags scoped to `--target=istio`: `--namespace`, `--workload-label key=value` (repeatable, ≥1 required), `--name` (optional, defaults to `permission-validation-<appId>`), `--probe-paths` (comma-separated literal paths, defaults to `/healthz,/readyz,/livez`).
- New `--routes-file` flag on the `permission-validation` sidecar binary. When set, the sidecar parses `routes.yaml` at startup using existing `internal/config.Parse` + `Validate`, compiles the routes into matchers, and short-circuits the ext_proc stream for skipped routes and the `defaultBehavior` catch-all before any header parsing or PCS call. Empty default preserves Phase 1 behavior (route decisions stay in Envoy via the static target's per-route config).
- A new package `internal/routes` holding the compiled-route table and `Lookup(method, path) → behavior` function. Used by the sidecar's `Decide()` orchestrator.
- A new internal renderer `internal/config.TranslateIstio(rc *RouteConfig, opts IstioOptions) ([]byte, error)` with its own embedded template `envoy-filter.tmpl.yaml` and a new `IstioOptions` struct (independent from `TranslateOptions`; no shared "target" field in either).
- Tests: translator unit tests covering shape, defaults, overrides, negative-flag-rejection cases; CLI tests covering flag/target combinations; sidecar `internal/routes` matcher tests; an extension to the `Decide()` test for the three new short-circuit paths (skipped, default-deny, default-skipped).
- Docs: module README updates, an "Adopt in an Istio-injected pod" section in the onboarding README, and a committed `examples/onboarding/envoyfilter.yaml` rendered by the new target.

### Out of scope

- An xDS server. The generated `EnvoyFilter` is applied via `kubectl`/Argo/Helm or whatever GitOps the app team uses; no control plane component ships from this work.
- Generation of `ConfigMap`, `Deployment` patch, `Service`, `VirtualService`, `DestinationRule`, or any other Kubernetes manifest. App teams own those. We publish documentation that describes the required shape.
- Non-Istio meshes (Linkerd uses linkerd-proxy not Envoy; Consul, Cilium, Kuma, Gloo each have their own extension surfaces). The `--target` enum is extensible later; this iteration recognizes only `static` and `istio`.
- An in-CI Istio integration harness (`kind` + `istioctl` + Helm pins). Deferred — see §6.
- Encrypted/signed `X-Auth-Context` headers, URL/body cross-checking, decision caching. All remain Phase 1.5 / Phase 2 topics as per the existing PRD.

## 3. Architecture

### 3.1 Runtime topology

Three runtime containers per protected pod when the Istio target is in use:

| # | Container | Image source | Listens on | Role |
|---|-----------|--------------|------------|------|
| 1 | `istio-proxy` | injected by Istio (`docker.io/istio/proxyv2`) | `:15006` inbound, `:15001` outbound, `:15021` health | Envoy data plane; receives all inbound pod traffic; calls (2) over gRPC ext_proc. |
| 2 | `permission-validation` (pv) | `<registry>/permission-validation:<tag>` | `127.0.0.1:50051` | Our sidecar. Reads `routes.yaml` from a mounted ConfigMap; answers ext_proc; talks to PCS. |
| 3 | `app` | whatever the app team ships | `127.0.0.1:<app-port>` | The application. istio-proxy forwards granted requests here. |

(An `istio-init` init container sets up iptables; not a runtime container.) On Kubernetes 1.28+ with native sidecars (Istio 1.20+, `PILOT_ENABLE_NATIVE_SIDECARS=true`), (1) and (2) can both be `initContainers` with `restartPolicy: Always`. Not required.

Per-request flow:

```text
client → istio-proxy (1) → pv (2) via 127.0.0.1:50051
                       │     │
                       │     └─→ PCS (external)
                       │
                       └─→ app (3) on 127.0.0.1:<app-port>   ← only if allowed
```

### 3.2 What the EnvoyFilter does

Three patches, all scoped to `context: SIDECAR_INBOUND`:

1. **`applyTo: CLUSTER` / `operation: ADD`** — registers `pv_sidecar` as a STATIC cluster with one endpoint at `127.0.0.1:50051`, HTTP/2 explicit (gRPC requires it). Equivalent to the static bootstrap's `pv_sidecar` cluster.
2. **`applyTo: HTTP_FILTER` / `operation: INSERT_BEFORE`** — inserts `envoy.filters.http.ext_proc` immediately before `envoy.filters.http.router` in the inbound HTTP connection manager's filter chain. `failure_mode_allow: false`, `processing_mode.request_header_mode: SEND` (rest `SKIP`/`NONE`). Identical to the static bootstrap.
3. **`applyTo: VIRTUAL_HOST` / `operation: MERGE`** — merges routes for `/healthz`, `/readyz`, `/livez` (or whatever `--probe-paths` lists) with `typed_per_filter_config.envoy.filters.http.ext_proc.disabled: true`. This is the probe-path carve-out: liveness/readiness probes bypass pv at the Envoy level so a pv outage doesn't fail probes.

### 3.3 What the sidecar does differently

When started with `--routes-file=<path>`:

1. **At startup:** parse the file via `config.Parse` + `config.Validate`. On error, exit non-zero. Compile each route into a matcher (method exact or `*`; path via existing `globToRegex`). Store in an ordered slice preserving file order — schema's "first rule wins" semantics.
2. **At request time, before any header parsing or PCS call:**
   - Extract `:method` and `:path` headers from the incoming `ProcessRequest`.
   - Look up via `routes.Lookup(method, path) → (behavior, matched)`.
   - If `matched && behavior == "skipped"` → respond ALLOW, return.
   - If `!matched && defaultBehavior == "skipped"` → respond ALLOW, return.
   - If `!matched && defaultBehavior == "deny"` → respond DENY (403), return.
   - Otherwise (matched protected route) → fall through to existing header parse + PCS path.

With `--routes-file` unset, the sidecar behavior is unchanged from Phase 1 (the static target embeds route decisions in Envoy's per-route filter config).

### 3.4 The probe-path carve-out, in detail

The carve-out is **only** in the EnvoyFilter. The sidecar's matcher never sees those requests, so the sidecar doesn't need to know about them. This is by design: if the sidecar is the source of truth for probe bypass, a sidecar restart fails probes. By baking the probe paths into the EnvoyFilter, probes survive sidecar restarts entirely.

Probe paths are **exact-match** (Envoy `match: { path: "/healthz" }`), not prefix or regex, so a user route like `/healthzap` is not accidentally bypassed. App teams who use non-default probe paths (e.g., `/custom-health`) override via `--probe-paths`.

Probe paths are **unauthenticated by design**. The onboarding README will explicitly warn against routing real traffic through them.

## 4. CLI surface

The matrix of which flags are valid for which target:

| Flag | `--target=static` | `--target=istio` | Notes |
|------|-------------------|------------------|-------|
| `<routes-file>` (positional) | required | required | Same input. |
| `-o <path>` | optional | optional | Output file or stdout. |
| `--sidecar-host` | required (cluster DNS or IP) | ignored (always `127.0.0.1`) | Sibling container; not parameterized. |
| `--sidecar-port` | optional (default `50051`) | optional (default `50051`) | Kept overridable. |
| `--backend-host`, `--backend-port` | required | **rejected with error** | Istio routes to the app, not us. |
| `--admin-host` | optional (default `127.0.0.1`) | **rejected with error** | Admin is istio-proxy's. |
| `--access-log` | optional | **rejected with error** | Mesh-level concern. |
| `--namespace <ns>` | **rejected with error** | required | `metadata.namespace`. No default. |
| `--workload-label key=value` (repeatable) | **rejected with error** | required, ≥1 | `workloadSelector.labels`. |
| `--name <name>` | **rejected with error** | optional | `metadata.name`, defaults to `permission-validation-<appId>`. |
| `--probe-paths <comma-list>` | **rejected with error** | optional | Defaults to `/healthz,/readyz,/livez`. Literal paths only. |

"Rejected with error" means the CLI exits non-zero with a stderr message naming the offending flag and pointing at `-h`. Silent ignores ship the wrong config to prod.

### 4.1 Example invocations

```bash
# Static target (Phase 1, unchanged):
validate-routes translate routes.yaml -o envoy.yaml \
  --sidecar-host sidecar --sidecar-port 50051 \
  --backend-host orders-app --backend-port 8080 \
  --access-log

# Istio target:
validate-routes translate routes.yaml -o envoyfilter.yaml \
  --target=istio \
  --namespace orders \
  --workload-label app=orders-app
```

### 4.2 Internal refactor

`internal/config.Translate(rc, opts)` keeps its current signature. A new exported `internal/config.TranslateIstio(rc, opts IstioOptions)` is added. The CLI's `runTranslate` parses target, dispatches, and validates the target/flag matrix before either renderer runs. No shared "target" field in any options struct — each renderer takes its own options type.

## 5. EnvoyFilter content (concrete sample)

Reference render for the onboarding example: `appId: orders-app`, `--namespace orders`, `--workload-label app=orders-app`, default probe paths.

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: permission-validation-orders-app
  namespace: orders
spec:
  workloadSelector:
    labels:
      app: orders-app
  configPatches:

    # (a) STATIC cluster for the pv sibling container.
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
                        socket_address: { address: 127.0.0.1, port_value: 50051 }

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
            - match: { path: "/healthz" }
              typed_per_filter_config:
                envoy.filters.http.ext_proc:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
                  disabled: true
            - match: { path: "/readyz" }
              typed_per_filter_config:
                envoy.filters.http.ext_proc:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
                  disabled: true
            - match: { path: "/livez" }
              typed_per_filter_config:
                envoy.filters.http.ext_proc:
                  "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExtProcPerRoute
                  disabled: true
```

### 5.1 Patch decisions, annotated

- **All patches `context: SIDECAR_INBOUND`.** Wrong contexts have severe blast radius: `GATEWAY` patches ingress gateways (validates inbound mesh traffic, wrong target); `SIDECAR_OUTBOUND` patches when the app makes outbound calls (validates the app's own clients, wrong direction).
- **`operation: ADD` for the cluster** — it doesn't exist in the mesh's view. **`INSERT_BEFORE`** for the filter — positions it before the router, which is always last in the HCM chain. **`MERGE`** for the vhost routes — additive to whatever Istio generated from VirtualService.
- **HTTP/2 explicit on the cluster.** ext_proc is gRPC; gRPC is HTTP/2. Same as `envoy-static.tmpl.yaml`.
- **`failure_mode_allow: false`.** Same fail-closed posture as Phase 1.

## 6. Testing strategy

### 6.1 Translator unit tests (`internal/config/translate_istio_test.go`)

- `TestTranslateIstio_MinimalProducesValidYAML` — render with one label, one route; parse via `yaml.v3`; assert `apiVersion`, `kind`, `metadata.name`, `metadata.namespace`, `spec.workloadSelector.labels`.
- `TestTranslateIstio_NameDefaultsFromAppID` — empty `Name` → `permission-validation-<appId>`.
- `TestTranslateIstio_ProbePathsDefault` — empty `ProbePaths` → exactly three exact-match routes for `/healthz`, `/readyz`, `/livez`, each with `disabled: true`.
- `TestTranslateIstio_ProbePathsOverride` — `["/health", "/ready"]` → exactly those two; defaults not emitted.
- `TestTranslateIstio_WorkloadLabelsRendered` — multiple labels render as a map.
- `TestTranslateIstio_RoutesNotInOutput` — routes from `routes.yaml` (other than probe paths) **must not** appear in the EnvoyFilter. Negative assertion; explicit because it's the contract.
- `TestTranslateIstio_RejectsEmptyWorkloadLabels` / `TestTranslateIstio_RejectsEmptyNamespace` — `IstioOptions` validation returns a non-nil error.
- `TestTranslateIstio_ContextIsSidecarInbound` — every `configPatches[].match.context` is `SIDECAR_INBOUND`.

### 6.2 CLI unit tests (`cmd/validate-routes/main_test.go`)

- `TestTranslate_TargetIstio_WritesFile` — happy path against `testdata/routes/valid-minimal.yaml`.
- `TestTranslate_TargetIstio_RequiresNamespace` — missing `--namespace` → exit 2.
- `TestTranslate_TargetIstio_RequiresWorkloadLabel` — missing → exit 2.
- `TestTranslate_TargetIstio_RejectsBackendFlags` — `--backend-host`/`--admin-host`/`--access-log` with `--target=istio` → exit 2. Table-driven.
- `TestTranslate_TargetStatic_RejectsIstioFlags` — `--workload-label`/`--namespace`/`--probe-paths` with default static target → exit 2.
- `TestTranslate_TargetInvalid` — `--target=nginx` → exit 2 with valid values listed.

### 6.3 Sidecar unit tests

- `internal/routes/match_test.go` — table-driven matcher tests: method exact and `*`; path exact, prefix (`/api/orders/**`), glob (`/api/orders/*`); first-match-wins ordering; default-deny vs default-skipped on no match.
- `internal/extproc/decide_test.go` — three new cases extending the existing decide test: skipped match → ALLOW without PCS call (mock asserts zero invocations), default-deny no-match → DENY without PCS call, protected match → existing PCS path unchanged.

### 6.4 E2E (deferred)

The current docker-compose e2e harness exercises the static target only. No Istio installation in CI for this iteration. Mitigations:

- The unit tests above are tight on rendered shape and context safety.
- The EnvoyFilter is small (~70 lines), inspectable in PRs.
- App-team adoption flow includes `kubectl apply --dry-run=server` validation against their real istiod.

A `test/e2e-istio/` harness using `kind` + `istioctl` is a reasonable follow-up but explicitly out of scope for this work. Tracked in `test/e2e/README.md` under "Out of scope."

## 7. Documentation changes

### 7.1 `permission-validation/README.md`

- New paragraph under "validate-routes CLI" introducing `--target=istio` with a side-by-side example next to the static invocation.
- New flag documented on the sidecar binary: `--routes-file`. Default empty preserves Phase 1 behavior; when set, sidecar parses at startup, fail-fast on error.
- New "Deployment topologies" subsection: *Static* (today) vs *Istio-injected* (this work).

### 7.2 `permission-validation/examples/onboarding/README.md`

- New "Adopt in an Istio-injected pod" section. Five-step checklist:
  1. `kubectl create configmap pv-routes --from-file=routes.yaml=./routes.yaml -n <ns>`
  2. Add the `permission-validation` container to the app's Deployment with the ConfigMap mounted at `/etc/pv` and `--routes-file=/etc/pv/routes.yaml` in `args`.
  3. `validate-routes translate routes.yaml --target=istio --namespace <ns> --workload-label app=<appId> -o envoyfilter.yaml`
  4. `kubectl apply -f envoyfilter.yaml`
  5. Verify with `istioctl proxy-config listener <pod> -n <ns> | grep ext_proc`.
- "Liveness/readiness probes" subsection: default `/healthz`/`/readyz`/`/livez` bypass; override via `--probe-paths`; **do not** route real traffic through them.
- Brief note about K8s 1.28+ native sidecars; link out; not required.

### 7.3 `permission-validation/examples/onboarding/envoyfilter.yaml`

New, committed. Rendered by the example invocation in §7.2. Reviewers see exactly what the generator produces.

### 7.4 `permission-validation/test/e2e/README.md`

One line added under "Out of scope": "The Istio target is covered by unit tests + optional `kubectl --dry-run=server` lint; no in-CI mesh integration."

## 8. Acceptance criteria

- `validate-routes translate --target=istio --namespace <ns> --workload-label k=v <routes.yaml>` produces a YAML document that `kubectl apply --dry-run=server` accepts against a vanilla Istio installation.
- Every flag/target combination in §4's table behaves as specified (required, optional, ignored, or rejected with a clear stderr message).
- `permission-validation --routes-file <path>` started against a valid `routes.yaml` short-circuits skipped routes and the catch-all without invoking PCS, while routing protected requests through the existing decision path.
- Without `--routes-file`, the sidecar binary behaves identically to Phase 1.
- All unit tests in §6.1, §6.2, §6.3 pass.
- `go test ./...`, `go vet ./...`, and `gofmt -l .` clean.
- `examples/onboarding/envoyfilter.yaml` is committed, regenerable, and matches the §7.2 invocation byte-for-byte.

## 9. Open questions

None at design time. Implementation may surface follow-ups; track in the implementation plan rather than back-amending this spec.
