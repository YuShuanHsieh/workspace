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
