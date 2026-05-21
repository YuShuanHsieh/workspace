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
| `envoyfilter.yaml` | Literal `validate-routes translate --target=istio` output — STATIC `pv_sidecar` cluster at 127.0.0.1:50051 + `ext_proc` filter + probe-path carve-out. Regenerable. |
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

## Skipped routes via VIRTUAL_HOST patch

`/healthz`, `/readyz`, and `/livez` bypass `ext_proc` at the Envoy level —
the EnvoyFilter's third `configPatch` (`applyTo: VIRTUAL_HOST`) marks
them with `ExtProcPerRoute.disabled: true`, so istio-proxy never opens
a gRPC stream to the sidecar for those paths. This means probes survive
sidecar restarts entirely.

`routes.yaml`'s `behavior: skipped` rules are honoured by the sidecar
itself (via `--routes-file`) for any other skipped routes you declare;
the EnvoyFilter only handles the probe paths.

To override the probe paths at generation time:

```sh
validate-routes translate routes.yaml --target=istio \
  --namespace demo-istio --workload-label app=echo-app \
  --probe-paths=/healthz,/custom-ready \
  -o envoyfilter.yaml
```
