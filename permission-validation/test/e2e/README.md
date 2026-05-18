# End-to-end tests

Black-box suite that drives real HTTP through Envoy + sidecar + fake PCS +
fake backend, running in docker-compose. Use this when you need to verify
end-to-end behavior the unit suite cannot reach (header propagation,
`ext_proc` framing, Envoy bootstrap correctness).

The Go suite itself lives in `e2e_test.go` and is gated by `//go:build e2e`,
so a plain `go test ./...` from the repo root does **not** require Docker.

## Stack

| Service | Image / source | Host port | Purpose |
|---|---|---|---|
| `envoy` | `envoyproxy/envoy:v1.31-latest` + generated `envoy.yaml` | `8000`, admin `9901` | Front door; routes `/api/orders/*` through the sidecar, `/health` straight to backend. |
| `sidecar` | `Dockerfile.sidecar` (builds `cmd/permission-validation`) | `50051` | The thing under test. Talks to fake-pcs. `--otel-disabled`. |
| `fake-pcs` | `fakes/pcs/main.go` | `9000` | Programmable PCS. Admin endpoints let tests register rules and reset state. |
| `fake-backend` | `fakes/backend/main.go` | `8080` | 200-OK echo with a call counter so tests can assert "did this request reach the backend?". |

`envoy.yaml` is generated from `routes.yaml` by the `validate-routes` CLI
and is checked in so reviewers can read it without running the generator.
Regenerate after editing `routes.yaml`.

## Prerequisites

- Go 1.25 (for regenerating `envoy.yaml` and running the test binary).
- Docker + Docker Compose v2 (`docker compose`, not the old `docker-compose`).
- `make` is optional — every Makefile target is a one-liner you can run by hand
  (see below).

## Run it (with `make`)

```
make -C test/e2e up                       # build + start the stack in the background
go test -tags=e2e ./test/e2e/... -v       # run from repo root
make -C test/e2e down                     # stop + remove containers, network, volumes
```

`make -C test/e2e envoy.yaml` regenerates `envoy.yaml` from `routes.yaml`;
`make up` depends on it.

## Run it (no `make`)

Same three steps spelled out, run from the repo root:

```
# 1. Regenerate envoy.yaml (only needed if routes.yaml or the translator changed).
go run ./cmd/validate-routes translate test/e2e/routes.yaml \
  -o test/e2e/envoy.yaml \
  --sidecar-host sidecar --sidecar-port 50051 \
  --backend-host fake-backend --backend-port 8080

# 2. Bring the stack up (first run builds three images; cached after).
docker compose -f test/e2e/docker-compose.yaml up --build -d

# 3. Run the suite.
go test -tags=e2e ./test/e2e/... -v

# 4. Tear it down.
docker compose -f test/e2e/docker-compose.yaml down -v
```

The Go suite probes `GET http://127.0.0.1:8000/health` for up to 20 s before
the first test runs, so you don't usually need to wait between `up` and
`go test`. Set `E2E_SKIP_WAIT=1` to skip the readiness probe if the stack is
already known to be ready.

## What the suite covers

| Test | Asserts |
|---|---|
| `TestE2E_GrantedReachesBackend` | PCS allow → request reaches backend, backend call counter increments. |
| `TestE2E_DeniedReturns403` | PCS deny → `403`, backend **never** sees the request. |
| `TestE2E_MissingAuthRejected` | No `Authorization` header → `403` at the sidecar. |
| `TestE2E_MalformedContextRejected` | `X-Auth-Context` missing a segment → `403`. |
| `TestE2E_OverLengthContextRejected` | `objectId > 1024 bytes` → `403`. |
| `TestE2E_PCSErrorFailClosed` | PCS returns 5xx → `403` (fail-closed). |
| `TestE2E_SkippedRouteBypassesSidecar` | `/health` (declared `skipped` in `routes.yaml`) bypasses the sidecar. |

Each test calls `POST /_admin/reset` on both fake-pcs and fake-backend, then
seeds the PCS rule it needs via `POST /_admin/rules`.

## Poke the stack by hand

While the stack is up, the fakes expose admin endpoints so you can drive
scenarios from `curl` without running the Go suite:

```
# Seed a rule: allow doc-1|document|view.
curl -s -X POST http://127.0.0.1:9000/_admin/rules \
  -H 'content-type: application/json' \
  -d '{"doc-1|document|view": true}'

# Send a request through Envoy with the matching context.
curl -i http://127.0.0.1:8000/api/orders/123 \
  -H 'Authorization: Bearer sso-tok' \
  -H 'X-Auth-Context: doc-1:document:view'

# Inspect calls that reached the backend / decisions PCS made.
curl -s http://127.0.0.1:8080/_admin/calls | jq
curl -s http://127.0.0.1:9000/_admin/calls | jq

# Reset between scenarios.
curl -s -X POST http://127.0.0.1:9000/_admin/reset
curl -s -X POST http://127.0.0.1:8080/_admin/reset
```

Envoy's admin UI is on `http://127.0.0.1:9901` — useful for inspecting the
loaded config (`/config_dump`) or listener stats.

## Troubleshooting

- **`go test` returns `no test files`** — you forgot `-tags=e2e`; the suite
  is build-tag-gated.
- **Probe fails / connection refused on `:8000`** — `docker compose ps`. If
  `envoy` is restarting, `docker compose logs envoy` usually points at a
  bad `envoy.yaml` (regenerate it).
- **Rule lookups miss** — admin reset wipes both rules and call logs.
  Re-seed the rule after `/_admin/reset`. Rule keys are
  `objectId|objectType|action` (pipe-delimited), not the colon form used in
  the `X-Auth-Context` header.
- **Sidecar build is slow on first `up`** — `Dockerfile.sidecar` compiles
  `cmd/permission-validation` inside the image. Subsequent builds hit the
  Docker layer cache; only changes under `cmd/` or `internal/` invalidate
  it.
- **Port already in use** — another stack is still up. `docker compose
  -f test/e2e/docker-compose.yaml down -v` (or just `docker ps` to find the
  offender).
