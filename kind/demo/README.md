# kind/demo — Umbrella Helm chart for the ext-authz kind demo

This folder packages the entire ext-authz kind demo as a single Helm release.

## Why this exists

To make registry / image / tag changes a one-file edit. When running this demo
in a company that uses a private Docker registry, edit
[`values.yaml`](values.yaml) under `images.*` and re-run `setup.sh`. No other
file changes are required.

## What's inside

| File | Purpose |
|---|---|
| `Chart.yaml` | Umbrella chart metadata + dependencies on `base` and `istiod` |
| `values.yaml` | Central config: image overrides, resource sizing, EnvoyFilter wiring, gateway settings |
| `charts/` | Vendored Istio subchart tarballs (`base-1.24.2.tgz`, `istiod-1.24.2.tgz`) |
| `templates/` | App Kubernetes manifests as Helm templates that read image refs from `values.yaml` |

## Limitation: gateways are NOT subchart deps

Helm installs all subchart dependencies into the parent release's namespace.
`base` and `istiod` both belong in `istio-system` (so they work as subchart
deps of an umbrella released into `istio-system`). The two per-namespace
ingressgateways must live in `documents` and `wiki` respectively, which a
single Helm release cannot do. `setup.sh` therefore performs two additional
`helm upgrade --install` steps for the gateways, **reading the same
`values.yaml`** so configuration stays centralised.

## Swap to a private registry

Edit `values.yaml`:

```yaml
images:
  echoServer:
    repository: my-private-registry.local/workspace/echo-server
    tag: v1.0.0
  ...
  istio:
    hub: my-private-registry.local/istio
    tag: 1.24.2
```

Re-run `./kind/setup.sh`. The umbrella chart and the two gateway installs all
read from this file.

## See also

- Full design: [`../../docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md`](../../docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md)
- The original per-resource manifests still live at [`../manifests/`](../manifests/) for reference and are NOT used by setup.sh — the umbrella chart's `templates/` are the source of truth now.
