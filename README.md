# workspace

Permission-validation Phase 1 sidecar + supporting tooling.

## Repo layout

- `permission-validation/` — the Phase 1 sidecar (Go) and `validate-routes` CLI. See `permission-validation/README.md`.
- `prd/permission-validation/` — Phase 1 PRD, request/header/route contracts, topology decision record.
- `sample-apps/` — small Go services used by the kind demos.
- `kind/` — kind-based end-to-end demos. See [`kind/DEMO.md`](kind/DEMO.md).
- `docs/superpowers/specs/` — design docs (this branch's authoritative spec for the ext_proc kind demos is `2026-05-21-kind-demo-ext-proc-design.md`).

## kind demos

Two co-equal ext_proc kind demos live under `kind/`:

| Script | Cluster name | What it shows |
|---|---|---|
| `kind/setup-plain.sh` | `ext-proc-plain-demo` | main's design as written: plain Envoy + sidecar + echo in one pod, istio-injection disabled. |
| `kind/setup-istio.sh` | `ext-proc-istio-demo` | The same sidecar adapted to an Istio cluster via an EnvoyFilter. |

See [`kind/DEMO.md`](kind/DEMO.md) for the presenter script.
