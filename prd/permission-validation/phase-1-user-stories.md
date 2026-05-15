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

**Decision doc:** [phase-1-topology-decision.md](./phase-1-topology-decision.md)

**User Story:**
> As an SRE, I want the Phase 1 validation topology chosen based on operational complexity, latency, and forward-compatibility with Phase 1.5, so I can support the sidecar in production without a re-platforming later.

**Acceptance Criteria:**

- A recommendation is documented with trade-offs and known risks.
- The recommended option is operable by SRE (clear deployment model, debuggable, observable).
- The recommended option preserves the Phase 1.5 §3.2 invariant ("client receives `2xx` only after the event has been durably WAL-appended").

**Tasks:**

1. Compare all three options (custom proxy sidecar, Envoy `ext_authz`, Envoy `ext_proc`) against the Phase 1 flow.
2. Cover latency impact, operational complexity, development effort, debugging experience, and fit for future Phase 2 caching/invalidation.
3. Explicitly address Phase 1.5 forward-compatibility, noting that `ext_authz` is request-only and cannot satisfy the Phase 1.5 §3.2 invariant without an additional response-side mechanism.
4. Identify any required proof-of-concept or benchmark scope.

**Estimate Effort:** M

## PV1-002: Define Phase 1 Request Contract

**Background/Goal:** The client, sidecar, and Permission Checking Service need a shared contract before implementation starts.

**Decision doc:** [phase-1-request-contract.md](./phase-1-request-contract.md)

**User Story:**
> As an application developer, I want a documented request contract for the sidecar (which client headers to send and what the sidecar sends to PCS), so my client code knows exactly what to produce and I can debug request failures.

**Acceptance Criteria:**

- An application developer can read the contract and produce valid client requests without reading sidecar source code.
- An application developer can read the contract and predict which requests will be rejected versus forwarded.
- The contract makes clear that `action` is user intent, not proof of permission.

**Tasks:**

1. Document required client request headers (SSO token + combined context header).
2. Specify the combined context header format as `objectId:objectType:action` (literal `:` separator, three non-empty segments).
3. Document that `:` is disallowed inside any of the three segment values; values containing `:` are a rejection case.
4. Document the sidecar-to-Permission-Checking request body (`objectId`, `objectType`, `permission`).
5. Document sidecar-to-Permission-Checking headers, including forwarding the user's SSO token verbatim.
6. Enumerate missing or malformed required fields as rejection cases.

**Estimate Effort:** S

## PV1-003: Define Context Header Format

**Background/Goal:** The sidecar reads `objectId`, `objectType`, and `action` from a single client-supplied header. The format must be unambiguous to parse and unambiguous to reject. Phase 1 uses a plain-text colon-separated triple: `objectId:objectType:action`. No encryption, no signing, no encoding — the client is trusted (see Phase 1 Scope trust model).

**Decision doc:** [phase-1-context-header-format.md](./phase-1-context-header-format.md)

**User Story:**
> As an application developer, I want an unambiguous context header format with clear rejection rules, so I know how to construct valid values and can diagnose rejections without reading sidecar internals.

**Acceptance Criteria:**

- The format is tight enough that a malformed value never silently turns into a valid PCS call.
- An application developer can construct a valid header from spec alone.
- Every rejection reason is named, so an SRE looking at a parse-failure metric can tell *why* it was rejected.

**Tasks:**

1. Specify the header value as exactly three non-empty segments separated by literal `:` characters.
2. Specify that `:` is disallowed inside any segment value; segments containing `:` are rejected.
3. Specify that empty segments (e.g., `:foo:bar`, `foo::bar`, `foo:bar:`) are rejected.
4. Specify that leading or trailing whitespace on any segment is rejected (no trimming).
5. Define and document a maximum total header length; over-length values are rejected.
6. Enumerate all rejection conditions with reason labels for SRE metrics.

**Estimate Effort:** S

## PV1-004: Define Protected And Skipped Path Configuration

**Background/Goal:** Application developers need a simple way to declare which application paths require permission validation and which paths should be skipped. Advanced extraction rules, body parsing, fail-open behavior, and cache controls are out of Phase 1 scope.

**Decision doc:** [phase-1-route-config-schema.md](./phase-1-route-config-schema.md)

**User Story:**
> As an application developer, I want to declare protected and skipped routes through a simple configuration schema, so I can onboard the sidecar to my service without writing custom code.

**Acceptance Criteria:**

- An application developer can adopt the schema without writing any sidecar-specific code.
- The schema covers the common need (e.g., protect API routes, skip health checks and public assets).
- Default behavior for unmatched routes is documented and unambiguous.

**Tasks:**

1. Support protected routes with HTTP method and path pattern.
2. Support skipped routes with HTTP method and path pattern.
3. Include health check and public asset examples as skipped routes.
4. Document the default behavior for unmatched routes.

**Estimate Effort:** M

## PV1-005: Implement Route Matching

**Background/Goal:** The sidecar must know whether each incoming request should be validated or skipped, based on the protected/skipped configuration from PV1-004.

**User Story:**
> As an application developer, I want my declared protected routes validated and skipped routes bypassed, so traffic goes through the right path without me writing per-route auth code.

**Acceptance Criteria:**

- Requests matching skipped routes reach the backend without a Permission Checking Service call.
- Requests matching protected routes enter the validation flow.
- Unmatched routes follow the documented default behavior.

**Tasks:**

1. Load protected/skipped route configuration at sidecar startup.
2. Implement skip-route forwarding path.
3. Implement protected-route validation entry.
4. Apply the documented default for unmatched routes.
5. Cover method/path combinations with tests.

**Estimate Effort:** M

## PV1-006: Extract Required Request Headers

**Background/Goal:** Phase 1 intentionally avoids flexible context extraction. The sidecar only needs to read the user's SSO token and the combined context header.

**User Story:**
> As an SRE, I want protected requests with missing required headers rejected cleanly and counted in metrics, so I can quickly spot misconfigured clients without inspecting payloads.

**Acceptance Criteria:**

- Protected requests missing the SSO token or context header are rejected.
- A missing-header count appears in SRE metrics.
- Skipped routes are unaffected.

**Tasks:**

1. Extract the user's SSO token from the configured header.
2. Extract the combined context header from the configured header.
3. Reject protected requests when either required header is missing.
4. Count header-presence errors in SRE metrics.

**Estimate Effort:** S

## PV1-007: Parse And Validate Context Header

**Background/Goal:** The sidecar must extract `objectId`, `objectType`, and `action` from the context header before calling Permission Checking Service. Phase 1 has no encryption — the parser is the only line of defense against malformed values reaching PCS.

**User Story:**
> As an SRE, I want malformed context headers rejected at the sidecar with labeled metrics, so bad client input never reaches Permission Checking Service and I can identify which clients are sending which kind of bad value.

**Acceptance Criteria:**

- A malformed context header never produces a PCS call.
- The parsed `objectId`, `objectType`, and `action` are passed to permission checking unchanged when the header is valid.
- Each parse failure is counted with the reason label from PV1-003.

**Tasks:**

1. Split the context header on `:` and require exactly three non-empty segments.
2. Reject values containing extra `:` characters in any segment.
3. Reject values with empty segments, leading/trailing whitespace, or over the maximum length.
4. Pass valid parsed values to the permission checking step unchanged.
5. Count parse failures in SRE metrics, broken down by reason label from PV1-003.

**Estimate Effort:** S

## PV1-008: Build Permission Checking Request

**Background/Goal:** The sidecar must translate the intercepted request into the Permission Checking Service contract.

**User Story:**
> As an application developer, I want my protected requests translated into Permission Checking Service calls using the values I sent, so authorization decisions reflect the actual `objectId`, `objectType`, and `action` from my client.

**Acceptance Criteria:**

- The outbound PCS payload uses the values parsed from the client's context header, not modified or substituted values.
- The user's SSO token from the client reaches PCS unchanged.
- Permission Checking Service validation errors result in request rejection rather than silent forwarding.

**Tasks:**

1. Build the JSON payload with `objectId`, `objectType`, and `permission` from the parsed context header.
2. Forward the user's SSO token in HTTP headers on the PCS call.
3. Handle PCS validation errors as request rejection.

**Estimate Effort:** M

## PV1-009: Enforce Permission Decision

**Background/Goal:** The sidecar is responsible for allowing granted requests and rejecting denied requests before the application backend is reached.

**User Story:**
> As an application developer, I want the sidecar to allow granted requests through to my backend and reject denied requests at the sidecar, so my backend code never has to enforce authorization itself.

**Acceptance Criteria:**

- Granted requests reach the application backend.
- Denied requests return `403 Forbidden`.
- Rejected requests never reach the application backend.
- Permission Checking Service timeouts or errors result in rejection by default.
- Allow, deny, and error outcomes appear in SRE metrics.

**Tasks:**

1. Forward granted permission checks to the application backend.
2. Return `403 Forbidden` for denied permission checks.
3. Treat Permission Checking Service timeout or error as rejection by default.
4. Count allow, deny, and error outcomes in SRE metrics.

**Estimate Effort:** M

## PV1-010: Add Minimal SRE Metrics

**Background/Goal:** SRE needs basic visibility into traffic, validation outcomes, failures, and latency before Phase 1 can be operated safely.

**User Story:**
> As an SRE, I want metrics for traffic volume, validation outcomes, failure reasons, and latency, so I can answer "is validation healthy?", "how much latency does it add?", and "why are requests being rejected?" without reading logs.

**Acceptance Criteria:**

- SRE can determine the rate of allow, deny, and error outcomes from metrics alone.
- SRE can determine sidecar and Permission Checking Service latency contributions separately.
- SRE can determine why parse failures are happening from the reason-label breakdown.
- All exposed metrics are documented for SRE consumers.

**Tasks:**

1. Expose total validation requests.
2. Expose allow, deny, and error counts.
3. Expose sidecar validation latency.
4. Expose Permission Checking Service latency.
5. Expose missing-header count.
6. Expose context-header parse-failure count, broken down by reason label from PV1-003.
7. Document the exposed metrics for SRE consumers.

**Estimate Effort:** M

## PV1-011: Add Phase 1 Integration Tests

**Background/Goal:** The Phase 1 flow must be validated end to end before pilot adoption.

**User Story:**
> As an application developer, I want end-to-end tests covering granted, denied, malformed, and dependency-failure cases against fake backends, so I can trust the Phase 1 flow before piloting it on my service.

**Acceptance Criteria:**

- Granted, denied, missing-header, malformed-context-header, and PCS-failure cases are all covered.
- Tests verify that rejected requests never reach the backend.
- Tests run against fake application backend and fake Permission Checking Service.

**Tasks:**

1. Add a test for a granted request forwarded to the backend.
2. Add a test for a denied request returning `403`.
3. Add tests for missing required headers.
4. Add tests for malformed context header (extra `:`, empty segment, over-length, whitespace).
5. Add a test for Permission Checking Service timeout or error.
6. Verify rejected requests do not reach the backend.

**Estimate Effort:** M

## PV1-012: Create Phase 1 Onboarding Example

**Background/Goal:** Application teams need a concrete example to adopt Phase 1 without guessing how the pieces fit together.

**User Story:**
> As an application developer, I want a minimal onboarding example showing route configuration, client headers, context header format, and the sidecar-to-PCS shape, so I can adopt Phase 1 by copy-and-adapt rather than guessing.

**Acceptance Criteria:**

- Following the example end-to-end produces a working Phase 1 integration.
- The example surfaces the Phase 1 trust assumption so adopters do not misinterpret the security boundary.
- Common rejection cases are visible to the reader.

**Tasks:**

1. Include protected and skipped route configuration in the example.
2. Show client request headers for SSO token and the combined `objectId:objectType:action` context header.
3. Call out the Phase 1 trust assumption (client is trusted to declare `objectId`/`objectType`).
4. Show the sidecar-to-Permission-Checking payload and headers.
5. Document common rejection cases.

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
