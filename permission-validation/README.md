# permission-validation

Phase 1 of the permission-validation sidecar. Runs alongside an application
backend as an Envoy `ext_proc` gRPC service: on every protected request,
Envoy hands the request headers to the sidecar; the sidecar asks the
Permission Checking Service (PCS) whether the caller may perform the stated
action on the stated object; granted requests are forwarded to the
application backend, denied requests are rejected at the sidecar with
`403 Forbidden` so the backend never sees them.

The full design — topology, request contract, header format, route-config
schema, user stories — lives under [`../prd/permission-validation/`](../prd/permission-validation/).
The implementation plan executed to produce this code is at
[`../docs/superpowers/plans/2026-05-15-permission-validation-phase-1.md`](../docs/superpowers/plans/2026-05-15-permission-validation-phase-1.md).

## Trust model (must read)

Phase 1 trusts the client to declare a truthful `(objectId, objectType)` in
the `X-Auth-Context` header. The `action` segment is **user intent**, not
proof of permission — PCS decides whether the SSO user holds that permission
on that object.

- A client substituting an `objectId` they do **not** have permission on is
  harmless (PCS denies).
- A client substituting an `objectId` they **do** have permission on, while
  the application backend operates on a different object referenced in the
  URL or body, is the accepted Phase 1 residual risk. Cross-checking the URL
  or body against `X-Auth-Context.objectId` is **out of Phase 1 scope** and
  is deferred to a later phase.

See [`examples/onboarding/README.md`](examples/onboarding/README.md) for the
wire format, rejection table, and adoption checklist.

## Repo layout

```text
cmd/
  permission-validation/   sidecar entrypoint (ext_proc gRPC server)
  validate-routes/         CLI: validate routes.yaml / translate to envoy.yaml
internal/
  config/                  YAML schema + validator + Envoy translator
  extproc/                 ext_proc gRPC server + Decide() orchestrator
  header/                  Authorization + X-Auth-Context extract/parse
  metrics/                 OpenTelemetry counters and histograms
  pcs/                     HTTP client for the Permission Checking Service
examples/onboarding/       Annotated routes.yaml, curl snippets, README
test/e2e/                  docker-compose harness + black-box Go suite
testdata/routes/           Valid/invalid route-config fixtures
```

## Build and run

Requires Go 1.25.

Build the two binaries:

```sh
go build ./cmd/permission-validation
go build ./cmd/validate-routes
```

Run the sidecar against a real PCS:

```sh
./permission-validation \
  --listen 0.0.0.0:50051 \
  --pcs-endpoint http://permission-checking.internal:8080 \
  --pcs-timeout 250ms \
  --otel-endpoint otel-collector.internal:4317
```

For local development without an OTel collector, pass `--otel-disabled` to
use a no-op meter provider:

```sh
./permission-validation --listen 0.0.0.0:50051 --pcs-endpoint http://127.0.0.1:9000 --otel-disabled
```

The sidecar shuts down gracefully on SIGINT/SIGTERM.

## `validate-routes` CLI

Two subcommands. The first positional argument is the route-config file;
flags follow it.

```sh
validate-routes validate routes.yaml
```

Exits 0 if the file parses and validates, 1 otherwise; errors print to
stderr.

```sh
# Static target (Phase 1 — app team controls its own Envoy):
validate-routes translate routes.yaml \
  -o envoy.yaml \
  --sidecar-host sidecar --sidecar-port 50051 \
  --backend-host orders-app --backend-port 8080 \
  --access-log

# Istio target (Phase 1.5 — pv sidecar lives in an Istio-injected pod):
validate-routes translate routes.yaml \
  -o envoyfilter.yaml \
  --target=istio \
  --namespace orders \
  --workload-label app=orders-app
```

The **static** target renders an Envoy 1.31 static bootstrap (validates
first; errors abort output). The **istio** target renders an Istio
`EnvoyFilter` CRD with three patches scoped to `SIDECAR_INBOUND`: a STATIC
cluster for the pv sidecar at 127.0.0.1, the `ext_proc` HTTP filter, and a
probe-path carve-out for liveness/readiness paths. Omit `-o` to write to
stdout. App teams put this command in CI so config drift fails the build
instead of silently shipping a stale render.

Flag availability differs per target:

| Flag | `--target=static` | `--target=istio` |
|---|---|---|
| `--sidecar-host` | optional (default 127.0.0.1) | not allowed (always 127.0.0.1) |
| `--sidecar-port` | optional (default 50051) | optional (default 50051) |
| `--backend-host`, `--backend-port`, `--admin-host`, `--access-log` | as documented | **rejected with error** |
| `--namespace` | **rejected with error** | required |
| `--workload-label key=value` (repeatable) | **rejected with error** | required, ≥1 |
| `--name` | **rejected with error** | optional (defaults to `permission-validation-<appId>`) |
| `--probe-paths <a,b,c>` | **rejected with error** | optional (defaults to `/healthz,/readyz,/livez`) |

Static-target-only flags:

- `--admin-host` (default `127.0.0.1`): bind address for Envoy's `:9901`
  admin interface. Defaults to loopback because that listener is
  unauthenticated and exposes mutating endpoints (`/quitquitquit`,
  runtime overrides). The e2e harness overrides it to `0.0.0.0` so the
  host can curl admin endpoints; do **not** do that in production.
- `--access-log` (default off): emits an `http_connection_manager` stdout
  access log. Off by default to keep test runs quiet; turn it on for
  production renders.

See [`examples/onboarding/`](examples/onboarding/) for a committed
production-style render and the adoption checklist.

### Sidecar `--routes-file`

With the **istio** target, route decisions move into the sidecar (the
EnvoyFilter only inserts the filter; it does not duplicate the route
table). Pass the same `routes.yaml` to the sidecar via `--routes-file`:

```sh
./permission-validation \
  --listen 0.0.0.0:50051 \
  --pcs-endpoint http://permission-checking.internal:8080 \
  --pcs-timeout 250ms \
  --routes-file /etc/pv/routes.yaml \
  --otel-disabled
```

The sidecar parses the file at startup (fail-fast on schema error) and
consults its compiled matcher before any header parse or PCS call:
skipped routes and the default-deny catch-all return ALLOW/DENY
immediately; protected matches fall through to the Phase 1 decision
path. Without `--routes-file`, the sidecar behaves exactly as Phase 1.

## Route config

YAML schema in [`../prd/permission-validation/phase-1-route-config-schema.md`](../prd/permission-validation/phase-1-route-config-schema.md);
fixtures under `testdata/routes/`. Sketch:

```yaml
version: v1
appId: orders-app
defaultBehavior: deny           # or "skipped"
routes:
  - method: GET
    path: /health
    behavior: skipped           # bypasses the sidecar entirely
  - method: GET
    path: /api/orders/*         # gitignore-style glob (* = one segment, ** = many)
    behavior: protected         # validated via PCS
  - method: POST
    path: /api/orders
    behavior: protected
```

`defaultBehavior` controls the catch-all route Envoy gets for anything not
matched above — `deny` emits a `direct_response: 403`, `skipped` emits a
catch-all route with `ext_proc` disabled.

## Metrics

OpenTelemetry instruments exposed via OTLP/gRPC (see
[`../prd/permission-validation/phase-1-request-contract.md`](../prd/permission-validation/phase-1-request-contract.md) §5):

| Name | Type | Attributes |
|---|---|---|
| `pv.decisions.total` | counter | `outcome=allow\|deny\|error` |
| `pv.header_invalid.total` | counter | `reason=missing_authz\|missing_ctx\|malformed_authz` |
| `pv.ctx_parse_failure.total` | counter | `reason=` (six labels — see context-header design doc) |
| `pv.sidecar.latency` | histogram (ms) | — |
| `pv.pcs.latency` | histogram (ms) | — |

`pv.pcs.latency` is recorded for both successful and failed PCS calls, so
SREs can distinguish "PCS is slow" from "the sidecar itself is slow."

## Testing

Unit tests, vet, build, gofmt:

```sh
go test ./...
go vet ./...
go build ./...
test -z "$(gofmt -l .)"
```

End-to-end tests are build-tag-gated (`//go:build e2e`) so a plain
`go test ./...` does not require Docker. The e2e suite drives HTTP through
Envoy on `:8000` and asserts granted/denied/missing/malformed/over-length/
PCS-error/skipped-route behavior end to end.

```sh
cd test/e2e && make up                # docker compose up --build -d
cd ../.. && go test -tags=e2e ./test/e2e/... -v
cd test/e2e && make down
```

`make envoy.yaml` regenerates `test/e2e/envoy.yaml` from
`test/e2e/routes.yaml` via the `validate-routes` CLI; the generated file is
checked in so reviewers and CI can read it without running the generator.

## Out of scope for Phase 1

Recorded here so they aren't accidentally added without a design pass:

- URL / body cross-check against `X-Auth-Context.objectId` (the trust-model
  caveat above).
- Response-phase observation for the Phase 1.5 WAL invariant — the sidecar
  currently configures `processing_mode.response_header_mode: SKIP`.
- Decision caching and event-driven invalidation.
- xDS-based Envoy config delivery — Phase 1 ships with static bootstrap.
- Fail-open. Every error path (missing header, parse failure, PCS error/
  timeout) results in `403 Forbidden`.
