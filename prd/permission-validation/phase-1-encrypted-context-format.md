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
- Keys are retrieved only via authenticated reads from the App Credential Store; the specific transport security between the store and the sidecar is a platform concern outside Phase 1 scope.

## 6. Validation rules and rejection

This section covers context-validation rejections only. Header-presence and header-format rejections (missing `Authorization`, malformed `Bearer` token, empty `X-Requested-Action`, etc.) are listed in [phase-1-request-contract.md §6](./phase-1-request-contract.md#6-rejection-cases) and use a separate `header_invalid_total` metric.

The sidecar rejects a context — returning `403 Forbidden` — if any of the following hold:

| Failure condition | Reason label |
|---|---|
| Envelope is not `v1.<keyId>.<base64url>` (wrong part count, missing version prefix, non-base64url payload) | `malformed_envelope` |
| `keyId` in the envelope is unknown to this sidecar | `unknown_key` |
| AEAD tag verification fails (tampered, wrong key, or wrong AAD `phase1-auth-ctx-v1\|<keyId>`) | `tampered` |
| Cleartext does not parse as JSON, is missing a §2 field, or contains an unrecognized field | `malformed_payload` |
| Payload `keyId` does not equal envelope `keyId` (downgrade / substitution attempt) | `key_downgrade` |
| Payload `appId` does not equal the sidecar's configured `appId` | `wrong_audience` |
| `expiresAt <= now - skew` (skew tolerance ±30s) | `expired` |
| `issuedAt > now + skew` (future-dated; likely clock skew or replay) | `future_dated` |

Every rejection increments `decrypt_failure_total{reason="<label>"}` using the **Reason label** from the table above (per [PV1-010](./phase-1-user-stories.md#pv1-010-add-minimal-sre-metrics)). The same set of labels is referenced from [phase-1-request-contract.md §6](./phase-1-request-contract.md#6-rejection-cases), which defers to this section for the canonical enumeration.

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
| AEAD authenticated encryption (Phase 1 locks to AEAD; non-AEAD constructions out of scope per §3) | §3, §4 |
| App credential ownership and provisioning documented | §5 |
| Expired / undecryptable / malformed / wrong-audience rejected | §6 |
| Plain permissions are UI display only | §7 |
