# Phase 1 Context Header Format

| | |
|---|---|
| **Status** | Draft |
| **User story** | [PV1-003](./phase-1-user-stories.md#pv1-003-define-context-header-format) |
| **Related** | [phase-1-request-contract.md](./phase-1-request-contract.md) · [phase-1-architecture.md §1](./phase-1-architecture.md#1-software-architecture) |

## 1. Purpose and trust boundary

The context header (`X-Auth-Context`) carries the three values the sidecar needs to call PCS: `objectId`, `objectType`, and `action`. Phase 1 deliberately does not encrypt or sign this header — see [phase-1-user-stories.md → Phase 1 Scope](./phase-1-user-stories.md#phase-1-scope) for the trust assumption ("client is trusted to declare the resource it is acting on") and [phase-1-request-contract.md §4](./phase-1-request-contract.md#4-trust-model) for the defense-in-depth model.

Because the format is plain text and not authenticated, the parser is the only line of defense against malformed values reaching PCS. The rules in §3 and §4 are intentionally strict: any deviation is a rejection, never a best-effort interpretation.

## 2. Wire format

The `X-Auth-Context` header value is a single line of plain ASCII text:

```
<objectId>:<objectType>:<action>
```

- Exactly three segments.
- Segments are separated by exactly one `:` character.
- No surrounding quotes, no encoding (no base64, no URL-encoding), no version prefix.

Example:

```
X-Auth-Context: doc-42:document:edit
```

## 3. Segment rules

| Segment | Allowed characters | Notes |
|---|---|---|
| `objectId` | Any printable ASCII except `:`, whitespace, and control characters | Opaque to the sidecar; PCS interprets the value. |
| `objectType` | Any printable ASCII except `:`, whitespace, and control characters | Opaque to the sidecar; PCS interprets the value. |
| `action` | Any printable ASCII except `:`, whitespace, and control characters | The permission name PCS will check. |

Each segment must be **non-empty** after parsing (no implicit defaults).

The total header value length, including separators, is bounded at **1024 bytes**. This is generous for typical IDs and permission names while preventing pathological inputs.

## 4. Validation rules and rejection

The sidecar rejects the request — returning `403 Forbidden` — if any of the following hold. Each rejection increments `ctx_parse_failure_total{reason="<label>"}` per [PV1-010](./phase-1-user-stories.md#pv1-010-add-minimal-sre-metrics).

| Failure condition | Reason label |
|---|---|
| Header value is over the maximum length (1024 bytes) | `over_length` |
| Header value contains fewer or more than two `:` separators (i.e., not exactly three segments) | `wrong_segment_count` |
| Any segment is empty (e.g., `:foo:bar`, `foo::bar`, `foo:bar:`) | `empty_segment` |
| Any segment contains whitespace (leading, trailing, or interior) | `whitespace` |
| Any segment contains a control character (bytes `0x00`–`0x1F` or `0x7F`) | `control_char` |
| Header value is not valid UTF-8 / not printable ASCII | `non_printable` |

Header-presence rejections (missing `X-Auth-Context` entirely, missing `Authorization`) are listed in [phase-1-request-contract.md §5](./phase-1-request-contract.md#5-rejection-cases) and use the separate `header_invalid_total` metric.

## 5. Plain-text rationale

Phase 1 deliberately omits encryption, signing, encoding, and expiry:

- **No encryption.** The trust model (client is trusted) makes encryption unnecessary; PCS is the authority. Adding encryption now would also require app credential provisioning, key rotation, and an Access Management API in the request path — all explicitly out of scope.
- **No base64 / URL-encoding.** With `:` and whitespace disallowed in segments and a 1024-byte cap, the values fit cleanly on the wire.
- **No expiry / `issuedAt`.** With no platform-issued binding, there is no meaningful "issuance time" to check. Replay protection, when needed, will come with a future signed/encrypted format.

These omissions are Phase 1 simplifications. See [phase-1-user-stories.md → Out Of Scope](./phase-1-user-stories.md#out-of-scope-for-phase-1).

## 6. Out of scope (Phase 1)

- Encryption or authenticated encryption.
- Signing (HMAC, JWT, etc.).
- Embedding `appId`, `userId`, `issuedAt`, or `expiresAt` in the header.
- App credential provisioning and key management.
- Access Management API issuance flow.
- URL-encoding, base64, or other transport encodings of the segment values.

## 7. Acceptance criteria mapping

| Acceptance criterion | Section |
|---|---|
| Header value is exactly three non-empty segments separated by `:` | §2, §3 |
| `:` disallowed inside any segment | §3 (allowed-characters table), §4 (`wrong_segment_count`) |
| Empty segments rejected | §4 (`empty_segment`) |
| Whitespace rejected (no trimming) | §3, §4 (`whitespace`) |
| Maximum total length bounded and documented | §3 (1024 bytes), §4 (`over_length`) |
| All rejection conditions enumerated with reason labels | §4 |
