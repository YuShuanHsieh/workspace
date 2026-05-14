# Workspace — Ext-Authz Kind Demo

A local kind cluster that demonstrates the Envoy `ext_authz` pattern: every
HTTP request to a protected workload is gated through a sidecar, which calls
a Permission Checking Service (PCS) for an allow/deny decision before the
request ever reaches the app container. Deny is enforced at the Envoy
sidecar — the app code is never invoked for denied requests.

The demo exercises three onboarding cases end-to-end:

1. **Main product workload** — `documents-api` in the `documents` namespace.
2. **Sibling workload in the same namespace** — `documents-search`, also in `documents`.
3. **Workload in a different namespace** — `wiki-api` in `wiki`, with its own copied EnvoyFilter that calls back across namespaces into the documents-team-owned PCS.

## Repository map

| Path | Purpose |
|---|---|
| [`docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md`](docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md) | Full design spec (12 sections — architecture, ownership, request flow, EnvoyFilter wiring, trade-offs, success criteria) |
| [`docs/superpowers/plans/2026-05-14-ext-authz-kind-demo.md`](docs/superpowers/plans/2026-05-14-ext-authz-kind-demo.md) | 32-task implementation plan |
| [`DEMO.md`](DEMO.md) | Live demo walkthrough — step-by-step presenter script, expected output, FAQ, and troubleshooting |
| [`sample-apps/echo-server/`](sample-apps/echo-server/) | Shared Go HTTP echo server — image used by `documents-api`, `documents-search`, `wiki-api` |
| [`sample-apps/pcs/`](sample-apps/pcs/) | Permission Checking Service (Go) — `POST /check` returns 200 / 403 |
| [`sample-apps/dashboard-client/`](sample-apps/dashboard-client/) | 6-call request driver loop |
| [`kind/`](kind/) | kind cluster config, `setup.sh`, `teardown.sh`, vendored Istio charts |
| [`kind/demo/`](kind/demo/) | Umbrella Helm chart for the app k8s manifests (single `values.yaml` swap point for image registry / tag) |
| [`kind/manifests/`](kind/manifests/) | Original per-resource k8s manifests (reference; not used by `setup.sh` anymore) |
| [`kind/chart-values/`](kind/chart-values/) | Per-chart values from the pre-umbrella shape (reference) |

## Architecture diagrams (Figma)

- **Top-down cluster topology:** <https://www.figma.com/board/ETLvYum9OPBgUtdFi0CC6r>
- **Left-to-right request flow:** <https://www.figma.com/board/wizcwM5QT7kknm5ZDLTXr6>

## TL;DR — run the demo

> Giving a live demo? See [DEMO.md](DEMO.md) for the full walkthrough script + FAQ.

Prerequisites: Docker Desktop running with ≥ 6 GB RAM, plus `kind`, `kubectl`, `helm`, and `go` (≥ 1.25) installed.

> **Cross-platform note:** the demo runs on macOS Docker Desktop AND Linux Docker without changes. All host ports are unprivileged (8080, 8081, 8443), so no `sudo` is required for port binding on either platform — only for the optional one-time `/etc/hosts` edit.

```bash
./kind/setup.sh
```

`setup.sh` is idempotent and takes about 2-3 minutes on a warm Docker cache. When it finishes, watch the protected-traffic cycle:

```bash
kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f
```

External curl — primary path (add one line to `/etc/hosts`, then use clean URLs):

```bash
# One-time setup (requires sudo):
echo '127.0.0.1  documents.local wiki.local' | sudo tee -a /etc/hosts

# Then:
curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local:8080/hello   # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello   # 403
curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello        # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello        # 403
```

Alternative (no `/etc/hosts` edit — useful in CI or environments without sudo):

```bash
curl --resolve documents.local:8080:127.0.0.1 -H "x-workspace-user-id: alice@workspace.test"   http://documents.local:8080/hello
curl --resolve documents.local:8080:127.0.0.1 -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello
curl --resolve wiki.local:8081:127.0.0.1      -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello
curl --resolve wiki.local:8081:127.0.0.1      -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello
```

Teardown:

```bash
./kind/teardown.sh
```

## Swapping to a private container registry

Edit [`kind/demo/values.yaml`](kind/demo/values.yaml) — the `images.*` block is the single override point. Re-run `./kind/setup.sh`. See [`kind/demo/README.md`](kind/demo/README.md) for a walked example.

## Design philosophy

This is the local verification harness for the broader Permission Validation Phase 1 design at [`docs/superpowers/specs/2026-05-13-permission-validation-phase1-sidecar-design.md`](docs/superpowers/specs/2026-05-13-permission-validation-phase1-sidecar-design.md). The demo proves the headline architectural claim — deny is enforced at the Envoy sidecar layer, not in app code — in a fully self-contained local cluster.

Stage 1 of the demo (current scope): per-namespace `EnvoyFilter` copies, with each app team's filter calling the documents-team-owned PCS via cluster DNS. Stage 2 (future): collapse the per-namespace filters into a single resource in `istio-system` after platform team approval — same opt-in label, no app-team Deployment changes required.
