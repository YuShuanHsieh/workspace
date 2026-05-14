# Demo Walkthrough — Ext-Authz Kind Cluster

> This is the script for a live demo of the ext-authz kind cluster. Read it
> top-to-bottom before presenting; everything you need (commands, expected
> output, FAQ) is in here. No need to switch to the spec mid-presentation.

---

## 1. The 60-second pitch

The headline claim of this demo is simple: **when a request is denied, the
application code never runs.** The deny decision is made and enforced inside the
Envoy sidecar — before the request touches the app container. You can prove this
live by grepping the app logs and seeing zero lines for a denied user, even while
that user has been sending requests non-stop in a loop.

This is the Envoy `ext_authz` pattern. Every protected HTTP workload in the mesh
has a sidecar (Envoy / istio-proxy) sitting in front of its app container. When
the `ext_authz` filter is wired in, Envoy intercepts each inbound request,
pauses it, and calls a Permission Checking Service (PCS) to ask "should I let
this through?" PCS returns 200 for allowed users, 403 for denied ones. If PCS
says 403, Envoy replies 403 directly to the caller and discards the request. The
app never wakes up.

What you'll show in this demo: a live kind cluster with three protected
workloads (`documents-api`, `documents-search`, `wiki-api`) spread across two
namespaces, a dashboard-client firing a 6-call loop alternating an allowed user
and a denied user, PCS logging every decision in real time, and the proof that
the denied user's requests never reach any app container. You'll also show
fail-closed behaviour by scaling PCS to zero, and you'll show the same
allow/deny pattern from outside the cluster via curl.

---

## 2. The four key actors

### dashboard-client (the caller)

- **Namespace:** `documents`
- **What it does:** Runs a `for` loop forever, cycling through six HTTP calls
  with a 2-second sleep between each. The targets are `documents-api`,
  `documents-search`, and `wiki-api`. For each target it makes one call as
  `alice@workspace.test` (allowed) and one as `mallory@workspace.test` (denied).
  Full cycle: 12 seconds, 6 calls, 3 targets × 2 users.
- **Why it's interesting:** It generates a steady, predictable stream of both
  allowed and denied traffic simultaneously, across all three workloads and both
  namespaces. Watching its logs is the fastest way to confirm the system is
  working end-to-end. dashboard-client does NOT carry the opt-in label — it is a
  caller, not a receiver, so its sidecar is not patched with `ext_authz`.

### documents-api / documents-search / wiki-api (the protected workloads)

- **Namespaces:** `documents-api` and `documents-search` are in `documents`;
  `wiki-api` is in `wiki`.
- **What they do:** All three run the same `echo-server` binary — a tiny Go HTTP
  server with a single `GET /hello` endpoint that returns `"hello from
  $POD_NAME"`. They log every request they receive using structured JSON.
- **Why they're interesting:** They demonstrate three distinct onboarding cases.
  `documents-api` is the main product workload. `documents-search` is a sibling
  in the same namespace — adding a second opted-in workload here required only
  adding one label to its Deployment, no new EnvoyFilter. `wiki-api` is in a
  completely different namespace owned by a different team — it uses a copied
  EnvoyFilter that calls back into the documents team's PCS across namespace
  boundaries. All three carry the opt-in label `workspace.io/ext-authz: enabled`.

### pcs (the Permission Checking Service)

- **Namespace:** `documents` (owned by the documents team — not a separate
  platform namespace)
- **What it does:** Listens on port 8080. Handles `POST /check` (and any path
  with `/check` prefix, since Envoy appends the original path). Reads the
  `x-workspace-user-id` header. If the user is in its hard-coded allow-list
  (`alice@workspace.test`, `bob@workspace.test`), returns HTTP 200 with no body.
  Otherwise returns 403 with no body. Logs every decision as structured JSON.
- **Why it's interesting:** PCS is the single decision point for all three
  workloads across both namespaces. Both `documents-ext-authz` and
  `wiki-ext-authz` call `pcs.documents.svc.cluster.local:8080` — the cluster DNS
  FQDN resolves from any namespace. The documents team owns and operates PCS as
  part of their product.

### Envoy sidecar / istio-proxy (the gate)

- **Lives in:** every Pod in the cluster (both app namespaces have
  `istio-injection: enabled`)
- **What it does:** The Envoy sidecar is auto-injected alongside every app
  container. For Pods that carry the `workspace.io/ext-authz: enabled` label, the
  relevant EnvoyFilter patches the sidecar's `SIDECAR_INBOUND` filter chain to
  insert the `ext_authz` HTTP filter immediately before the router filter. This
  means every inbound HTTP request to those Pods is intercepted before routing
  and a PCS check is performed.
- **Why it's the most important actor for the headline claim:** The app container
  never participates in the deny path. The sidecar receives the request, calls
  PCS, and if PCS says 403, the sidecar returns 403 to the caller and drops the
  request. The app container's listen socket is never contacted. This is
  enforcement at the infrastructure layer — no app code changes required, and no
  app bug can bypass it.

---

## 3. Architecture at a glance

### Diagram 1 — Top-down cluster topology

<https://www.figma.com/board/ETLvYum9OPBgUtdFi0CC6r>

This FigJam shows the three namespaces stacked vertically: `istio-system` at the
top (control plane only — no EnvoyFilters there in Stage 1), `documents` in the
middle (the main product namespace with all its workloads, PCS, the
ingressgateway, and the `documents-ext-authz` EnvoyFilter), and `wiki` on the
right (the cross-namespace onboarding case with its own copied `wiki-ext-authz`
and ingressgateway). Arrows show which EnvoyFilter patches which Pods and which
Pods call PCS via `POST /check`.

### Diagram 2 — Left-to-right request flow

<https://www.figma.com/board/wizcwM5QT7kknm5ZDLTXr6>

This FigJam shows a single request's lifecycle: caller sends `GET /hello` with
the user-id header → Envoy sidecar intercepts on `SIDECAR_INBOUND` → sidecar
POSTs to PCS → PCS responds 200 or 403 → for 200 Envoy forwards to the app
container and returns the response; for 403 Envoy returns 403 directly and the
app container box is never reached. The label on the return arrow makes it
explicit: "deny — app never sees the request."

### Plain-English topology summary

Inside one kind cluster there are three namespaces. `istio-system` is the Istio
control plane — istiod and the CRDs live here; no EnvoyFilters are written here
in Stage 1. `documents` is the main product team's namespace: it holds
`documents-api`, `documents-search`, the `dashboard-client`, and crucially PCS —
the Permission Checking Service is part of the documents product, not a separate
platform service. The `documents-ext-authz` EnvoyFilter lives here too; its
`workloadSelector` matches any Pod in this namespace that carries the opt-in
label, which covers both `documents-api` and `documents-search` with a single
filter resource. `wiki` is a separate team's namespace: they copied the
EnvoyFilter shape into their own namespace as `wiki-ext-authz`, pointed it at the
same `pcs.documents.svc.cluster.local:8080`, and added the opt-in label to their
`wiki-api` Deployment. That cross-namespace DNS call is the Stage 1 onboarding
story for new teams: copy the filter YAML into your namespace, add the label, and
you're protected — no platform team write access required.

---

## 4. What happens during an ALLOW request (step by step)

Scenario: `dashboard-client` calls `documents-api` with header
`x-workspace-user-id: alice@workspace.test`.

**Step 1 — dashboard-client builds the HTTP request**

`dashboard-client`'s `main.go` constructs a `GET
http://documents-api.documents.svc.cluster.local:8080/hello` request, sets the
`x-workspace-user-id: alice@workspace.test` header, and calls `client.Do(req)`.
The log line emitted just before the call shows up in the dashboard-client pod
log — but the interesting log line comes after the response:

```
{"level":"INFO","msg":"call result","target":"documents-api","user":"alice@workspace.test","status":200,"body":"hello from documents-api-<hash>"}
```

**Step 2 — Outbound traffic intercepted by dashboard-client's own sidecar**

Traffic leaving the `dashboard-client` app container is intercepted by the
`istio-proxy` sidecar running in the same Pod on `SIDECAR_OUTBOUND`. The sidecar
looks up the destination service via Istio's service registry and initiates an
mTLS handshake to the destination pod's sidecar. No log from this step under
normal conditions — it is transparent.

**Step 3 — mTLS handshake to documents-api's sidecar**

The dashboard-client's Envoy establishes an mTLS connection to the `documents-api`
Pod's Envoy sidecar. Both sidecars are in PERMISSIVE mode (the Istio default), so
mTLS works automatically for any pod-to-pod call inside the mesh. No application
code is involved in the handshake.

**Step 4 — documents-api sidecar receives the request on SIDECAR_INBOUND**

The `documents-api` Pod's istio-proxy receives the decrypted request. The HTTP
filter chain fires in order. The `ext_authz` filter — inserted by the
`documents-ext-authz` EnvoyFilter resource into the `SIDECAR_INBOUND` chain via
`INSERT_BEFORE` — is the first filter to run.

**Step 5 — ext_authz filter PAUSES the request**

The Envoy `ext_authz` HTTP filter holds the request in memory. It does not
forward it to the app container yet. The request is in flight but suspended.

**Step 6 — Envoy calls PCS**

The `ext_authz` filter issues `POST http://pcs.documents.svc.cluster.local:8080/check/hello`
(path_prefix `/check` + original path `/hello`). It forwards two headers:
`x-workspace-user-id: alice@workspace.test` and any `authorization` header if
present. This is an HTTP call — not gRPC. Timeout is 1 second.

**Step 7 — PCS evaluates the request**

PCS `checkHandler` reads `x-workspace-user-id`. The value `alice@workspace.test`
is in the allow-list. Decision: `allow`. PCS emits:

```
{"level":"INFO","msg":"decision","user":"alice@workspace.test","decision":"allow","ts":"2026-05-14T10:23:01.123456789Z"}
```

PCS returns HTTP 200 with no body.

**Step 8 — Envoy receives 200 from PCS, resumes the request**

The `ext_authz` filter sees the 200 and releases the hold. The request continues
down the filter chain to the router filter.

**Step 9 — Envoy forwards the original request to the app container**

The router filter forwards `GET /hello` to `localhost:8080` — the
`documents-api` app container's listen port inside the same Pod. The
`x-workspace-user-id` header is present (Envoy preserves it).

**Step 10 — echo-server handles the request**

The `documents-api` app container's `GET /hello` handler runs. It logs:

```
{"level":"INFO","msg":"hello request","pod":"documents-api-<hash>","user":"alice@workspace.test"}
```

It returns `HTTP 200 "hello from documents-api-<hash>"`.

**Step 11 — Response travels back**

The 200 response flows back through documents-api's sidecar → mTLS back to
dashboard-client's sidecar → delivered to dashboard-client's app container.

**Step 12 — dashboard-client logs the result**

```
{"level":"INFO","msg":"call result","target":"documents-api","user":"alice@workspace.test","status":200,"body":"hello from documents-api-<hash>"}
```

**Summary of logs you'll see for an ALLOW:**
- PCS log: `"decision":"allow"` for alice
- documents-api app log: `"msg":"hello request"` with alice's user ID
- dashboard-client log: `"status":200`

---

## 5. What happens during a DENY request (step by step)

Scenario: `dashboard-client` calls `documents-api` with header
`x-workspace-user-id: mallory@workspace.test`.

Steps 1–6 are identical to the ALLOW path. The request reaches PCS via the same
ext_authz mechanism.

**Step 7 — PCS evaluates the request**

PCS `checkHandler` reads `x-workspace-user-id`. The value
`mallory@workspace.test` is NOT in the allow-list. Decision: `deny`. PCS emits:

```
{"level":"INFO","msg":"decision","user":"mallory@workspace.test","decision":"deny","ts":"2026-05-14T10:23:03.234567890Z"}
```

PCS returns HTTP 403 with no body.

**Step 8 — Envoy receives 403 from PCS, REJECTS the request**

The `ext_authz` filter sees the 403. It does NOT forward the request to the app
container. It immediately returns `HTTP 403` to the caller (dashboard-client's
sidecar). The request is discarded.

**THE CRITICAL DIFFERENCE — say this explicitly during the demo:**

> "mallory's request was denied here, at the sidecar, in step 8. The
> documents-api app container never received this request. mallory does not
> appear anywhere in the documents-api app logs. This is the headline."

**Step 9 — App container is never contacted**

The `documents-api` app container's `GET /hello` handler does NOT run for
mallory. There is no log line in the `documents-api` container for mallory.
Ever. Even though mallory has been sending requests in a non-stop loop.

**Step 10 — dashboard-client logs the 403**

```
{"level":"INFO","msg":"call result","target":"documents-api","user":"mallory@workspace.test","status":403,"body":""}
```

Body is empty because the 403 response from Envoy carries no body.

**Summary of logs you'll see for a DENY:**
- PCS log: `"decision":"deny"` for mallory
- documents-api app log: **nothing** — zero lines mentioning mallory
- dashboard-client log: `"status":403`

**This is the proof you'll demonstrate live:**

```bash
kubectl -n documents logs deploy/documents-api -c documents-api | grep -c mallory
# Expected: 0
```

Zero. That number is the demo. Denial is enforced at the sidecar layer, not in
app code. The app is never invoked for denied requests.

---

## 6. Request enrichment (mutation) — what PCS returns flows into App B

### What mutation is

Mutation is the optional second purpose of `ext_authz`. When PCS allows a
request, it does not have to return a bare `200` with no body. It can also set
response headers — and Envoy can be told to **append those headers to the
original request** before forwarding it to the upstream app. The application
then sees those headers as if they were sent by the caller, but they weren't:
they were injected by Envoy after the PCS decision. Because they came from PCS
(infrastructure), the app can **trust** them as authoritative identity
information rather than reading the caller-supplied `x-workspace-user-id`
directly (which a caller could forge).

This is how identity propagates in real authz-mesh deployments: the application
trusts the authz service's decision and the identity it returns, rather than
reading the user's identity from the client request directly.

### The Envoy knob: `authorization_response.allowed_upstream_headers`

In each EnvoyFilter's `http_service` block, alongside `authorization_request`,
there is now an `authorization_response` section:

```yaml
authorization_response:
  allowed_upstream_headers:
    patterns:
    - exact: x-user-id
    - exact: x-user-role
    - exact: x-allowed-scopes
```

This tells Envoy: "if PCS sets any of these headers in its `200` response,
copy them onto the original request before forwarding it upstream." Headers
that PCS does not set are simply absent on the forwarded request.

### The three injected headers

| Header | Example value | Meaning |
|--------|---------------|---------|
| `X-User-Id` | `alice-uid-001` | Stable internal identifier for the user |
| `X-User-Role` | `editor` | Role granted by the policy service |
| `X-Allowed-Scopes` | `documents:read,documents:write` | Scopes the user is allowed to exercise |

### Request flow for an alice request (with mutation)

1. `dashboard-client` sends `GET /hello` with `x-workspace-user-id: alice@workspace.test`
2. `ext_authz` POSTs to PCS with that header
3. PCS returns `200` + `X-User-Id: alice-uid-001`, `X-User-Role: editor`, `X-Allowed-Scopes: documents:read,documents:write`
4. Envoy reads those response headers (because `allowed_upstream_headers` lists them) and **appends them to the original request**
5. App container receives `GET /hello` with the original header AND the three injected headers
6. App logs them as structured fields (`injected_user_id`, `injected_role`, `injected_scopes`) and includes uid + role in the response body

### Visible proof

The app container log now shows the injected fields for every alice request:

```json
{"msg":"hello request","pod":"documents-api-...","user":"alice@workspace.test","injected_user_id":"alice-uid-001","injected_role":"editor","injected_scopes":"documents:read,documents:write"}
```

And the response body itself carries the identity:

```
hello from documents-api-846d5bd8bd-ptfcf (uid=alice-uid-001 role=editor)
```

Mallory's requests never reach the app container, so Mallory's identity is
never injected — because there is no PCS `200` response for Mallory to trigger
the mutation path. The app sees `(uid=..., role=...)` only for users that PCS
has affirmatively allowed.

---

## 7. The demo flow (live script)

### Before the demo

- Run `./kind/setup.sh` from the repo root (`/Users/joe/ashwini-repos/workspace/`).
  Takes 2-3 minutes on a warm Docker cache. The cluster is named `ext-authz-demo`.
- Confirm your kubectl context:
  ```bash
  kubectl config current-context
  # Expected: kind-ext-authz-demo
  ```
- For the cleanest demo URLs, add one line to `/etc/hosts` once (requires sudo):
  ```bash
  echo '127.0.0.1  documents.local wiki.local' | sudo tee -a /etc/hosts
  ```
  If you can't or prefer not to, pass the host explicitly with `-H "Host: documents.local"`
  (Istio's VirtualService matches on the `Host` header, not real DNS — see the
  troubleshooting section for the full no-edit form).
- Open 4 terminal windows and pre-position them. Suggested layout:
  - Terminal 1 (top-left): kubectl commands you'll type live
  - Terminal 2 (top-right): `dashboard-client` log (follow mode, pre-running)
  - Terminal 3 (bottom-left): `pcs` log (follow mode, pre-running)
  - Terminal 4 (bottom-right): spare / curl commands
- Pre-run the log follows in terminals 2 and 3 before you start talking — it
  looks better if the logs are already scrolling when you point at them.

---

### Step 1 — Show the cluster topology

**Say:** "First, let me show you what's running inside the kind cluster."

```bash
kubectl get pod -A
```

**Point at:**
- The `documents` and `wiki` namespaces with your workload pods
- The `istio-system` namespace with istiod
- Each pod showing `2/2` containers READY — that second container is the
  `istio-proxy` sidecar injected alongside every app container

**Expected output (abridged):**
```
NAMESPACE      NAME                                         READY   STATUS    ...
documents      dashboard-client-<hash>                      2/2     Running
documents      documents-api-<hash>                         2/2     Running
documents      documents-ingressgateway-<hash>              1/1     Running
documents      documents-search-<hash>                      2/2     Running
documents      pcs-<hash>                                   2/2     Running
istio-system   istiod-<hash>                                1/1     Running
wiki           wiki-api-<hash>                              2/2     Running
wiki           wiki-ingressgateway-<hash>                   1/1     Running
```

**What to explain:** "Every pod in `documents` and `wiki` shows `2/2` — two
containers: the app container and the `istio-proxy` Envoy sidecar. The
ingressgateway pods only have `1/1` because they are pure Envoy, no app
container. The gateway pods don't carry the opt-in label, so they are not
patched with `ext_authz`."

---

### Step 2 — Show the EnvoyFilters

**Say:** "Two EnvoyFilters wire the ext_authz check — one per namespace. Notice
nothing in istio-system."

```bash
kubectl get envoyfilter -A
```

**Point at:**
- `documents/documents-ext-authz` — covers `documents-api` and `documents-search`
- `wiki/wiki-ext-authz` — covers `wiki-api`
- No row for `istio-system` — this is the Stage 1 proof that the product team
  does not need platform (istio-system) write access

**Expected output:**
```
NAMESPACE   NAME                    AGE
documents   documents-ext-authz     5m
wiki        wiki-ext-authz          5m
```

**What to explain:** "In Stage 1, each team owns their own EnvoyFilter in their
own namespace. The wiki team copied the documents team's EnvoyFilter shape into
their namespace — two fields changed: `metadata.namespace` and `metadata.name`.
Everything else, including the PCS URL, is identical. That PCS URL is
`pcs.documents.svc.cluster.local:8080` — cluster DNS resolves it from any
namespace."

---

### Step 3 — Watch the dashboard-client cycle

**Say:** "dashboard-client is in a 6-call loop, alternating alice (allowed) and
mallory (denied) across three target workloads."

```bash
kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f
```

**Point at:**
- Lines with `"status":200` and `"user":"alice@workspace.test"` — the allowed calls
- Lines with `"status":403` and `"user":"mallory@workspace.test"` — the denied calls
- The pattern: alice always 200, mallory always 403, cycling across documents-api,
  documents-search, wiki-api

**Expected output (rolling, one line per 2 seconds):**
```json
{"level":"INFO","msg":"call result","target":"documents-api","user":"alice@workspace.test","status":200,"body":"hello from documents-api-... (uid=alice-uid-001 role=editor)"}
{"level":"INFO","msg":"call result","target":"documents-api","user":"mallory@workspace.test","status":403,"body":""}
{"level":"INFO","msg":"call result","target":"documents-search","user":"alice@workspace.test","status":200,"body":"hello from documents-search-... (uid=alice-uid-001 role=editor)"}
{"level":"INFO","msg":"call result","target":"documents-search","user":"mallory@workspace.test","status":403,"body":""}
{"level":"INFO","msg":"call result","target":"wiki-api","user":"alice@workspace.test","status":200,"body":"hello from wiki-api-... (uid=alice-uid-001 role=editor)"}
{"level":"INFO","msg":"call result","target":"wiki-api","user":"mallory@workspace.test","status":403,"body":""}
```

**Point at:** alice's body now carries `(uid=alice-uid-001 role=editor)` — injected
by Envoy from PCS's response. mallory's body is empty because the request was
denied before reaching the app.

---

### Step 4 — Watch PCS decisions

**Say:** "And here's PCS logging every allow/deny decision. One pair per target,
three pairs per cycle."

(In a second terminal, already running)

```bash
kubectl -n documents logs deploy/pcs -c pcs -f
```

**Point at:**
- The `"decision":"allow"` lines for alice
- The `"decision":"deny"` lines for mallory
- The fact that PCS sees decisions for ALL THREE workloads — documents-api,
  documents-search, and wiki-api — even though wiki-api is in a different
  namespace. That cross-namespace DNS call is working.

**Expected output (rolling):**
```json
{"level":"INFO","msg":"decision","user":"alice@workspace.test","decision":"allow","ts":"..."}
{"level":"INFO","msg":"decision","user":"mallory@workspace.test","decision":"deny","ts":"..."}
```

---

### Step 5 — Reveal the headline claim

**Say:** "Now here's the headline. mallory was denied — but did mallory's request
ever reach the app container? Let me grep for it."

```bash
kubectl -n documents logs deploy/documents-api -c documents-api | grep -c mallory
```

**Expected output:**
```
0
```

**Point at:** The zero. Say it explicitly: "Zero. mallory has been sending
requests every 12 seconds since the cluster came up, and the app container has
never seen a single one of those requests. The deny happened at the Envoy
sidecar, before the request was forwarded. App code was never invoked."

Optionally repeat for the other two workloads to drive it home:

```bash
kubectl -n documents logs deploy/documents-search -c documents-search | grep -c mallory
kubectl -n wiki logs deploy/wiki-api -c wiki-api | grep -c mallory
```

All three should return `0`.

And confirm alice DID reach the app — AND that the injected identity is visible:

```bash
kubectl -n documents logs deploy/documents-api -c documents-api --tail=3
```

**Expected:** structured JSON lines for alice showing `injected_user_id=alice-uid-001`,
`injected_role=editor`, `injected_scopes=documents:read,documents:write`. This is the
mutation in action — PCS returned those headers on allow, Envoy injected them
into the request, and the app received them as first-class request headers.

**Step 5b — Show mutation in the response body**

**Say:** "The response body itself now carries the identity too — visual proof."

```bash
kubectl -n documents logs deploy/documents-api -c documents-api --tail=3
# See injected_user_id, injected_role in the log

# Primary (with /etc/hosts):
curl -s -H "x-workspace-user-id: alice@workspace.test" http://documents.local:8080/hello
# Alternative (no /etc/hosts) — pass the VirtualService host as a header:
curl -s -H "Host: documents.local" -H "x-workspace-user-id: alice@workspace.test" http://127.0.0.1:8080/hello
```

**Expected:** `hello from documents-api-... (uid=alice-uid-001 role=editor)`

**Point at:** "The app didn't get that uid and role from the client request — it
got it from Envoy, which got it from PCS. The client only sent
`x-workspace-user-id: alice@workspace.test`. The identity enrichment happened
in the infrastructure layer."

---

### Step 6 — Show mutation from the outside (external curl)

**Say:** "The enriched body is visible from outside the cluster too."

```bash
# Primary (with /etc/hosts):
curl -s -H "x-workspace-user-id: alice@workspace.test" http://documents.local:8080/hello
```

**Expected:** `hello from documents-api-... (uid=alice-uid-001 role=editor)`

**Say:** "Try mallory — no mutation, just a bare 403."

```bash
# Primary (with /etc/hosts):
curl -s -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello
```

**Expected:** `403`, empty body — mallory never reached the app.

---

### Step 7 — External curl demonstrating both gateways

**Say:** "The same allow/deny behaviour is reachable from outside the cluster via
the per-namespace ingressgateways."

```bash
# Primary: add to /etc/hosts once (if not already done):
#   echo '127.0.0.1  documents.local wiki.local' | sudo tee -a /etc/hosts

# documents namespace — port 8080
curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local:8080/hello
curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello

# wiki namespace — port 8081
curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello
curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello
```

Alternative (no `/etc/hosts` edit needed) — Istio's VirtualService matches on the
`Host` header, so passing it explicitly is enough:

```bash
curl -H "Host: documents.local" -H "x-workspace-user-id: alice@workspace.test"   http://127.0.0.1:8080/hello
curl -H "Host: documents.local" -H "x-workspace-user-id: mallory@workspace.test" http://127.0.0.1:8080/hello
curl -H "Host: wiki.local"      -H "x-workspace-user-id: alice@workspace.test"   http://127.0.0.1:8081/hello
curl -H "Host: wiki.local"      -H "x-workspace-user-id: mallory@workspace.test" http://127.0.0.1:8081/hello
```

(`curl --resolve documents.local:8080:127.0.0.1 …` also works if you prefer.)

**Point at:**
- alice returning `200 hello from documents-api-...` and `200 hello from wiki-api-...`
- mallory returning `403` (empty body) for both
- The port difference: documents uses port 8080, wiki uses port 8081 — each
  namespace runs its own ingressgateway with its own NodePort

**Note:** `documents-search` is not exposed externally — it is an internal-only
sibling service. That is intentional. The `ext_authz` check still fires on its
sidecar for cluster-internal calls, as the dashboard-client loop demonstrates.

---

### Step 8 — Fail-closed proof (optional, 60 extra seconds)

**Say:** "What if PCS is down? The filter is configured with
`failure_mode_allow: false` — fail-closed. Let's prove it."

```bash
kubectl -n documents scale deploy/pcs --replicas=0
```

Wait about 8 seconds for the sidecar config to propagate, then:

```bash
# Primary (with /etc/hosts):
curl -H "x-workspace-user-id: alice@workspace.test" http://documents.local:8080/hello
# Alternative (no /etc/hosts):
curl -H "Host: documents.local" -H "x-workspace-user-id: alice@workspace.test" http://127.0.0.1:8080/hello
```

**Expected:** `503` or `403` — even alice is denied because PCS is unreachable
and Envoy is configured to fail closed.

Restore PCS:

```bash
kubectl -n documents scale deploy/pcs --replicas=1
kubectl -n documents wait --for=condition=Available deploy/pcs --timeout=60s
```

After PCS comes back up (allow ~10 seconds for sidecar config to stabilise),
alice's requests return to 200.

**Point at:** "The application does not need to implement a fallback for a
missing auth service. The infrastructure layer enforces fail-closed. No app code
change required."

---

## 8. What to expect (and what NOT to expect)

### What you SHOULD see

- Alternating `"status":200` / `"status":403` in the dashboard-client log,
  cycling every 2 seconds through all three targets
- PCS logging 3 allow + 3 deny decisions per 12-second dashboard-client cycle —
  one pair per workload
- `documents-api` app log shows `"msg":"hello request"` lines for alice only
  (never a line mentioning mallory), each with `injected_user_id`, `injected_role`,
  and `injected_scopes` fields populated by PCS via Envoy's request mutation
- The same pattern for `documents-search` and `wiki-api` app logs
- Both gateways responding 200 or 403 based on the `x-workspace-user-id` header
- `kubectl get envoyfilter -A` showing exactly two rows: `documents-ext-authz`
  and `wiki-ext-authz` — nothing in `istio-system`
- Each pod showing `2/2` READY (app container + istio-proxy) except the
  ingressgateway pods which show `1/1`
- Scaling PCS to 0 causing all calls — including from the wiki namespace — to
  fail with a non-200 response, even for alice

### What you should NOT see

- A `404` from any workload. If you see 404s, the EnvoyFilter may be
  misconfigured or PCS is not ready yet; give it 30 seconds or check
  `kubectl -n documents get pod -l app=pcs`.
- App containers logging any mallory traffic. If `grep -c mallory` returns a
  non-zero number for any app container, the EnvoyFilter is not patching that
  workload's sidecar — check whether the Pod carries the `workspace.io/ext-authz:
  enabled` label.
- mTLS warnings or certificate errors. PERMISSIVE mode handles this automatically
  for all injected pods; you should see no TLS errors.
- Image pull delays during `setup.sh` after the first run. All three app images
  are built locally and loaded into kind — Docker never reaches the internet for
  them. Istio images are pulled once and cached; subsequent `setup.sh` runs do
  not re-pull them.
- A non-empty response body in the 403 case. Envoy returns 403 with an empty
  body when `ext_authz` denies. PCS returns no body either.

**Common surprise:** After a pod restart or a `kubectl scale`, there is a
propagation delay before istiod pushes the updated sidecar config to all Envoy
instances. If you see a few unexpected 200s or 503s in the first 10-15 seconds
after a change, wait and re-check. The steady-state is what matters.

---

## 9. FAQ — questions your audience will ask

**Q: Why are there sidecars on every pod? Doesn't that add latency and memory?**

Yes, Envoy sidecars add roughly 1-3 ms per hop under normal load and consume
about 50-150 MB of memory per pod. That is the trade-off for getting transparent
mTLS, traffic management, and `ext_authz` enforcement without touching app code.
In this demo the pods are sized to 50m CPU / 128Mi memory for the sidecar — very
conservative. Production sizing depends on your traffic volume.

**Q: Why is PCS in the documents namespace instead of a separate platform namespace?**

By design. PCS is part of the documents product's offering — the documents team
owns and operates it as part of their product. In Stage 1 there is no separate
platform-owned namespace. Other teams (like wiki) consume PCS over cluster DNS,
which resolves cross-namespace. A future Stage 2 could introduce a platform
namespace if needed, but the PCS contract and URL would stay the same.

**Q: What stops a malicious workload from calling documents-api directly, bypassing the sidecar?**

In a STRICT mTLS mesh, pod-to-pod traffic without a valid Istio certificate is
rejected. In PERMISSIVE mode (this demo's default), a pod without Istio's
certificate can still connect but the ext_authz check still fires on the
receiver's sidecar. The receiver's `SIDECAR_INBOUND` chain is what enforces the
policy — the attacker would have to bypass the receiving pod's own sidecar, which
sits between the network interface and the app container's listen socket.

**Q: How does the wiki team's EnvoyFilter call PCS in the documents namespace?**

Via cluster DNS. The filter config contains the FQDN
`pcs.documents.svc.cluster.local:8080`. Kubernetes DNS resolves this from any
namespace — the namespace is encoded in the FQDN. Istio's service registry also
knows about it so Envoy can route the call via its outbound cluster config. No
special cross-namespace networking setup required.

**Q: What if PCS is slow — does it block the request?**

Yes. The `ext_authz` filter holds the inbound request while it waits for the PCS
response. The filter is configured with a 1-second timeout (`timeout: 1s` in the
EnvoyFilter). If PCS does not respond within 1 second, Envoy fails the request
as if PCS had returned a non-200 status. Because `failure_mode_allow: false`, a
timeout produces a denial, not a pass-through. PCS response times therefore
directly add to end-user latency on the allowed path.

**Q: Why two EnvoyFilters instead of one mesh-wide filter in istio-system?**

This is Stage 1. In Stage 1, each team writes into their own namespace only —
no one has to request istio-system write access from the platform team. The cost
is duplication: two near-identical YAML files. Stage 2 (documented in the spec)
collapses both into a single resource in istio-system once the platform team is
on board. The opt-in label is the same in both stages, so app teams do not need
to change their Deployments.

**Q: How would this look at company scale with many app teams?**

Each team copies the EnvoyFilter template into their namespace (or in Stage 2,
does nothing extra — the platform's single filter covers them). They add one
label to their Deployment's pod template. That is the onboarding cost for a new
team: one YAML file copy + one label. For a Stage 2 mesh-wide filter the cost
drops to just the label.

**Q: Does the app container know about the deny? Does it ever see it?**

No, on both counts. The `ext_authz` filter operates entirely within Envoy. When
Envoy denies a request, the app container's listen socket is never called. The
app container has no knowledge that a request was attempted and denied. The deny
is invisible to the app.

**Q: What's the failure mode if a sidecar crashes?**

If the Envoy sidecar process in a pod crashes, Kubernetes restarts the container
(it is a separate container within the same pod). During the brief restart
window, traffic to that pod is unroutable — the pod's service endpoint becomes
not-ready. Requests are routed to other replicas if any exist. In this single-
replica demo, calls to that workload would fail with a connection error until the
sidecar restarts, which is typically a few seconds.

**Q: Can I add another app team to this pattern? What would they need to do?**

In Stage 1: (1) Create their namespace with `istio-injection: enabled`. (2) Copy
`documents-ext-authz.yaml`, change `metadata.namespace` and `metadata.name`, keep
everything else. (3) Apply it to their namespace. (4) Add
`workspace.io/ext-authz: enabled` to their Deployment's pod template labels. Done
— their workload is now gated through the documents team's PCS. In Stage 2 they
skip step 2 and 3 entirely.

**Q: Can PCS pass info back to App B?**

A: Yes — via the ext_authz `authorization_response.allowed_upstream_headers`
configuration. PCS sets response headers (e.g. `X-User-Id`,
`X-User-Role`), Envoy appends them to the original request, and App B sees
them. This is how identity propagates in real authz-mesh deployments:
the application trusts the authz service's decision and the identity it
returns, rather than reading the user's identity from the client request
directly.

**Q: Is this how Istio's AuthorizationPolicy works under the hood?**

Not exactly. `AuthorizationPolicy` with `action: CUSTOM` and a registered
`extensionProvider` is Istio's first-class API for ext_authz — it is higher-level
and less brittle to Envoy version changes. The `EnvoyFilter` approach used in
this demo patches raw Envoy config directly, which is more flexible but ties you
more closely to Envoy internals. This demo uses `EnvoyFilter` because that
matches the team's existing production pattern. The spec (Section 10.7) documents
a future migration path to `AuthorizationPolicy action: CUSTOM`.

---

## 10. Troubleshooting

**Problem: Getting `404` instead of `200` or `403`**

The workload responded but Envoy could not route to it. Most likely causes:
(1) The `VirtualService` or `Gateway` host name does not match — verify you are
using the correct hostname and port in your curl command (e.g. `http://documents.local:8080/hello`
with `/etc/hosts` containing `127.0.0.1 documents.local wiki.local`, or — without any
`/etc/hosts` edit — `curl -H "Host: documents.local" http://127.0.0.1:8080/hello`,
since Istio routes on the `Host` header). (2) PCS is not ready
and Envoy's upstream cluster for PCS is in a bad state — check
`kubectl -n documents get pod -l app=pcs`. (3) The EnvoyFilter was applied before
istiod pushed config to the sidecar — wait 15 seconds and retry.

**Problem: All requests return `403`, even for alice**

PCS is down or not reachable. Check:
```bash
kubectl -n documents get pod -l app=pcs
kubectl -n documents logs deploy/pcs -c pcs --tail=20
```
If PCS crashed, `kubectl -n documents rollout restart deploy/pcs`. The filter's
`failure_mode_allow: false` makes Envoy deny when PCS is unreachable, so even
allowed users get 403 when PCS is unavailable — that is the fail-closed behaviour
working correctly. Wait for PCS to be Available before retrying.

**Problem: `./kind/setup.sh` fails or the cluster is not responding**

Verify Docker Desktop is running and has at least 6 GB RAM allocated. Check:
```bash
kind get clusters
kubectl config current-context
```
If the cluster does not appear, re-run `./kind/setup.sh` from the repo root —
it is idempotent and will pick up where it left off. If the cluster exists but
`kubectl` fails, run:
```bash
kubectl config use-context kind-ext-authz-demo
```

**Problem: External curl times out or `connection refused`**

Check (1) You are using the correct port — documents gateway is on `8080`, wiki
gateway is on `8081`. Either add `127.0.0.1 documents.local wiki.local` to `/etc/hosts`,
or send the host as a header directly: `-H "Host: documents.local"` against
`http://127.0.0.1:8080/hello` (Istio's VirtualService matches on the `Host` header).
(2) The ingressgateway pods are running:
```bash
kubectl -n documents get pod -l istio=documents-ingressgateway
kubectl -n wiki      get pod -l istio=wiki-ingressgateway
```
(3) Docker Desktop's port forwarding is active — kind maps `localhost:8080` to
`NodePort 30080` and `localhost:8081` to `NodePort 30081`. Because both ports are
unprivileged (≥ 1024), no `sudo` is needed on macOS or Linux.

**Problem: Sidecars not injected (pods show `1/1` instead of `2/2`)**

The namespace is missing the injection label. Check:
```bash
kubectl get namespace documents -o jsonpath='{.metadata.labels}'
kubectl get namespace wiki      -o jsonpath='{.metadata.labels}'
```
Both should include `"istio-injection":"enabled"`. If they don't, the umbrella
chart was not applied correctly — re-run `./kind/setup.sh`. If the label is
present but pods are `1/1`, the pods may have been created before the label was
applied — restart the deployment:
```bash
kubectl -n documents rollout restart deploy/documents-api
```

**Problem: mallory appears in app logs (grep returns non-zero)**

The EnvoyFilter is not patching that pod's sidecar. Verify the pod carries the
opt-in label:
```bash
kubectl -n documents get pod -l app=documents-api -o jsonpath='{.items[0].metadata.labels}'
```
Look for `"workspace.io/ext-authz":"enabled"`. If it is missing, the Deployment
template is wrong — check `kubectl -n documents get deploy documents-api -o yaml`.
Also verify the EnvoyFilter exists and its `workloadSelector` matches:
```bash
kubectl -n documents get envoyfilter documents-ext-authz -o jsonpath='{.spec.workloadSelector}'
```

---

## 11. Glossary (for the audience)

**Pod** — The smallest deployable unit in Kubernetes. A Pod holds one or more
containers that share a network namespace (same IP address, same localhost).

**Sidecar** — A helper container injected into a Pod alongside the main app
container. In Istio, the sidecar is always `istio-proxy` (Envoy). It handles all
inbound and outbound network traffic for the Pod.

**Envoy** — The open-source proxy that Istio uses as its data plane. It is the
process running inside `istio-proxy`. All traffic interception, mTLS, and
`ext_authz` enforcement happen inside Envoy.

**istio-proxy** — Istio's name for the sidecar container in each Pod. It runs
the Envoy binary, configured by istiod.

**EnvoyFilter** — An Istio CRD that patches raw Envoy configuration directly.
Used in this demo to insert the `ext_authz` HTTP filter into the inbound filter
chain of opted-in pods. Lives in a namespace; its `workloadSelector` restricts
which Pods in that namespace it applies to.

**ext_authz filter** — An Envoy HTTP filter that calls an external authorization
service (PCS) for every request before routing it. Returns allow (forward to app)
or deny (return error to caller). Configured inside the EnvoyFilter resource.

**SIDECAR_INBOUND** — The Envoy listener context that handles traffic arriving at
a Pod from other pods or from the ingressgateway. This is where the `ext_authz`
filter is inserted in this demo — on the receiving side, not the sending side.

**Gateway** — An Istio resource that configures the ingressgateway pod to accept
traffic for a given hostname and port. Think of it as an nginx `server {}` block.

**VirtualService** — An Istio resource that configures routing rules for traffic
entering via a Gateway or for cluster-internal calls. Routes requests to the
correct backend service.

**ClusterIP** — The default Kubernetes Service type. Creates a stable virtual IP
reachable from inside the cluster. `pcs.documents.svc.cluster.local` resolves to
a ClusterIP.

**NodePort** — A Kubernetes Service type that maps a port on the cluster node
(or, in kind, on the host machine) to a pod port. Used by the ingressgateways to
accept traffic from outside the cluster.

**Helm chart** — A package format for Kubernetes manifests. This demo uses a thin
umbrella chart at `kind/demo/` for the app-side resources. Istio components are
separate Helm releases.

**kind** — Kubernetes IN Docker. Runs a Kubernetes cluster inside Docker
containers on your laptop. Used here as a self-contained local demo environment.

**workloadSelector** — A field in Istio resources (EnvoyFilter, AuthorizationPolicy,
etc.) that restricts which Pods the resource applies to, by matching Pod labels.

**opt-in label** — The label `workspace.io/ext-authz: enabled` that Pods must
carry to be matched by the EnvoyFilter. Pods without this label are unaffected
by the ext_authz wiring.

**failure_mode_allow: false** — The EnvoyFilter setting that makes Envoy deny
requests when the authorization service (PCS) is unreachable or times out.
"Fail-closed" — the safe default for a security gate.

**mTLS (mutual TLS)** — Both sides of a connection authenticate each other with
certificates. Istio provides this automatically between any two injected pods via
SPIFFE/X.509 certificates issued by istiod. In PERMISSIVE mode (this demo) it is
automatic for injected pods and optional for non-injected callers.
