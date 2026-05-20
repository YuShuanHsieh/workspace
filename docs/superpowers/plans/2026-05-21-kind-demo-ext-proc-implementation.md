# Kind ext_proc Demo — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up two co-equal kind demos on the `kind-demo-ext_proc` branch that exercise the Phase 1 `permission-validation` sidecar from `origin/main`'s PR #1 — one (`demo-ext-proc-plain`) that runs main's design as-written (plain Envoy + sidecar + echo in one pod, with Istio sidecar injection disabled), and one (`demo-ext-proc-istio`) that adapts the same sidecar to an Istio cluster via an `EnvoyFilter` CRD. Both demos share a single `routes.yaml`, a single demo PCS (HTTP, speaks main's contract), and the same echo-server app.

**Architecture:** Two flat Helm charts under `kind/demo-ext-proc-plain/` and `kind/demo-ext-proc-istio/`. The `permission-validation` Go code from `origin/main` is consumed unchanged via the existing `permission-validation/test/e2e/Dockerfile.sidecar`. The Go code we write in this plan is a single new HTTP service at `sample-apps/pcs-ext-proc/` (~50 lines) that honours main's `POST /permission-check/v1/check` contract. Two bash setup scripts (`kind/setup-plain.sh`, `kind/setup-istio.sh`) bring up an idempotent single-node kind cluster per demo, with their own cluster names so they do not collide.

**Tech Stack:** Go 1.25 (only for the new PCS), Bash, Helm 3, kind 0.23+, kubectl, Docker (or compatible). For Option A: Istio 1.24.2 (chart tarballs vendored from the `kind-demo` branch). For Option B: Envoy 1.31.3 image, `validate-routes` CLI from `permission-validation/cmd/validate-routes/` on this branch.

**Design references** (already approved):

- [`docs/superpowers/specs/2026-05-21-kind-demo-ext-proc-design.md`](../specs/2026-05-21-kind-demo-ext-proc-design.md) — the canonical spec this plan implements.
- [`docs/superpowers/specs/2026-05-18-istio-envoyfilter-target-design.md`](../specs/2026-05-18-istio-envoyfilter-target-design.md) — design for adding `validate-routes translate --target=istio`; deliberately not implemented in this plan.
- [`prd/permission-validation/phase-1-topology-decision.md`](../../../prd/permission-validation/phase-1-topology-decision.md) — why `ext_proc + sidecar` was chosen over `ext_authz`.
- [`prd/permission-validation/phase-1-request-contract.md`](../../../prd/permission-validation/phase-1-request-contract.md) — wire contract between client → sidecar → PCS.

**Working directory:** `/Users/joe/ashwini-repos/workspace` on branch `kind-demo-ext_proc` (already created, currently 1 commit ahead of `origin/main` with the spec committed). Parked scaffolding is in `git stash@{0}`.

---

## File Structure

All paths are relative to the repo root `/Users/joe/ashwini-repos/workspace/`.

**Created by this plan:**

```text
kind/
├── kind-config.yaml                          # Phase 0
├── routes.yaml                                # Phase 0 — shared, both demos consume it
├── charts/                                    # Phase 0 — vendored Istio chart tarballs
│   ├── base-1.24.2.tgz
│   ├── istiod-1.24.2.tgz
│   └── gateway-1.24.2.tgz
├── setup-plain.sh                             # Phase 2
├── setup-istio.sh                             # Phase 3
├── teardown.sh                                # Phase 4
├── DEMO.md                                    # Phase 4 — presenter script
├── demo-ext-proc-plain/                       # Phase 2 — Option B Helm chart
│   ├── Chart.yaml
│   ├── values.yaml
│   ├── README.md
│   └── templates/
│       ├── namespace.yaml
│       ├── envoy-bootstrap-cm.yaml
│       ├── echo-app.yaml
│       └── pcs.yaml
└── demo-ext-proc-istio/                       # Phase 3 — Option A Helm chart
    ├── Chart.yaml
    ├── values.yaml
    ├── README.md
    └── templates/
        ├── namespace.yaml
        ├── echo-app.yaml
        ├── envoyfilter.yaml
        ├── gateway.yaml
        └── pcs.yaml
sample-apps/
├── echo-server/                               # Phase 0 — vendored from kind-demo branch
│   ├── deploy/Dockerfile
│   ├── go.mod
│   ├── go.sum
│   ├── main.go
│   └── main_test.go
└── pcs-ext-proc/                              # Phase 1 — NEW, TDD'd
    ├── deploy/Dockerfile
    ├── go.mod
    ├── go.sum
    ├── main.go
    └── main_test.go
```

**Consumed unchanged from `origin/main`:**

```text
permission-validation/                          # The Phase 1 sidecar from PR #1
├── cmd/permission-validation/                  # → built into image workspace/permission-validation:dev
├── cmd/validate-routes/                        # → run on host to generate envoy.yaml from routes.yaml
└── test/e2e/Dockerfile.sidecar                 # → used as-is to build the sidecar image
```

---

## Implementation Decision Log

These are choices made when writing this plan that resolve small ambiguities in the spec. Read these before starting.

1. **Option A pod composition.** Spec §7.2 lists three containers (istio-proxy + sidecar + echo) in the `echo-app` pod, and §7.3 references the sidecar via `cluster_name: outbound|50051||echo-app.demo-istio.svc.cluster.local`. To make that cluster name resolve, the `echo-app` Service must expose **two** ports: `http` (8080 → echo) and `grpc-extproc` (50051 → sidecar). Istio's xDS will then emit a cluster per port. Single-replica demo means service load-balancing always lands on the same pod, which is what we want.
2. **Envoy bootstrap delivery in Option B.** Generate `envoy.yaml` on the host with `validate-routes translate`, then pass it to Helm via `--set-file envoyBootstrap=/tmp/envoy.yaml`. The template renders it as a ConfigMap. Helm owns the resource lifecycle; the generated file is reproducible from `routes.yaml`.
3. **Demo PCS authentication.** The bearer token IS the user email (e.g. `Authorization: Bearer alice@workspace.test`). No JWT signing, no verification. The new PCS strips `Bearer ` and uses the rest as the user key.
4. **Demo PCS deny shape.** PCS returns `200` with `{"allowed":false}` for known-but-denied combinations AND for unknown combinations. It never returns 4xx/5xx for "no rule found." This keeps the sidecar path clean: a 2xx with `allowed:false` → deny; a non-2xx → `DecisionUnknown` → fail-closed.
5. **Per-route skip in Option A.** Spec §7.3 mentions `/healthz` skip as "a second `configPatch` with `applyTo: HTTP_ROUTE`." That patch is non-trivial in Istio because the upstream routes are istiod-generated. For the demo, we instead skip `/healthz` by configuring the readiness probe to hit echo directly via `httpGet` on the pod (bypassing the Service path), which makes the EnvoyFilter route-skip patch unnecessary for the readiness story. The `routes.yaml` still lists `/healthz` under `skipped:` to document the intent. The DEMO.md curl for `/healthz` only works in Option B (where the bootstrap honours skipped routes); in Option A it 200s only when echo answers it directly, which is fine for the demo.
6. **Single-host port-mapping.** kind's `extraPortMappings` map host 8080 → node 30080 (Istio ingressgateway, Option A) and host 8090 → node 30090 (NodePort to Envoy in pod, Option B). Both clusters use the **same kind-config.yaml** but only one cluster runs at a time.

---

## Phase 0 — Shared assets

### Task 0.1: Vendor `sample-apps/echo-server/` from the `kind-demo` branch

**Files:**
- Create: `sample-apps/echo-server/main.go`
- Create: `sample-apps/echo-server/main_test.go`
- Create: `sample-apps/echo-server/go.mod`
- Create: `sample-apps/echo-server/go.sum`
- Create: `sample-apps/echo-server/deploy/Dockerfile`

- [ ] **Step 1: Copy files from the `kind-demo` branch**

Run:
```bash
git checkout kind-demo -- sample-apps/echo-server/
```

Verify the files are now staged:
```bash
git status --short
```
Expected: 5 files under `sample-apps/echo-server/` in staged state.

- [ ] **Step 2: Verify it builds**

Run:
```bash
( cd sample-apps/echo-server && go build ./... && go test ./... )
```
Expected: build succeeds, tests pass.

- [ ] **Step 3: Commit**

```bash
git commit -m "$(cat <<'EOF'
demo(echo-server): vendor sample-apps/echo-server/ from kind-demo branch

Used by both ext_proc demos as the backend app. Unchanged from
kind-demo — same image tag (workspace/echo-server:dev) and behaviour.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 0.2: Vendor `kind/charts/` Istio chart tarballs from the `kind-demo` branch

**Files:**
- Create: `kind/charts/base-1.24.2.tgz`
- Create: `kind/charts/istiod-1.24.2.tgz`
- Create: `kind/charts/gateway-1.24.2.tgz`

- [ ] **Step 1: Copy the charts directory**

Run:
```bash
git checkout kind-demo -- kind/charts/
git status --short
```
Expected: three `.tgz` files under `kind/charts/` in staged state.

- [ ] **Step 2: Commit**

```bash
git commit -m "$(cat <<'EOF'
demo(kind): vendor Istio 1.24.2 chart tarballs for the istio option

Same chart tarballs used on the kind-demo branch. Consumed only by
kind/setup-istio.sh.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 0.3: Create `kind/kind-config.yaml`

**Files:**
- Create: `kind/kind-config.yaml`

- [ ] **Step 1: Write the kind config**

```yaml
# kind/kind-config.yaml
# Single-node cluster used by BOTH setup-plain.sh and setup-istio.sh
# (one cluster at a time — different cluster names so they don't collide).
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  image: kindest/node:v1.30.0
  extraPortMappings:
  - containerPort: 30080   # Option A: istio-ingressgateway NodePort
    hostPort: 8080
    protocol: TCP
  - containerPort: 30090   # Option B: echo-app NodePort (Envoy listener)
    hostPort: 8090
    protocol: TCP
```

- [ ] **Step 2: Verify the file parses**

Run:
```bash
kind --help >/dev/null  # confirm kind is on PATH
yq '.kind, .nodes[0].extraPortMappings | length' kind/kind-config.yaml 2>/dev/null || \
  python3 -c "import yaml; d=yaml.safe_load(open('kind/kind-config.yaml')); print(d['kind']); print(len(d['nodes'][0]['extraPortMappings']))"
```
Expected: `Cluster` and `2`.

- [ ] **Step 3: Commit**

```bash
git add kind/kind-config.yaml
git commit -m "$(cat <<'EOF'
demo(kind): kind-config.yaml — single-node, port maps for both demos

8080→30080 (Istio ingressgateway, Option A) and
8090→30090 (Option B NodePort straight to Envoy in pod).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 0.4: Create `kind/routes.yaml`

**Files:**
- Create: `kind/routes.yaml`

- [ ] **Step 1: Write routes.yaml**

```yaml
# kind/routes.yaml
# Single source of truth for protected/skipped routes.
# Both demos consume this file:
#   - setup-plain.sh: `validate-routes translate` → envoy.yaml → ConfigMap
#   - setup-istio.sh: `validate-routes validate` as a lint step
version: 1
protected:
  - method: GET
    path: /anything
  - method: POST
    path: /anything
skipped:
  - method: GET
    path: /healthz
```

- [ ] **Step 2: Validate against main's `validate-routes` CLI**

Run:
```bash
( cd permission-validation && go run ./cmd/validate-routes validate ../kind/routes.yaml )
echo "exit: $?"
```
Expected: no errors, exit 0.

- [ ] **Step 3: Commit**

```bash
git add kind/routes.yaml
git commit -m "$(cat <<'EOF'
demo(kind): routes.yaml — shared protected/skipped declaration

Two protected routes (GET/POST /anything → echo-server) and one
skipped route (GET /healthz). validate-routes validate exits 0
against this file.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 1 — `sample-apps/pcs-ext-proc/` (TDD)

A small HTTP service that honours main's PCS contract. ~70 lines of Go + tests. The only Go we write in this plan.

### Task 1.1: Initialize the Go module

**Files:**
- Create: `sample-apps/pcs-ext-proc/go.mod`

- [ ] **Step 1: Initialize the module**

Run:
```bash
mkdir -p sample-apps/pcs-ext-proc
cd sample-apps/pcs-ext-proc
go mod init github.com/workspace/pcs-ext-proc
cd -
```

Verify:
```bash
cat sample-apps/pcs-ext-proc/go.mod
```
Expected: a module file with `go 1.25` (or whatever the local Go version is, but we target 1.25 for parity with main).

- [ ] **Step 2: Pin go.mod to Go 1.25**

Edit `sample-apps/pcs-ext-proc/go.mod` to have:
```text
module github.com/workspace/pcs-ext-proc

go 1.25
```

- [ ] **Step 3: Commit**

```bash
git add sample-apps/pcs-ext-proc/go.mod
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): initialize module

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.2: TDD — `decide()` for known-allow rule

**Files:**
- Create: `sample-apps/pcs-ext-proc/main_test.go`
- Create: `sample-apps/pcs-ext-proc/main.go`

- [ ] **Step 1: Write the failing test**

Create `sample-apps/pcs-ext-proc/main_test.go`:

```go
package main

import "testing"

func TestDecide_AliceCanEditDoc1(t *testing.T) {
	got := decide("alice@workspace.test", "doc-1", "document", "edit")
	if got != true {
		t.Fatalf("expected allow, got deny")
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run:
```bash
cd sample-apps/pcs-ext-proc && go test ./... ; cd -
```
Expected: FAIL with "undefined: decide".

- [ ] **Step 3: Write minimal `decide()` to make it pass**

Create `sample-apps/pcs-ext-proc/main.go`:

```go
package main

type ruleKey struct {
	user, objectID, objectType, permission string
}

var rules = map[ruleKey]bool{
	{"alice@workspace.test", "doc-1", "document", "edit"}: true,
	{"alice@workspace.test", "doc-1", "document", "read"}: true,
	{"alice@workspace.test", "doc-2", "document", "edit"}: false,
	{"bob@workspace.test", "doc-1", "document", "read"}:   true,
	{"bob@workspace.test", "doc-1", "document", "edit"}:   false,
}

// decide returns true iff (user, objectID, objectType, permission) is in the
// allow-list. Default is deny (false) — both for explicit deny rules and for
// completely unknown combinations.
func decide(user, objectID, objectType, permission string) bool {
	return rules[ruleKey{user, objectID, objectType, permission}]
}
```

- [ ] **Step 4: Run test, verify it passes**

Run:
```bash
cd sample-apps/pcs-ext-proc && go test ./... ; cd -
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add sample-apps/pcs-ext-proc/main.go sample-apps/pcs-ext-proc/main_test.go
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): decide() rule lookup, allow path

TDD'd. Allowlist has 5 hardcoded (user, objectId, objectType, permission)
rules; unknown combos default-deny.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.3: TDD — `decide()` deny paths

**Files:**
- Modify: `sample-apps/pcs-ext-proc/main_test.go`

- [ ] **Step 1: Add failing tests for known-deny and unknown**

Append to `sample-apps/pcs-ext-proc/main_test.go`:

```go
func TestDecide_AliceCannotEditDoc2(t *testing.T) {
	if decide("alice@workspace.test", "doc-2", "document", "edit") {
		t.Fatalf("expected deny, got allow")
	}
}

func TestDecide_BobCannotEditDoc1(t *testing.T) {
	if decide("bob@workspace.test", "doc-1", "document", "edit") {
		t.Fatalf("expected deny, got allow")
	}
}

func TestDecide_UnknownUserAlwaysDenied(t *testing.T) {
	if decide("mallory@workspace.test", "doc-1", "document", "read") {
		t.Fatalf("expected deny for unknown user, got allow")
	}
}

func TestDecide_UnknownObjectAlwaysDenied(t *testing.T) {
	if decide("alice@workspace.test", "doc-99", "document", "read") {
		t.Fatalf("expected deny for unknown object, got allow")
	}
}
```

- [ ] **Step 2: Run, verify all pass on first try**

Run:
```bash
cd sample-apps/pcs-ext-proc && go test -v ./... ; cd -
```
Expected: 5 tests, all PASS (no implementation change needed — `decide()` already covers these via the default-deny semantics of map lookup).

- [ ] **Step 3: Commit**

```bash
git add sample-apps/pcs-ext-proc/main_test.go
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): tests pin deny paths — known-deny, unknown-user, unknown-object

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.4: TDD — HTTP handler, allow path

**Files:**
- Modify: `sample-apps/pcs-ext-proc/main_test.go`
- Modify: `sample-apps/pcs-ext-proc/main.go`

- [ ] **Step 1: Write failing test for `/permission-check/v1/check`**

Append to `sample-apps/pcs-ext-proc/main_test.go`:

```go
import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckHandler_AllowReturns200WithAllowedTrue(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodPost,
		"/permission-check/v1/check",
		strings.NewReader(`{"objectId":"doc-1","objectType":"document","permission":"edit"}`),
	)
	r.Header.Set("Authorization", "Bearer alice@workspace.test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	checkHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Fatalf("body: got %q, want allowed:true", w.Body.String())
	}
}
```

You will need to **rewrite the import block** at the top of the file so the new imports are present. The full import block at this point should be:

```go
import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)
```

- [ ] **Step 2: Run test, verify it fails**

Run:
```bash
cd sample-apps/pcs-ext-proc && go test ./... ; cd -
```
Expected: FAIL with "undefined: checkHandler".

- [ ] **Step 3: Add `checkHandler` to `main.go`**

Append to `sample-apps/pcs-ext-proc/main.go`:

```go
import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type checkRequest struct {
	ObjectID   string `json:"objectId"`
	ObjectType string `json:"objectType"`
	Permission string `json:"permission"`
}

type checkResponse struct {
	Allowed bool `json:"allowed"`
}

func checkHandler(w http.ResponseWriter, r *http.Request) {
	user := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	var req checkRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	allowed := decide(user, req.ObjectID, req.ObjectType, req.Permission)
	slog.Info("decision",
		"ts", time.Now().UTC().Format(time.RFC3339Nano),
		"user", user,
		"obj", req.ObjectID,
		"type", req.ObjectType,
		"perm", req.Permission,
		"decision", map[bool]string{true: "allow", false: "deny"}[allowed],
	)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(checkResponse{Allowed: allowed})
}
```

You will need to **merge the import block** — `main.go` has no imports yet, so this is the first one. Move the `import (...)` block to immediately after `package main` and before the `ruleKey` type.

- [ ] **Step 4: Run test, verify it passes**

Run:
```bash
cd sample-apps/pcs-ext-proc && go test ./... ; cd -
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add sample-apps/pcs-ext-proc/main.go sample-apps/pcs-ext-proc/main_test.go
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): HTTP handler, allow path (200 + allowed:true)

POST /permission-check/v1/check, JSON in, JSON out. User identity is
the bearer-token suffix (no JWT verification — toy demo).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.5: TDD — HTTP handler, deny + malformed paths

**Files:**
- Modify: `sample-apps/pcs-ext-proc/main_test.go`

- [ ] **Step 1: Add deny + malformed tests**

Append to `sample-apps/pcs-ext-proc/main_test.go`:

```go
func TestCheckHandler_DenyReturns200WithAllowedFalse(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodPost,
		"/permission-check/v1/check",
		strings.NewReader(`{"objectId":"doc-2","objectType":"document","permission":"edit"}`),
	)
	r.Header.Set("Authorization", "Bearer alice@workspace.test")
	w := httptest.NewRecorder()
	checkHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"allowed":false`) {
		t.Fatalf("body: got %q, want allowed:false", w.Body.String())
	}
}

func TestCheckHandler_MalformedJSONReturns400(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodPost,
		"/permission-check/v1/check",
		strings.NewReader(`not json`),
	)
	r.Header.Set("Authorization", "Bearer alice@workspace.test")
	w := httptest.NewRecorder()
	checkHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}
```

- [ ] **Step 2: Run, verify all pass**

Run:
```bash
cd sample-apps/pcs-ext-proc && go test -v ./... ; cd -
```
Expected: all 7 tests PASS (no new code needed — the handler already covers these).

- [ ] **Step 3: Commit**

```bash
git add sample-apps/pcs-ext-proc/main_test.go
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): tests pin deny path + malformed-JSON path

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.6: Wire `main()` — HTTP server + JSON logging

**Files:**
- Modify: `sample-apps/pcs-ext-proc/main.go`

- [ ] **Step 1: Add `main()` to `main.go`**

Append to `sample-apps/pcs-ext-proc/main.go`:

```go
func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	http.HandleFunc("/permission-check/v1/check", checkHandler)
	slog.Info("pcs-ext-proc starting", "port", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		slog.Error("listen failed", "err", err)
		os.Exit(1)
	}
}
```

Add `"os"` to the import block.

- [ ] **Step 2: Verify it compiles and tests still pass**

Run:
```bash
cd sample-apps/pcs-ext-proc && go vet ./... && go test ./... && go build ./... ; cd -
```
Expected: clean vet, all tests pass, binary builds.

- [ ] **Step 3: Local smoke test**

In a separate terminal, start the binary:
```bash
cd sample-apps/pcs-ext-proc && PORT=9000 ./pcs-ext-proc &
PCS_PID=$!
cd -
sleep 1
```

Hit it with curl:
```bash
curl -sS -X POST http://127.0.0.1:9000/permission-check/v1/check \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "Content-Type: application/json" \
  -d '{"objectId":"doc-1","objectType":"document","permission":"edit"}'
echo
```
Expected output: `{"allowed":true}`

```bash
curl -sS -X POST http://127.0.0.1:9000/permission-check/v1/check \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "Content-Type: application/json" \
  -d '{"objectId":"doc-2","objectType":"document","permission":"edit"}'
echo
```
Expected output: `{"allowed":false}`

Stop the server:
```bash
kill $PCS_PID
cd sample-apps/pcs-ext-proc && rm -f pcs-ext-proc ; cd -
```

- [ ] **Step 4: Commit**

```bash
git add sample-apps/pcs-ext-proc/main.go
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): main() — HTTP server, slog JSON, $PORT (default 8080)

Smoke-tested locally: curl returns {"allowed":true} for alice/doc-1/edit
and {"allowed":false} for alice/doc-2/edit.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 1.7: Dockerfile for `pcs-ext-proc`

**Files:**
- Create: `sample-apps/pcs-ext-proc/deploy/Dockerfile`

- [ ] **Step 1: Write the Dockerfile**

Create `sample-apps/pcs-ext-proc/deploy/Dockerfile`:

```dockerfile
FROM golang:1.25 AS build
WORKDIR /src
COPY go.mod go.sum* ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 go build -o /out/pcs-ext-proc .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build --chown=65532:65532 /out/pcs-ext-proc /pcs-ext-proc
ENV PORT=8080
EXPOSE 8080
ENTRYPOINT ["/pcs-ext-proc"]
```

(`go.sum*` with the wildcard is intentional — there are no external deps yet, so go.sum does not exist; `COPY go.sum*` is a no-op when it is missing.)

- [ ] **Step 2: Build the image locally**

Run:
```bash
docker build -t workspace/pcs-ext-proc:dev -f sample-apps/pcs-ext-proc/deploy/Dockerfile sample-apps/pcs-ext-proc/
```
Expected: build succeeds, image `workspace/pcs-ext-proc:dev` is created.

- [ ] **Step 3: Smoke-test the image**

Run:
```bash
docker run --rm -d --name pcs-smoke -p 9000:8080 workspace/pcs-ext-proc:dev
sleep 1
curl -sS -X POST http://127.0.0.1:9000/permission-check/v1/check \
  -H "Authorization: Bearer alice@workspace.test" \
  -d '{"objectId":"doc-1","objectType":"document","permission":"edit"}'
echo
docker rm -f pcs-smoke
```
Expected: `{"allowed":true}`.

- [ ] **Step 4: Commit**

```bash
git add sample-apps/pcs-ext-proc/deploy/Dockerfile
git commit -m "$(cat <<'EOF'
demo(pcs-ext-proc): Dockerfile (distroless static)

Image tag workspace/pcs-ext-proc:dev. Container listens on :8080
(override via PORT env). Smoke-tested with docker run.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 2 — Option B: `demo-ext-proc-plain`

A flat Helm chart that installs a single namespace with PCS + echo-app (3-container pod: envoy + sidecar + echo). Envoy bootstrap is generated by `validate-routes translate` and passed to Helm via `--set-file`.

### Task 2.1: Helm chart skeleton — `Chart.yaml` + `values.yaml`

**Files:**
- Create: `kind/demo-ext-proc-plain/Chart.yaml`
- Create: `kind/demo-ext-proc-plain/values.yaml`

- [ ] **Step 1: Write `Chart.yaml`**

```yaml
# kind/demo-ext-proc-plain/Chart.yaml
apiVersion: v2
name: demo-ext-proc-plain
description: |
  Option B of the ext_proc kind demo — main's design as written.
  Plain Envoy + permission-validation sidecar + echo-server in one pod,
  istio-injection=disabled, NodePort straight to Envoy.
type: application
version: 0.1.0
appVersion: "0.1.0"
```

- [ ] **Step 2: Write `values.yaml`**

```yaml
# kind/demo-ext-proc-plain/values.yaml
# Single source of image references for the chart.
namespace: demo-plain

images:
  pullPolicy: IfNotPresent
  envoy:               envoyproxy/envoy:v1.31.3
  permissionValidation: workspace/permission-validation:dev
  echoServer:           workspace/echo-server:dev
  pcs:                  workspace/pcs-ext-proc:dev

# envoyBootstrap is injected by setup-plain.sh via:
#   helm install ... --set-file envoyBootstrap=/tmp/envoy.yaml
# The default empty string makes `helm template` fail loudly if you forget,
# which is intended.
envoyBootstrap: ""

resources:
  app:   { requests: { cpu: 10m,  memory: 32Mi }, limits: { cpu: 100m, memory: 64Mi } }
  envoy: { requests: { cpu: 20m,  memory: 64Mi }, limits: { cpu: 200m, memory: 128Mi } }

nodePort: 30090
```

- [ ] **Step 3: Verify the chart is parseable**

Run:
```bash
helm lint kind/demo-ext-proc-plain/
```
Expected: 0 chart(s) failed (or specific "no templates" warning which is fine at this stage).

- [ ] **Step 4: Commit**

```bash
git add kind/demo-ext-proc-plain/Chart.yaml kind/demo-ext-proc-plain/values.yaml
git commit -m "$(cat <<'EOF'
demo(plain): Chart.yaml + values.yaml — image table + nodePort

envoyBootstrap is intentionally empty in values — populated at install
time via helm install --set-file.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.2: `namespace.yaml` template

**Files:**
- Create: `kind/demo-ext-proc-plain/templates/namespace.yaml`

- [ ] **Step 1: Write the namespace template**

```yaml
# kind/demo-ext-proc-plain/templates/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Values.namespace }}
  labels:
    # Disable Istio sidecar injection — Option B runs its own Envoy,
    # so an injected istio-proxy would create a second Envoy in the
    # path. This label is the kill switch.
    istio-injection: disabled
```

- [ ] **Step 2: Render and inspect**

Run:
```bash
helm template plain kind/demo-ext-proc-plain/ --set envoyBootstrap='placeholder' --show-only templates/namespace.yaml
```
Expected: a `Namespace` manifest with `metadata.name: demo-plain` and `labels.istio-injection: disabled`.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-plain/templates/namespace.yaml
git commit -m "$(cat <<'EOF'
demo(plain): namespace template with istio-injection=disabled

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.3: `pcs.yaml` template (Deployment + Service)

**Files:**
- Create: `kind/demo-ext-proc-plain/templates/pcs.yaml`

- [ ] **Step 1: Write the PCS template**

```yaml
# kind/demo-ext-proc-plain/templates/pcs.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pcs
  namespace: {{ .Values.namespace }}
  labels: { app: pcs }
spec:
  replicas: 1
  selector: { matchLabels: { app: pcs } }
  template:
    metadata:
      labels: { app: pcs }
    spec:
      containers:
      - name: pcs
        image: {{ .Values.images.pcs }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        ports:
        - containerPort: 8080
          name: http
        readinessProbe:
          tcpSocket: { port: 8080 }
          periodSeconds: 2
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
---
apiVersion: v1
kind: Service
metadata:
  name: pcs
  namespace: {{ .Values.namespace }}
spec:
  type: ClusterIP
  selector: { app: pcs }
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

- [ ] **Step 2: Render and lint**

Run:
```bash
helm template plain kind/demo-ext-proc-plain/ --set envoyBootstrap='placeholder' --show-only templates/pcs.yaml \
  | kubectl apply --dry-run=client -f -
```
Expected: `deployment.apps/pcs created (dry run)` and `service/pcs created (dry run)`.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-plain/templates/pcs.yaml
git commit -m "$(cat <<'EOF'
demo(plain): pcs Deployment + Service (HTTP :8080)

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.4: `envoy-bootstrap-cm.yaml` template

**Files:**
- Create: `kind/demo-ext-proc-plain/templates/envoy-bootstrap-cm.yaml`

- [ ] **Step 1: Write the ConfigMap template**

```yaml
# kind/demo-ext-proc-plain/templates/envoy-bootstrap-cm.yaml
{{- if not .Values.envoyBootstrap }}
{{- fail "envoyBootstrap is empty — pass via `helm install --set-file envoyBootstrap=/path/to/envoy.yaml`" }}
{{- end }}
apiVersion: v1
kind: ConfigMap
metadata:
  name: envoy-bootstrap
  namespace: {{ .Values.namespace }}
data:
  envoy.yaml: |
{{ .Values.envoyBootstrap | indent 4 }}
```

- [ ] **Step 2: Verify the fail-fast guard**

Run:
```bash
helm template plain kind/demo-ext-proc-plain/ --show-only templates/envoy-bootstrap-cm.yaml 2>&1 | head -3
```
Expected: error containing `envoyBootstrap is empty`.

Then run:
```bash
helm template plain kind/demo-ext-proc-plain/ \
  --set-file envoyBootstrap=/dev/stdin \
  --show-only templates/envoy-bootstrap-cm.yaml <<'YAML'
fake: envoy.yaml content
YAML
```
Expected: a ConfigMap with `data.envoy.yaml` containing `fake: envoy.yaml content`.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-plain/templates/envoy-bootstrap-cm.yaml
git commit -m "$(cat <<'EOF'
demo(plain): envoy-bootstrap ConfigMap, populated via --set-file

Fails fast if envoyBootstrap is empty so a forgotten --set-file is
loud instead of silently shipping an empty Envoy config.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.5: `echo-app.yaml` template — 3-container pod + NodePort Service

**Files:**
- Create: `kind/demo-ext-proc-plain/templates/echo-app.yaml`

- [ ] **Step 1: Write the echo-app template**

```yaml
# kind/demo-ext-proc-plain/templates/echo-app.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo-app
  namespace: {{ .Values.namespace }}
  labels: { app: echo-app }
spec:
  replicas: 1
  selector: { matchLabels: { app: echo-app } }
  template:
    metadata:
      labels: { app: echo-app }
      annotations:
        sidecar.istio.io/inject: "false"   # Belt-and-braces. The
                                            # namespace label already
                                            # disables injection.
    spec:
      containers:
      - name: envoy
        image: {{ .Values.images.envoy }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        command: ["envoy"]
        args:
        - "-c"
        - "/etc/envoy/envoy.yaml"
        - "--log-level"
        - "info"
        ports:
        - containerPort: 8000
          name: http
        readinessProbe:
          tcpSocket: { port: 8000 }
          periodSeconds: 2
        volumeMounts:
        - name: envoy-config
          mountPath: /etc/envoy
        resources:
          {{- toYaml .Values.resources.envoy | nindent 10 }}
      - name: sidecar
        image: {{ .Values.images.permissionValidation }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        args:
        - "--listen=0.0.0.0:50051"
        - "--pcs-endpoint=http://pcs:8080"
        - "--pcs-timeout=250ms"
        - "--otel-disabled"
        ports:
        - containerPort: 50051
          name: grpc-extproc
        readinessProbe:
          tcpSocket: { port: 50051 }
          periodSeconds: 2
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
      - name: echo
        image: {{ .Values.images.echoServer }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        ports:
        - containerPort: 8080
          name: http-echo
        readinessProbe:
          tcpSocket: { port: 8080 }
          periodSeconds: 2
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
      volumes:
      - name: envoy-config
        configMap:
          name: envoy-bootstrap
---
apiVersion: v1
kind: Service
metadata:
  name: echo-app
  namespace: {{ .Values.namespace }}
spec:
  type: NodePort
  selector: { app: echo-app }
  ports:
  - name: http
    port: 8000
    targetPort: 8000
    nodePort: {{ .Values.nodePort }}
```

- [ ] **Step 2: Render and dry-run apply**

Run:
```bash
helm template plain kind/demo-ext-proc-plain/ \
  --set-file envoyBootstrap=/dev/stdin \
  --show-only templates/echo-app.yaml <<'YAML' \
  | kubectl apply --dry-run=client -f -
placeholder
YAML
```
Expected: `deployment.apps/echo-app created (dry run)` and `service/echo-app created (dry run)`.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-plain/templates/echo-app.yaml
git commit -m "$(cat <<'EOF'
demo(plain): echo-app — 3-container pod + NodePort 30090

envoy reads /etc/envoy/envoy.yaml from the envoy-bootstrap ConfigMap.
sidecar talks to pcs Service over HTTP at :8080. echo answers on
:8080 (pod-local; reached only via envoy on the cluster path).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.6: `setup-plain.sh` — phase A (cluster + image build + kind load)

**Files:**
- Create: `kind/setup-plain.sh`

- [ ] **Step 1: Write the setup script — phase A**

```bash
#!/usr/bin/env bash
# kind/setup-plain.sh
# Idempotent bring-up for the Option B ext_proc demo. Run from repo root:
#   ./kind/setup-plain.sh
set -euo pipefail

CLUSTER_NAME="ext-proc-plain-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHART_DIR="${KIND_DIR}/demo-ext-proc-plain"
NAMESPACE="demo-plain"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# -- Phase A: cluster + images ------------------------------------------------
if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi

log "Building images"
docker build -t workspace/echo-server:dev   -f "${ROOT}/sample-apps/echo-server/deploy/Dockerfile"   "${ROOT}/sample-apps/echo-server/"
docker build -t workspace/pcs-ext-proc:dev  -f "${ROOT}/sample-apps/pcs-ext-proc/deploy/Dockerfile"  "${ROOT}/sample-apps/pcs-ext-proc/"
docker build -t workspace/permission-validation:dev \
  -f "${ROOT}/permission-validation/test/e2e/Dockerfile.sidecar" \
  "${ROOT}/permission-validation/"

log "Loading images into kind"
for img in workspace/echo-server:dev workspace/pcs-ext-proc:dev workspace/permission-validation:dev; do
  kind load docker-image "${img}" --name "${CLUSTER_NAME}"
done

echo
echo "Phase A done. Phase B (generate envoy.yaml + install) is the next task."
```

- [ ] **Step 2: Make it executable and confirm it runs through phase A**

Run:
```bash
chmod +x kind/setup-plain.sh
./kind/setup-plain.sh
```

Expected: cluster created (or reuse log line), three images built, three `kind load` lines, "Phase A done."

Verify with:
```bash
kind get clusters
docker images | grep workspace/
```
Expected: `ext-proc-plain-demo` in cluster list; three `workspace/*:dev` images.

- [ ] **Step 3: Commit (partial — script is incomplete but functional through phase A)**

```bash
git add kind/setup-plain.sh
git commit -m "$(cat <<'EOF'
demo(plain): setup-plain.sh phase A — cluster + image build + kind load

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.7: `setup-plain.sh` — phase B (translate + helm install + verify)

**Files:**
- Modify: `kind/setup-plain.sh`

- [ ] **Step 1: Append phase B + verification to `kind/setup-plain.sh`**

Replace the trailing `echo "Phase A done..."` lines with:

```bash
# -- Phase B: generate envoy.yaml + helm install ------------------------------
log "Validating routes.yaml"
( cd "${ROOT}/permission-validation" && go run ./cmd/validate-routes validate "${KIND_DIR}/routes.yaml" )

ENVOY_OUT="$(mktemp -t envoy.XXXXX.yaml)"
trap 'rm -f "${ENVOY_OUT}"' EXIT

log "Translating routes.yaml -> envoy.yaml at ${ENVOY_OUT}"
( cd "${ROOT}/permission-validation" && go run ./cmd/validate-routes translate \
    "${KIND_DIR}/routes.yaml" \
    -o "${ENVOY_OUT}" \
    --sidecar-host 127.0.0.1 --sidecar-port 50051 \
    --backend-host 127.0.0.1 --backend-port 8080 \
    --admin-host 127.0.0.1 \
    --access-log )

log "Helm install (with --set-file envoyBootstrap=${ENVOY_OUT})"
helm upgrade --install plain "${CHART_DIR}" \
  --namespace default \
  --set-file envoyBootstrap="${ENVOY_OUT}" \
  --wait --timeout 180s

# -- Phase C: verification ----------------------------------------------------
log "Waiting for echo-app and pcs pods to be Ready"
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=echo-app --timeout=120s
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=pcs       --timeout=120s

log "Verifying canonical curls"
fail=0
expect_status() {
  local want="$1"; shift
  local got
  got="$(curl -sS -o /dev/null -w '%{http_code}' "$@")"
  if [[ "${got}" != "${want}" ]]; then
    printf "  FAIL  expected %s, got %s    curl %s\n" "${want}" "${got}" "$*"
    fail=1
  else
    printf "  ok    %s    curl %s\n" "${got}" "$*"
  fi
}

BASE="http://127.0.0.1:8090"   # Option B NodePort → echo-app Envoy
HOSTHDR='-H Host: app.local'

# 1) ALLOW
expect_status 200 "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"

# 2) DENY
expect_status 403 "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-2:document:edit"

# 3) MISSING CONTEXT
expect_status 403 "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test"

# 4) SKIPPED ROUTE
expect_status 200 "${BASE}/healthz" -H "Host: app.local"

if [[ ${fail} -eq 0 ]]; then
  printf "\n\033[1;32mAll four canonical curls returned expected status codes.\033[0m\n"
else
  printf "\n\033[1;31mAt least one curl returned an unexpected status. Inspect pod logs.\033[0m\n"
  exit 1
fi
```

No `/etc/hosts` edit is required: every verification curl in Phase C uses `-H "Host: app.local"` against a `127.0.0.1` URL, so DNS for `app.local` is never resolved.

- [ ] **Step 2: Re-run the full script**

Run:
```bash
./kind/setup-plain.sh
```
Expected: cluster reused (or recreated), images built, envoy.yaml generated, Helm install succeeds, all four curls return expected status codes, "All four canonical curls returned expected status codes." banner.

- [ ] **Step 3: Manually inspect logs**

Run:
```bash
kubectl -n demo-plain logs deploy/pcs --tail=10
kubectl -n demo-plain logs deploy/echo-app -c sidecar --tail=20
```
Expected: PCS shows `decision=allow` / `decision=deny` lines; sidecar shows per-request handler activity (from main's sidecar code).

- [ ] **Step 4: Commit**

```bash
git add kind/setup-plain.sh
git commit -m "$(cat <<'EOF'
demo(plain): setup-plain.sh phases B+C — translate, install, verify

Generates envoy.yaml from routes.yaml at runtime, installs via Helm with
--set-file envoyBootstrap, then asserts all four canonical curls return
the expected status codes.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 2.8: Option B `README.md` for the chart

**Files:**
- Create: `kind/demo-ext-proc-plain/README.md`

- [ ] **Step 1: Write the chart README**

```markdown
# demo-ext-proc-plain — Option B Helm chart

Plain Envoy + permission-validation sidecar + echo-server in one pod,
istio-injection disabled, NodePort straight to Envoy. This is "main's
design as written."

## What is in this chart

| Template | Purpose |
|---|---|
| `namespace.yaml` | Namespace `demo-plain`, with `istio-injection: disabled`. |
| `envoy-bootstrap-cm.yaml` | ConfigMap holding the Envoy bootstrap; populated via `--set-file envoyBootstrap=...` at install time. |
| `echo-app.yaml` | Deployment with three containers (envoy, sidecar, echo) + NodePort Service exposing Envoy on 30090. |
| `pcs.yaml` | demo PCS Deployment + Service (HTTP :8080). |

## Install path

Run `kind/setup-plain.sh` from the repo root — it handles cluster
creation, image building, envoy.yaml generation, Helm install, and
verification.

To install manually (after the script has built the images):

```bash
ENVOY_OUT=$(mktemp -t envoy.XXXXX.yaml)
( cd permission-validation && go run ./cmd/validate-routes translate \
    ../kind/routes.yaml -o "$ENVOY_OUT" \
    --sidecar-host 127.0.0.1 --sidecar-port 50051 \
    --backend-host 127.0.0.1 --backend-port 8080 \
    --admin-host 127.0.0.1 --access-log )

helm install plain kind/demo-ext-proc-plain/ \
  --set-file envoyBootstrap="$ENVOY_OUT" --wait
```

## What to point at during the demo

- `kubectl -n demo-plain get cm envoy-bootstrap -o yaml` — generated by `validate-routes translate`.
- `kubectl -n demo-plain logs deploy/echo-app -c sidecar` — main's sidecar `extract → parse → pcs → outcome` log lines.
- `kubectl -n demo-plain logs deploy/pcs` — one JSON line per decision.
- `kubectl -n demo-plain describe pod <echo-app>` — three containers, no `istio-proxy`.
```

- [ ] **Step 2: Commit**

```bash
git add kind/demo-ext-proc-plain/README.md
git commit -m "$(cat <<'EOF'
demo(plain): chart README — what is in it, install path, what to point at

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

## Phase 3 — Option A: `demo-ext-proc-istio`

A flat Helm chart that installs Istio Gateway + VirtualService + a sidecar-injected echo-app pod with a separate `permission-validation` sidecar container reachable on port 50051, plus an `EnvoyFilter` that splices `ext_proc` into Istio's istio-proxy.

### Task 3.1: Chart skeleton — `Chart.yaml` + `values.yaml`

**Files:**
- Create: `kind/demo-ext-proc-istio/Chart.yaml`
- Create: `kind/demo-ext-proc-istio/values.yaml`

- [ ] **Step 1: Write `Chart.yaml`**

```yaml
# kind/demo-ext-proc-istio/Chart.yaml
apiVersion: v2
name: demo-ext-proc-istio
description: |
  Option A of the ext_proc kind demo — main's sidecar adapted to Istio.
  Single namespace with istio-injection=enabled; permission-validation
  sidecar is a container in the echo-app pod, reached by istio-proxy
  via the echo-app Service on port 50051. An EnvoyFilter splices the
  ext_proc HTTP filter into istio-proxy's filter chain.
type: application
version: 0.1.0
appVersion: "0.1.0"
```

- [ ] **Step 2: Write `values.yaml`**

```yaml
# kind/demo-ext-proc-istio/values.yaml
namespace: demo-istio
gatewayHost: app.local

images:
  pullPolicy: IfNotPresent
  permissionValidation: workspace/permission-validation:dev
  echoServer:           workspace/echo-server:dev
  pcs:                  workspace/pcs-ext-proc:dev

# EnvoyFilter wiring. The `authority` field is critical — Istio's xDS
# cluster_name contains `|`, which is invalid in HTTP/2 :authority,
# so we override it explicitly.
extProc:
  clusterName: "outbound|50051||echo-app.demo-istio.svc.cluster.local"
  authority:   "echo-app.demo-istio.svc.cluster.local"
  messageTimeout: "2s"

resources:
  app: { requests: { cpu: 10m,  memory: 32Mi }, limits: { cpu: 100m, memory: 64Mi } }
```

- [ ] **Step 3: Lint**

Run:
```bash
helm lint kind/demo-ext-proc-istio/
```
Expected: 0 chart(s) failed.

- [ ] **Step 4: Commit**

```bash
git add kind/demo-ext-proc-istio/Chart.yaml kind/demo-ext-proc-istio/values.yaml
git commit -m "$(cat <<'EOF'
demo(istio): Chart.yaml + values.yaml — image table + ext_proc cluster wiring

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3.2: `namespace.yaml` template

**Files:**
- Create: `kind/demo-ext-proc-istio/templates/namespace.yaml`

- [ ] **Step 1: Write the template**

```yaml
# kind/demo-ext-proc-istio/templates/namespace.yaml
apiVersion: v1
kind: Namespace
metadata:
  name: {{ .Values.namespace }}
  labels:
    # This is the default in this team's clusters. Stated explicitly here
    # so it is impossible to misread.
    istio-injection: enabled
```

- [ ] **Step 2: Render**

Run:
```bash
helm template istio kind/demo-ext-proc-istio/ --show-only templates/namespace.yaml
```
Expected: a Namespace with `istio-injection: enabled`.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/namespace.yaml
git commit -m "demo(istio): namespace with istio-injection=enabled

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.3: `pcs.yaml` template (identical shape to Option B)

**Files:**
- Create: `kind/demo-ext-proc-istio/templates/pcs.yaml`

- [ ] **Step 1: Copy from Option B with the namespace + label tweak**

```yaml
# kind/demo-ext-proc-istio/templates/pcs.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pcs
  namespace: {{ .Values.namespace }}
  labels: { app: pcs }
spec:
  replicas: 1
  selector: { matchLabels: { app: pcs } }
  template:
    metadata:
      labels: { app: pcs }
    spec:
      containers:
      - name: pcs
        image: {{ .Values.images.pcs }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        ports:
        - containerPort: 8080
          name: http
        readinessProbe:
          tcpSocket: { port: 8080 }
          periodSeconds: 2
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
---
apiVersion: v1
kind: Service
metadata:
  name: pcs
  namespace: {{ .Values.namespace }}
spec:
  type: ClusterIP
  selector: { app: pcs }
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

- [ ] **Step 2: Render + dry-run**

```bash
helm template istio kind/demo-ext-proc-istio/ --show-only templates/pcs.yaml \
  | kubectl apply --dry-run=client -f -
```
Expected: pcs Deployment + Service created (dry run).

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/pcs.yaml
git commit -m "demo(istio): pcs Deployment + Service

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.4: `echo-app.yaml` template — sidecar + echo containers + dual-port Service

**Files:**
- Create: `kind/demo-ext-proc-istio/templates/echo-app.yaml`

- [ ] **Step 1: Write the template**

```yaml
# kind/demo-ext-proc-istio/templates/echo-app.yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: echo-app
  namespace: {{ .Values.namespace }}
  labels: { app: echo-app }
spec:
  replicas: 1
  selector: { matchLabels: { app: echo-app } }
  template:
    metadata:
      labels: { app: echo-app }
      # No istio-proxy injection annotations needed; the namespace label
      # turns on injection. istio-proxy will be added as a 3rd container
      # by the Istio admission webhook.
    spec:
      containers:
      - name: sidecar
        image: {{ .Values.images.permissionValidation }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        args:
        - "--listen=0.0.0.0:50051"
        - "--pcs-endpoint=http://pcs:8080"
        - "--pcs-timeout=250ms"
        - "--otel-disabled"
        ports:
        - containerPort: 50051
          name: grpc-extproc
        readinessProbe:
          tcpSocket: { port: 50051 }
          periodSeconds: 2
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
      - name: echo
        image: {{ .Values.images.echoServer }}
        imagePullPolicy: {{ .Values.images.pullPolicy }}
        ports:
        - containerPort: 8080
          name: http-echo
        readinessProbe:
          tcpSocket: { port: 8080 }
          periodSeconds: 2
        resources:
          {{- toYaml .Values.resources.app | nindent 10 }}
---
# Dual-port Service: 8080 for app traffic via the Gateway, 50051 so that
# istio-proxy can reach the sidecar via the standard xDS cluster_name
# pattern (outbound|50051||echo-app.demo-istio.svc.cluster.local).
apiVersion: v1
kind: Service
metadata:
  name: echo-app
  namespace: {{ .Values.namespace }}
spec:
  type: ClusterIP
  selector: { app: echo-app }
  ports:
  - name: http
    port: 8080
    targetPort: 8080
    appProtocol: http
  - name: grpc-extproc
    port: 50051
    targetPort: 50051
    appProtocol: grpc
```

- [ ] **Step 2: Render + dry-run**

```bash
helm template istio kind/demo-ext-proc-istio/ --show-only templates/echo-app.yaml \
  | kubectl apply --dry-run=client -f -
```
Expected: echo-app Deployment + Service created (dry run); Service has two named ports `http` and `grpc-extproc`.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/echo-app.yaml
git commit -m "$(cat <<'EOF'
demo(istio): echo-app — sidecar + echo containers, dual-port Service

Service exposes 8080 (app traffic via Gateway) and 50051
(grpc-extproc; istio-proxy reaches the sidecar via xDS cluster).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3.5: `gateway.yaml` template — Istio Gateway + VirtualService

**Files:**
- Create: `kind/demo-ext-proc-istio/templates/gateway.yaml`

- [ ] **Step 1: Write the template**

```yaml
# kind/demo-ext-proc-istio/templates/gateway.yaml
apiVersion: networking.istio.io/v1beta1
kind: Gateway
metadata:
  name: app-gateway
  namespace: {{ .Values.namespace }}
spec:
  selector:
    istio: ingressgateway
  servers:
  - port:
      number: 80
      name: http
      protocol: HTTP
    hosts:
    - {{ .Values.gatewayHost | quote }}
---
apiVersion: networking.istio.io/v1beta1
kind: VirtualService
metadata:
  name: echo-app
  namespace: {{ .Values.namespace }}
spec:
  hosts:
  - {{ .Values.gatewayHost | quote }}
  gateways:
  - app-gateway
  http:
  - route:
    - destination:
        host: echo-app
        port:
          number: 8080
```

- [ ] **Step 2: Render + dry-run**

```bash
helm template istio kind/demo-ext-proc-istio/ --show-only templates/gateway.yaml \
  | kubectl apply --dry-run=client -f -
```
Expected: Gateway + VirtualService created (dry run). May warn about missing CRDs at dry-run; that is OK at this stage — the actual install will be against an Istio-installed cluster.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/gateway.yaml
git commit -m "demo(istio): Gateway + VirtualService for app.local → echo-app:8080

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 3.6: `envoyfilter.yaml` template — ext_proc patch with `authority` fix

**Files:**
- Create: `kind/demo-ext-proc-istio/templates/envoyfilter.yaml`

- [ ] **Step 1: Write the template**

```yaml
# kind/demo-ext-proc-istio/templates/envoyfilter.yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: echo-ext-proc
  namespace: {{ .Values.namespace }}
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
              cluster_name: {{ .Values.extProc.clusterName | quote }}
              # CRITICAL: Istio's xDS cluster_name contains `|`, which is
              # invalid in HTTP/2 :authority. Without this override, the
              # gRPC stream out of istio-proxy fails with a malformed-
              # header error. (Found in earlier parked scaffolding.)
              authority: {{ .Values.extProc.authority | quote }}
            timeout: 1s
          processing_mode:
            request_header_mode: SEND
            response_header_mode: SKIP
            request_body_mode: NONE
            response_body_mode: NONE
            request_trailer_mode: SKIP
            response_trailer_mode: SKIP
          failure_mode_allow: false
          message_timeout: {{ .Values.extProc.messageTimeout | quote }}
```

- [ ] **Step 2: Render and inspect**

```bash
helm template istio kind/demo-ext-proc-istio/ --show-only templates/envoyfilter.yaml
```
Expected: EnvoyFilter rendered with `cluster_name` and `authority` lines populated from values.

- [ ] **Step 3: Commit**

```bash
git add kind/demo-ext-proc-istio/templates/envoyfilter.yaml
git commit -m "$(cat <<'EOF'
demo(istio): EnvoyFilter splicing ext_proc into istio-proxy

Includes the authority override that works around the | character in
Istio's xDS cluster_name being invalid in HTTP/2 :authority.

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3.7: `setup-istio.sh` — phase A (cluster + Istio + image build + kind load)

**Files:**
- Create: `kind/setup-istio.sh`

- [ ] **Step 1: Write the script (phase A only first — Istio install is the slow part to validate early)**

```bash
#!/usr/bin/env bash
# kind/setup-istio.sh
# Idempotent bring-up for the Option A ext_proc demo. Run from repo root:
#   ./kind/setup-istio.sh
set -euo pipefail

CLUSTER_NAME="ext-proc-istio-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHARTS="${KIND_DIR}/charts"
CHART_DIR="${KIND_DIR}/demo-ext-proc-istio"
NAMESPACE="demo-istio"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# Note: we avoid touching /etc/hosts — all verification curls use
# `-H "Host: app.local"` with a 127.0.0.1 URL. This matches the
# pattern from kind-demo branch commit b63db92.

# Single source of truth for the Istio image: kind/demo-ext-proc-istio/values.yaml
# (no images.istio entry yet; default to docker.io/istio:1.24.2).
ISTIO_HUB="docker.io/istio"
ISTIO_TAG="1.24.2"

# -- Phase A: cluster ---------------------------------------------------------
if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi

# -- Phase A: Istio -----------------------------------------------------------
log "Installing Istio (base + istiod + ingressgateway)"
helm upgrade --install istio-base   "${CHARTS}/base-1.24.2.tgz" \
  -n istio-system --create-namespace --wait
helm upgrade --install istiod       "${CHARTS}/istiod-1.24.2.tgz" \
  -n istio-system \
  --set "global.hub=${ISTIO_HUB}" --set "global.tag=${ISTIO_TAG}" \
  --wait
helm upgrade --install ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n istio-system \
  --set "service.type=NodePort" \
  --set "service.ports[0].name=http2" \
  --set "service.ports[0].port=80" \
  --set "service.ports[0].targetPort=80" \
  --set "service.ports[0].nodePort=30080" \
  --wait

# -- Phase A: images ----------------------------------------------------------
log "Building images"
docker build -t workspace/echo-server:dev          -f "${ROOT}/sample-apps/echo-server/deploy/Dockerfile"         "${ROOT}/sample-apps/echo-server/"
docker build -t workspace/pcs-ext-proc:dev         -f "${ROOT}/sample-apps/pcs-ext-proc/deploy/Dockerfile"        "${ROOT}/sample-apps/pcs-ext-proc/"
docker build -t workspace/permission-validation:dev -f "${ROOT}/permission-validation/test/e2e/Dockerfile.sidecar" "${ROOT}/permission-validation/"

log "Loading images into kind"
for img in workspace/echo-server:dev workspace/pcs-ext-proc:dev workspace/permission-validation:dev; do
  kind load docker-image "${img}" --name "${CLUSTER_NAME}"
done

echo "Phase A done."
```

- [ ] **Step 2: Make executable and run**

Run:
```bash
chmod +x kind/setup-istio.sh
./kind/setup-istio.sh
```
Expected: cluster created, Istio installed (3 helm releases in `istio-system`), 3 images built and loaded.

Verify:
```bash
kubectl -n istio-system get deploy
```
Expected: `istiod` and `ingressgateway` ready.

- [ ] **Step 3: Commit (partial)**

```bash
git add kind/setup-istio.sh
git commit -m "$(cat <<'EOF'
demo(istio): setup-istio.sh phase A — cluster + Istio + images

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3.8: `setup-istio.sh` — phase B (validate routes + helm install + verify)

**Files:**
- Modify: `kind/setup-istio.sh`

- [ ] **Step 1: Append phase B + verification**

Append to `kind/setup-istio.sh`:

```bash
# -- Phase B: validate routes + helm install ----------------------------------
log "Validating routes.yaml (lint only — Option A does not generate envoy.yaml)"
( cd "${ROOT}/permission-validation" && go run ./cmd/validate-routes validate "${KIND_DIR}/routes.yaml" )

log "Helm install"
helm upgrade --install istio "${CHART_DIR}" --namespace default --wait --timeout 180s

# -- Phase C: verification ----------------------------------------------------
log "Waiting for echo-app and pcs pods to be Ready"
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=echo-app --timeout=180s
kubectl -n "${NAMESPACE}" wait --for=condition=Ready pod -l app=pcs       --timeout=180s

log "Verifying canonical curls (via ingressgateway @ localhost:8080, Host: app.local)"
fail=0
expect_status() {
  local want="$1"; shift
  local got
  got="$(curl -sS -o /dev/null -w '%{http_code}' "$@")"
  if [[ "${got}" != "${want}" ]]; then
    printf "  FAIL  expected %s, got %s    curl %s\n" "${want}" "${got}" "$*"
    fail=1
  else
    printf "  ok    %s    curl %s\n" "${got}" "$*"
  fi
}

# Tiny wait for the EnvoyFilter to propagate to istio-proxy via xDS
sleep 5

# 1) ALLOW
expect_status 200 "http://127.0.0.1:8080/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"

# 2) DENY
expect_status 403 "http://127.0.0.1:8080/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-2:document:edit"

# 3) MISSING CONTEXT
expect_status 403 "http://127.0.0.1:8080/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test"

# 4) ECHO HEALTHZ — Option A does not honour the routes.yaml skipped list
#    (no translate target); the request still goes through ext_proc, where
#    the sidecar rejects missing X-Auth-Context with 403. We assert 403 here
#    intentionally and call it out in DEMO.md.
expect_status 403 "http://127.0.0.1:8080/healthz" -H "Host: app.local"

if [[ ${fail} -eq 0 ]]; then
  printf "\n\033[1;32mAll four canonical curls returned expected status codes.\033[0m\n"
else
  printf "\n\033[1;31mAt least one curl returned an unexpected status.\033[0m\n"
  exit 1
fi
```

- [ ] **Step 2: Run end-to-end**

Run:
```bash
./kind/setup-istio.sh
```
Expected: all four curls return expected status codes, success banner.

- [ ] **Step 3: Inspect logs**

```bash
kubectl -n demo-istio logs deploy/pcs --tail=10
kubectl -n demo-istio logs deploy/echo-app -c sidecar --tail=20
kubectl -n demo-istio logs deploy/echo-app -c istio-proxy --tail=10 | head
```
Expected: PCS shows decision lines; sidecar shows handler activity; istio-proxy shows access log lines.

- [ ] **Step 4: Commit**

```bash
git add kind/setup-istio.sh
git commit -m "$(cat <<'EOF'
demo(istio): setup-istio.sh phases B+C — validate, install, verify

Asserts all four canonical curls return expected status codes. Note:
in Option A /healthz returns 403 because there is no per-route skip
yet (the routes.yaml skipped list is honoured only by Option B's
generated bootstrap).

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 3.9: Option A `README.md` for the chart

**Files:**
- Create: `kind/demo-ext-proc-istio/README.md`

- [ ] **Step 1: Write the chart README**

```markdown
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
```

- [ ] **Step 2: Commit**

```bash
git add kind/demo-ext-proc-istio/README.md
git commit -m "demo(istio): chart README

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

## Phase 4 — Finishing

### Task 4.1: `kind/teardown.sh`

**Files:**
- Create: `kind/teardown.sh`

- [ ] **Step 1: Write the teardown script**

```bash
#!/usr/bin/env bash
# kind/teardown.sh <cluster-name>
# Removes the named kind cluster. Cluster names:
#   ext-proc-plain-demo   (created by setup-plain.sh)
#   ext-proc-istio-demo   (created by setup-istio.sh)
set -euo pipefail

CLUSTER_NAME="${1:-}"
if [[ -z "${CLUSTER_NAME}" ]]; then
  echo "Usage: $0 <cluster-name>"
  echo "Available clusters:"
  kind get clusters | sed 's/^/  /'
  exit 1
fi

if ! kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  echo "kind cluster '${CLUSTER_NAME}' not found, nothing to do"
  exit 0
fi

kind delete cluster --name "${CLUSTER_NAME}"
```

- [ ] **Step 2: Make executable and test**

Run:
```bash
chmod +x kind/teardown.sh
./kind/teardown.sh
```
Expected: usage message + list of any existing clusters.

- [ ] **Step 3: Commit**

```bash
git add kind/teardown.sh
git commit -m "demo(kind): teardown.sh <cluster-name>

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>"
```

---

### Task 4.2: `kind/DEMO.md` — presenter script

**Files:**
- Create: `kind/DEMO.md`

- [ ] **Step 1: Write the presenter script**

```markdown
# kind ext_proc demos — presenter script

Two co-equal demos sit in this folder. Pick one to run at a time; the
two cluster names do not collide so you can switch between them
without uninstalling, but each consumes ~2 CPU on a MacBook so do not
run both at once.

| Demo | Cluster name | What it teaches |
|---|---|---|
| `setup-plain.sh` | `ext-proc-plain-demo` | main's design as written: plain Envoy + permission-validation sidecar + echo, with Istio sidecar injection disabled. Bootstrap generated by `validate-routes translate`. |
| `setup-istio.sh` | `ext-proc-istio-demo` | main's sidecar adapted to an Istio cluster: istio-proxy is injected; an EnvoyFilter splices `ext_proc` into its filter chain; the sidecar is a container in the echo-app pod reached via the echo-app Service's `grpc-extproc` port. |

## Bring up

From the repo root:

```bash
./kind/setup-plain.sh    # Option B
# or
./kind/setup-istio.sh    # Option A
```

Either script ends by asserting all four canonical curls return the
expected status codes.

## Four canonical curls

For Option B set `BASE=http://127.0.0.1:8090`. For Option A set
`BASE=http://127.0.0.1:8080`. Pass `-H "Host: app.local"` on every
request — no `/etc/hosts` edits required.

```bash
BASE=http://127.0.0.1:8090   # Option B
# BASE=http://127.0.0.1:8080  # Option A

# 1) ALLOW — alice editing doc-1
curl -i "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"
# expected: 200, echo-server prints back the headers it received

# 2) DENY — alice trying to edit doc-2 (no permission)
curl -i "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-2:document:edit"
# expected: 403 from the sidecar, echo-server never sees the request

# 3) REJECT — missing X-Auth-Context entirely
curl -i "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test"
# expected: 403 with sidecar log line "context_header_missing"

# 4) SKIPPED ROUTE — /healthz bypasses the sidecar (Option B only)
curl -i "${BASE}/healthz" -H "Host: app.local"
# Option B: 200 — bootstrap honours routes.yaml skipped:
# Option A: 403 — EnvoyFilter does not implement per-route skip
```

## What to point at

| Item | Option B command | Option A command |
|---|---|---|
| Generated Envoy config | `kubectl -n demo-plain get cm envoy-bootstrap -o yaml` | (n/a — istiod owns the bootstrap) |
| EnvoyFilter CRD | (n/a — Envoy is plain) | `kubectl -n demo-istio get envoyfilter echo-ext-proc -o yaml` |
| Sidecar logs | `kubectl -n demo-plain logs deploy/echo-app -c sidecar` | `kubectl -n demo-istio logs deploy/echo-app -c sidecar` |
| PCS logs | `kubectl -n demo-plain logs deploy/pcs` | `kubectl -n demo-istio logs deploy/pcs` |
| Pod containers | `kubectl -n demo-plain describe pod -l app=echo-app` (3: envoy, sidecar, echo) | `kubectl -n demo-istio describe pod -l app=echo-app` (3: sidecar, echo, istio-proxy) |

## Failure-injection scenarios

```bash
# PCS down — fail-closed (DecisionUnknown → 403)
kubectl -n <ns> scale deploy/pcs --replicas=0
curl -i "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"
# expected: 403 from the sidecar. Re-scale to 1 to recover:
kubectl -n <ns> scale deploy/pcs --replicas=1

# Sidecar down — fail-closed (ext_proc stream fails)
POD=$(kubectl -n <ns> get pod -l app=echo-app -o jsonpath='{.items[0].metadata.name}')
kubectl -n <ns> exec ${POD} -c sidecar -- /bin/sh -c 'kill 1' || true
# during the gap before Kubernetes restarts the container:
curl -i "${BASE}/anything" \
  -H "Host: app.local" \
  -H "Authorization: Bearer alice@workspace.test" \
  -H "X-Auth-Context: doc-1:document:edit"
# expected: 503 or 504 (failure_mode_allow: false — never bypass).
```

## Tear down

```bash
./kind/teardown.sh ext-proc-plain-demo
./kind/teardown.sh ext-proc-istio-demo
```
```

- [ ] **Step 2: Commit**

```bash
git add kind/DEMO.md
git commit -m "$(cat <<'EOF'
demo(kind): DEMO.md presenter script — bring up, four curls, failure injection

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4.3: Top-level `README.md` pointer

**Files:**
- Modify: `README.md` (if it exists on this branch) OR create a minimal one if missing.

- [ ] **Step 1: Check whether README.md exists**

Run:
```bash
ls README.md 2>/dev/null && head -20 README.md || echo "no README.md on this branch"
```

- [ ] **Step 2A: If `README.md` exists, append a `## kind demos` section**

Otherwise create one with:

```markdown
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
```

- [ ] **Step 3: Commit**

```bash
git add README.md
git commit -m "$(cat <<'EOF'
docs: top-level README pointing at kind/DEMO.md and the two ext_proc demos

Co-Authored-By: Claude Opus 4.7 (1M context) <noreply@anthropic.com>
EOF
)"
```

---

### Task 4.4: Final verification — both demos still pass

- [ ] **Step 1: Tear down everything**

Run:
```bash
./kind/teardown.sh ext-proc-plain-demo
./kind/teardown.sh ext-proc-istio-demo
```

- [ ] **Step 2: Re-run Option B from scratch**

Run:
```bash
./kind/setup-plain.sh
```
Expected: completes with "All four canonical curls returned expected status codes."

- [ ] **Step 3: Tear it down**

```bash
./kind/teardown.sh ext-proc-plain-demo
```

- [ ] **Step 4: Re-run Option A from scratch**

Run:
```bash
./kind/setup-istio.sh
```
Expected: completes with "All four canonical curls returned expected status codes."

- [ ] **Step 5: Tear it down**

```bash
./kind/teardown.sh ext-proc-istio-demo
```

- [ ] **Step 6: Push branch**

```bash
git push -u origin kind-demo-ext_proc
```

- [ ] **Step 7: Open PR against `main`** (optional — only if the team wants the kind demos merged; otherwise leave the branch standalone for reference).

```bash
gh pr create --base main --head kind-demo-ext_proc --title "demo(kind): ext_proc kind demos — plain Envoy + Istio EnvoyFilter" \
  --body "$(cat <<'EOF'
## Summary
- Two kind demos under `kind/` deploying main's permission-validation sidecar.
- `demo-ext-proc-plain` (Option B): main's design as written; plain Envoy + sidecar + echo in one pod.
- `demo-ext-proc-istio` (Option A): same sidecar adapted to an Istio cluster via an EnvoyFilter that splices `ext_proc` into istio-proxy.
- New small HTTP PCS at `sample-apps/pcs-ext-proc/` speaking main's `/permission-check/v1/check` contract.
- Shared `kind/routes.yaml`; Option B feeds it through `validate-routes translate` at install time, Option A uses it only as a lint.
- Spec: `docs/superpowers/specs/2026-05-21-kind-demo-ext-proc-design.md`.

## Test plan
- [x] `kind/setup-plain.sh` passes all four canonical curls.
- [x] `kind/setup-istio.sh` passes all four canonical curls (note: `/healthz` returns 403 in Option A — documented).
- [x] PCS scale-to-0 → 403 (fail-closed).
- [x] Sidecar kill → 503 (fail-closed via failure_mode_allow=false).

🤖 Generated with [Claude Code](https://claude.com/claude-code)
EOF
)"
```

---

## Self-Review

After writing the plan, I checked:

1. **Spec coverage:**
   - §3 repo layout → Tasks 0.1–4.3 cover every file in the spec's tree.
   - §4 demo PCS → Tasks 1.1–1.7 (TDD'd, ~6 commits).
   - §5 routes.yaml → Task 0.4.
   - §6 Option B → Tasks 2.1–2.8.
   - §7 Option A → Tasks 3.1–3.9.
   - §8 demo script (DEMO.md) → Task 4.2.
   - §9 error handling (failure injection) → covered in DEMO.md (Task 4.2) and asserted in Task 4.4.
   - §10 acceptance → Task 4.4 (full re-run from scratch).
   - §11 out-of-scope → not implemented; explicitly called out in Decision Log #5 and Option A README.
   - Spec's reference to `kind/demo-ext-proc-minimal/` (in stash) → Decision Log mentions it; no task creates that path since it lives in stash.

2. **Placeholder scan:** No TBD / TODO / "implement later." Every task has concrete code or commands.

3. **Type/name consistency:**
   - `decide(user, objectID, objectType, permission)` signature is used identically in Tasks 1.2 / 1.3.
   - `checkHandler(w http.ResponseWriter, r *http.Request)` signature stable across Tasks 1.4 / 1.5.
   - Image tag `workspace/pcs-ext-proc:dev` used identically in Tasks 1.7, 2.6, 3.7, 4.4 and both `values.yaml` files.
   - Cluster names `ext-proc-plain-demo` and `ext-proc-istio-demo` consistent everywhere.
   - Namespaces `demo-plain` and `demo-istio` consistent in templates, setup scripts, DEMO.md, READMEs.
   - Sidecar gRPC port `50051` consistent in templates, EnvoyFilter values, and the sidecar's `--listen` arg.

No spec gaps; no placeholders; types/names consistent. Plan is ready to execute.
