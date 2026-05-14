# Phase 1 Topology Decision

| | |
|---|---|
| **Status** | Decided — Option B (Envoy `ext_proc` + sidecar) |
| **User story** | [PV1-001](./phase-1-user-stories.md#pv1-001-compare-validation-topology-options) |
| **Related** | [phase-1-architecture.md §1](./phase-1-architecture.md#1-software-architecture) · [phase-1-5-metadata-sync-design.md](./phase-1-5-metadata-sync-design.md) · [PRD §5](./PRD.md#5-proposed-architecture-sidecar-pattern) |

## 1. Context

Phase 1 must intercept every protected request, decrypt the authorization context, call the Permission Checking Service (PCS), and forward or reject before the application backend sees the request. Three topologies are on the table; this doc compares them against the Phase 1 flow and Phase 1.5 forward-compat constraints, then recommends one.

The Phase 1 flow (decrypt → PCS check → enforce) is described in [phase-1-architecture.md §2](./phase-1-architecture.md#2-data-flow--protected-request) and applies to all three options. What differs is **where** route matching and HTTP forwarding live, and **what** wire protocol the sidecar speaks.

## 2. Options

### 2.1 Option A — Custom proxy sidecar

The sidecar is a small HTTP proxy in the pod. It owns route matching, header extraction, decryption, the PCS call, enforcement, and upstream forwarding to the application backend.

- One process per pod; no platform dependency beyond Kubernetes.
- All Phase 1 components ([phase-1-architecture.md §1](./phase-1-architecture.md#1-software-architecture)) live in sidecar code.
- Skipped routes are matched by sidecar configuration and forwarded directly.

### 2.2 Option B — Envoy `ext_proc` + sidecar (recommended)

Envoy is in the request path. The sidecar implements `envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor` and handles a bidirectional gRPC stream per HTTP transaction. Envoy owns route matching, upstream forwarding, and timeouts; the sidecar owns the validator logic (extract → decrypt → PCS → enforce).

- Two processes per pod (Envoy + sidecar).
- Skipped routes are expressed as Envoy `ExtProcPerRoute.disabled: true` and never reach the sidecar.
- Response-phase participation is available via `response_header_mode: SEND` and `response_body_mode: BUFFERED`; this is the lever Phase 1.5 needs.

### 2.3 Option C — Envoy `ext_authz` + sidecar

Envoy is in the request path. The sidecar exposes a request-only `Check` API. Envoy calls `Check` per request and uses the response to allow or deny.

- Simpler sidecar API than `ext_proc` (one unary RPC, no streaming).
- Sidecar **never sees the response phase**. This is the decisive limitation.

## 3. Comparison

### 3.1 Latency

| | Sidecar hops | Notable overhead |
|---|---|---|
| A | client → sidecar → backend | One HTTP parse/forward in sidecar code; depends on chosen HTTP library. |
| B | client → Envoy → sidecar (gRPC) → Envoy → backend | One gRPC stream open per HTTP transaction; Envoy filter chain overhead (~sub-ms in published benchmarks). |
| C | client → Envoy → sidecar (gRPC unary) → Envoy → backend | One gRPC unary per request; lowest filter-side overhead of B/C. |

All three are expected to fit under the PRD §4 10ms p95 budget for validation, assuming PCS is colocated. None is excluded on latency alone; this needs to be confirmed by the POC (§6).

### 3.2 Operational complexity

| | Processes per pod | Config delivery | Failure surface |
|---|---|---|---|
| A | 1 (sidecar) | ConfigMap (route config) | Sidecar bugs affect every request. |
| B | 2 (Envoy + sidecar) | Envoy xDS or static config + ConfigMap | Envoy version pinning, xDS rollout, ExtProcPerRoute correctness. |
| C | 2 (Envoy + sidecar) | Same as B | Same as B; slightly smaller filter config surface. |

A is operationally simplest. B and C add Envoy as a platform-owned dependency.

### 3.3 Development effort

- **A** requires building an HTTP proxy: connection pool, keep-alives, timeouts, retries (or explicit no-retry), upstream health, request/response streaming. This is the largest sidecar surface of the three.
- **B** requires implementing the `ext_proc` bidirectional stream, handling `request_headers` and (for Phase 1.5) `response_headers`/`response_body`, and emitting `ImmediateResponse` for deny. No HTTP proxy code in the sidecar.
- **C** requires implementing the `ext_authz` `Check` unary RPC. Smallest sidecar.

### 3.4 Debugging experience

- **A:** single process, single log stream, single language. Easiest to debug end-to-end.
- **B / C:** two processes; correlation IDs must flow through Envoy access logs and sidecar logs. Envoy admin endpoint and stats are useful but require operator familiarity.

### 3.5 Fit for Phase 2 caching / invalidation

All three options can carry a request-side decision cache inside the sidecar. The choice does not constrain Phase 2 cache mechanics. The relevant difference is invalidation feed plumbing, which is out of scope for this comparison and identical across options.

### 3.6 Phase 1.5 forward-compatibility (the tiebreaker)

[phase-1-5-metadata-sync-design.md §3.2](./phase-1-5-metadata-sync-design.md) requires that "the client receives `2xx` only after the event has been durably WAL-appended." That invariant requires the sidecar to participate in the **response** phase: it must observe the backend's response, durably append, and only then release the `2xx` to the client.

- **A** is in the response path natively; the sidecar already proxies the response. Adding a Response Tap is straightforward.
- **B** supports response-phase participation: enabling `response_header_mode: SEND` and `response_body_mode: BUFFERED` on the relevant routes gives the sidecar exactly the lever it needs. The streaming complexity is bounded — one stream per HTTP transaction.
- **C** is **request-only**. Without a separate response-side mechanism (e.g., an application-side outbox), it cannot satisfy the Phase 1.5 §3.2 invariant. Adopting C now forces a second mechanism in Phase 1.5, which doubles the moving parts and effectively re-litigates this decision.

## 4. Recommendation

**Adopt Option B (Envoy `ext_proc` + sidecar) for Phase 1.**

Reasoning:

1. **Forward-compatibility with Phase 1.5.** B provides a native response-phase hook; C does not, and A would have to be re-architected if Envoy is later introduced for other reasons.
2. **Smaller sidecar surface than A.** No HTTP proxy code, no upstream connection pool, no route matcher — the sidecar is a pure validator. Less surface to harden, fewer attack vectors on the data path.
3. **Standard Envoy filter contract.** `ext_proc` is a documented, versioned extension; the sidecar's contract is stable and language-agnostic.
4. **Skipped routes bypass the sidecar entirely.** `ExtProcPerRoute.disabled: true` keeps health checks and public assets off the sidecar's critical path.

## 5. Trade-offs and known risks

- **Envoy becomes a platform dependency.** Every protected pod runs Envoy. Operational ownership, version pinning, and config delivery (xDS or static) must be settled before pilot adoption. Mitigation: pin a single Envoy version for Phase 1 pilots; use static config; defer xDS to Phase 2.
- **gRPC stream ordering complexity.** The sidecar maintains one bidirectional stream per HTTP transaction. Out-of-order or partial-message bugs can manifest as 5xx storms. Mitigation: integration tests (PV1-011) include malformed-stream cases; use Envoy's `failure_mode_allow: false` so a buggy sidecar fails closed, not silently open.
- **`failure_mode_allow` misconfiguration.** A `true` value would silently bypass validation. Mitigation: enforced via config schema (PV1-004) and verified in deploy-time validation.
- **Two-process debugging.** Cross-log correlation is required. Mitigation: standardize on `X-Request-Id` propagation; document the Envoy admin/stats endpoints SREs should know.

## 6. POC / benchmark scope

A short POC is required before pilot adoption. Scope:

1. **Minimal `ext_proc` handler.** Sidecar handles `request_headers`, returns `CONTINUE` for allow and `ImmediateResponse(403)` for deny. No real decryption; stub PCS.
2. **Latency benchmark.** Measure added p50 / p95 / p99 latency under sustained load (target: stay under the PRD §4 10ms validation budget). Compare against a no-filter baseline and against a stub Option A.
3. **Phase 1.5 ordering check.** Enable `response_header_mode: SEND` on one route and confirm the sidecar sees `response_headers` before the client sees the `2xx`. This is the forward-compat smoke test, not a full Phase 1.5 implementation.
4. **Failure-mode confirmation.** Kill the sidecar mid-stream; confirm Envoy returns `5xx` (fail-closed) given `failure_mode_allow: false`.
5. **Skipped-route bypass.** Configure `ExtProcPerRoute.disabled: true` on `/health`; confirm via sidecar logs that the request is never observed.

The POC is a single user-story-sized effort (estimated M). It does **not** implement decryption or any real PCS integration — those are PV1-007 / PV1-008.

## 7. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| All three options compared using the Phase 1 flow | §2, §3 |
| Comparison covers latency, ops complexity, dev effort, debugging, Phase 2 fit | §3.1, §3.2, §3.3, §3.4, §3.5 |
| Phase 1.5 forward-compatibility addressed; `ext_authz` cannot satisfy §3.2 alone | §3.6 |
| Recommendation documented with trade-offs and risks | §4, §5 |
| POC / benchmark scope identified | §6 |
