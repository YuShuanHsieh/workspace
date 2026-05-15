# Permission Validation Phase 1 User Stories

This document contains only the Phase 1 user stories for the Permission Validation Flow.

Estimate buckets:

- **S:** 1-2 days
- **M:** 3-5 days
- **L:** 1-2 weeks
- **XL:** Split or spike first

## Phase 1 Scope

Phase 1 delivers the minimum production validation flow under the simplifying assumption that **the client is trusted** to declare the resource it is acting on. There is no encrypted authorization context and no Access Management API call in the request path:

1. The client sends an action request to the application with headers for the user's SSO token and a single `objectId:objectType:action` context header.
2. The sidecar intercepts the request before it reaches the application backend.
3. The sidecar parses the context header into `objectId`, `objectType`, and `action`.
4. The sidecar sends a permission check request to Permission Checking Service with `objectId`, `objectType`, and `permission` (= the parsed `action`); the user's SSO token is forwarded in HTTP headers so PCS can identify the user.
5. The sidecar forwards granted requests to the application backend and rejects denied requests.

Trust model: PCS is the sole authority on whether the SSO user holds the requested permission on `(objectId, objectType)`. A client substituting an `objectId` they do not have permission on is harmless (PCS denies). A client substituting an `objectId` they *do* have permission on, while the application backend operates on a different object referenced in the URL/body, is an accepted Phase 1 risk — see "Out Of Scope" below and the related Phase 2 work on URL/body cross-checking.

## PV1-001: Compare Validation Topology Options

**Background/Goal:** The platform needs to decide which topology Phase 1 should use to intercept and validate requests. Three options are on the table: a custom HTTP-proxy sidecar in the request path, Envoy `ext_authz` calling a request-only authorization service, and Envoy `ext_proc` calling a processor that participates in both request and response phases. The decision affects routing configuration, header extraction, metrics, deployment, request and response forwarding, and forward-compatibility with the [Phase 1.5 metadata sync design](./phase-1-5-metadata-sync-design.md).

**Description:** Evaluate the three topology options against the Phase 1 validation flow:

- **Custom proxy sidecar.** Sidecar implements HTTP forwarding; route matching, context-header parsing, PCS call, and enforcement all live in sidecar code.
- **Envoy `ext_authz` + sidecar.** Envoy stays in the data path; sidecar exposes a request-only `Check` API. Route matching moves to Envoy; sidecar never sees the response.
- **Envoy `ext_proc` + sidecar.** Envoy stays in the data path; sidecar implements the `ExternalProcessor` bidirectional gRPC stream and can participate in both request and response phases.

The evaluation should cover implementation complexity, latency, Kubernetes deployment model, request and response forwarding, context-header parsing, route skip/protect configuration, SRE metrics support, and forward-compatibility with the Phase 1.5 Response Tap.

**Decision doc:** [phase-1-topology-decision.md](./phase-1-topology-decision.md)

**Acceptance Criteria:**

- All three options (custom proxy sidecar, Envoy `ext_authz`, Envoy `ext_proc`) are compared using the Phase 1 flow.
- The comparison covers latency impact, operational complexity, development effort, debugging experience, and fit for future Phase 2 caching/invalidation.
- The comparison explicitly addresses Phase 1.5 forward-compatibility: `ext_authz` is request-only and cannot satisfy the Phase 1.5 §3.2 invariant ("client receives `2xx` only after the event has been durably WAL-appended") without an additional response-side mechanism.
- Recommendation is documented with trade-offs and known risks.
- Any required proof-of-concept or benchmark scope is identified.

**Estimate Effort:** M

## PV1-002: Define Phase 1 Request Contract

**Background/Goal:** The client, sidecar, and Permission Checking Service need a shared contract before implementation starts.

**Description:** Define the Phase 1 request contract. The client sends a single context header `objectId:objectType:action` together with the user's SSO token. The sidecar parses the header and uses the three values to call Permission Checking Service.

**Decision doc:** [phase-1-request-contract.md](./phase-1-request-contract.md)

**Acceptance Criteria:**

- Required client request headers are documented (SSO token + combined context header).
- The combined context header format is `objectId:objectType:action` (literal `:` separator, three non-empty segments).
- `:` is disallowed inside any of the three segment values; values containing `:` are a rejection case.
- Sidecar-to-Permission-Checking request body is documented with `objectId`, `objectType`, and `permission`.
- Sidecar-to-Permission-Checking headers are documented, including forwarding the user's SSO token verbatim.
- The `action` segment is explicitly defined as user intent, not proof of permission.
- Missing or malformed required fields are defined as rejection cases.

**Estimate Effort:** S

## PV1-003: Define Context Header Format

**Background/Goal:** The sidecar reads `objectId`, `objectType`, and `action` from a single client-supplied header. The format must be unambiguous to parse and unambiguous to reject.

**Description:** Define the wire format of the context header value. Phase 1 uses a plain-text colon-separated triple: `objectId:objectType:action`. No encryption, no signing, no encoding — the client is trusted (see Phase 1 Scope trust model). The format and rejection rules must be tight enough that a malformed value never silently turns into a valid PCS call.

**Decision doc:** [phase-1-context-header-format.md](./phase-1-context-header-format.md)

**Acceptance Criteria:**

- Header value is exactly three non-empty segments separated by literal `:` characters.
- `:` is disallowed inside any segment value; segments containing `:` are rejected.
- Empty segments (e.g., `:foo:bar`, `foo::bar`, `foo:bar:`) are rejected.
- Leading or trailing whitespace on any segment is rejected (no trimming).
- Maximum total header length is bounded and documented; over-length values are rejected.
- All rejection conditions are enumerated with reason labels for SRE metrics.

**Estimate Effort:** S

## PV1-004: Define Protected And Skipped Path Configuration

**Background/Goal:** Application developers need a simple way to declare which application paths require permission validation and which paths should be skipped.

**Description:** Define a minimal path configuration schema for Phase 1. The schema should support protected routes and skipped routes using HTTP method and path matching. Advanced extraction rules, body parsing, fail-open behavior, and cache controls are out of Phase 1 scope.

**Decision doc:** [phase-1-route-config-schema.md](./phase-1-route-config-schema.md)

**Acceptance Criteria:**

- Configuration supports protected routes with HTTP method and path pattern.
- Configuration supports skipped routes with HTTP method and path pattern.
- Health check and public asset examples are included as skipped routes.
- Default behavior for unmatched routes is documented.
- The schema is simple enough for application teams to adopt without custom code.

**Estimate Effort:** M

## PV1-005: Implement Route Matching

**Background/Goal:** The sidecar must know whether each incoming request should be validated or skipped.

**Description:** Implement Phase 1 route matching based on the protected/skipped path configuration. Skipped requests are forwarded directly to the application backend. Protected requests continue through the permission validation flow.

**Acceptance Criteria:**

- Sidecar loads protected/skipped route configuration.
- Requests matching skipped routes are forwarded without calling Permission Checking Service.
- Requests matching protected routes enter the validation flow.
- Behavior for unmatched routes follows the documented default.
- Route matching is covered by tests for method and path combinations.

**Estimate Effort:** M

## PV1-006: Extract Required Request Headers

**Background/Goal:** Phase 1 intentionally avoids flexible context extraction. The sidecar only needs to read the user's SSO token and the combined context header.

**Description:** Implement header extraction for protected requests. The sidecar should extract the SSO token and the combined `objectId:objectType:action` context header. It should reject protected requests when required headers are missing.

**Acceptance Criteria:**

- Sidecar extracts user's SSO token from the configured header.
- Sidecar extracts the combined context header from the configured header.
- Missing SSO token or context header causes rejection.
- Header presence errors are counted in SRE metrics.

**Estimate Effort:** S

## PV1-007: Parse And Validate Context Header

**Background/Goal:** The sidecar must extract `objectId`, `objectType`, and `action` from the context header before calling Permission Checking Service. Phase 1 has no encryption — the parser is the only line of defense against malformed values reaching PCS.

**Description:** Implement parsing and validation of the context header per PV1-003. The sidecar splits the header on `:`, validates that exactly three non-empty segments are present, and rejects any value that violates the format rules.

**Acceptance Criteria:**

- Sidecar splits the context header on `:` and requires exactly three non-empty segments.
- Sidecar rejects values containing extra `:` characters in any segment.
- Sidecar rejects values with empty segments, leading/trailing whitespace, or over the maximum length.
- The parsed `objectId`, `objectType`, and `action` are passed to permission checking unchanged.
- Parse failures are counted in SRE metrics with the reason label from PV1-003.

**Estimate Effort:** S

## PV1-008: Build Permission Checking Request

**Background/Goal:** The sidecar must translate the intercepted request into the Permission Checking Service contract.

**Description:** Build the outbound request to Permission Checking Service using the parsed context-header values. The JSON payload contains `objectId`, `objectType`, and `permission`. The user's SSO token is forwarded in HTTP headers for identity validation by Permission Checking Service.

**Acceptance Criteria:**

- Sidecar sends `objectId`, `objectType`, and `permission` in the JSON payload.
- `objectId`, `objectType`, and `permission` come from the parsed context header.
- User's SSO token is forwarded in HTTP headers.
- Permission Checking Service validation errors are handled as request rejection.

**Estimate Effort:** M

## PV1-009: Enforce Permission Decision

**Background/Goal:** The sidecar is responsible for allowing granted requests and rejecting denied requests before the application backend is reached.

**Description:** Implement enforcement behavior based on Permission Checking Service responses. Granted requests are forwarded to the application backend. Denied requests are rejected by the sidecar.

**Acceptance Criteria:**

- Granted permission checks are forwarded to the application backend.
- Denied permission checks return `403 Forbidden`.
- Permission Checking Service timeout or error causes rejection by default.
- Rejected requests do not reach the application backend.
- Allow, deny, and error outcomes are counted in SRE metrics.

**Estimate Effort:** M

## PV1-010: Add Minimal SRE Metrics

**Background/Goal:** SRE needs basic visibility into traffic, validation outcomes, failures, and latency before Phase 1 can be operated safely.

**Description:** Expose minimal metrics for Phase 1 sidecar operation. Metrics should help answer whether validation is healthy, how much latency it adds, and why requests are being rejected.

**Acceptance Criteria:**

- Metrics include total validation requests.
- Metrics include allow, deny, and error counts.
- Metrics include sidecar validation latency.
- Metrics include Permission Checking Service latency.
- Metrics include missing-header count.
- Metrics include context-header parse-failure count, broken down by reason label from PV1-003.
- Metrics are documented for SRE consumers.

**Estimate Effort:** M

## PV1-011: Add Phase 1 Integration Tests

**Background/Goal:** The Phase 1 flow must be validated end to end before pilot adoption.

**Description:** Add integration tests using fake application backend and fake Permission Checking Service. Tests should cover successful forwarding, denied requests, missing headers, malformed context-header values, and dependency failures.

**Acceptance Criteria:**

- Test covers granted request forwarded to backend.
- Test covers denied request returning `403`.
- Test covers missing required headers.
- Test covers malformed context header (extra `:`, empty segment, over-length, whitespace).
- Test covers Permission Checking Service timeout or error.
- Test verifies rejected requests do not reach the backend.

**Estimate Effort:** M

## PV1-012: Create Phase 1 Onboarding Example

**Background/Goal:** Application teams need a concrete example to adopt Phase 1 without guessing how the pieces fit together.

**Description:** Create a minimal onboarding example showing route configuration, client request headers, context header format, and sidecar-to-Permission-Checking request shape.

**Acceptance Criteria:**

- Example includes protected and skipped route configuration.
- Example shows client request headers for SSO token and the combined `objectId:objectType:action` context header.
- Example calls out the Phase 1 trust assumption (client is trusted to declare `objectId`/`objectType`).
- Example shows the sidecar-to-Permission-Checking payload and headers.
- Example documents common rejection cases.

**Estimate Effort:** S

## Out Of Scope For Phase 1

- Encrypted or signed authorization context (deferred; Phase 1 trusts client-supplied `objectId`/`objectType`).
- Access Management API in the request path.
- App credential provisioning and key management.
- Decision caching.
- Event-driven cache invalidation.
- Body extraction.
- Query parameter extraction.
- General path parameter extraction for permission context.
- Fail-open behavior.
- Distributed tracing.
- Detailed audit logging.
- Per-route cache behavior.
- Advanced route/action mapping.
- Validation that the application path / body object ID matches the context-header `objectId`.
