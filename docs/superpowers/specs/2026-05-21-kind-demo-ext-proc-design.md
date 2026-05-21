# Ext-Proc Kind Demo — Sidecar + Envoy + PCS End-to-End

> **Status:** Draft for review
> **Audience:** Platform team members learning Envoy's `ext_proc` filter and the Phase 1 `permission-validation` sidecar that lives in `permission-validation/` on `main`; reviewers comparing the `ext_proc` topology against the existing `ext_authz` demo on the `kind-demo` branch.
> **Owner:** Ashwini (platform)
> **New branch:** `kind-demo-ext_proc`, forked from `origin/main` (which contains PR #1 — the Phase 1 sidecar).
> **Related documents:**
> - [`2026-05-18-istio-envoyfilter-target-design.md`](2026-05-18-istio-envoyfilter-target-design.md) — *(on this branch)* the design for adding a `validate-routes translate --target=istio` render mode that would emit an Istio `EnvoyFilter` CRD instead of a static bootstrap. Not yet implemented; this demo's Option A hand-writes the EnvoyFilter for that reason.
> - [`prd/permission-validation/phase-1-topology-decision.md`](../../../prd/permission-validation/phase-1-topology-decision.md) — *(on this branch)* the decision record that chose `ext_proc + sidecar` over `ext_authz` and a custom HTTP proxy.
> - `docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md` — *(on the `kind-demo` branch)* the sibling `ext_authz` demo. This branch is the `ext_proc` counterpart.
> - `docs/superpowers/specs/2026-05-13-permission-validation-phase1-sidecar-design.md` — *(on the `kind-demo` branch)* the Phase 1 sidecar design from before PR #1 landed. The sidecar binary used here is the one that ultimately shipped on `origin/main`; this demo deploys it without modification.
> - *Parked scaffolding (currently in a git stash on this branch):* an earlier `kind/demo-ext-proc-minimal/` and `sample-apps/pcs-ext-proc-minimal/` — a "hello-world ext_proc on Istio" written as one combined gRPC service that bypasses main's sidecar. Not the canonical demo. The `authority` HTTP/2 fix that fell out of it is carried into this spec's Option A EnvoyFilter (§7.3). To inspect: `git stash list` on this branch.

---

## 1. Background and Motivation

PR #1 on `origin/main` shipped the Phase 1 `permission-validation` sidecar — a Go service that implements Envoy's `envoy.service.ext_proc.v3.ExternalProcessor` and asks the Permission Checking Service (PCS) whether each protected request should be allowed. Main also ships a `validate-routes` CLI whose `translate` subcommand renders a complete Envoy 1.31 static bootstrap from a developer-friendly `routes.yaml`. Until now the sidecar has only been exercised via `permission-validation/test/e2e`, which uses docker-compose. There is no Kubernetes deployment of it anywhere — not even on the `kind-demo` branch, which deliberately uses the simpler `ext_authz` filter for its demo.

This spec covers a new `kind-demo-ext_proc` branch whose purpose is to make the Phase 1 sidecar legible on a laptop: stand it up on a single-node `kind` cluster, send curl requests, watch logs, and read the YAML the sidecar runs against. The branch sits alongside `kind-demo` (which remains the `ext_authz` reference) so a reader can compare the two topologies file by file. Both branches share a common ancestor (`211c33b`) but contain no overlapping deployment code; this spec only adds new folders on the new branch.

The demo serves two readers:

- **Someone learning what `permission-validation` actually does.** They want to point at the running Envoy config and the gRPC stream messages, not read the source. They run one script, curl four URLs, and watch logs.
- **Someone deciding how `permission-validation` would deploy in this team's clusters.** Their clusters have Istio sidecar injection on by default. They need to see whether the sidecar slots into Istio cleanly via an `EnvoyFilter` patch, and what changes if Istio injection is turned off.

To serve both, the demo ships **two co-equal deployment shapes** in two separate folders. Both shapes use the same `permission-validation` sidecar image, the same `routes.yaml` source of truth, and the same demo PCS. They differ only in how Envoy is provided to the pod.

## 2. Goal and Non-Goals

### 2.1 Goal

On a fresh checkout of the `kind-demo-ext_proc` branch, a reader can stand up either of two single-node `kind` clusters that exercise the Phase 1 sidecar end-to-end:

- **`demo-ext-proc-plain`** — a plain Envoy container reads the bootstrap that `validate-routes translate` produced; the sidecar and the backend run in the same pod; Istio sidecar injection is disabled on the namespace. This is "main's design as written."
- **`demo-ext-proc-istio`** — the cluster has Istio installed; the app pod is sidecar-injected as usual; an `EnvoyFilter` CRD patches `envoy.filters.http.ext_proc` into Istio's sidecar Envoy. This is "main's sidecar adapted to an Istio cluster," the deployment shape this team's production clusters would actually use.

The two shapes produce identical curl-level behaviour: four canonical requests yield `200 / 403 / 403 / 200` for allow / deny-on-permission / reject-on-malformed-header / skipped-route respectively.

### 2.2 Non-Goals

- **No changes to the `permission-validation` Go code.** The sidecar binary is consumed as-is from `permission-validation/cmd/permission-validation/` on `origin/main`. The image is built from the existing `permission-validation/test/e2e/Dockerfile.sidecar`.
- **No new `validate-routes` subcommand.** Main has `validate` and `translate`. There is no `translate --target=istio` mode and we do not add one in this demo. The design for that mode is captured in [`2026-05-18-istio-envoyfilter-target-design.md`](2026-05-18-istio-envoyfilter-target-design.md) and is the right follow-up if this team adopts Option A as the production deployment shape. In this demo the EnvoyFilter is hand-written in Helm; `validate-routes validate` runs as a lint step on the shared `routes.yaml` so the route declarations stay honest even though the EnvoyFilter is not generated from them.
- **No production-grade auth.** The `Authorization` bearer token is the user's email plain. PCS does not verify signatures. This is a teaching demo for the request flow, not a security demo.
- **No multi-namespace, multi-app onboarding story.** The sibling `ext_authz` demo deliberately covers three onboarding cases across two namespaces; this demo covers one app in one namespace. The point is to make the new filter type legible, not to re-demonstrate the multi-tenancy story.
- **No xDS, no Istio operator, no Helm subcharts.** Both demo charts are flat. Istio in the Istio demo is installed by the same `istio-base` / `istiod` / `gateway` chart tarballs already vendored on the `kind-demo` branch at `kind/charts/`.

## 3. Repository Layout on the `kind-demo-ext_proc` Branch

The branch is forked from `origin/main`, so everything PR #1 shipped is already present at `permission-validation/`. The additions for this demo are:

```text
kind-demo-ext_proc/                       (branched from origin/main)
├── permission-validation/                ← unchanged, inherited from main
├── kind/
│   ├── kind-config.yaml                  ← single-node cluster, extraPortMappings for both demos
│   ├── charts/                           ← Istio chart tarballs (consumed only by Option A)
│   │   ├── base-1.24.2.tgz
│   │   ├── istiod-1.24.2.tgz
│   │   └── gateway-1.24.2.tgz
│   ├── routes.yaml                       ← single source of truth, used by both demos
│   ├── demo-ext-proc-plain/              ← Option B — main's design as written
│   │   ├── Chart.yaml
│   │   ├── values.yaml
│   │   ├── README.md
│   │   └── templates/
│   │       ├── namespace.yaml            ← label: istio-injection=disabled
│   │       ├── envoy-bootstrap-cm.yaml   ← ConfigMap built from `validate-routes translate`
│   │       ├── echo-app.yaml             ← 3-container pod (envoy + sidecar + echo) + NodePort Svc
│   │       └── pcs.yaml                  ← demo-pcs Deployment + Service
│   ├── demo-ext-proc-istio/              ← Option A — sidecar adapted to Istio
│   │   ├── Chart.yaml
│   │   ├── values.yaml
│   │   ├── README.md
│   │   └── templates/
│   │       ├── namespace.yaml            ← label: istio-injection=enabled (the default in this team's clusters)
│   │       ├── echo-app.yaml             ← 2-container pod (sidecar + echo); istio-proxy auto-injected
│   │       ├── envoyfilter.yaml          ← patches ext_proc into istio-proxy
│   │       ├── gateway.yaml              ← Istio Gateway + VirtualService for app.local
│   │       └── pcs.yaml                  ← demo-pcs Deployment + Service
│   ├── setup-plain.sh                    ← Option B bring-up
│   ├── setup-istio.sh                    ← Option A bring-up
│   ├── teardown.sh                       ← shared teardown (takes cluster name as arg)
│   └── DEMO.md                           ← presenter script: 4 curls, expected outputs, what to point at
└── sample-apps/
    ├── echo-server/                      ← copied verbatim from kind-demo (deploy/Dockerfile, main.go)
    └── pcs-ext-proc/                     ← new, ~50 lines, speaks main's PCS contract
        ├── deploy/Dockerfile
        ├── go.mod
        └── main.go
```

The `permission-validation/test/e2e/Dockerfile.sidecar` on `main` already produces a usable image; `setup-*.sh` calls `docker build -f permission-validation/test/e2e/Dockerfile.sidecar -t workspace/permission-validation:dev permission-validation/` and `kind load docker-image`s it. No fork of the Dockerfile is required.

`routes.yaml` is checked in at the top of `kind/` because both demos consume it and neither owns it.

## 4. Demo PCS (`sample-apps/pcs-ext-proc/`)

A small Go HTTP service, ~50 lines, that honours main's PCS contract verbatim. It exists because the `test/e2e/fakes/pcs` on main is a test fixture (it returns 503 on an unknown rule, which is jarring for a learner) and reusing `kind-demo`'s PCS does not work — that PCS speaks the `ext_authz_http` contract (`x-workspace-user-id` header, status-code-as-decision), not main's JSON contract.

### 4.1 Contract

- **Endpoint:** `POST /permission-check/v1/check`
- **Request body:** `{"objectId":"...", "objectType":"...", "permission":"..."}`
- **Request header:** `Authorization: Bearer <user-email>` (in this demo, `<user-email>` is the user identity directly; no JWT verification)
- **Response 200:** `{"allowed": true | false}`
- **Response non-2xx:** the sidecar treats as `DecisionUnknown` and returns 403 to the client (fail-closed per PV1-009)

### 4.2 Rules (hardcoded)

| user | objectId | objectType | permission | decision |
|---|---|---|---|---|
| `alice@workspace.test` | `doc-1` | `document` | `edit` | allow |
| `alice@workspace.test` | `doc-1` | `document` | `read` | allow |
| `alice@workspace.test` | `doc-2` | `document` | `edit` | deny |
| `bob@workspace.test`   | `doc-1` | `document` | `read` | allow |
| `bob@workspace.test`   | `doc-1` | `document` | `edit` | deny |
| anything else | * | * | * | deny (default-deny) |

### 4.3 Logging

Every decision is logged as a single JSON line so the presenter can tail logs and read out individual decisions:

```text
{"ts":"2026-05-21T10:14:22Z","user":"alice@workspace.test","obj":"doc-1","type":"document","perm":"edit","decision":"allow"}
```

## 5. Shared Route Configuration (`kind/routes.yaml`)

```yaml
version: v1
appId: kind-demo
defaultBehavior: deny
routes:
  - method: GET
    path: /anything
    behavior: protected
  - method: POST
    path: /anything
    behavior: protected
  - method: GET
    path: /healthz
    behavior: skipped
```

`/anything` is `echo-server`'s standard "echo back the headers/body you got" endpoint, which is ideal for a demo because the response body shows the reader exactly which headers the backend received. The `/healthz` rule with `behavior: skipped` marks a route that bypasses the sidecar entirely (per-route `ExtProcPerRoute.disabled: true` in Envoy / a route-level exemption in the EnvoyFilter). The schema (`version`/`appId`/`defaultBehavior`/`routes` with per-rule `behavior`) is what `permission-validation/internal/config/schema.go` on this branch enforces — `version: "v1"`, an `appId` string, a `defaultBehavior` string, and a `routes` list where each entry carries its own `behavior`.

`validate-routes validate kind/routes.yaml` runs early in both `setup-*.sh` scripts and fails the run on a parse error.

## 6. Option B — `demo-ext-proc-plain` (Main's Design as Written)

### 6.1 Cluster shape

- Cluster name `ext-proc-plain-demo`.
- Single namespace `demo-plain` with `istio-injection=disabled` (so the pod is honoured exactly as written).
- Two Deployments: `pcs` (separate) and `echo-app` (which is a single Deployment whose pod template includes three containers: Envoy, sidecar, echo).
- One `Service` of type `NodePort` for `echo-app`, exposing pod port `8000` (Envoy's listener) on node port `30090`. `kind-config.yaml` maps host `8090 → 30090`.

### 6.2 Pod composition

The `echo-app` pod has three containers in the order Envoy → sidecar → app, but Kubernetes does not enforce startup order; all three boot in parallel and the sidecar's gRPC server, Envoy's listener, and echo-server's HTTP server all become ready independently. Envoy's `ext_proc` filter will retry on the gRPC stream until the sidecar is up; the readiness probe on `echo-app` Service is on Envoy's `:8000/healthz` (which routes to echo-server through the `skipped:` rule, so it does not depend on the sidecar at all).

| Container | Image | Port |
|---|---|---|
| `envoy` | `envoyproxy/envoy:v1.31.3` | `8000` (data), `9901` (admin, bound to `127.0.0.1`) |
| `sidecar` | `workspace/permission-validation:dev` (built from main's Dockerfile.sidecar) | `50051` (gRPC) |
| `echo` | `workspace/echo-server:dev` (from `sample-apps/echo-server/deploy/Dockerfile`) | `8080` |

### 6.3 Envoy bootstrap

The bootstrap is **generated** at setup time:

```text
go run ./permission-validation/cmd/validate-routes translate \
  kind/routes.yaml \
  -o /tmp/envoy.yaml \
  --sidecar-host 127.0.0.1 --sidecar-port 50051 \
  --backend-host 127.0.0.1 --backend-port 8080 \
  --admin-host 127.0.0.1
kubectl -n demo-plain create configmap envoy-bootstrap --from-file=envoy.yaml=/tmp/envoy.yaml
```

The ConfigMap is mounted into the `envoy` container at `/etc/envoy/envoy.yaml`. All three containers share the pod network namespace, so `127.0.0.1:50051` resolves to the sidecar and `127.0.0.1:8080` resolves to echo-server.

### 6.4 Request flow

```text
[host] curl -H "Authorization: Bearer alice@workspace.test" \
            -H "X-Auth-Context: doc-1:document:edit" \
            http://app.local:8090/anything
   │
   ▼  host port 8090 → kind NodePort 30090
[Service: echo-app, NodePort, demo-plain ns]
   │
   ▼  Service routes to pod port 8000
[Pod: echo-app]
  envoy:8000  ── ext_proc gRPC ──▶  sidecar:50051
                                       │
                                       │ HTTP POST /permission-check/v1/check
                                       ▼
                                    pcs Service (in-cluster)
                                       │
                                       ▼  {"allowed": true}
                                    sidecar replies CONTINUE
  envoy:8000  ──────────────────────▶  echo:8080
   │
   ▼
  response back to host
```

`app.local` is just a friendly name; because Envoy in this demo binds `virtual_hosts.domains: ["*"]`, the `Host` header on the request is not validated, and `curl http://app.local:8090/anything` works as long as `/etc/hosts` (or the kind extraPortMapping resolver) sends `app.local` to localhost.

### 6.5 What to point at

- `kubectl -n demo-plain get cm envoy-bootstrap -o yaml` — "this YAML was generated by `validate-routes translate`."
- `kubectl -n demo-plain logs <echo-app-pod> -c envoy` — Envoy access logs (if `--access-log` was passed; for the demo it is).
- `kubectl -n demo-plain logs <echo-app-pod> -c sidecar` — main's sidecar logs the `extract → parse → pcs → outcome` sequence per request.
- `kubectl -n demo-plain logs deploy/pcs` — one JSON line per decision (see §4.3).
- `kubectl -n demo-plain describe pod <echo-app>` — three containers, no `istio-proxy`.

## 7. Option A — `demo-ext-proc-istio` (Sidecar Adapted to Istio)

### 7.1 Cluster shape

- Cluster name `ext-proc-istio-demo`.
- Istio installed via the vendored `istio-base`/`istiod`/`gateway` charts in `kind/charts/` — same versions and same install steps as the `kind-demo` branch.
- Single namespace `demo-istio` with `istio-injection=enabled` (the default in this team's production clusters).
- Two Deployments: `pcs`, `echo-app`. Istio injects `istio-proxy` into `echo-app`'s pod automatically.
- One Istio `Gateway` and one `VirtualService` for `Host: app.local` routing to `echo-app` Service.
- `kind-config.yaml` maps host `8080 → 30080` (the istio-ingressgateway's NodePort).

### 7.2 Pod composition

| Container | Image | Source | Notes |
|---|---|---|---|
| `istio-proxy` | Istio-injected | Istio mutating webhook | The Envoy that this demo patches via EnvoyFilter |
| `sidecar` | `workspace/permission-validation:dev` | This demo | Same image as Option B |
| `echo` | `workspace/echo-server:dev` | `sample-apps/echo-server/` | Same image as Option B |

There is **no** standalone Envoy container in this option — Istio's `istio-proxy` is the only Envoy in the pod, and the `EnvoyFilter` CRD splices `ext_proc` into its existing HTTP filter chain.

### 7.3 EnvoyFilter

A single `EnvoyFilter` in `demo-istio` namespace, scoped via `workloadSelector` to the `echo-app` Deployment's labels:

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: echo-ext-proc
  namespace: demo-istio
spec:
  workloadSelector:
    labels:
      app: echo-app
  configPatches:
  - applyTo: HTTP_FILTER
    match:
      context: SIDECAR_INBOUND
      listener:
        filterChain:
          filter:
            name: envoy.filters.network.http_connection_manager
            subFilter:
              name: envoy.filters.http.router
    patch:
      operation: INSERT_BEFORE
      value:
        name: envoy.filters.http.ext_proc
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
          grpc_service:
            envoy_grpc:
              cluster_name: outbound|50051||echo-app.demo-istio.svc.cluster.local
              # Override :authority because Istio's xDS cluster_name contains
              # the `|` character, which is invalid in HTTP/2 :authority. Without
              # this, the gRPC call out of istio-proxy fails with a malformed-
              # header error. Lifted from earlier parked scaffolding (see Related
              # documents at the top of this spec).
              authority: echo-app.demo-istio.svc.cluster.local
            timeout: 1s
          processing_mode:
            request_header_mode: SEND
            response_header_mode: SKIP
            request_body_mode: NONE
            response_body_mode: NONE
          failure_mode_allow: false
          message_timeout: 250ms
```

The `cluster_name` references the Service that points at the sidecar's gRPC port. Because the sidecar lives in the same pod as `echo-app`, `127.0.0.1:50051` would also work, but a Service-backed cluster is what Istio expects and what xDS naming convention dictates.

The `/healthz` skip from `routes.yaml` is rendered as a second `configPatch` with `applyTo: HTTP_ROUTE` and `match.routeConfiguration.vhost.route.match.prefix: /healthz`, patching in an `ExtProcPerRoute.disabled: true` override. Implementation detail; not load-bearing for the request flow.

### 7.4 Request flow

```text
[host] curl -H "Host: app.local" \
            -H "Authorization: Bearer alice@workspace.test" \
            -H "X-Auth-Context: doc-1:document:edit" \
            http://localhost:8080/anything
   │
   ▼  host port 8080 → kind NodePort 30080
[istio-ingressgateway]
   │  Gateway + VirtualService route Host: app.local
   │  to demo-istio/echo-app Service
   ▼
[Pod: echo-app, Istio-injected]
  istio-proxy   ── ext_proc gRPC ──▶  sidecar:50051
   (Envoy with                            │
    ext_proc filter                       │ HTTP POST /permission-check/v1/check
    patched in by                         ▼
    EnvoyFilter)                       pcs Service
                                          │
                                          ▼  {"allowed": true}
                                       sidecar replies CONTINUE
  istio-proxy ─────────────────────▶  echo:8080
   │
   ▼
  response back through istio-proxy, ingressgateway, to host
```

Two Envoys are in the path here (ingressgateway's and the pod's istio-proxy), which is normal for any Istio deployment.

### 7.5 What to point at

- `kubectl -n demo-istio get envoyfilter echo-ext-proc -o yaml` — "this is how you splice ext_proc into an existing Istio sidecar Envoy."
- `kubectl -n demo-istio describe pod <echo-app>` — three containers including `istio-proxy`.
- `kubectl -n demo-istio logs <echo-app-pod> -c istio-proxy` — Envoy access logs from istio-proxy.
- `kubectl -n demo-istio logs <echo-app-pod> -c sidecar` — same sidecar logs as Option B.
- `kubectl -n demo-istio logs deploy/pcs` — same decision lines as Option B.

## 8. Demo Script (`kind/DEMO.md`)

For either cluster, the four canonical curls are:

```bash
# 1) ALLOW — alice editing doc-1
curl -i \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit" \
  ${BASE_URL}/anything
# Expected: 200 from echo-server, response body shows the headers it received.

# 2) DENY — alice trying to edit doc-2
curl -i \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-2:document:edit" \
  ${BASE_URL}/anything
# Expected: 403 from the sidecar; echo-server never sees the request.

# 3) REJECT — missing X-Auth-Context entirely
curl -i \
  -H "Authorization: Bearer alice@workspace.test" \
  ${BASE_URL}/anything
# Expected: 403 with reason metric "context_header_missing".

# 4) SKIPPED ROUTE — /healthz bypasses the sidecar
curl -i ${BASE_URL}/healthz
# Expected: 200, sidecar logs show route was bypassed (no PCS call).
```

For `demo-ext-proc-plain`, `BASE_URL=http://app.local:8090` and the request goes through the NodePort directly to the pod's Envoy.

For `demo-ext-proc-istio`, `BASE_URL=http://localhost:8080` plus `-H "Host: app.local"` and the request goes through istio-ingressgateway.

`DEMO.md` also includes two failure-injection scenarios. PCS is a separate Deployment in both options so its scenario is a clean `scale --replicas=0`; the sidecar is a pod-local container so its scenario uses `kubectl exec` to terminate the process inside the pod:

```bash
# PCS down — fail-closed (DecisionUnknown → 403)
kubectl -n <ns> scale deploy/pcs --replicas=0
# wait for endpoints to drain, then:
curl -i ${BASE_URL}/anything \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"
# Expected: 403 from the sidecar (fail-closed per PV1-009). Re-scale to 1 to recover.

# Sidecar down — fail-closed (ext_proc stream fails)
POD=$(kubectl -n <ns> get pod -l app=echo-app -o jsonpath='{.items[0].metadata.name}')
kubectl -n <ns> exec ${POD} -c sidecar -- /bin/sh -c 'kill 1'
# Kubernetes will restart the container; during the gap, requests hit a closed gRPC port.
curl -i ${BASE_URL}/anything \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"
# Expected: 503/504 from Envoy (failure_mode_allow: false everywhere — never bypass).
```

## 9. Error Handling and Failure Modes

No new error logic is being added. The demo surfaces main's existing behaviour:

- **`failure_mode_allow: false`** on the `ext_proc` filter in both options. Confirmed visually by the sidecar-down scenario in §8 — Envoy returns 5xx, never bypasses.
- **PCS unreachable** → `pcs.Client.Check` returns `DecisionUnknown` → `Handler.Decide` returns `OutcomeRejectError` → 403 with `internal_error` reason. Confirmed by the PCS-down scenario in §8.
- **Malformed `X-Auth-Context`** → parser returns `ParseError` → 403 with reason metric `bad_format`. Main's tests cover this; the demo just curls it to show the log line.
- **Missing `Authorization` header** → header extractor rejects → 403 with reason `auth_missing`. Same.

## 10. Testing and Acceptance Criteria

This demo is "tested" by the presenter script in `DEMO.md` running cleanly end-to-end on a fresh kind cluster:

1. `./kind/setup-plain.sh` (or `setup-istio.sh`) finishes within ~3 minutes on a MacBook (matching the sibling `kind-demo` branch's wall-clock).
2. The four canonical curls return the expected status codes (200, 403, 403, 200).
3. `kubectl logs deploy/pcs` shows one decision line per PCS-touching request.
4. `kubectl logs <echo-app-pod> -c sidecar` shows the `extract → parse → pcs → outcome` sequence — already logged by main's sidecar.
5. The two failure-injection scenarios in §8 behave as documented (5xx and 403 respectively).
6. `./kind/teardown.sh <cluster-name>` removes the cluster cleanly.

No new automated tests on this branch. The sidecar's own correctness is covered by `permission-validation/test/e2e` on `main`; this branch's job is deployment.

## 11. Out of Scope / Future Work

- ~~**A `translate --target=istio` mode in `validate-routes`.**~~ **Done.** See [`docs/superpowers/plans/2026-05-21-istio-envoyfilter-target-implementation.md`](../plans/2026-05-21-istio-envoyfilter-target-implementation.md). Option A's `envoyfilter.yaml` is now generated from `routes.yaml`; `/healthz` returns 200 (the previous "known difference vs Option B" caveat is removed).
- **Multi-namespace onboarding stories.** The sibling `ext_authz` demo's stage-1/stage-2 story (per-namespace filter → shared filter in `istio-system`) applies identically to `ext_proc`. Skipped here to keep the surface area small.
- **Response-phase enforcement.** Phase 1.5 needs the sidecar to observe responses (per [`prd/permission-validation/phase-1-5-metadata-sync-design.md`](../../../prd/permission-validation/phase-1-5-metadata-sync-design.md) §3.2). The `ext_proc` filter config in this demo runs with `response_header_mode: SKIP`, so this demo does not exercise that lever — it is forward-compatible (we can flip the mode in a follow-up) but not yet active.
- **Cache, batching, or any Phase 2 behaviour.** Out of scope.
