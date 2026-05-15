# Phase 1 Request Contract

| | |
|---|---|
| **Status** | Draft |
| **User story** | [PV1-002](./phase-1-user-stories.md#pv1-002-define-phase-1-request-contract) |
| **Related** | [phase-1-context-header-format.md](./phase-1-context-header-format.md) · [phase-1-architecture.md §2](./phase-1-architecture.md#2-data-flow--protected-request) · [PRD §5.2](./PRD.md#52-the-validation-flow) |

## 1. Overview

This doc fixes the wire contracts that the client, the sidecar, and the Permission Checking Service (PCS) use for the Phase 1 validation flow. Field semantics, trust assumptions, and rejection cases are normative.

Phase 1 operates under the simplifying assumption that **the client is trusted** to declare the resource it is acting on. There is no encrypted context, no Access Management API call in the request path, and no app credential. The sidecar is a thin parser + PCS caller; PCS is the sole authority on whether the SSO user holds the requested permission on `(objectId, objectType)`.

Actors:

- **Client** — UI or app code holding the user's SSO token. Composes the context header from `objectId`, `objectType`, and the action it intends to perform.
- **Sidecar / Envoy `ext_proc` handler** — parses the context header, calls PCS, enforces the decision.
- **Permission Checking Service (PCS)** — evaluates a permission for `objectId` + `objectType`, scoped to the user identified by the forwarded SSO token.

## 2. Client request headers (client → sidecar/Envoy)

| Header | Required | Value | Notes |
|---|---|---|---|
| `Authorization` | yes | `Bearer <SSO-token>` | Standard SSO bearer. The sidecar forwards this verbatim to PCS. |
| `X-Auth-Context` | yes | `objectId:objectType:action` | Single line, plain text. Format per [phase-1-context-header-format.md](./phase-1-context-header-format.md). |
| `X-Request-Id` | recommended | UUID or platform request id | Propagated to PCS for correlation. |

Header names are normative. Casing follows HTTP/2 lowercased convention on the wire; documentation uses the conventional title-case for readability.

The `action` segment of `X-Auth-Context` carries user intent and is **not** a separate header. See §4 for the trust model.

## 3. Sidecar → Permission Checking Service

### 3.1 Body

```
POST /permission-check/v1/check
Content-Type: application/json
```

```json
{
  "objectId": "<from parsed X-Auth-Context>",
  "objectType": "<from parsed X-Auth-Context>",
  "permission": "<from parsed X-Auth-Context>"
}
```

| Field | Source | Trust |
|---|---|---|
| `objectId` | `X-Auth-Context` segment 1 | **Client-declared.** Trusted under the Phase 1 assumption (§4); PCS still gates the decision. |
| `objectType` | `X-Auth-Context` segment 2 | **Client-declared.** Trusted under the Phase 1 assumption (§4); PCS still gates the decision. |
| `permission` | `X-Auth-Context` segment 3 (`action`) | **Untrusted intent.** PCS decides if the user has this permission. |

The user identity (`userId`, tenant, etc.) is **not** sent in the JSON body. PCS derives it from the forwarded SSO token (§3.2).

### 3.2 Headers

| Header | Required | Value | Notes |
|---|---|---|---|
| `Authorization` | yes | The user's SSO bearer, forwarded verbatim from the client request | PCS uses this to identify the user. |
| `Content-Type` | yes | `application/json` | |
| `X-Request-Id` | recommended | Same value the sidecar received, or one it generated | For correlation in PCS logs. |

The sidecar **does not** synthesize or modify the SSO token. If the client did not send `Authorization`, the request is rejected (§5) before PCS is called.

## 4. Trust model

Phase 1 trusts the client to put a truthful `(objectId, objectType)` in `X-Auth-Context`. The defense in depth is:

- **PCS gates the decision.** A client substituting an `objectId` they do not have permission on will be denied. They cannot forge access they do not have.
- **The `action` segment is intent, not proof.** Setting `X-Auth-Context: doc-1:document:admin-delete` does **not** grant admin-delete; PCS denies if the user lacks it.

The accepted residual risk: a client substitutes an `objectId` they *do* have permission on (e.g., another document they own) while the application backend operates on a different object referenced in the URL or body. Phase 1 does not cross-check the URL against the header. This risk is documented in [phase-1-user-stories.md → Out Of Scope](./phase-1-user-stories.md#out-of-scope-for-phase-1) and is the motivation for path/body validation in a later phase.

`objectId`, `objectType`, and `permission` come **only** from the parsed `X-Auth-Context`. They are never read from the URL, query string, or body.

## 5. Rejection cases

| Condition | Sidecar response | Reach backend? | Metric |
|---|---|---|---|
| Missing `Authorization` | `403 Forbidden` | no | `header_invalid_total{reason="missing_authz"}` |
| Missing `X-Auth-Context` | `403 Forbidden` | no | `header_invalid_total{reason="missing_ctx"}` |
| Malformed `Authorization` (not a well-formed `Bearer <token>` value) | `403 Forbidden` | no | `header_invalid_total{reason="malformed_authz"}` |
| `X-Auth-Context` malformed (wrong segment count, empty segment, whitespace, over-length) | `403 Forbidden` | no | `ctx_parse_failure_total{reason=...}` — labels enumerated in [phase-1-context-header-format.md §4](./phase-1-context-header-format.md#4-validation-rules-and-rejection) |
| PCS returns deny | `403 Forbidden` | no | `decisions_total{outcome="deny"}` |
| PCS times out or 5xx | `403 Forbidden` (fail-closed) | no | `decisions_total{outcome="error"}` |

The body of rejection responses is intentionally minimal in Phase 1; a single-line reason code is acceptable but it must not echo the raw context-header value or PCS internals.

## 6. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| Required client request headers documented | §2 |
| Combined context header format `objectId:objectType:action` | §2 + [phase-1-context-header-format.md](./phase-1-context-header-format.md) |
| `:` disallowed inside segment values | [phase-1-context-header-format.md §4](./phase-1-context-header-format.md#4-validation-rules-and-rejection) |
| Sidecar-to-PCS body with `objectId`, `objectType`, `permission` | §3.1 |
| Sidecar-to-PCS headers including forwarded SSO | §3.2 |
| `action` segment defined as intent, not proof | §4 |
| Missing/malformed required fields are rejection cases | §5 |
