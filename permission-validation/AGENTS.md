# permission-validation — Agent Context

Envoy `ext_proc` gRPC sidecar. On every request Envoy forwards headers here;
we call PCS and either forward or reject with 403.

## Internal packages

| Package | Role |
|---|---|
| `internal/config` | Parse + validate `routes.yaml`; translate to Envoy static bootstrap |
| `internal/extproc` | ext_proc gRPC server, `Decide()` orchestrator |
| `internal/header` | Parse `Authorization` and `X-Auth-Context` headers |
| `internal/pcs` | HTTP client for Permission Checking Service |
| `internal/metrics` | OpenTelemetry counters + histograms |

## Key binaries

```sh
go build ./cmd/permission-validation   # sidecar (ext_proc gRPC server)
go build ./cmd/validate-routes         # CLI: validate or translate routes.yaml
```

## Routes config (routes.yaml)

```yaml
version: v1
appId: orders-app
defaultBehavior: deny        # or "skipped"
routes:
  - method: GET
    path: /health
    behavior: skipped
  - method: GET
    path: /api/orders/*      # gitignore-style glob
    behavior: protected
```

`defaultBehavior: deny` → catch-all emits `direct_response: 403`.
`defaultBehavior: skipped` → catch-all disables ext_proc for unmatched routes.

Validate and translate:
```sh
validate-routes validate routes.yaml
validate-routes translate routes.yaml -o envoy.yaml \
  --sidecar-host sidecar --sidecar-port 50051 \
  --backend-host orders-app --backend-port 8080
```

## Testing

```sh
go test ./...          # unit tests (no Docker needed)
```

E2e (requires Docker):
```sh
cd test/e2e && make up
cd ../.. && go test -tags=e2e ./test/e2e/... -v
cd test/e2e && make down
```

Regenerate checked-in `test/e2e/envoy.yaml`:
```sh
cd test/e2e && make envoy.yaml
```

## Metrics reference

| Metric | Type | Labels |
|---|---|---|
| `pv.decisions.total` | counter | `outcome=allow\|deny\|error` |
| `pv.header_invalid.total` | counter | `reason=missing_authz\|missing_ctx\|malformed_authz` |
| `pv.sidecar.latency` | histogram (ms) | — |
| `pv.pcs.latency` | histogram (ms) | — |

## Fixtures

Valid/invalid route-config YAML: `testdata/routes/`
E2e wire examples: `examples/onboarding/`
