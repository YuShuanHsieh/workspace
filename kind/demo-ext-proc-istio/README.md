# demo-ext-proc-istio — Option A Helm chart

main's permission-validation sidecar adapted to an Istio cluster. The
sidecar is a container in the echo-app pod, reached by istio-proxy via
an Envoy cluster derived from the echo-app Service's 50051 port. An
EnvoyFilter splices `envoy.filters.http.ext_proc` into istio-proxy's
HTTP filter chain.

## What is in this chart

| Template | Purpose |
|---|---|
| `namespace.yaml` | Namespace `demo-istio`, with `istio-injection: enabled`. |
| `echo-app.yaml` | Deployment with sidecar + echo containers + dual-port Service (8080 http, 50051 grpc-extproc). |
| `gateway.yaml`  | Istio Gateway + VirtualService for `Host: app.local` → echo-app:8080. |
| `envoyfilter.yaml` | The patch — splices ext_proc into istio-proxy with the `authority` HTTP/2 fix. |
| `pcs.yaml` | demo PCS Deployment + Service (HTTP :8080). |

## Install path

Run `kind/setup-istio.sh` from the repo root.

To install manually (assumes a kind cluster with Istio already installed and the three images already loaded):

```bash
helm install istio kind/demo-ext-proc-istio/ --wait
```

## What to point at during the demo

- `kubectl -n demo-istio get envoyfilter echo-ext-proc -o yaml` — how ext_proc gets spliced in.
- `kubectl -n demo-istio describe pod <echo-app>` — three containers including auto-injected `istio-proxy`.
- `kubectl -n demo-istio logs <echo-app-pod> -c sidecar` — same sidecar logs as Option B.
- `kubectl -n demo-istio logs <echo-app-pod> -c istio-proxy` — Envoy access logs.
- `kubectl -n demo-istio logs deploy/pcs` — one JSON line per decision.

## Known difference vs. Option B

`routes.yaml`'s `skipped:` list is **not** honoured by this option, because there is no per-route skip in the EnvoyFilter. A request to `/healthz` therefore goes through `ext_proc` and is rejected for missing `X-Auth-Context` (403), not passed through (200). To make Option A honour skipped routes, see [`docs/superpowers/specs/2026-05-18-istio-envoyfilter-target-design.md`](../../docs/superpowers/specs/2026-05-18-istio-envoyfilter-target-design.md).
