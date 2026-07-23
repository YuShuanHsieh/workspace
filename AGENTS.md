# Workspace — Agent Context

## Repo overview

Platform infrastructure sidecars for a Kubernetes workspace. Two independent Go 1.25 modules:

| Directory | Purpose |
|---|---|
| `permission-validation/` | Envoy `ext_proc` gRPC sidecar — intercepts requests, calls Permission Checking Service (PCS), rejects with 403 or forwards |
| `event-adapter/` | NATS JetStream → local HTTP dispatch sidecar — consumes CloudEvents, calls app handlers, publishes response events |

Design docs live under `prd/` (PRDs, contracts, schemas, user stories).
Implementation plans live under `docs/superpowers/plans/`.

## Cross-cutting conventions

- **Go 1.25** — each module has its own `go.mod`; run commands from within the module directory.
- **No shared module** — `permission-validation` and `event-adapter` are fully independent; do not introduce cross-module imports.
- **gofmt required** — CI checks `gofmt -l .`; always format before committing.
- **Build tags gate e2e** — `//go:build e2e` keeps Docker-dependent tests out of `go test ./...`.
- **OpenTelemetry** — all metrics use OTLP/gRPC; pass `--otel-disabled` for local dev without a collector.

## Standard check (run from module dir)

```sh
go build ./...
go vet ./...
go test ./...
test -z "$(gofmt -l .)"
```

## Design references

- Trust model & Phase 1 scope: `prd/permission-validation/PRD.md`
- Request contract (headers, PCS call): `prd/permission-validation/phase-1-request-contract.md`
- Route config schema: `prd/permission-validation/phase-1-route-config-schema.md`
- Event-adapter design: `prd/event-adapter/prd.md`
- App-to-app spec: `prd/app-to-app/draft.md`

## Hard constraints (do not violate without a design doc)

- Every error path (missing header, parse failure, PCS timeout) must return **403 Forbidden** — fail-closed, no fail-open.
- Response-phase observation (`response_header_mode`) is **SKIP** in Phase 1 — do not add response processing without a Phase 1.5 design pass.
- Decision caching, xDS config delivery, and URL/body cross-check against `X-Auth-Context.objectId` are **out of Phase 1 scope**.

See module-level `AGENTS.md` for build, test, and internal package details.
