# Permission Validation User Stories

This document breaks down the Permission Validation Flow design into estimation-sized user stories.

Estimate buckets:

- **S:** 1-2 days
- **M:** 3-5 days
- **L:** 1-2 weeks
- **XL:** Split or spike first

## Epic 0: Design Spikes

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-001 | As a platform engineer, I want to benchmark Envoy `ext_authz` vs custom sidecar so we can choose the right enforcement technology. | Benchmark covers latency, 5k RPS target, extraction flexibility, cache feasibility, operational complexity. | M |
| PV-002 | As a platform engineer, I want to define the normalized authorization request contract so sidecar and Permission Checking Service agree on inputs. | Contract includes `userId`, `tenantId`, `appId`, `resourceType`, `objectId`, `action`; v1 compatibility is documented. | M |
| PV-003 | As a platform engineer, I want to define the route configuration schema so app teams can onboard declaratively. | Schema covers public/protected routes, extraction rules, cache behavior, failure behavior. | M |

## Epic 1: Request Interception And Routing

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-004 | As an app team, I want protected routes to be intercepted before reaching my service. | Matching protected requests go through validation before forwarding. | M |
| PV-005 | As an app team, I want public routes to bypass validation. | Health checks/public routes are forwarded without Permission Checking calls. | S |
| PV-006 | As a platform team, I want unmatched routes to fail closed by default. | When sidecar is enabled and no rule matches, request is denied. | S |
| PV-007 | As an app team, I want route rules to support path/method matching. | Config can match method + path pattern reliably. | M |

## Epic 2: Context Extraction

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-008 | As an app team, I want to extract context from trusted headers. | `userId`, `tenantId`, etc. can be mapped from configured headers. | S |
| PV-009 | As an app team, I want to extract `objectId` from path parameters. | Example: `/documents/{id}` maps `{id}` to `objectId`. | M |
| PV-010 | As an app team, I want to extract context from query parameters. | Configured query params can populate authorization fields. | S |
| PV-011 | As an app team, I want limited body extraction for selected routes. | Body extraction is opt-in, size-limited, content-type restricted, and tested. | L |
| PV-012 | As a platform team, I want invalid or missing context to fail closed. | Missing required fields return deny/error without forwarding. | S |

## Epic 3: Signed Context Security

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-013 | As a platform team, I want signed context verification so client-carried context cannot be spoofed. | Signature, issuer, audience, expiration, and key version are validated. | L |
| PV-014 | As a platform team, I want key rotation support. | Sidecar can refresh trusted keys without restart or with safe restart behavior. | M |
| PV-015 | As a platform team, I want unsigned/self-signed/duplicate context rejected. | Spoofed or ambiguous context is denied and audited. | M |
| PV-016 | As a security team, I want trusted upstream assumptions documented. | Gateway/header stripping/signing responsibilities are explicit. | S |

## Epic 4: Permission Evaluation

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-017 | As a sidecar, I want to call Permission Checking Service for uncached decisions. | Sends normalized request and handles allow/deny responses. | M |
| PV-018 | As a platform team, I want connection pooling and timeouts. | HTTP client uses keep-alive, bounded timeout, no unbounded retries. | M |
| PV-019 | As an app user, I want denied requests to return `403`. | Denied requests never reach app container. | S |
| PV-020 | As a platform team, I want compatibility with current `userId/objectId` Permission Checking API. | Compatibility adapter or explicit limitation is implemented. | M |

## Epic 5: Cache Correctness

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-021 | As a platform team, I want local decision caching. | Cache key includes full auth tuple and policy version where available. | M |
| PV-022 | As a platform team, I want default TTLs for positive and negative decisions. | Positive TTL defaults to 30-60s; negative TTL defaults to 5-15s. | S |
| PV-023 | As an app team, I want per-route cache overrides. | Sensitive routes can disable cache or use shorter TTL. | M |
| PV-024 | As a platform team, I want event-driven cache invalidation. | Permission change event invalidates relevant cached decisions. | L |
| PV-025 | As a platform team, I want cache metrics. | Hit rate, miss rate, age, eviction, invalidation count are exposed. | S |

## Epic 6: Failure Behavior

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-026 | As a platform team, I want protected routes to fail closed by default. | Permission service errors, extraction errors, verification errors deny request. | S |
| PV-027 | As an app team, I want explicit fail-open for low-risk routes. | Fail-open requires route config and emits audit/metric event. | M |
| PV-028 | As an operator, I want clear error responses. | Missing context, invalid signature, timeout, and denied decision have consistent responses. | M |

## Epic 7: Observability And Audit

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-029 | As an operator, I want metrics for validation behavior. | Allow/deny/error latency/cache/fail-open metrics exposed. | M |
| PV-030 | As an operator, I want distributed tracing. | Trace spans cover sidecar, permission check, access management fetch, app forwarding. | M |
| PV-031 | As a security/support team, I want audit logs for important decisions. | Denied, fail-open, privileged/admin, config reload, malformed context events logged. | M |
| PV-032 | As support, I want a decision ID to correlate user reports. | Decision ID appears in logs and safe response metadata. | S |

## Epic 8: Deployment And Adoption

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-033 | As a platform team, I want Kubernetes deployment templates. | Sidecar can be deployed with app pods through standard manifests/Helm/Kustomize. | M |
| PV-034 | As an operator, I want health/readiness behavior. | Sidecar readiness reflects config load and dependency availability. | M |
| PV-035 | As an app team, I want a sample onboarding config. | Example app demonstrates public route, protected route, extraction, cache override. | S |
| PV-036 | As a platform team, I want dry-run/shadow mode for rollout. | Sidecar records decisions without enforcing, useful before fail-closed rollout. | M |

## Epic 9: Validation And Performance

| ID | User Story | Acceptance Criteria | Estimate |
|---|---|---|---|
| PV-037 | As a platform team, I want integration tests with fake services. | Tests cover allow, deny, missing context, invalid signature, service timeout. | M |
| PV-038 | As a platform team, I want security abuse tests. | Tests cover spoofed headers, duplicate headers, expired signatures, malformed tokens. | M |
| PV-039 | As a platform team, I want load testing against the SLO. | Test demonstrates target RPS and P95 latency under expected cache hit rates. | L |
| PV-040 | As a platform team, I want failure-mode tests. | Access Management down, Permission Checking down, cache cold, invalid config all covered. | M |

## Prioritized Backlog

Priority levels:

- **P0:** Required before implementation estimates can be trusted.
- **P1:** Required for a safe production pilot.
- **P2:** Important hardening or rollout capability after the first pilot path works.
- **P3:** Optional or app-dependent capability.

### P0: Foundation And Architecture Decisions

These stories should happen first because they shape the architecture, estimates, and implementation boundaries.

| Priority | ID | Rationale |
|---|---|---|
| P0 | PV-001 | Technology choice affects most downstream implementation work. |
| P0 | PV-002 | The authorization request contract must be stable before sidecar and Permission Checking work starts. |
| P0 | PV-003 | Route configuration drives extraction, enforcement, cache, and developer experience. |
| P0 | PV-016 | The trust boundary must be explicit before signed context handling is implemented. |

### P1: Safe Production Pilot

These stories form the recommended first pilot release. They provide protected-route enforcement, trusted context handling, basic caching, deployment, and enough observability to operate safely.

| Priority | ID | Rationale |
|---|---|---|
| P1 | PV-004 | Core enforcement path for protected routes. |
| P1 | PV-005 | Required so health checks and public routes continue working. |
| P1 | PV-006 | Required for secure default behavior. |
| P1 | PV-007 | Required for practical route-level adoption. |
| P1 | PV-008 | Most identity and tenant context will likely come from trusted headers. |
| P1 | PV-009 | Common resource lookup pattern for REST-style applications. |
| P1 | PV-010 | Low-cost support for common query-based resource references. |
| P1 | PV-012 | Required for fail-closed behavior when context is incomplete. |
| P1 | PV-013 | Required if client-carried or gateway-provided signed context is used. |
| P1 | PV-014 | Required to avoid unsafe operational handling of signing keys. |
| P1 | PV-015 | Required to prevent spoofing and ambiguous context attacks. |
| P1 | PV-017 | Core Permission Checking integration. |
| P1 | PV-018 | Required to stay within latency and reliability constraints. |
| P1 | PV-019 | Required enforcement behavior for denied requests. |
| P1 | PV-020 | Required if the existing Permission Checking Service cannot immediately accept the normalized contract. |
| P1 | PV-021 | Required for latency target feasibility. |
| P1 | PV-022 | Required to bound stale permission decisions. |
| P1 | PV-023 | Required so sensitive routes can tighten or disable caching. |
| P1 | PV-025 | Required to operate and tune cache behavior. |
| P1 | PV-026 | Required secure failure behavior. |
| P1 | PV-029 | Required minimum operational visibility. |
| P1 | PV-031 | Required minimum security/support visibility. |
| P1 | PV-033 | Required for Kubernetes adoption. |
| P1 | PV-034 | Required for safe rollout and orchestration. |
| P1 | PV-035 | Required for app-team adoption. |
| P1 | PV-037 | Required to validate the main happy path and common failures. |
| P1 | PV-038 | Required before handling signed or header-carried context in production. |
| P1 | PV-039 | Required to validate the latency and throughput SLO. |
| P1 | PV-040 | Required to verify fail-closed behavior under dependency failures. |

### P2: Production Hardening And Rollout Control

These stories are valuable after the pilot path is proven, or sooner if the pilot app needs the specific behavior.

| Priority | ID | Rationale |
|---|---|---|
| P2 | PV-024 | Event-driven invalidation improves revocation responsiveness but adds platform integration complexity. |
| P2 | PV-028 | Consistent error responses improve supportability and developer experience. |
| P2 | PV-030 | Distributed tracing is valuable for debugging latency and dependency behavior. |
| P2 | PV-032 | Decision IDs improve support correlation and audit workflows. |
| P2 | PV-036 | Dry-run/shadow mode reduces rollout risk for broader adoption. |

### P3: App-Dependent Extensions

These stories should be implemented only when a real adopter requires them.

| Priority | ID | Rationale |
|---|---|---|
| P3 | PV-011 | Body extraction is useful but materially increases latency, buffering, content-type, and security complexity. |
| P3 | PV-027 | Fail-open should remain rare and explicitly justified by low-risk use cases. |

## Recommended Implementation Sequence

1. **Architecture gate:** `PV-001`, `PV-002`, `PV-003`, `PV-016`
2. **Minimal safe enforcement:** `PV-004` to `PV-010`, `PV-012`, `PV-017` to `PV-020`, `PV-026`
3. **Security and cache readiness:** `PV-013` to `PV-015`, `PV-021` to `PV-023`, `PV-025`
4. **Pilot operations:** `PV-029`, `PV-031`, `PV-033` to `PV-035`
5. **Pilot validation:** `PV-037` to `PV-040`
6. **Post-pilot hardening:** `PV-024`, `PV-028`, `PV-030`, `PV-032`, `PV-036`
7. **App-dependent extensions:** `PV-011`, `PV-027`

## Suggested MVP Slice

For a first production pilot, estimate these first:

- `PV-001` to `PV-003`
- `PV-016`
- `PV-004` to `PV-010`
- `PV-012` to `PV-015`
- `PV-017` to `PV-023`
- `PV-025` to `PV-026`
- `PV-029`
- `PV-031`
- `PV-033` to `PV-035`
- `PV-037` to `PV-040`

Defer body extraction, event-driven invalidation, fail-open, tracing, and full shadow mode unless the pilot app truly needs them. Those items are useful, but they add complexity quickly.
