# Phase 1 Onboarding — orders-app example

This example shows the minimum a new application team needs to onboard the
permission-validation sidecar in Phase 1.

## Files

- `routes.yaml` — protected and skipped routes for your app.
- `envoy.yaml` — production-style Envoy bootstrap rendered from
  `routes.yaml` by `validate-routes translate`. Committed so reviewers can
  see what the generator produces; regenerate with the command in
  [Generating `envoy.yaml`](#generating-envoyyaml).
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

## Generating `envoy.yaml`

`validate-routes translate` renders an Envoy 1.31 static bootstrap from
`routes.yaml` plus a handful of environment knobs. The committed
`envoy.yaml` in this directory is the **production-style** render —
admin bound to loopback, access logs enabled:

```
validate-routes translate examples/onboarding/routes.yaml \
  -o examples/onboarding/envoy.yaml \
  --sidecar-host sidecar --sidecar-port 50051 \
  --backend-host orders-app --backend-port 8080 \
  --access-log
```

Knobs that matter when promoting from test to production:

| Flag | Default | Production | Why |
|---|---|---|---|
| `--admin-host` | `127.0.0.1` | leave as default | Envoy's `:9901` admin interface is unauthenticated and exposes mutating endpoints (`/quitquitquit`, runtime overrides). Binding to loopback means only processes inside the pod can reach it; ops can still `kubectl exec` + `curl localhost:9901` for diagnostics. Override to `0.0.0.0` **only** for local docker-compose stacks where the host needs to poke admin from outside the container (see `test/e2e/Makefile`). |
| `--access-log` | off | **on** | Adds an `http_connection_manager` access log writing to stdout. Off by default so unit and e2e runs stay quiet; turn it on for production so SREs see request lines next to the sidecar's own logs. |
| `--sidecar-host` / `--sidecar-port` | `127.0.0.1:50051` | service name + port reachable from Envoy | In Kubernetes, set to the sidecar container or service DNS name. |
| `--backend-host` / `--backend-port` | `127.0.0.1:8080` | service name + port of your app | Where Envoy forwards granted requests. |

Run `validate-routes translate routes.yaml -h` to see every flag.

## Apply in production

Run Envoy with the generated file as its static bootstrap:

```
envoy -c /etc/envoy/envoy.yaml --log-level info
```

In Kubernetes the typical shape is: a `ConfigMap` containing `envoy.yaml`,
mounted at `/etc/envoy/envoy.yaml` in the Envoy sidecar container; the
permission-validation sidecar as a sibling container reachable at
`--sidecar-host sidecar --sidecar-port 50051`; the application backend as
a third container or as the targeted upstream service.

Treat `envoy.yaml` as a build artifact, not a hand-edited file. The
canonical flow is:

1. App team edits `routes.yaml` in their repo.
2. CI runs `validate-routes validate routes.yaml` — fails the build on
   schema errors before any image is built.
3. CI runs `validate-routes translate routes.yaml -o envoy.yaml
   --access-log ...` — the rendered file ships with the deployment.
4. Reviewers inspect both `routes.yaml` (intent) and `envoy.yaml`
   (what Envoy will actually load) in the PR.

## Adopt in your repo

1. Copy `routes.yaml` next to your app source.
2. Validate locally: `validate-routes validate routes.yaml`.
3. Render `envoy.yaml` once and commit it so reviewers see the generated
   output:
   `validate-routes translate routes.yaml -o envoy.yaml --sidecar-host sidecar --sidecar-port 50051 --backend-host <your-app> --backend-port <your-port> --access-log`
4. Have the platform CI run the same `validate-routes validate` step and
   re-run `translate` to detect drift between `routes.yaml` and the
   committed `envoy.yaml`.
5. Tell every client that produces requests to your app to send `Authorization`
   and `X-Auth-Context` per the wire format above.
