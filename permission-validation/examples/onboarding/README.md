# Phase 1 Onboarding — orders-app example

This example shows the minimum a new application team needs to onboard the
permission-validation sidecar in Phase 1.

## Files

- `routes.yaml` — protected and skipped routes for your app.
- `client-request.sh` — `curl` snippets that produce a granted call, a denied
  call, a malformed-header call, and a missing-header call.

## Trust model (must read)

Phase 1 trusts the client to declare a truthful `(objectId, objectType)` in
`X-Auth-Context`. The `action` segment is **user intent**, not proof of
permission. The Permission Checking Service (PCS) is the sole authority on
whether the SSO user holds the requested permission on `(objectId, objectType)`.

A client substituting an `objectId` they do not have permission on is harmless
(PCS denies). A client substituting an `objectId` they *do* have permission on,
while the application backend operates on a different object referenced in the
URL or body, is the accepted Phase 1 residual risk. Cross-checking the URL /
body against the header is out of Phase 1 scope.

## Wire format

```
Authorization: Bearer <SSO token>
X-Auth-Context: <objectId>:<objectType>:<action>
```

Rules (rejection labels in parentheses):

- Exactly three non-empty segments separated by `:` (`wrong_segment_count`, `empty_segment`).
- No `:` inside any segment (parses as `wrong_segment_count`).
- No whitespace anywhere (`whitespace`).
- No control characters or non-ASCII bytes (`control_char`, `non_printable`).
- Header value ≤ 1024 bytes (`over_length`).

## What the sidecar sends to PCS

```
POST http://permission-checking/permission-check/v1/check
Content-Type: application/json
Authorization: Bearer <SSO token, forwarded verbatim>

{
  "objectId":   "<from segment 1 of X-Auth-Context>",
  "objectType": "<from segment 2>",
  "permission": "<from segment 3>"
}
```

## Common rejection cases

| Client sent | Sidecar response | Backend reached? |
|---|---|---|
| `X-Auth-Context: doc-1:document:view`, valid SSO, PCS allows | `200` (backend response) | yes |
| `X-Auth-Context: doc-1:document:admin-delete`, valid SSO, PCS denies | `403 Forbidden` | no |
| Missing `Authorization` | `403 Forbidden` | no |
| Missing `X-Auth-Context` | `403 Forbidden` | no |
| `X-Auth-Context: doc-1:document` (too few segments) | `403 Forbidden` | no |
| `X-Auth-Context: doc-1::view` (empty segment) | `403 Forbidden` | no |
| `X-Auth-Context: doc-1:document:view ` (trailing space) | `403 Forbidden` | no |
| PCS timeout or 5xx | `403 Forbidden` (fail-closed) | no |

## Adopt in your repo

1. Copy `routes.yaml` next to your app source.
2. Validate locally: `validate-routes validate routes.yaml`.
3. Have the platform CI run the same `validate-routes` step.
4. Tell every client that produces requests to your app to send `Authorization`
   and `X-Auth-Context` per the wire format above.
