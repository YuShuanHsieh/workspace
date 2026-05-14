# Phase 1 Permission Validation — Design Deliverables Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Produce the four Phase 1 design deliverable docs (PV1-001 through PV1-004) under `prd/permission-validation/`, unblocking the Phase 1 sidecar implementation work and formally landing on Envoy `ext_proc` as the chosen topology.

**Architecture:** Each user story becomes one Markdown design doc in the existing `prd/permission-validation/` directory. Docs cross-link to the user stories, the existing architecture doc, the Phase 1.5 metadata-sync design (for forward-compat constraints), and the PRD. After the four design docs land, update `phase-1-user-stories.md` and `phase-1-architecture.md` to link to them and reflect that PV1-001 is decided.

**Tech Stack:** Markdown (GitHub-flavored / CommonMark) + Mermaid diagrams. No code. Git for review and commits. Branch: `docs/prd` (already current).

---

## File Structure

**Create:**

- `prd/permission-validation/phase-1-topology-decision.md` — PV1-001. Three-way comparison of custom proxy sidecar (A), Envoy `ext_authz` + sidecar (C), Envoy `ext_proc` + sidecar (B); recommends Option B with rationale, trade-offs, and POC scope.
- `prd/permission-validation/phase-1-request-contract.md` — PV1-002. AM-API response shape, client request headers, sidecar→PCS body and headers, action-as-intent trust model, rejection cases.
- `prd/permission-validation/phase-1-encrypted-context-format.md` — PV1-003. AEAD payload format, required fields, key ownership/provisioning, validation rules, plain-permission clarification.
- `prd/permission-validation/phase-1-route-config-schema.md` — PV1-004. Protected/skipped route schema, glob matching, defaults, distribution, adoption story.

**Modify:**

- `prd/permission-validation/phase-1-user-stories.md` — add a "Decision doc:" pointer line under each of PV1-001…PV1-004.
- `prd/permission-validation/phase-1-architecture.md` — replace the speculative "PV1-001 is evaluating…" framing in §1 with a settled link to the topology decision doc; keep both diagrams (A and B) but mark Option B as the chosen topology.

**Conventions all four design docs follow:**

- Title is `# Phase 1 <Subject>`.
- First block is a metadata table: **Status**, **User story**, **Related**.
- All section headings use sentence case.
- Cross-references use repo-relative links: `[label](./other-doc.md#anchor)`.
- Mermaid is allowed but optional.
- Acceptance criteria from the user story are mapped 1:1 to sections; the doc ends with an "Acceptance criteria mapping" section that lists each criterion and the section that satisfies it.

---

## Task 1 — Topology Decision Document (PV1-001)

**Files:**

- Create: `prd/permission-validation/phase-1-topology-decision.md`

**User-story acceptance criteria (must each map to a section):**

1. All three options (custom proxy sidecar, Envoy `ext_authz`, Envoy `ext_proc`) compared using the Phase 1 flow.
2. Comparison covers latency, operational complexity, dev effort, debugging, fit for Phase 2 caching/invalidation.
3. Phase 1.5 forward-compatibility explicitly addressed; `ext_authz` cannot satisfy the §3.2 invariant alone.
4. Recommendation documented with trade-offs and known risks.
5. Required POC / benchmark scope identified.

- [ ] **Step 1: Create the doc with full skeleton + content**

Write the file with the following content verbatim:

````markdown
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
````

- [ ] **Step 2: Verify acceptance criteria coverage**

Open `prd/permission-validation/phase-1-user-stories.md` to the PV1-001 section. For each bullet under "Acceptance Criteria," scroll to the cited section in §7 of the new doc and confirm the content actually addresses it. If a criterion is not addressed, add the missing content before committing.

Expected: every PV1-001 acceptance criterion has a non-empty mapped section.

- [ ] **Step 3: Commit**

```bash
git add prd/permission-validation/phase-1-topology-decision.md
git commit -m "docs: add phase-1 topology decision (PV1-001) — ext_proc"
```

---

## Task 2 — Phase 1 Request Contract Document (PV1-002)

**Files:**

- Create: `prd/permission-validation/phase-1-request-contract.md`

**User-story acceptance criteria:**

1. Access Management API response contract documented.
2. Required client request headers documented.
3. Sidecar-to-PCS request body documented with `objectId`, `objectType`, `permission`.
4. Sidecar-to-PCS headers documented, including forwarding the user's SSO token.
5. Requested-action header is explicitly defined as user intent, not proof of permission.
6. Missing or malformed required fields are defined as rejection cases.

- [ ] **Step 1: Create the doc with full skeleton + content**

Write the file with the following content verbatim:

````markdown
# Phase 1 Request Contract

| | |
|---|---|
| **Status** | Draft |
| **User story** | [PV1-002](./phase-1-user-stories.md#pv1-002-define-phase-1-request-contract) |
| **Related** | [phase-1-encrypted-context-format.md](./phase-1-encrypted-context-format.md) · [phase-1-architecture.md §2](./phase-1-architecture.md#2-data-flow--protected-request) · [PRD §5.2](./PRD.md#52-the-validation-flow) |

## 1. Overview

This doc fixes the wire contracts that the client, the Access Management API (AM), the sidecar, and the Permission Checking Service (PCS) use for the Phase 1 validation flow. Field semantics, trust assumptions, and rejection cases are normative.

Actors:

- **Client** — UI or app code holding the user's SSO token.
- **Access Management API (AM)** — issues encrypted authorization context plus the plain permission list for UI display.
- **Sidecar / Envoy `ext_proc` handler** — decrypts, calls PCS, enforces.
- **Permission Checking Service (PCS)** — evaluates a permission for `objectId` + `objectType`, scoped to the user identified by the forwarded SSO token.

## 2. Access Management API response

**Request (client → AM):** out of scope of Phase 1 sidecar; documented here only to anchor the response shape.

```
GET /access-mgmt/v1/contexts?objectId={id}&objectType={type}
Authorization: Bearer <SSO-token>
```

**Response (AM → client):**

```json
{
  "encryptedContext": "v1.<keyId>.<base64url-payload>",
  "plainPermissions": ["read", "edit", "delete"]
}
```

| Field | Type | Required | Description |
|---|---|---|---|
| `encryptedContext` | string | yes | Single-line encoded encrypted authorization context. Format defined in [phase-1-encrypted-context-format.md](./phase-1-encrypted-context-format.md). |
| `plainPermissions` | string[] | yes | List of permissions the user has on the resource, for **UI display only**. Sidecar never reads this field. |

## 3. Client request headers (client → sidecar/Envoy)

| Header | Required | Value | Notes |
|---|---|---|---|
| `Authorization` | yes | `Bearer <SSO-token>` | Standard SSO bearer. The sidecar forwards this verbatim to PCS. |
| `X-Auth-Context` | yes | `encryptedContext` value from AM response (§2) | Single-line; format per [phase-1-encrypted-context-format.md](./phase-1-encrypted-context-format.md). |
| `X-Requested-Action` | yes | The permission the user is attempting (e.g., `edit`) | **User intent only; see §5.** |
| `X-Request-Id` | recommended | UUID or platform request id | Propagated to PCS for correlation. |

Header names are normative. Casing follows HTTP/2 lowercased convention on the wire; documentation uses the conventional title-case for readability.

## 4. Sidecar → Permission Checking Service

### 4.1 Body

```
POST /permission-check/v1/check
Content-Type: application/json
```

```json
{
  "objectId": "<from decrypted context>",
  "objectType": "<from decrypted context>",
  "permission": "<from X-Requested-Action header>"
}
```

| Field | Source | Trust |
|---|---|---|
| `objectId` | Decrypted authorization context (PV1-003) | Trusted — issued by AM, AEAD-protected. |
| `objectType` | Decrypted authorization context (PV1-003) | Trusted — issued by AM, AEAD-protected. |
| `permission` | `X-Requested-Action` header | **Untrusted intent.** PCS decides if the user has this permission. |

The user identity (`userId`, tenant, etc.) is **not** sent in the JSON body. PCS derives it from the forwarded SSO token (§4.2).

### 4.2 Headers

| Header | Required | Value | Notes |
|---|---|---|---|
| `Authorization` | yes | The user's SSO bearer, forwarded verbatim from the client request | PCS uses this to identify the user. |
| `Content-Type` | yes | `application/json` | |
| `X-Request-Id` | recommended | Same value the sidecar received, or one it generated | For correlation in PCS logs. |

The sidecar **does not** synthesize or modify the SSO token. If the client did not send `Authorization`, the request is rejected (§6) before PCS is called.

## 5. Trust model and action-header semantics

The `X-Requested-Action` header is treated as **user intent**, not proof of permission:

- The client declares "I am trying to perform action X."
- PCS decides whether the user is allowed to perform X on (`objectId`, `objectType`).
- The action header is not signed and not encrypted; tampering with it changes only which permission is checked, never whose decision is enforced.

Concretely, a client that sets `X-Requested-Action: admin-delete` does **not** thereby gain admin-delete; PCS will deny if the user does not have it. The only way the action header can matter is by checking a permission the user *does* have, which is harmless.

`objectId` and `objectType` come **only** from the decrypted authorization context. They are never read from the URL, query string, or body. This is what makes the decision tamper-resistant — see [phase-1-encrypted-context-format.md](./phase-1-encrypted-context-format.md).

## 6. Rejection cases

| Condition | Sidecar response | Reach backend? | Metric |
|---|---|---|---|
| Missing `Authorization` | `401 Unauthorized` | no | `header_invalid_total{reason="missing_authz"}` |
| Missing `X-Auth-Context` | `403 Forbidden` | no | `header_invalid_total{reason="missing_ctx"}` |
| Missing `X-Requested-Action` | `403 Forbidden` | no | `header_invalid_total{reason="missing_action"}` |
| `X-Auth-Context` undecryptable, tampered, expired, or wrong `appId` | `403 Forbidden` | no | `decrypt_failure_total{reason=...}` |
| PCS returns deny | `403 Forbidden` | no | `decisions_total{outcome="deny"}` |
| PCS times out or 5xx | `403 Forbidden` (fail-closed) | no | `decisions_total{outcome="error"}` |

The body of rejection responses is intentionally minimal in Phase 1; a single-line reason code is acceptable but it must not leak decrypted context fields or PCS internals.

## 7. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| AM API response contract documented | §2 |
| Required client request headers documented | §3 |
| Sidecar-to-PCS body with `objectId`, `objectType`, `permission` | §4.1 |
| Sidecar-to-PCS headers including forwarded SSO | §4.2 |
| Action header defined as intent, not proof | §5 |
| Missing/malformed required fields are rejection cases | §6 |
````

- [ ] **Step 2: Verify acceptance criteria coverage**

Read PV1-002 in `phase-1-user-stories.md` side by side with §7 of the new doc. Confirm each acceptance criterion is satisfied by the referenced section.

- [ ] **Step 3: Commit**

```bash
git add prd/permission-validation/phase-1-request-contract.md
git commit -m "docs: add phase-1 request contract (PV1-002)"
```

---

## Task 3 — Encrypted Authorization Context Format (PV1-003)

**Files:**

- Create: `prd/permission-validation/phase-1-encrypted-context-format.md`

**User-story acceptance criteria:**

1. Encrypted payload includes `appId`, `objectId`, `objectType`, `issuedAt`, `expiresAt`, `keyId`.
2. Encryption uses an authenticated encryption mode or equivalent tamper-proof envelope.
3. App credential ownership and provisioning responsibilities documented.
4. Expired, undecryptable, malformed, or wrong-audience contexts are rejected.
5. Plain permission list documented as UI display data only.

- [ ] **Step 1: Create the doc with full skeleton + content**

Write the file with the following content verbatim:

````markdown
# Phase 1 Encrypted Authorization Context Format

| | |
|---|---|
| **Status** | Draft |
| **User story** | [PV1-003](./phase-1-user-stories.md#pv1-003-define-encrypted-authorization-context-format) |
| **Related** | [phase-1-request-contract.md](./phase-1-request-contract.md) · [phase-1-architecture.md §1](./phase-1-architecture.md#1-software-architecture) |

## 1. Purpose and trust boundary

The encrypted authorization context is the **only** trusted source of `objectId` and `objectType` in Phase 1. It is issued by the Access Management API at the start of a user interaction, carried by the client in the `X-Auth-Context` header, and decrypted by the sidecar (per-app symmetric key) immediately before the PCS call. Anything outside this envelope is untrusted.

Two properties matter:

- **Authenticated.** A client must not be able to tamper with `objectId`/`objectType`/`appId` without the sidecar noticing.
- **Bounded-lifetime.** A leaked context must stop being useful after a short window.

## 2. Payload fields

The cleartext payload is a JSON object with the following fields. All fields are required; unrecognized fields are rejected.

| Field | Type | Description |
|---|---|---|
| `appId` | string | The app for which this context was issued. The sidecar verifies this matches its own `appId` (audience check). |
| `objectId` | string | Resource ID being accessed. |
| `objectType` | string | Resource type. |
| `issuedAt` | integer (Unix seconds) | When AM issued the context. |
| `expiresAt` | integer (Unix seconds) | After this instant, the context is rejected. |
| `keyId` | string | Identifies which symmetric key was used. Carried in the envelope **and** inside the payload so the sidecar can detect downgrade attacks. |

Phase 1 default lifetime: `expiresAt - issuedAt = 300s` (5 minutes). This is a starting value and can be tuned per-app in a later phase.

## 3. Encryption requirements

- **Algorithm:** AEAD. AES-256-GCM is the default; ChaCha20-Poly1305 is an acceptable alternative if the runtime cannot accelerate AES.
- **Key:** Per-app 256-bit symmetric key. Provisioned at app registration (§5).
- **Nonce:** 96-bit, random per encryption. Never reused with the same key.
- **Associated data (AAD):** the ASCII byte string `phase1-auth-ctx-v1|` concatenated with the `keyId`. Binding `keyId` into the AAD prevents an attacker from substituting a payload encrypted under a different (still-valid) key.

Any non-AEAD construction (CBC + HMAC, encrypt-then-sign, etc.) is out of scope for Phase 1; AEAD is the single supported family.

## 4. Envelope / wire format

The header value is a dot-separated string:

```
v1.<keyId>.<base64url(nonce || ciphertext || tag)>
```

- `v1` — version prefix; future formats bump to `v2`, etc.
- `<keyId>` — the same `keyId` that is bound into the AAD and inside the payload.
- `nonce || ciphertext || tag` — 96-bit nonce, ciphertext of the JSON cleartext, and 128-bit AEAD tag, concatenated and base64url-encoded (no padding).

The sidecar parses by splitting on `.`, requiring exactly three parts and a leading `v1` literal. Any deviation is a malformed-context rejection (§6).

## 5. App credential ownership and provisioning

**Owner:** the Access Management platform team owns the lifecycle of app credentials.

**Provisioning:**

1. At app registration time, AM generates a 256-bit symmetric key.
2. The key is written to the App Credential Store (the platform secrets backend — Vault or equivalent), keyed by `(appId, keyId)`.
3. The sidecar reads the key by `keyId` at decrypt time. Sidecars are provisioned with read access to keys for their own `appId` only.
4. AM holds the same key; AM and the sidecar are the only legitimate readers.

**Phase 1 simplifications:**

- One active key per app. No automatic rotation (out of scope per `phase-1-user-stories.md` "Out Of Scope" list).
- Manual rotation is supported (issue a new `keyId`, update sidecar config) but expected to be rare in Phase 1.
- Keys never leave the App Credential Store except through authenticated mTLS reads.

## 6. Validation rules and rejection

The sidecar rejects a context — returning `403 Forbidden` — if any of the following hold:

| Check | Failure cause |
|---|---|
| Envelope is well-formed (`v1.<keyId>.<base64url>`) | Malformed envelope. |
| `keyId` is known to this sidecar | Unknown key (likely wrong app or rotated-out). |
| AEAD decryption succeeds (tag verifies under key + AAD `phase1-auth-ctx-v1|<keyId>`) | Tampered or wrong-key context. |
| Cleartext parses as JSON with the §2 fields and no unknown fields | Malformed payload. |
| Payload `keyId` equals envelope `keyId` | Downgrade / substitution attempt. |
| Payload `appId` equals the sidecar's configured `appId` | Wrong audience. |
| `expiresAt > now` (with a small clock-skew tolerance, e.g. ±30s) | Expired context. |
| `issuedAt <= now + skew` | Future-dated context (likely clock skew or replay). |

Every rejection increments a `decrypt_failure_total{reason=...}` counter (per [PV1-010](./phase-1-user-stories.md#pv1-010-add-minimal-sre-metrics)).

## 7. Plain permission list

The Access Management API returns a `plainPermissions` array alongside the encrypted context (see [phase-1-request-contract.md §2](./phase-1-request-contract.md#2-access-management-api-response)). This list is for **UI display only**:

- It is not signed.
- It is not encrypted.
- The sidecar never reads it.
- A client that tampers with it can only mislead its own UI; the actual decision still comes from PCS with `objectId`/`objectType` sourced from the decrypted context.

Documenting this explicitly is important: it prevents future implementers from "optimizing" by trusting the plain list.

## 8. Out of scope (Phase 1)

- Automatic key rotation.
- Asymmetric (signed-by-AM, verified-by-sidecar) variants.
- Per-route lifetime overrides.
- Embedding user identity in the payload (identity comes from the SSO token).

## 9. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| Payload includes `appId`, `objectId`, `objectType`, `issuedAt`, `expiresAt`, `keyId` | §2 |
| AEAD or equivalent tamper-proof envelope | §3, §4 |
| App credential ownership and provisioning documented | §5 |
| Expired / undecryptable / malformed / wrong-audience rejected | §6 |
| Plain permissions are UI display only | §7 |
````

- [ ] **Step 2: Verify acceptance criteria coverage**

Read PV1-003 in `phase-1-user-stories.md` and confirm §9 of the new doc maps each criterion to a non-empty section.

- [ ] **Step 3: Commit**

```bash
git add prd/permission-validation/phase-1-encrypted-context-format.md
git commit -m "docs: add phase-1 encrypted authorization context format (PV1-003)"
```

---

## Task 4 — Route Config Schema (PV1-004)

**Files:**

- Create: `prd/permission-validation/phase-1-route-config-schema.md`

**User-story acceptance criteria:**

1. Configuration supports protected routes with HTTP method and path pattern.
2. Configuration supports skipped routes with HTTP method and path pattern.
3. Health check and public asset examples included as skipped routes.
4. Default behavior for unmatched routes is documented.
5. Schema is simple enough for application teams to adopt without custom code.

- [ ] **Step 1: Create the doc with full skeleton + content**

Write the file with the following content verbatim:

````markdown
# Phase 1 Route Config Schema

| | |
|---|---|
| **Status** | Draft |
| **User story** | [PV1-004](./phase-1-user-stories.md#pv1-004-define-protected-and-skipped-path-configuration) |
| **Related** | [phase-1-topology-decision.md](./phase-1-topology-decision.md) · [phase-1-architecture.md §1](./phase-1-architecture.md#1-software-architecture) · [PRD §5.3](./PRD.md#53-declarative-route-configuration) |

## 1. Purpose

Application teams declare which routes are protected and which are skipped. The platform translates this declaration into Envoy route configuration plus `ExtProcPerRoute` overrides (per the Option B topology decision in [phase-1-topology-decision.md](./phase-1-topology-decision.md)).

Phase 1 deliberately keeps the schema minimal: no extraction rules, no body parsing, no per-route caching, no fail-open. Those land in later phases. The schema must be small enough that an application team can adopt validation by writing one YAML file.

## 2. Schema

The configuration is a single YAML document per app.

```yaml
version: v1
appId: <string>                       # required; matches the appId in encrypted contexts (PV1-003)
defaultBehavior: deny | skipped       # required; behavior for routes that match no rule. Default value: deny.
routes:                               # required; list. First match wins.
  - method: GET | POST | PUT | DELETE | PATCH | "*"
    path: <pattern>                   # see §2.1
    behavior: protected | skipped
```

**Required fields:** `version`, `appId`, `defaultBehavior`, `routes`, and each route's `method`, `path`, `behavior`.

**Validation rules** (enforced at config-load time, before any traffic is served):

- `version` must equal `v1`.
- `appId` must match the sidecar's provisioned `appId`.
- `defaultBehavior` is `deny` or `skipped`. `protected` is not a valid default because it would require validation context the request cannot supply.
- `routes` is a non-empty list.
- Each `method` is one of `GET`, `POST`, `PUT`, `DELETE`, `PATCH`, or `*` (any).
- Each `path` is a non-empty pattern (§2.1).
- Each `behavior` is `protected` or `skipped`.

### 2.1 Path matching

Path patterns are gitignore-style globs:

- A literal segment matches itself (`/orders`).
- `*` matches exactly one path segment (no `/`).
- `**` matches one or more path segments (greedy).
- Patterns must start with `/`.
- Trailing-slash handling: a pattern with a trailing slash matches only paths with a trailing slash. `/orders` does not match `/orders/`.

Matching is **first-match-wins** in list order. Application teams are responsible for ordering specific rules before general rules.

## 3. Examples

### 3.1 Protected routes

```yaml
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

### 3.2 Skipped routes (health checks and public assets)

```yaml
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
```

### 3.3 Complete minimal example

```yaml
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /assets/**
    behavior: skipped
  - method: GET
    path: /api/orders/*
    behavior: protected
  - method: POST
    path: /api/orders
    behavior: protected
```

## 4. Default behavior for unmatched routes

`defaultBehavior` controls what happens to a request whose method + path matches no rule.

| Value | Meaning | When to use |
|---|---|---|
| `deny` (recommended) | Reject with `403`. | The safe default. New routes are protected by default until explicitly listed. |
| `skipped` | Forward without validation. | Only for apps that do not handle sensitive data and want to opt out of route-by-route enumeration. |

There is no `protected` default in Phase 1. Protected requests require an encrypted context, and requiring it for unenumerated routes would block the app's first traffic the day it deploys. Teams that want everything protected must list their routes explicitly.

## 5. Distribution

In the Option B topology:

- The YAML lives in the application team's repo, alongside the app.
- Platform CI validates the schema (§2 rules) and rejects invalid configs.
- Validated configs are translated by a platform tool into:
  - Envoy `RouteConfiguration` (method + path → route).
  - `ExtProcPerRoute` overrides on each route: `disabled: true` for `skipped`, default-enabled for `protected`.
- The translated Envoy config is delivered via a `ConfigMap` (static, Phase 1) or xDS (later phases). Phase 1 uses static config — see [phase-1-topology-decision.md §5](./phase-1-topology-decision.md#5-trade-offs-and-known-risks).

The application team never writes Envoy config directly.

## 6. Adoption / DX expectations

- A new team should be able to onboard with **one YAML file** and zero custom code.
- The platform provides a CLI / CI check (`validate-routes config.yaml`) that runs schema validation locally and in CI.
- An onboarding example covering all the patterns in §3 ships under [PV1-012](./phase-1-user-stories.md#pv1-012-create-phase-1-onboarding-example).

## 7. Out of scope (Phase 1)

- Per-route fail-open behavior.
- Per-route cache TTL or cache eligibility.
- Body or query-parameter extraction rules.
- Route-to-permission mapping (the permission comes from `X-Requested-Action`, not the route).
- Cross-checking that the URL's object ID matches the decrypted `objectId`.

These appear in later phases; teams that need them today should flag the requirement during pilot.

## 8. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| Protected routes with method + path pattern | §2, §3.1 |
| Skipped routes with method + path pattern | §2, §3.2 |
| Health check and public asset examples as skipped | §3.2 |
| Default behavior for unmatched routes documented | §4 |
| Schema simple enough for application teams to adopt without custom code | §2 (size), §6 (DX) |
````

- [ ] **Step 2: Verify acceptance criteria coverage**

Read PV1-004 in `phase-1-user-stories.md` and confirm §8 maps each criterion to a non-empty section. Particular attention: criterion 5 ("simple enough … without custom code") — confirm that nothing in §2 requires the team to write Go/Rust/etc. just to adopt.

- [ ] **Step 3: Commit**

```bash
git add prd/permission-validation/phase-1-route-config-schema.md
git commit -m "docs: add phase-1 route config schema (PV1-004)"
```

---

## Task 5 — Cross-link from user stories and architecture docs

**Files:**

- Modify: `prd/permission-validation/phase-1-user-stories.md`
- Modify: `prd/permission-validation/phase-1-architecture.md`

This task makes the new design docs discoverable from the existing docs.

- [ ] **Step 1: Add "Decision doc:" pointer to each design user story**

In `prd/permission-validation/phase-1-user-stories.md`, locate each of PV1-001, PV1-002, PV1-003, PV1-004. Immediately after the `**Description:**` paragraph and before `**Acceptance Criteria:**`, insert one new line:

For PV1-001:

```markdown
**Decision doc:** [phase-1-topology-decision.md](./phase-1-topology-decision.md)
```

For PV1-002:

```markdown
**Decision doc:** [phase-1-request-contract.md](./phase-1-request-contract.md)
```

For PV1-003:

```markdown
**Decision doc:** [phase-1-encrypted-context-format.md](./phase-1-encrypted-context-format.md)
```

For PV1-004:

```markdown
**Decision doc:** [phase-1-route-config-schema.md](./phase-1-route-config-schema.md)
```

- [ ] **Step 2: Update architecture doc to reflect the settled topology**

In `prd/permission-validation/phase-1-architecture.md` §1, the current opening sentence reads:

> This section documents Phase 1 under the **custom proxy sidecar** topology (Option A). PV1-001 is evaluating an alternative topology that places Envoy in the request path and runs the sidecar as an `ext_proc` gRPC handler (Option B) — see [§1, Alternative Topology — Envoy `ext_proc` Filter](#alternative-topology--envoy-ext_proc-filter) at the end of this section.

Replace it with:

> This section documents Phase 1 under both candidate topologies; PV1-001 is decided in favor of **Option B (Envoy `ext_proc` + sidecar)** — see [phase-1-topology-decision.md](./phase-1-topology-decision.md). The data flow (§2) and invariants (§3) apply to both topologies; the Option A diagram is preserved below for reference because the validator-level component responsibilities are identical.

At the end of the "Alternative Topology — Envoy `ext_proc` Filter" subsection, replace the final line:

> PV1-001 evaluates whether Option A or Option B is the right Phase 1 topology.

with:

> PV1-001 selected Option B as the Phase 1 topology — see [phase-1-topology-decision.md](./phase-1-topology-decision.md).

- [ ] **Step 3: Verify the edits render**

Run `git diff prd/permission-validation/phase-1-user-stories.md prd/permission-validation/phase-1-architecture.md` and confirm only the intended lines changed and that the Markdown link syntax is well-formed.

Expected: four `**Decision doc:**` lines added in user stories; two prose blocks changed in architecture.

- [ ] **Step 4: Commit**

```bash
git add prd/permission-validation/phase-1-user-stories.md prd/permission-validation/phase-1-architecture.md
git commit -m "docs: link phase-1 design docs from user stories and architecture"
```

---

## Task 6 — Final acceptance-criteria sweep

This task is a single review pass over all four new docs against the user-story acceptance criteria, plus a link-check.

- [ ] **Step 1: Run the criteria checklist**

For each of the four user stories, walk through every acceptance-criteria bullet in `phase-1-user-stories.md` and tick it off against the corresponding "Acceptance criteria mapping" section in the design doc:

- PV1-001 → `phase-1-topology-decision.md` §7
- PV1-002 → `phase-1-request-contract.md` §7
- PV1-003 → `phase-1-encrypted-context-format.md` §9
- PV1-004 → `phase-1-route-config-schema.md` §8

Expected: every bullet maps to a non-empty section; the cited section actually addresses the bullet (not just names it).

If a gap is found, add the missing content to the relevant doc, then re-run this step before continuing.

- [ ] **Step 2: Cross-link sanity check**

Confirm that every repo-relative link in the four new docs resolves to an existing file. From the repo root:

```bash
for f in prd/permission-validation/phase-1-topology-decision.md \
         prd/permission-validation/phase-1-request-contract.md \
         prd/permission-validation/phase-1-encrypted-context-format.md \
         prd/permission-validation/phase-1-route-config-schema.md; do
  echo "=== $f ==="
  grep -oE '\]\(\./[^)]+\)' "$f" | sed -E 's/^\]\(\.\///; s/\)$//' | while read target; do
    file=$(echo "$target" | cut -d'#' -f1)
    if [ ! -f "prd/permission-validation/$file" ]; then
      echo "BROKEN: $target"
    fi
  done
done
```

Expected: no `BROKEN:` lines. If any appear, fix the link (typo or missing file) before continuing.

- [ ] **Step 3: PR-readiness — confirm the branch state is clean and pushable**

```bash
git status
git log --oneline -8
```

Expected: `git status` reports a clean working tree; `git log` shows the five new commits from Tasks 1–5 on top of the previous `docs/prd` history.

- [ ] **Step 4: Hand off**

The four design deliverables are landed. PV1-005 through PV1-012 (implementation, metrics, integration tests, onboarding example) are unblocked and out of scope of this plan; they should be planned separately once the team is ready to start the sidecar codebase.

---

## Out of scope for this plan

- PV1-005 through PV1-012 (implementation, metrics, integration tests, onboarding example).
- Any code in any language.
- The Phase 1.5 metadata-sync design (already covered in [phase-1-5-metadata-sync-design.md](../../../prd/permission-validation/phase-1-5-metadata-sync-design.md); referenced from the topology decision only for forward-compat).
- Phase 2 cache and invalidation design.

---

## Self-review notes (for the plan author)

**Spec coverage:** All 12 acceptance criteria across PV1-001…PV1-004 are mapped to specific sections of the new docs; mappings are reproduced as "Acceptance criteria mapping" sections in each doc and re-checked in Task 6 §1.

**Placeholder scan:** No `TBD`, `TODO`, "implement later", "similar to above", or unspecified handling. Every doc body is included verbatim in the plan, not described abstractly.

**Type consistency:** Field names match across docs — `appId`, `objectId`, `objectType`, `issuedAt`, `expiresAt`, `keyId` are spelled the same in PV1-002 §4.1, PV1-003 §2, and PV1-004 §2; `X-Auth-Context` and `X-Requested-Action` are spelled the same in PV1-002 §3 and PV1-003 §1.
