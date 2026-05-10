# Product Requirements Document: Permission Validation Flow

**Status:** Draft
**Target Audience:** Platform Team, Application Development Teams
**Related Documents:** [Draft Notes](./draft.md)

---

## 1. Background

Currently, our platform provides two centralized services to handle permission and access control for various applications:

1. **Access Management Service:** Provides permission management functionalities. It acts as the source of truth for permission contexts, such as user attributes, roles, and resource metadata. 
2. **Permission Checking Service:** Provides high-throughput permission evaluation. It exposes an HTTP POST endpoint that accepts a JSON payload `{"userId": "<id>", "objectId": "<id>"}` and returns whether the access is granted or denied.

Currently, application developers must manually set up their app's permissions via the Access Management Service and then embed logic within their own applications to call both the Access Management and Permission Checking services at runtime. This leads to duplicated effort, inconsistent implementations, and heavy boilerplate code for application teams.

## 2. Purpose

The goal of this initiative is to provide a seamless, platform-managed **Access Control Validation Flow** that automatically enforces permissions before end-user requests ever reach the application service. 

By abstracting this validation layer, we allow:
- **Application Developers** to simply "plug in" their permissions via declarative configurations without writing repetitive auth logic.
- **End-Users** (or App Admins) to seamlessly configure access control rules for their resources.
- **The Platform Team** to centrally manage, scale, and secure the permission validation lifecycle.

## 3. Goals & Non-Goals

### Goals
- Implement an automated permission validation flow that intercepts application traffic.
- Decouple permission validation logic from the core application business logic.
- Ensure the solution scales to support sudden traffic bursts and high request volumes.
- Ensure required authorization context is extracted and normalized before validation, including `userId`, `tenantId`, `appId`, `resourceType`, `objectId`, and `action`.
- Provide a declarative route configuration model for application teams to define protected routes, public routes, context extraction rules, cache behavior, and failure behavior.

### Non-Goals
- Modifying the internal evaluation logic of the existing Permission Checking Service.
- Managing user authentication (e.g., login, password resets) — this flow assumes the user is already authenticated and identity (e.g., `userId`) is provided via headers/tokens.

## 4. SLOs & Performance Requirements

Given that this flow sits in the critical path of every user request, performance and reliability are paramount.

- **Peak Traffic:** Must handle up to **5,000 Requests Per Second (RPS)** per application cluster without degradation.
- **Latency Budget:** The entire permission validation flow (including context gathering and checking) must add **< 10ms** to the overall request latency (P95).
- **Availability:** 99.99% uptime.

## 5. Proposed Architecture: Sidecar Pattern

Since our deployment environment is Kubernetes, we will utilize the **Sidecar Pattern** for the permission validation flow. 

A lightweight proxy/agent will be deployed alongside the application container within the same Kubernetes Pod. All incoming traffic to the application will first route through this sidecar.

### 5.1 Architecture Components

1. **Validation Sidecar (Proxy):** Intercepts incoming HTTP requests, extracts and verifies required authorization context, and communicates with the Permission Checking Service.
2. **Context Extraction:** The sidecar will extract required authorization context from declarative sources such as trusted headers, path parameters, query parameters, and selected request bodies where explicitly configured.
3. **Signed Context Verification:** If authorization context is carried by the client or provided by an upstream gateway, the sidecar must verify that the context was signed by a trusted issuer before using it. Client-provided unsigned or self-signed context must be treated as untrusted.
4. **Local Cache (Context & Decisions):** To meet the strict < 10ms latency SLO, the sidecar will implement intelligent local caching for permission checking decisions and context, reducing the need to make external network calls on every single request.

### 5.2 The Validation Flow

1. **Intercept:** An external user sends a request to the application. The Sidecar intercepts it.
2. **Context Extraction:** The Sidecar parses the request to extract the required context.
   - **Option 1 (Header Injection):** The permission context (e.g., user attributes, roles) is already injected into the HTTP header by an upstream gateway. The sidecar must decode and parse this header content.
   - **Option 2 (API Fetch):** The sidecar fetches the permission context dynamically by calling the Access Management API directly.
   The sidecar also extracts request-specific context such as `objectId` and `action` based on the route's declarative extraction rules.
3. **Signature Verification:** If context is supplied through signed headers or signed tokens, the sidecar verifies that it was signed by a trusted issuer and validates signature, issuer, audience, expiration, and key version before trusting the context.
4. **Cache Check:** The Sidecar checks its local cache for a recent, valid permission decision for the normalized authorization tuple (`userId`, `tenantId`, `appId`, `resourceType`, `objectId`, `action`, and policy version where available).
5. **Evaluation:** If not cached, the Sidecar sends a normalized authorization request to the **Permission Checking Service** for evaluation.
   ```json
   {
     "userId": "<id>",
     "tenantId": "<id>",
     "appId": "<id>",
     "resourceType": "<type>",
     "objectId": "<id>",
     "action": "<action>"
   }
   ```
   If the Permission Checking Service v1 only supports `{"userId", "objectId"}`, the implementation must define a compatibility adapter or explicitly limit v1 support to permissions that can be safely represented by those two fields.
6. **Enforcement:**
   - **Allowed:** The Sidecar proxies the request to the application container.
   - **Denied:** The Sidecar immediately returns a `403 Forbidden` response to the user. The application is never reached.

### 5.3 Declarative Route Configuration

Application teams will adopt the validation flow through a declarative configuration managed by the platform. The configuration must support:

- **Public routes:** Routes that bypass permission validation, such as health checks or public assets.
- **Protected routes:** Routes that require permission validation before the request reaches the application.
- **Extraction rules:** How to derive `userId`, `tenantId`, `appId`, `resourceType`, `objectId`, and `action` from trusted headers, path parameters, query parameters, or explicitly allowed request body fields.
- **Failure behavior:** Per-route behavior for validation failures. The default for protected routes is fail-closed.
- **Cache behavior:** Per-route cache eligibility and TTL overrides for sensitive operations.

If the sidecar is enabled and no route rule matches a request, the default behavior must be conservative: deny the request unless the route is explicitly configured as public.

### 5.4 Cache Correctness

The sidecar may cache permission decisions, but cache behavior must be bounded and observable:

- Positive decisions should use a short default TTL, recommended at 30-60 seconds.
- Negative decisions should use a shorter default TTL, recommended at 5-15 seconds.
- Privileged or admin actions should either bypass the decision cache or use a very short TTL with policy-version checks.
- Permission revocation should support event-driven invalidation where available, such as through NATS or another platform event bus.
- The implementation must define the maximum acceptable staleness for permission changes and expose cache hit rate, cache age, and invalidation metrics.

### 5.5 Security And Failure Behavior

The validation flow must fail closed by default. If the Permission Checking Service, Access Management Service, context verification, or extraction step fails, protected routes must return an authorization failure response instead of forwarding the request to the application.

Fail-open behavior may be enabled only through explicit per-route configuration for low-risk routes. Fail-open events must be audited and exposed through metrics.

Signed context is acceptable only when the trust boundary is explicit and the signature is produced by a trusted issuer, such as the platform gateway or identity service. The sidecar must verify signatures using approved keys, enforce issuer and audience checks, reject expired context, support key rotation, and ignore unsigned, self-signed, or duplicate client-supplied context headers. Transport-level protection, such as TLS or mTLS between trusted infrastructure components, is still required.

### 5.6 Observability And Auditability

The platform must provide enough visibility for operations, support, and security teams to understand authorization behavior in production:

- Metrics for allowed requests, denied requests, validation latency, cache hit rate, cache invalidation, upstream errors, malformed context, and fail-open events.
- Distributed tracing across sidecar validation, Access Management calls, Permission Checking calls, and application forwarding.
- Audit logs for denied decisions, fail-open events, privileged/admin access checks, configuration reloads, and validation errors.
- A decision identifier in logs and response metadata where safe, so support teams can correlate user reports with validation decisions.
- Logs must avoid sensitive tokens, raw signed context, and unnecessary personally identifiable information.

## 6. Constraints & Considerations

- **Team Boundaries:** We are the platform team. We must design a developer experience that is extremely easy for other application teams to adopt. The configuration schema must be intuitive.
- **Network Overhead:** Network calls between the sidecar and the Permission Checking Service must be highly optimized (e.g., connection pooling, keep-alives) to ensure we stay within the 10ms budget.
- **Proxy Technology:** Flexible context extraction does not automatically require a fully custom proxy. Envoy with `ext_authz`, route metadata, and a custom authorization service remains a strong candidate. A custom sidecar should be chosen only if Envoy cannot meet the required extraction, caching, latency, or operational requirements.

## 7. Open Questions / Next Steps for Design

*To finalize the technical design, the following points will need to be evaluated in the implementation phase:*

1. **Sidecar Technology Evaluation:** Both a custom lightweight proxy (e.g., in Go or Rust) and an existing service mesh tool like Envoy (using the `ext_authz` filter) are viable options. The final decision will be based on benchmarking latency (< 10ms SLO), extraction flexibility, cache requirements, and operational overhead.
2. **Cache Invalidation:** How will the sidecar know when to invalidate its cache if a user's permission suddenly changes in the Access Management service? The baseline recommendation is short TTLs plus event-driven invalidation where available.
