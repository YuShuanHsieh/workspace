# kind/demo — Thin umbrella chart for the ext-authz demo app resources

This is a Helm chart that packages the **app-side** Kubernetes resources of the
ext-authz kind demo: namespaces, Deployments, Services, EnvoyFilters, Gateway,
and VirtualService for both the `documents` and `wiki` namespaces.

Istio itself (istio-base, istiod, the two per-namespace ingressgateways) is
installed by [`../setup.sh`](../setup.sh) as separate Helm releases from
[`../charts/`](../charts/). This split is intentional — see "Why no subcharts"
below.

## What this chart owns

```
templates/
├── _helpers.tpl                      # image-ref helpers, sidecar annotation, opt-in label
├── namespace-documents.yaml
├── namespace-wiki.yaml
├── pcs.yaml                          # Deployment + Service in documents ns
├── documents-api.yaml                # Deployment + Service (opt-in label)
├── documents-search.yaml             # Deployment + Service (opt-in label)
├── documents-ext-authz.yaml          # EnvoyFilter (workload-selector → opt-in label)
├── documents-gateway.yaml            # Gateway + VirtualService for documents.local
├── dashboard-client.yaml             # Deployment (caller; no opt-in label)
├── wiki-api.yaml                     # Deployment + Service (opt-in label)
├── wiki-ext-authz.yaml               # EnvoyFilter (cross-ns copy; same PCS target)
└── wiki-gateway.yaml                 # Gateway + VirtualService for wiki.local
```

## Single point to swap container registry / tag

Edit [`values.yaml`](values.yaml). Every image used by the demo — app AND
Istio — is one line in the `images.*` block. One image = one line.
For a private-registry deployment:

```yaml
images:
  pullPolicy: IfNotPresent
  echoServer:      my-private-registry.local/workspace/echo-server:v1.0.0
  pcs:             my-private-registry.local/workspace/pcs:v1.0.0
  dashboardClient: my-private-registry.local/workspace/dashboard-client:v1.0.0
  istio:           my-private-registry.local/istio:1.24.2   # hub:tag pair
```

Notes:
- The `istio:` line is a `hub:tag` pair (no image-name suffix). The Istio
  charts append `pilot`, `proxyv2`, etc. internally, so all Istio components
  (istiod, both ingressgateways, every sidecar) inherit from this one line.
- `setup.sh` reads `images.istio` with a tiny `awk` one-liner and passes
  `--set global.hub` / `--set global.tag` to the istiod and gateway Helm
  installs — that's how the single source of truth propagates everywhere.

Then re-run `./kind/setup.sh`. All four Helm releases (istio-base, istiod, both
ingressgateways, and `demo`) pick up the new values. The kind node image is
NOT in this file — see [`../kind-config.yaml`](../kind-config.yaml) for that
one-line override.

## Other tunables in values.yaml

- `resources.app` / `resources.caller` / `resources.ingressgateway` — Pod CPU/memory requests and limits. Defaults are MacBook-minimal.
- `resources.sidecar.*` — Istio sidecar (Envoy) resource caps via Pod annotations.
- `extAuthz.optInLabel` — the workload-selector label that EnvoyFilter matches.
- `extAuthz.pcsService` — host/port/path-prefix that both EnvoyFilters call to reach PCS.
- `gateways.*` — per-namespace ingressgateway host/NodePort (consumed by `setup.sh`).

## Why no subcharts

Earlier attempts wrapped the Istio charts (`istio-base`, `istiod`, `gateway`) as
subchart dependencies of this umbrella. Three Helm behaviours combined to make
that approach fail:

1. **`helm dependency update` semantics** — vendored `.tgz` files at
   `charts/<dep>.tgz` only work as subchart deps when Chart.yaml declares
   `repository: ""` AND the deps are unpacked into directories, not left as
   tarballs. With tarballs only, `helm dep update` errors with "directory not
   found"; with empty repo and tarballs, install proceeds but is shaky.

2. **Resource ownership conflicts** — when `istio-base` is installed as a
   separate release first (to provide CRDs for our EnvoyFilter / Gateway /
   VirtualService templates), then the umbrella's `istiod` subchart attempts to
   re-create overlapping resources (`istio-reader-service-account`,
   `ClusterRole`s). Helm refuses to import resources owned by another release.

3. **Auto-pickup of `charts/*.tgz`** — Helm picks up *any* tarball in a chart's
   `charts/` directory at install time, even if `Chart.yaml` does not declare it
   as a dependency. Leftover tarballs caused additional `PodDisruptionBudget`
   conflicts.

The pragmatic answer is the same shape Istio's own install docs use: install
`istio-base`, `istiod`, and each gateway as their own top-level releases. This
chart keeps the app side cleanly templated and the values centralized.

## Develop locally

Render the chart locally without installing:

```bash
helm template demo kind/demo --skip-tests
```

Verify the renderer:

```bash
helm lint kind/demo
```

The chart has no subchart dependencies, so `helm dependency update` is not
required.
