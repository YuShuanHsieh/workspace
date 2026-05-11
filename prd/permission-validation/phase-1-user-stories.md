# Permission Validation Phase 1 User Stories

This document contains only the Phase 1 user stories for the Permission Validation Flow.

Estimate buckets:

- **S:** 1-2 days
- **M:** 3-5 days
- **L:** 1-2 weeks
- **XL:** Split or spike first

## Phase 1 Scope

Phase 1 delivers the minimum production validation flow:

1. The client calls Access Management API with `objectId` and `objectType`.
2. Access Management API returns an encrypted authorization context and a plain permission list for UI display.
3. The client sends an action request to the application with headers for the user's SSO token, encrypted authorization context, and requested action.
4. The sidecar intercepts the request before it reaches the application backend.
5. The sidecar decrypts the authorization context using the app credential provisioned during app registration.
6. The sidecar sends a permission check request to Permission Checking Service with `objectId`, `objectType`, and requested `permission`; the user's SSO token is forwarded in HTTP headers.
7. The sidecar forwards granted requests to the application backend and rejects denied requests.

## PV1-001: Compare Envoy And Custom Sidecar

**Background/Goal:** The platform needs to decide whether Phase 1 should use Envoy `ext_authz` with an authorization service or a custom sidecar. This decision affects routing, decryption, header extraction, metrics, deployment, and future extensibility.

**Description:** Evaluate Envoy-based and custom-sidecar-based approaches against the Phase 1 validation flow. The evaluation should focus on implementation complexity, latency, Kubernetes deployment model, request forwarding behavior, encrypted context handling, route skip/protect configuration, and SRE metrics support.

**Acceptance Criteria:**

- Envoy `ext_authz` and custom sidecar options are compared using the Phase 1 flow.
- The comparison covers latency impact, operational complexity, development effort, debugging experience, and fit for future Phase 2 needs.
- Recommendation is documented with trade-offs and known risks.
- Any required proof-of-concept or benchmark scope is identified.

**Estimate Effort:** M

## PV1-002: Define Phase 1 Request Contract

**Background/Goal:** The client, sidecar, Access Management API, and Permission Checking Service need a shared contract before implementation starts.

**Description:** Define the Phase 1 request and response contract. The client receives an encrypted authorization context and plain permissions from Access Management API. The client sends the encrypted context, requested action, and user's SSO token to the application request. The sidecar uses decrypted context plus requested action to call Permission Checking Service.

**Acceptance Criteria:**

- Access Management API response contract is documented.
- Required client request headers are documented.
- Sidecar-to-Permission-Checking request body is documented with `objectId`, `objectType`, and `permission`.
- Sidecar-to-Permission-Checking headers are documented, including forwarding the user's SSO token.
- The requested action header is explicitly defined as user intent, not proof of permission.
- Missing or malformed required fields are defined as rejection cases.

**Estimate Effort:** M

## PV1-003: Define Encrypted Authorization Context Format

**Background/Goal:** The encrypted authorization context is the trusted source for `objectId` and `objectType` in Phase 1. It must be tamper-resistant and decryptable only by trusted platform components.

**Description:** Define the encrypted payload format and encryption requirements. The payload should be encrypted with symmetric app credentials provisioned during app registration and should use authenticated encryption so the sidecar can detect tampering.

**Acceptance Criteria:**

- Encrypted payload includes `appId`, `objectId`, `objectType`, `issuedAt`, `expiresAt`, and `keyId`.
- Encryption uses an authenticated encryption mode or equivalent tamper-proof envelope.
- App credential ownership and provisioning responsibilities are documented.
- Expired, undecryptable, malformed, or wrong-audience contexts are rejected.
- The plain permission list is documented as UI display data only.

**Estimate Effort:** M

## PV1-004: Define Protected And Skipped Path Configuration

**Background/Goal:** Application developers need a simple way to declare which application paths require permission validation and which paths should be skipped.

**Description:** Define a minimal path configuration schema for Phase 1. The schema should support protected routes and skipped routes using HTTP method and path matching. Advanced extraction rules, body parsing, fail-open behavior, and cache controls are out of Phase 1 scope.

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

**Background/Goal:** Phase 1 intentionally avoids flexible context extraction. The sidecar only needs to read the user's SSO token, encrypted authorization context, and requested action from headers.

**Description:** Implement header extraction for protected requests. The sidecar should extract the SSO token, encrypted context, and requested action. It should reject protected requests when required headers are missing or malformed.

**Acceptance Criteria:**

- Sidecar extracts user's SSO token from the configured header.
- Sidecar extracts encrypted authorization context from the configured header.
- Sidecar extracts requested action from the configured header.
- Missing SSO token, encrypted context, or requested action causes rejection.
- Header parsing errors are counted in SRE metrics.

**Estimate Effort:** S

## PV1-007: Decrypt And Validate Authorization Context

**Background/Goal:** The sidecar must recover trusted `objectId` and `objectType` from the encrypted authorization context before calling Permission Checking Service.

**Description:** Implement decryption and validation of the encrypted authorization context using the app credential. The sidecar should reject protected requests if the context cannot be decrypted or fails validation.

**Acceptance Criteria:**

- Sidecar decrypts authorization context using the app credential.
- Sidecar validates required fields: `appId`, `objectId`, `objectType`, `issuedAt`, `expiresAt`, and `keyId`.
- Expired context is rejected.
- Tampered or undecryptable context is rejected.
- The decrypted `objectId` and `objectType` are used for permission checking.
- Decryption failures are counted in SRE metrics.

**Estimate Effort:** L

## PV1-008: Build Permission Checking Request

**Background/Goal:** The sidecar must translate the intercepted request into the Permission Checking Service contract.

**Description:** Build the outbound request to Permission Checking Service using trusted decrypted context and requested action. The JSON payload contains `objectId`, `objectType`, and `permission`. The user's SSO token is forwarded in HTTP headers for identity validation by Permission Checking Service.

**Acceptance Criteria:**

- Sidecar sends `objectId`, `objectType`, and `permission` in the JSON payload.
- `objectId` and `objectType` come from decrypted context.
- `permission` comes from the requested action header.
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
- Metrics include missing or invalid header count.
- Metrics include decryption failure count.
- Metrics are documented for SRE consumers.

**Estimate Effort:** M

## PV1-011: Add Phase 1 Integration Tests

**Background/Goal:** The Phase 1 flow must be validated end to end before pilot adoption.

**Description:** Add integration tests using fake application backend and fake Permission Checking Service. Tests should cover successful forwarding, denied requests, malformed requests, decryption failures, and dependency failures.

**Acceptance Criteria:**

- Test covers granted request forwarded to backend.
- Test covers denied request returning `403`.
- Test covers missing required headers.
- Test covers malformed or expired encrypted context.
- Test covers Permission Checking Service timeout or error.
- Test verifies rejected requests do not reach the backend.

**Estimate Effort:** M

## PV1-012: Create Phase 1 Onboarding Example

**Background/Goal:** Application teams need a concrete example to adopt Phase 1 without guessing how the pieces fit together.

**Description:** Create a minimal onboarding example showing route configuration, client request headers, encrypted context expectations, and sidecar-to-Permission-Checking request shape.

**Acceptance Criteria:**

- Example includes protected and skipped route configuration.
- Example shows client request headers for SSO token, encrypted context, and requested action.
- Example explains that plain permissions are for UI display only.
- Example shows the sidecar-to-Permission-Checking payload and headers.
- Example documents common rejection cases.

**Estimate Effort:** S

## Out Of Scope For Phase 1

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
- Automatic key rotation.
- Validation that application path object ID matches decrypted `objectId`.
