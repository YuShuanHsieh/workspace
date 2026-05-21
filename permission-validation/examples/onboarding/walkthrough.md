# App-team adoption walkthrough

A concrete, worked example of onboarding a real service to permission-validation in an Istio-injected cluster — written from the app team's perspective, not the platform team's.

> **Status note.** This walkthrough describes the workflow that will exist once the `validate-routes translate --target=istio` mode and the sidecar's `--routes-file` flag ship. The implementation plan for that work is at [`docs/superpowers/plans/2026-05-21-istio-envoyfilter-target-implementation.md`](../../../docs/superpowers/plans/2026-05-21-istio-envoyfilter-target-implementation.md). Until then, app teams running on a static Envoy (no Istio) should follow the existing [`README.md`](./README.md) in this directory.

Companion to [`README.md`](./README.md): the README is a reference (commands, flag tables, contract details); this walkthrough is a narrative around one concrete example. Read this first, then go to the README for the details that come up mid-task.

## The scenario

Team "Orders" owns the `orders-app` service. They live in namespace `orders`. The platform team has Istio installed cluster-wide; `istio-injection: enabled` is the namespace default. They want to start enforcing permissions on their endpoints, using PCS (the Permission Checking Service the platform team operates).

## Day 1: One-time onboarding (~45 minutes)

### Step 1 — Decide what to protect (~10 min, a meeting)

The team lists their HTTP endpoints and classifies each:

| Endpoint | Behaviour | Why |
|---|---|---|
| `GET /api/orders` | **protected** | Authenticated callers only, action = `list` on the tenant |
| `GET /api/orders/:id` | **protected** | Authenticated callers only, action = `read` on the specific order |
| `POST /api/orders` | **protected** | Authenticated callers only, action = `create` |
| `DELETE /api/orders/:id` | **protected** | Authenticated callers only, action = `delete` |
| `GET /metrics` | **skipped** | Prometheus scrapes from inside the mesh; no auth on this path |
| `GET /healthz`, `/readyz`, `/livez` | (handled by EnvoyFilter probe-path patch) | Kubelet probes; no auth required |
| Anything else | (covered by `defaultBehavior: deny`) | Unknown paths return 403 — defense in depth |

The probe paths are handled automatically by the EnvoyFilter's `VIRTUAL_HOST` patch; they do not appear in `routes.yaml`.

### Step 2 — Write `routes.yaml` (~5 min)

Place it next to the team's existing Kubernetes manifests:

```yaml
# orders-app/deploy/routes.yaml
version: v1
appId: orders-app
defaultBehavior: deny     # any unmatched path → 403, no PCS call

routes:
  - { method: GET,    path: /api/orders,       behavior: protected }
  - { method: GET,    path: /api/orders/*,     behavior: protected }
  - { method: POST,   path: /api/orders,       behavior: protected }
  - { method: DELETE, path: /api/orders/*,     behavior: protected }
  - { method: GET,    path: /metrics,          behavior: skipped   }
```

### Step 3 — Lint before going further (~10 sec)

```bash
validate-routes validate orders-app/deploy/routes.yaml
echo "exit: $?"
```

Expected: exit 0. Typos and schema errors surface here instead of in Kubernetes after a deploy.

### Step 4 — Generate the EnvoyFilter (~10 sec)

```bash
validate-routes translate orders-app/deploy/routes.yaml \
  --target=istio \
  --namespace orders \
  --workload-label app=orders-app \
  -o orders-app/deploy/envoyfilter.yaml
```

The output is a ~70-line `EnvoyFilter` CRD. **Commit it to the repo next to `routes.yaml`.** Reviewers see exactly what is being applied to the cluster; the CRD is regenerable byte-for-byte by re-running the command above.

### Step 5 — Update the Deployment to add the sidecar container (~15 min)

Two additions to `orders-app/deploy/deployment.yaml`: the `pv-sidecar` container, and the `pv-routes` ConfigMap volume.

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: orders-app
  namespace: orders
spec:
  template:
    spec:
      containers:
      # (existing app container — unchanged)
      - name: orders-app
        image: orders-app:1.2.3
        ports: [{ containerPort: 8080 }]

      # NEW — the permission-validation sidecar
      - name: pv-sidecar
        image: <registry>/permission-validation:<tag>
        args:
        - "--listen=127.0.0.1:50051"      # loopback only — istio-proxy reaches it pod-local
        - "--pcs-endpoint=http://permission-checking.platform:8080"
        - "--pcs-timeout=250ms"
        - "--routes-file=/etc/pv/routes.yaml"
        volumeMounts:
        - name: pv-routes
          mountPath: /etc/pv

      # NEW — volume for routes.yaml
      volumes:
      - name: pv-routes
        configMap:
          name: pv-routes
```

Plus a ConfigMap holding `routes.yaml` (or have helm/kustomize generate it from `routes.yaml` automatically — no manual copy):

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: pv-routes
  namespace: orders
data:
  routes.yaml: |
    version: v1
    appId: orders-app
    defaultBehavior: deny
    routes:
      # ... exact content of routes.yaml ...
```

### Step 6 — Apply everything (~30 sec)

Order matters slightly: ConfigMap first (pod needs it on first start), then EnvoyFilter (istiod needs it before the pod gets its config), then the Deployment.

```bash
kubectl apply -f orders-app/deploy/configmap.yaml
kubectl apply -f orders-app/deploy/envoyfilter.yaml
kubectl apply -f orders-app/deploy/deployment.yaml
```

### Step 7 — Verify (~5 min)

```bash
# Sidecar is up and read routes.yaml
kubectl -n orders logs deploy/orders-app -c pv-sidecar
# Expected: "permission-validation listening on 127.0.0.1:50051"

# EnvoyFilter is actually in istio-proxy's filter chain
POD=$(kubectl -n orders get pod -l app=orders-app -o jsonpath='{.items[0].metadata.name}')
istioctl proxy-config listener $POD -n orders | grep ext_proc
# Expected: ext_proc filter present on the inbound listener

# Real curl from inside the mesh
kubectl -n orders run testcurl --rm -it --image=curlimages/curl -- \
  curl -i -H "Authorization: Bearer alice@workspace.test" \
         -H "X-Auth-Context: order-123:order:read" \
         http://orders-app:8080/api/orders/123
# Expected: 200 (assuming alice has order:read on order-123 in PCS)
```

That's the entire one-time setup. From here on, the workflow is the iteration loop below.

## Day 2+: Iteration

### Add a new endpoint to the app

The team adds `GET /api/orders/export`. Two edits, one redeploy:

1. Add a line to `routes.yaml`:
   ```yaml
     - { method: GET, path: /api/orders/export, behavior: protected }
   ```
2. Update the ConfigMap (kustomize/helm picks up the file change automatically; otherwise `kubectl create configmap pv-routes --from-file=routes.yaml=... -n orders --dry-run=client -o yaml | kubectl apply -f -`).
3. `kubectl rollout restart deployment/orders-app -n orders` so the sidecar reloads.

The EnvoyFilter **does not need re-applying** because route decisions live in the sidecar's `routes.yaml`, not in the EnvoyFilter. The EnvoyFilter only needs re-applying if probe paths, namespace, or workload-label change — all rare events.

Total time: ~2 minutes.

### Skip a route that no longer needs auth

Change `behavior: protected` → `behavior: skipped` in `routes.yaml`, update ConfigMap, rollout restart. The sidecar starts short-circuiting that path; PCS never sees those requests. `routes.yaml` is the source of truth; the EnvoyFilter is not touched.

### Remove a route entirely

Delete the entry from `routes.yaml`, update ConfigMap, rollout restart. With `defaultBehavior: deny`, the removed path now returns 403 to callers without ever reaching the app — defense in depth.

## Caller-side: what consumers of `orders-app` must change

This is the part app teams sometimes forget to communicate to their callers.

**Every caller of `orders-app` now needs two headers on every protected request:**

```http
GET /api/orders/123 HTTP/1.1
Host: orders-app
Authorization: Bearer <SSO-token>
X-Auth-Context: order-123:order:read
```

- `Authorization: Bearer <SSO-token>` — the caller's identity, forwarded from whatever SSO/IdP they use. The sidecar passes this to PCS for verification.
- `X-Auth-Context: <objectId>:<objectType>:<action>` — the caller's **claim** about what they're trying to do. The sidecar parses it and asks PCS "is this SSO user allowed to perform `<action>` on `<objectId>` (of type `<objectType>`)?"

The `X-Auth-Context` header is a **claim**, not a **proof**. PCS validates the caller's identity against the SSO token and then checks whether that identity has the claimed permission. Callers can lie about the action they intend, but PCS won't grant a permission they don't actually hold.

### Failure modes callers should expect

| Caller mistake | What the caller sees | What to do |
|---|---|---|
| Missing `Authorization` header | `403`, sidecar log reason `auth_missing` | Add it |
| Missing `X-Auth-Context` header | `403`, sidecar log reason `context_header_missing` | Add it |
| Malformed `X-Auth-Context` (wrong separators, too long, etc.) | `403`, sidecar log reason `bad_format` | Fix the format: `objectId:objectType:action` |
| `X-Auth-Context` cites an object the caller has no permission on | `403`, PCS records the deny | Either the caller does need this permission (talk to your security team) or the action is wrong |
| Sidecar can't reach PCS | `403`, sidecar log shows `pcs_error` | Platform incident; not the caller's problem to fix |

## Mental model

After all this, the app team should leave with these five points:

1. **`routes.yaml` is the single source of truth** for "what is protected at what URL." Lives in their repo, reviewed in PRs, versioned alongside their app code.
2. **The CLI (`validate-routes translate`) is stateless** — it consumes `routes.yaml` and emits an `EnvoyFilter`. Run it whenever you want to regenerate the CRD. No databases, no daemons, no caches.
3. **The sidecar is a long-running validator.** It reads `routes.yaml` at startup and answers `istio-proxy`'s ext_proc questions. The team does not operate Envoy — Istio does. They just add one container to their pod template.
4. **PCS is the brain.** Permission logic does not live in the app. The app declares "this endpoint is protected with permission X" via `routes.yaml`; PCS decides whether any given caller has that permission on the cited object.
5. **The whole system is fail-closed.** PCS down, sidecar crashed, header malformed, network glitch — every failure path returns `403`. The system never silently allows.

## Where to go next

- [`README.md`](./README.md) — the technical reference: CLI flag tables, request/response wire contract, rejection table.
- [`../../README.md`](../../README.md) — module overview: CLI subcommands, run instructions, trust model.
- [`../../../prd/permission-validation/`](../../../prd/permission-validation/) — the design specs (PRD, request contract, header format, route schema, topology decision).

When you hit something this doc doesn't cover, the technical reference will.
