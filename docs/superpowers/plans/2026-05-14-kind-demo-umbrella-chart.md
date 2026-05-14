# Kind Demo Umbrella Helm Chart Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Wrap the ext-authz kind demo in a `kind/demo/` umbrella Helm chart with a single `values.yaml` as the source of truth for all image references and configuration, so swapping to a private registry requires editing only one file.

**Architecture:** An umbrella chart (`kind/demo/`) vendors `base` and `istiod` as subchart dependencies (both need `istio-system` namespace, which matches the umbrella's install namespace). The `gateway` chart cannot be a subchart dependency because it must install into app namespaces (`documents`, `wiki`) — so gateways are still installed separately by `setup.sh` but their values files live in `kind/demo/` and reference the same values structure. All app Kubernetes manifests move from `kind/manifests/` into `kind/demo/templates/`, with every image reference and configuration value pulled from `values.yaml`.

**Tech Stack:** Helm 3, Kubernetes 1.x, Istio 1.24.2, Go 1.25 apps (echo-server, pcs, dashboard-client), kind

---

## File Map

### Files to CREATE

| File | Purpose |
|------|---------|
| `kind/demo/Chart.yaml` | Umbrella chart definition; declares `base` and `istiod` as subchart dependencies |
| `kind/demo/values.yaml` | Single source of truth: image registry/tag/pullPolicy, resource sizes, ext-authz config, gateway config, istiod subchart pass-through |
| `kind/demo/README.md` | Explains registry-swap workflow; documents gateway namespace limitation |
| `kind/demo/charts/base-1.24.2.tgz` | Copied from `kind/charts/base-1.24.2.tgz` (Helm requires deps in `charts/`) |
| `kind/demo/charts/istiod-1.24.2.tgz` | Copied from `kind/charts/istiod-1.24.2.tgz` |
| `kind/demo/templates/_helpers.tpl` | Shared Helm helpers: `ext-authz-demo.extAuthzPatch` (EnvoyFilter patch block) and `ext-authz-demo.optInLabel` |
| `kind/demo/templates/namespace-documents.yaml` | Namespace `documents` with `istio-injection: enabled` |
| `kind/demo/templates/namespace-wiki.yaml` | Namespace `wiki` with `istio-injection: enabled` |
| `kind/demo/templates/pcs.yaml` | PCS Deployment + Service (combined, `---` separator) in `documents` namespace |
| `kind/demo/templates/documents-api.yaml` | documents-api Deployment + Service in `documents` namespace |
| `kind/demo/templates/documents-search.yaml` | documents-search Deployment + Service in `documents` namespace |
| `kind/demo/templates/documents-ext-authz.yaml` | documents EnvoyFilter |
| `kind/demo/templates/documents-gateway.yaml` | documents Gateway + VirtualService (combined) |
| `kind/demo/templates/dashboard-client.yaml` | dashboard-client Deployment in `documents` namespace |
| `kind/demo/templates/wiki-api.yaml` | wiki-api Deployment + Service in `wiki` namespace |
| `kind/demo/templates/wiki-ext-authz.yaml` | wiki EnvoyFilter (cross-ns copy, same PCS host) |
| `kind/demo/templates/wiki-gateway.yaml` | wiki Gateway + VirtualService (combined) |
| `kind/demo/values-gateway-documents.yaml` | Flat values file for `helm upgrade --install documents-ingressgateway` (workaround for namespace limitation) |
| `kind/demo/values-gateway-wiki.yaml` | Flat values file for `helm upgrade --install wiki-ingressgateway` |

### Files to MODIFY

| File | Change |
|------|--------|
| `kind/setup.sh` | Phase B: replace `istio-base` + `istiod` separate installs + all `kubectl apply` commands with `helm dependency update kind/demo` + `helm upgrade --install demo kind/demo -n istio-system --create-namespace --wait`; Phase C: change gateway `-f` flags to reference `kind/demo/values-gateway-*.yaml` |
| `kind/README.md` | Add section about umbrella chart and registry-swap workflow |

### Files to DELETE (after successful verification)

| File | Reason |
|------|--------|
| `kind/manifests/documents/` (entire dir) | Superseded by `kind/demo/templates/` |
| `kind/manifests/wiki/` (entire dir) | Superseded by `kind/demo/templates/` |
| `kind/chart-values/` (entire dir) | Content folded into `kind/demo/values.yaml` and `kind/demo/values-gateway-*.yaml` |

---

## Task 1: Create Chart.yaml and values.yaml

**Files:**
- Create: `kind/demo/Chart.yaml`
- Create: `kind/demo/values.yaml`

- [ ] **Step 1: Create the Chart.yaml**

```bash
mkdir -p /Users/joe/ashwini-repos/workspace/kind/demo/charts
mkdir -p /Users/joe/ashwini-repos/workspace/kind/demo/templates
```

Create `kind/demo/Chart.yaml` with this exact content:

```yaml
apiVersion: v2
name: ext-authz-demo
description: Umbrella chart for the ext-authz kind demo (per-namespace EnvoyFilter pattern).
type: application
version: 0.1.0
appVersion: "1.24.2"
dependencies:
- name: base
  version: 1.24.2
  repository: "file://charts"
- name: istiod
  version: 1.24.2
  repository: "file://charts"
```

- [ ] **Step 2: Create values.yaml**

Create `kind/demo/values.yaml` with this exact content:

```yaml
# ===========================================================================
# IMAGE OVERRIDES — swap these for a private registry (e.g. company mirror).
# To use a private registry, change repository to e.g.
# "my-company-registry.local/workspace/echo-server" and update tag.
# ===========================================================================
images:
  echoServer:
    repository: workspace/echo-server
    tag: dev
    pullPolicy: IfNotPresent
  pcs:
    repository: workspace/pcs
    tag: dev
    pullPolicy: IfNotPresent
  dashboardClient:
    repository: workspace/dashboard-client
    tag: dev
    pullPolicy: IfNotPresent
  # Istio component images. Used both by the umbrella's subchart deps
  # (base, istiod) and by the gateway installs performed separately by setup.sh.
  # NOTE: images.istio.hub/tag is intentionally duplicated as istiod.global.hub/tag
  # below so the subchart receives the value via Helm's dependency mechanism, while
  # setup.sh can also read images.istio.* for gateway installs.
  istio:
    hub: docker.io/istio
    tag: 1.24.2

# ===========================================================================
# RESOURCE FOOTPRINT — sized for a MacBook (every value can be raised in prod).
# ===========================================================================
resources:
  app:
    requests:
      cpu: 10m
      memory: 32Mi
    limits:
      cpu: 100m
      memory: 64Mi
  caller:
    requests:
      cpu: 5m
      memory: 16Mi
    limits:
      cpu: 50m
      memory: 32Mi
  ingressgateway:
    requests:
      cpu: 20m
      memory: 64Mi
    limits:
      cpu: 200m
      memory: 128Mi
  sidecar:
    proxyCPU: "10m"
    proxyMemory: "64Mi"
    proxyCPULimit: "200m"
    proxyMemoryLimit: "128Mi"

# ===========================================================================
# EXT-AUTHZ WIRING — point both EnvoyFilters at this PCS service.
# ===========================================================================
extAuthz:
  optInLabel:
    key: workspace.io/ext-authz
    value: enabled
  pcsService:
    host: pcs.documents.svc.cluster.local
    port: 8080
    pathPrefix: /check

# ===========================================================================
# INGRESS GATEWAYS — installed by setup.sh, not by this chart (subchart-namespace
# limitation; see kind/demo/README.md). The values are sourced from here so
# setup.sh has a single configuration file.
# ===========================================================================
gateways:
  documents:
    namespace: documents
    name: documents-ingressgateway
    nodePort: 30080
    host: documents.local
  wiki:
    namespace: wiki
    name: wiki-ingressgateway
    nodePort: 30081
    host: wiki.local

# ===========================================================================
# SUBCHART VALUES (passed to base / istiod via Helm dependency mechanism).
# ===========================================================================
base:
  defaultRevision: default

istiod:
  global:
    hub: docker.io/istio
    tag: 1.24.2
  pilot:
    resources:
      requests:
        cpu: 50m
        memory: 128Mi
      limits:
        cpu: 1000m
        memory: 512Mi
    autoscaleEnabled: false
    replicaCount: 1
```

- [ ] **Step 3: Verify YAML syntax**

```bash
cd /Users/joe/ashwini-repos/workspace
helm lint kind/demo --strict 2>&1 || true
# Expected: will fail on missing templates/charts — that's fine at this stage.
# What we're checking: no YAML parse errors in Chart.yaml or values.yaml.
python3 -c "import yaml; yaml.safe_load(open('kind/demo/Chart.yaml')); print('Chart.yaml OK')"
python3 -c "import yaml; yaml.safe_load(open('kind/demo/values.yaml')); print('values.yaml OK')"
```

Expected output:
```
Chart.yaml OK
values.yaml OK
```

---

## Task 2: Copy vendored chart tarballs and create gateway values files

**Files:**
- Create: `kind/demo/charts/base-1.24.2.tgz` (copy)
- Create: `kind/demo/charts/istiod-1.24.2.tgz` (copy)
- Create: `kind/demo/values-gateway-documents.yaml`
- Create: `kind/demo/values-gateway-wiki.yaml`

- [ ] **Step 1: Copy the tarballs**

```bash
cp /Users/joe/ashwini-repos/workspace/kind/charts/base-1.24.2.tgz \
   /Users/joe/ashwini-repos/workspace/kind/demo/charts/base-1.24.2.tgz

cp /Users/joe/ashwini-repos/workspace/kind/charts/istiod-1.24.2.tgz \
   /Users/joe/ashwini-repos/workspace/kind/demo/charts/istiod-1.24.2.tgz
```

Do NOT copy `gateway-1.24.2.tgz` here — gateways are not subchart deps (namespace limitation).

- [ ] **Step 2: Create kind/demo/values-gateway-documents.yaml**

This file exists as a workaround for Helm's subchart-namespace limitation — gateways must install into app namespaces, not `istio-system`.

```yaml
# values-gateway-documents.yaml
# Per-gateway values file for the documents-ingressgateway Helm release.
# This file exists because the gateway chart must be installed into the
# "documents" namespace, not "istio-system" where the umbrella chart lives.
# See kind/demo/README.md for the full explanation.
#
# Install command:
#   helm upgrade --install documents-ingressgateway kind/charts/gateway-1.24.2.tgz \
#     -n documents --wait --skip-schema-validation \
#     -f kind/demo/values-gateway-documents.yaml

name: documents-ingressgateway
labels:
  istio: documents-ingressgateway

service:
  type: NodePort
  ports:
  - name: status-port
    port: 15021
    targetPort: 15021
  - name: http2
    port: 80
    targetPort: 80
    nodePort: 30080
  - name: https
    port: 443
    targetPort: 443
    nodePort: 30443

resources:
  requests:
    cpu: 20m
    memory: 64Mi
  limits:
    cpu: 200m
    memory: 128Mi

autoscaling:
  enabled: false
```

- [ ] **Step 3: Create kind/demo/values-gateway-wiki.yaml**

```yaml
# values-gateway-wiki.yaml
# Per-gateway values file for the wiki-ingressgateway Helm release.
# This file exists because the gateway chart must be installed into the
# "wiki" namespace, not "istio-system" where the umbrella chart lives.
# See kind/demo/README.md for the full explanation.
#
# Install command:
#   helm upgrade --install wiki-ingressgateway kind/charts/gateway-1.24.2.tgz \
#     -n wiki --wait --skip-schema-validation \
#     -f kind/demo/values-gateway-wiki.yaml

name: wiki-ingressgateway
labels:
  istio: wiki-ingressgateway

service:
  type: NodePort
  ports:
  - name: status-port
    port: 15021
    targetPort: 15021
  - name: http2
    port: 80
    targetPort: 80
    nodePort: 30081
  - name: https
    port: 443
    targetPort: 443

resources:
  requests:
    cpu: 20m
    memory: 64Mi
  limits:
    cpu: 200m
    memory: 128Mi

autoscaling:
  enabled: false
```

- [ ] **Step 4: Verify helm can resolve the deps**

```bash
cd /Users/joe/ashwini-repos/workspace
helm dependency list kind/demo
```

Expected output (approximately):
```
NAME    VERSION REPOSITORY      STATUS
base    1.24.2  file://charts   ok
istiod  1.24.2  file://charts   ok
```

If STATUS is `missing`, run `helm dependency update kind/demo` — but since we manually copied the tarballs, it should show `ok` already (Helm reads from `charts/` directory).

---

## Task 3: Create _helpers.tpl

**Files:**
- Create: `kind/demo/templates/_helpers.tpl`

The helper defines a reusable EnvoyFilter patch block so both `documents-ext-authz.yaml` and `wiki-ext-authz.yaml` can DRY out.

- [ ] **Step 1: Create the helpers file**

Create `kind/demo/templates/_helpers.tpl` with this exact content:

```
{{/*
ext-authz-demo.extAuthzPatch — the shared EnvoyFilter configPatches block.
Both documents-ext-authz.yaml and wiki-ext-authz.yaml include this via
  {{- include "ext-authz-demo.extAuthzPatch" . | nindent 2 }}
*/}}
{{- define "ext-authz-demo.extAuthzPatch" -}}
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
      name: envoy.filters.http.ext_authz
      typed_config:
        "@type": type.googleapis.com/envoy.extensions.filters.http.ext_authz.v3.ExtAuthz
        transport_api_version: V3
        http_service:
          server_uri:
            uri: "http://{{ .Values.extAuthz.pcsService.host }}:{{ .Values.extAuthz.pcsService.port }}"
            cluster: "outbound|{{ .Values.extAuthz.pcsService.port }}||{{ .Values.extAuthz.pcsService.host }}"
            timeout: 1s
          path_prefix: {{ .Values.extAuthz.pcsService.pathPrefix | quote }}
          authorization_request:
            allowed_headers:
              patterns:
              - exact: x-workspace-user-id
              - exact: authorization
        failure_mode_allow: false
{{- end }}

{{/*
ext-authz-demo.optInLabel — the workloadSelector labels block for EnvoyFilter.
*/}}
{{- define "ext-authz-demo.optInLabel" -}}
{{ .Values.extAuthz.optInLabel.key }}: {{ .Values.extAuthz.optInLabel.value | quote }}
{{- end }}

{{/*
ext-authz-demo.sidecarAnnotations — sidecar resource annotation block.
Used in Pod template metadata.annotations.
*/}}
{{- define "ext-authz-demo.sidecarAnnotations" -}}
sidecar.istio.io/proxyCPU: {{ .Values.resources.sidecar.proxyCPU | quote }}
sidecar.istio.io/proxyMemory: {{ .Values.resources.sidecar.proxyMemory | quote }}
sidecar.istio.io/proxyCPULimit: {{ .Values.resources.sidecar.proxyCPULimit | quote }}
sidecar.istio.io/proxyMemoryLimit: {{ .Values.resources.sidecar.proxyMemoryLimit | quote }}
{{- end }}
```

---

## Task 4: Create namespace templates

**Files:**
- Create: `kind/demo/templates/namespace-documents.yaml`
- Create: `kind/demo/templates/namespace-wiki.yaml`

- [ ] **Step 1: Create namespace-documents.yaml**

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: documents
  labels:
    istio-injection: enabled
```

- [ ] **Step 2: Create namespace-wiki.yaml**

```yaml
apiVersion: v1
kind: Namespace
metadata:
  name: wiki
  labels:
    istio-injection: enabled
```

---

## Task 5: Create pcs.yaml template

**Files:**
- Create: `kind/demo/templates/pcs.yaml`

PCS is the ext-authz decision service. It deliberately has NO `workspace.io/ext-authz: enabled` label (it would be a circular dependency). It gets sidecar annotations for resource sizing.

- [ ] **Step 1: Create pcs.yaml**

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: pcs
  namespace: documents
  labels:
    app: pcs
spec:
  replicas: 1
  selector:
    matchLabels:
      app: pcs
  template:
    metadata:
      labels:
        app: pcs
        # PCS is the decision service itself; deliberately NO workspace.io/ext-authz label.
      annotations:
        {{- include "ext-authz-demo.sidecarAnnotations" . | nindent 8 }}
    spec:
      containers:
      - name: pcs
        image: "{{ .Values.images.pcs.repository }}:{{ .Values.images.pcs.tag }}"
        imagePullPolicy: {{ .Values.images.pcs.pullPolicy }}
        ports:
        - containerPort: 8080
        env:
        - name: PORT
          value: "8080"
        resources:
          requests:
            cpu: {{ .Values.resources.app.requests.cpu }}
            memory: {{ .Values.resources.app.requests.memory }}
          limits:
            cpu: {{ .Values.resources.app.limits.cpu }}
            memory: {{ .Values.resources.app.limits.memory }}
        readinessProbe:
          tcpSocket:
            port: 8080
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: pcs
  namespace: documents
  labels:
    app: pcs
spec:
  type: ClusterIP
  selector:
    app: pcs
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

---

## Task 6: Create documents-api.yaml template

**Files:**
- Create: `kind/demo/templates/documents-api.yaml`

documents-api opts in to ext-authz via the `workspace.io/ext-authz: enabled` label.

- [ ] **Step 1: Create documents-api.yaml**

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: documents-api
  namespace: documents
  labels:
    app: documents-api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: documents-api
  template:
    metadata:
      labels:
        app: documents-api
        {{- include "ext-authz-demo.optInLabel" . | nindent 8 }}
      annotations:
        {{- include "ext-authz-demo.sidecarAnnotations" . | nindent 8 }}
    spec:
      containers:
      - name: documents-api
        image: "{{ .Values.images.echoServer.repository }}:{{ .Values.images.echoServer.tag }}"
        imagePullPolicy: {{ .Values.images.echoServer.pullPolicy }}
        ports:
        - containerPort: 8080
        env:
        - name: PORT
          value: "8080"
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        resources:
          requests:
            cpu: {{ .Values.resources.app.requests.cpu }}
            memory: {{ .Values.resources.app.requests.memory }}
          limits:
            cpu: {{ .Values.resources.app.limits.cpu }}
            memory: {{ .Values.resources.app.limits.memory }}
        readinessProbe:
          tcpSocket:
            port: 8080
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: documents-api
  namespace: documents
  labels:
    app: documents-api
spec:
  type: ClusterIP
  selector:
    app: documents-api
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

---

## Task 7: Create documents-search.yaml template

**Files:**
- Create: `kind/demo/templates/documents-search.yaml`

documents-search opts in to ext-authz exactly like documents-api.

- [ ] **Step 1: Create documents-search.yaml**

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: documents-search
  namespace: documents
  labels:
    app: documents-search
spec:
  replicas: 1
  selector:
    matchLabels:
      app: documents-search
  template:
    metadata:
      labels:
        app: documents-search
        {{- include "ext-authz-demo.optInLabel" . | nindent 8 }}
      annotations:
        {{- include "ext-authz-demo.sidecarAnnotations" . | nindent 8 }}
    spec:
      containers:
      - name: documents-search
        image: "{{ .Values.images.echoServer.repository }}:{{ .Values.images.echoServer.tag }}"
        imagePullPolicy: {{ .Values.images.echoServer.pullPolicy }}
        ports:
        - containerPort: 8080
        env:
        - name: PORT
          value: "8080"
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        resources:
          requests:
            cpu: {{ .Values.resources.app.requests.cpu }}
            memory: {{ .Values.resources.app.requests.memory }}
          limits:
            cpu: {{ .Values.resources.app.limits.cpu }}
            memory: {{ .Values.resources.app.limits.memory }}
        readinessProbe:
          tcpSocket:
            port: 8080
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: documents-search
  namespace: documents
  labels:
    app: documents-search
spec:
  type: ClusterIP
  selector:
    app: documents-search
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

---

## Task 8: Create documents-ext-authz.yaml template

**Files:**
- Create: `kind/demo/templates/documents-ext-authz.yaml`

Uses the `extAuthzPatch` and `optInLabel` helpers from `_helpers.tpl`. The `subFilter.name: envoy.filters.http.router` match MUST be preserved — do not omit it.

- [ ] **Step 1: Create documents-ext-authz.yaml**

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: documents-ext-authz
  namespace: documents
spec:
  workloadSelector:
    labels:
      {{- include "ext-authz-demo.optInLabel" . | nindent 6 }}
  {{- include "ext-authz-demo.extAuthzPatch" . | nindent 2 }}
```

---

## Task 9: Create documents-gateway.yaml template

**Files:**
- Create: `kind/demo/templates/documents-gateway.yaml`

Gateway + VirtualService combined in one file.

- [ ] **Step 1: Create documents-gateway.yaml**

```yaml
---
apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: documents-gateway
  namespace: documents
spec:
  selector:
    istio: documents-ingressgateway
  servers:
  - port:
      number: 80
      name: http
      protocol: HTTP
    hosts:
    - {{ .Values.gateways.documents.host }}
---
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: documents-vs
  namespace: documents
spec:
  hosts:
  - {{ .Values.gateways.documents.host }}
  gateways:
  - documents-gateway
  http:
  - route:
    - destination:
        host: documents-api.documents.svc.cluster.local
        port:
          number: 8080
```

---

## Task 10: Create dashboard-client.yaml template

**Files:**
- Create: `kind/demo/templates/dashboard-client.yaml`

dashboard-client does NOT opt in to ext-authz (it's the caller, not the protected service). It uses cluster-internal DNS for API URLs (simpler than gateway hostnames).

- [ ] **Step 1: Create dashboard-client.yaml**

```yaml
apiVersion: apps/v1
kind: Deployment
metadata:
  name: dashboard-client
  namespace: documents
  labels:
    app: dashboard-client
spec:
  replicas: 1
  selector:
    matchLabels:
      app: dashboard-client
  template:
    metadata:
      labels:
        app: dashboard-client
      annotations:
        {{- include "ext-authz-demo.sidecarAnnotations" . | nindent 8 }}
    spec:
      containers:
      - name: dashboard-client
        image: "{{ .Values.images.dashboardClient.repository }}:{{ .Values.images.dashboardClient.tag }}"
        imagePullPolicy: {{ .Values.images.dashboardClient.pullPolicy }}
        env:
        - name: DOCUMENTS_API_URL
          value: "http://documents-api.documents.svc.cluster.local:8080/hello"
        - name: DOCUMENTS_SEARCH_URL
          value: "http://documents-search.documents.svc.cluster.local:8080/hello"
        - name: WIKI_API_URL
          value: "http://wiki-api.wiki.svc.cluster.local:8080/hello"
        - name: ALLOW_USER
          value: "alice@workspace.test"
        - name: DENY_USER
          value: "mallory@workspace.test"
        resources:
          requests:
            cpu: {{ .Values.resources.caller.requests.cpu }}
            memory: {{ .Values.resources.caller.requests.memory }}
          limits:
            cpu: {{ .Values.resources.caller.limits.cpu }}
            memory: {{ .Values.resources.caller.limits.memory }}
```

---

## Task 11: Create wiki-api.yaml template

**Files:**
- Create: `kind/demo/templates/wiki-api.yaml`

wiki-api opts in to ext-authz, uses echo-server image.

- [ ] **Step 1: Create wiki-api.yaml**

```yaml
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: wiki-api
  namespace: wiki
  labels:
    app: wiki-api
spec:
  replicas: 1
  selector:
    matchLabels:
      app: wiki-api
  template:
    metadata:
      labels:
        app: wiki-api
        {{- include "ext-authz-demo.optInLabel" . | nindent 8 }}
      annotations:
        {{- include "ext-authz-demo.sidecarAnnotations" . | nindent 8 }}
    spec:
      containers:
      - name: wiki-api
        image: "{{ .Values.images.echoServer.repository }}:{{ .Values.images.echoServer.tag }}"
        imagePullPolicy: {{ .Values.images.echoServer.pullPolicy }}
        ports:
        - containerPort: 8080
        env:
        - name: PORT
          value: "8080"
        - name: POD_NAME
          valueFrom:
            fieldRef:
              fieldPath: metadata.name
        resources:
          requests:
            cpu: {{ .Values.resources.app.requests.cpu }}
            memory: {{ .Values.resources.app.requests.memory }}
          limits:
            cpu: {{ .Values.resources.app.limits.cpu }}
            memory: {{ .Values.resources.app.limits.memory }}
        readinessProbe:
          tcpSocket:
            port: 8080
          periodSeconds: 5
---
apiVersion: v1
kind: Service
metadata:
  name: wiki-api
  namespace: wiki
  labels:
    app: wiki-api
spec:
  type: ClusterIP
  selector:
    app: wiki-api
  ports:
  - name: http
    port: 8080
    targetPort: 8080
```

---

## Task 12: Create wiki-ext-authz.yaml template

**Files:**
- Create: `kind/demo/templates/wiki-ext-authz.yaml`

This is the cross-namespace copy of the EnvoyFilter. It points to the same PCS service in `documents` namespace. The `subFilter.name: envoy.filters.http.router` match MUST be preserved.

- [ ] **Step 1: Create wiki-ext-authz.yaml**

```yaml
apiVersion: networking.istio.io/v1alpha3
kind: EnvoyFilter
metadata:
  name: wiki-ext-authz
  namespace: wiki
spec:
  workloadSelector:
    labels:
      {{- include "ext-authz-demo.optInLabel" . | nindent 6 }}
  {{- include "ext-authz-demo.extAuthzPatch" . | nindent 2 }}
```

---

## Task 13: Create wiki-gateway.yaml template

**Files:**
- Create: `kind/demo/templates/wiki-gateway.yaml`

- [ ] **Step 1: Create wiki-gateway.yaml**

```yaml
---
apiVersion: networking.istio.io/v1
kind: Gateway
metadata:
  name: wiki-gateway
  namespace: wiki
spec:
  selector:
    istio: wiki-ingressgateway
  servers:
  - port:
      number: 80
      name: http
      protocol: HTTP
    hosts:
    - {{ .Values.gateways.wiki.host }}
---
apiVersion: networking.istio.io/v1
kind: VirtualService
metadata:
  name: wiki-vs
  namespace: wiki
spec:
  hosts:
  - {{ .Values.gateways.wiki.host }}
  gateways:
  - wiki-gateway
  http:
  - route:
    - destination:
        host: wiki-api.wiki.svc.cluster.local
        port:
          number: 8080
```

---

## Task 14: Create kind/demo/README.md

**Files:**
- Create: `kind/demo/README.md`

- [ ] **Step 1: Create README.md**

```markdown
# ext-authz-demo Umbrella Helm Chart

This chart wraps the entire ext-authz kind demo into a single Helm release.
One `helm upgrade --install` installs Istio base, istiod, all app namespaces,
and all application workloads.

## Swapping to a private registry

Edit **only** `kind/demo/values.yaml`. Change the `images.*` section:

```yaml
# Before (local kind images)
images:
  echoServer:
    repository: workspace/echo-server
    tag: dev

# After (private registry)
images:
  echoServer:
    repository: my-company-registry.local/workspace/echo-server
    tag: 1.2.3
```

For Istio images, also update `images.istio.hub` and `istiod.global.hub`
(both fields, kept in sync — see the NOTE in values.yaml):

```yaml
images:
  istio:
    hub: my-company-registry.local/istio
    tag: 1.24.2

istiod:
  global:
    hub: my-company-registry.local/istio
    tag: 1.24.2
```

## Why gateways are NOT subchart dependencies

Helm subchart dependencies install into the **parent release's namespace**.
Since this chart installs into `istio-system`, any subchart dep would also
land in `istio-system`. The ingressgateways must live in the `documents` and
`wiki` namespaces so Istio's workload-scoped service discovery works correctly.

To work around this, `setup.sh` installs the two gateways as separate Helm
releases into their respective namespaces, using the values files in this
directory (`values-gateway-documents.yaml`, `values-gateway-wiki.yaml`).
Both files are kept here (not in `kind/chart-values/`) so this directory
remains the single configuration source.

## Chart structure

```
kind/demo/
├── Chart.yaml                       # umbrella; base + istiod subchart deps
├── values.yaml                      # CENTRAL config: images, resources, ext-authz, gateways
├── README.md                        # this file
├── values-gateway-documents.yaml    # documents-ingressgateway overrides (gateway workaround)
├── values-gateway-wiki.yaml         # wiki-ingressgateway overrides (gateway workaround)
├── charts/
│   ├── base-1.24.2.tgz              # vendored Istio base CRDs chart
│   └── istiod-1.24.2.tgz            # vendored Istio control-plane chart
└── templates/
    ├── _helpers.tpl                 # shared: extAuthzPatch, optInLabel, sidecarAnnotations
    ├── namespace-documents.yaml
    ├── namespace-wiki.yaml
    ├── pcs.yaml                     # PCS Deployment + Service
    ├── documents-api.yaml           # documents-api Deployment + Service
    ├── documents-search.yaml        # documents-search Deployment + Service
    ├── documents-ext-authz.yaml     # EnvoyFilter (documents ns)
    ├── documents-gateway.yaml       # Gateway + VirtualService
    ├── dashboard-client.yaml        # dashboard-client Deployment
    ├── wiki-api.yaml                # wiki-api Deployment + Service
    ├── wiki-ext-authz.yaml          # EnvoyFilter (wiki ns, cross-ns copy)
    └── wiki-gateway.yaml            # Gateway + VirtualService
```
```

---

## Task 15: Helm lint the completed chart

**Files:**
- No new files; validates everything created so far.

- [ ] **Step 1: Run helm lint**

```bash
cd /Users/joe/ashwini-repos/workspace
helm lint kind/demo --strict
```

Expected output:
```
==> Linting kind/demo
[INFO] Chart.yaml: icon is recommended

1 chart(s) linted, 0 chart(s) failed
```

The `icon is recommended` INFO is acceptable. Any ERROR or FAIL must be fixed before proceeding.

- [ ] **Step 2: Run helm template to preview rendered output**

```bash
cd /Users/joe/ashwini-repos/workspace
helm template demo kind/demo | head -100
```

Inspect the output. Verify that:
1. The `documents-ext-authz` EnvoyFilter contains `subFilter.name: envoy.filters.http.router` — confirm visually.
2. Image references show `workspace/echo-server:dev` (not empty or literal `.Values.*`).
3. Namespace resources appear at the top.

- [ ] **Step 3: Commit the umbrella chart structure**

```bash
cd /Users/joe/ashwini-repos/workspace
git add kind/demo/
git commit -m "feat(kind): add kind/demo umbrella Helm chart structure"
```

---

## Task 16: Rewrite setup.sh to use the umbrella chart

**Files:**
- Modify: `kind/setup.sh`

The new flow:
1. Phase A: kind cluster + build + load images (unchanged)
2. Phase B: `helm dependency update kind/demo` + single `helm upgrade --install demo kind/demo -n istio-system --create-namespace --wait`
3. Phase C: install two gateways using `kind/demo/values-gateway-*.yaml`
4. Verification banner (unchanged content)

- [ ] **Step 1: Read the existing setup.sh to understand all variable references**

Read `/Users/joe/ashwini-repos/workspace/kind/setup.sh` (already read above, included here for reference).

Key variables: `CLUSTER_NAME`, `ROOT`, `KIND_DIR`, `CHARTS`, `MANIFESTS`.

- [ ] **Step 2: Write the new setup.sh**

Replace the entire content of `kind/setup.sh` with:

```bash
#!/usr/bin/env bash
# One-shot bring-up for the ext-authz kind demo. Idempotent.
# Run from the repo root: ./kind/setup.sh
set -euo pipefail

CLUSTER_NAME="ext-authz-demo"
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
KIND_DIR="${ROOT}/kind"
CHARTS="${KIND_DIR}/charts"
DEMO="${KIND_DIR}/demo"

log() { printf "\n\033[1;32m▶ %s\033[0m\n" "$*"; }

# ─────────────────────────────────────────────────────────────────────────────
# Phase A — Cluster bootstrap + image build
# ─────────────────────────────────────────────────────────────────────────────

# 1. kind cluster
if kind get clusters | grep -qx "${CLUSTER_NAME}"; then
  log "kind cluster '${CLUSTER_NAME}' already exists, reusing"
else
  log "Creating kind cluster '${CLUSTER_NAME}'"
  kind create cluster --name "${CLUSTER_NAME}" --config "${KIND_DIR}/kind-config.yaml"
fi
kubectl config use-context "kind-${CLUSTER_NAME}"

# 2. Build and load local images
log "Building local images (echo-server, pcs, dashboard-client)"
(cd "${ROOT}/sample-apps/echo-server"      && docker build -t workspace/echo-server:dev      -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/pcs"              && docker build -t workspace/pcs:dev              -f deploy/Dockerfile .)
(cd "${ROOT}/sample-apps/dashboard-client" && docker build -t workspace/dashboard-client:dev -f deploy/Dockerfile .)

log "Loading images into kind"
kind load docker-image workspace/echo-server:dev      --name "${CLUSTER_NAME}"
kind load docker-image workspace/pcs:dev              --name "${CLUSTER_NAME}"
kind load docker-image workspace/dashboard-client:dev --name "${CLUSTER_NAME}"

# ─────────────────────────────────────────────────────────────────────────────
# Phase B — Umbrella chart install (Istio + all app workloads)
# ─────────────────────────────────────────────────────────────────────────────

log "Resolving umbrella chart dependencies"
helm dependency update "${DEMO}"

log "Installing ext-authz-demo umbrella chart (istio-base + istiod + all app workloads)"
helm upgrade --install demo "${DEMO}" \
  -n istio-system --create-namespace --wait \
  --timeout 300s

# ─────────────────────────────────────────────────────────────────────────────
# Phase C — Ingressgateways (separate installs due to namespace limitation)
# See kind/demo/README.md for why these cannot be subchart dependencies.
# ─────────────────────────────────────────────────────────────────────────────

log "Installing documents-ingressgateway (chart: gateway-1.24.2.tgz)"
helm upgrade --install documents-ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n documents --wait --skip-schema-validation \
  -f "${DEMO}/values-gateway-documents.yaml"

log "Installing wiki-ingressgateway (chart: gateway-1.24.2.tgz)"
helm upgrade --install wiki-ingressgateway "${CHARTS}/gateway-1.24.2.tgz" \
  -n wiki --wait --skip-schema-validation \
  -f "${DEMO}/values-gateway-wiki.yaml"

# ─────────────────────────────────────────────────────────────────────────────
# Verification banner
# ─────────────────────────────────────────────────────────────────────────────

cat <<EOF

─────────────────────────────────────────────────────────────
✓ kind ext-authz demo is up

Watch the dashboard-client cycle:
  kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f

Watch PCS decisions:
  kubectl -n documents logs deploy/pcs -c pcs -f

Curl from host (no /etc/hosts edit needed — use --resolve):
  curl --resolve documents.local:8080:127.0.0.1 \\
    -H "x-workspace-user-id: alice@workspace.test"   http://documents.local:8080/hello    # 200
  curl --resolve documents.local:8080:127.0.0.1 \\
    -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello    # 403
  curl --resolve wiki.local:8081:127.0.0.1 \\
    -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello         # 200
  curl --resolve wiki.local:8081:127.0.0.1 \\
    -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello         # 403

Alternative: add '127.0.0.1 documents.local wiki.local' to /etc/hosts (requires sudo).

EnvoyFilters in app namespaces (none in istio-system):
  kubectl get envoyfilter -A

Teardown:
  ${KIND_DIR}/teardown.sh
EOF
```

- [ ] **Step 3: Make setup.sh executable and verify syntax**

```bash
chmod +x /Users/joe/ashwini-repos/workspace/kind/setup.sh
bash -n /Users/joe/ashwini-repos/workspace/kind/setup.sh
```

Expected: no output (bash -n just checks syntax, exits 0 if clean).

- [ ] **Step 4: Commit setup.sh changes**

```bash
cd /Users/joe/ashwini-repos/workspace
git add kind/setup.sh
git commit -m "feat(kind): rewrite setup.sh to use kind/demo umbrella chart"
```

---

## Task 17: Update kind/README.md

**Files:**
- Modify: `kind/README.md`

- [ ] **Step 1: Read the current README.md**

File is at `/Users/joe/ashwini-repos/workspace/kind/README.md` (already read above).

- [ ] **Step 2: Add the umbrella chart section**

Replace the entire content of `kind/README.md` with:

```markdown
# Ext-Authz Kind Demo

A local kind cluster that demonstrates Envoy `ext_authz` wiring via per-namespace `EnvoyFilter` resources.

**Full design:** see [`../docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md`](../docs/superpowers/specs/2026-05-14-ext-authz-kind-demo-design.md).

## What this builds

| Namespace | What's inside |
|---|---|
| `istio-system` | istio-base + istiod (Stage 1 writes nothing else here) |
| `documents` | documents-api + documents-search + dashboard-client + pcs (owned by documents team) + documents-ext-authz EnvoyFilter + documents-ingressgateway |
| `wiki` | wiki-api + wiki-ext-authz (cross-ns copy) + wiki-ingressgateway |

The `documents` and `wiki` namespaces each demonstrate a distinct onboarding pattern. `dashboard-client` cycles through all three protected workloads with alternating `x-workspace-user-id` headers.

## Umbrella Helm Chart

All app manifests and Istio core (base + istiod) are managed via a single umbrella Helm chart at `kind/demo/`. This chart is the **single place to configure images, resource sizes, and ext-authz wiring**.

### Swapping to a private registry

Edit **only** `kind/demo/values.yaml`:

```yaml
images:
  echoServer:
    repository: my-company-registry.local/workspace/echo-server
    tag: 1.2.3
  istio:
    hub: my-company-registry.local/istio
    tag: 1.24.2

istiod:
  global:
    hub: my-company-registry.local/istio  # keep in sync with images.istio.hub
    tag: 1.24.2
```

That single edit propagates to all Deployments, the istiod subchart, and the gateway installs.

### Why gateways are separate

Helm subchart dependencies install into the parent release's namespace (`istio-system`). The ingressgateways must live in `documents` and `wiki`. They are installed as separate `helm upgrade --install` calls by `setup.sh`, but their values live in `kind/demo/values-gateway-documents.yaml` and `kind/demo/values-gateway-wiki.yaml` — still one directory.

## Prerequisites

- Docker Desktop running, ≥ 6 GB RAM allocated
- `kind`, `kubectl`, `helm`, `docker`, `go` (≥ 1.25) installed

## Run

From the repo root:

```bash
./kind/setup.sh
```

Idempotent — re-running picks up where the previous run left off. Total wall-clock on a warm Docker cache is ≤ 3 minutes.

Add to `/etc/hosts`:

```
127.0.0.1  documents.local wiki.local
```

## Verify

```bash
# Watch the request cycle
kubectl -n documents logs deploy/dashboard-client -c dashboard-client -f

# Watch authorization decisions
kubectl -n documents logs deploy/pcs -c pcs -f

# Curl from host (after /etc/hosts)
curl -H "x-workspace-user-id: alice@workspace.test"   http://documents.local:8080/hello   # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://documents.local:8080/hello   # 403
curl -H "x-workspace-user-id: alice@workspace.test"   http://wiki.local:8081/hello        # 200
curl -H "x-workspace-user-id: mallory@workspace.test" http://wiki.local:8081/hello        # 403
```

## Teardown

```bash
./kind/teardown.sh
```
```

- [ ] **Step 3: Commit**

```bash
cd /Users/joe/ashwini-repos/workspace
git add kind/README.md
git commit -m "docs(kind): document umbrella chart and registry-swap workflow in README"
```

---

## Task 18: End-to-end verification run

This is the critical gate. Do not proceed to cleanup until the live run passes.

- [ ] **Step 1: Teardown existing cluster**

```bash
cd /Users/joe/ashwini-repos/workspace
./kind/teardown.sh
```

Wait for completion.

- [ ] **Step 2: Run setup.sh and watch for errors**

```bash
cd /Users/joe/ashwini-repos/workspace
./kind/setup.sh 2>&1 | tee /tmp/setup-output.txt
```

Expected: the script runs to completion, prints the verification banner. If it exits non-zero, read the output to identify which Helm or kubectl command failed.

**Common failure modes to watch for:**
- `helm dependency update` fails: check that `kind/demo/charts/` has both `.tgz` files and `kind/demo/Chart.yaml` has correct `file://charts` repository references.
- `helm upgrade --install demo` fails with "namespace not found": `--create-namespace` should handle this; if not, check Helm version (`helm version`).
- Templates render with literal `<no value>` strings: a `.Values.` path is wrong; run `helm template demo kind/demo` to inspect.
- Gateway install fails: check `values-gateway-documents.yaml` and `values-gateway-wiki.yaml` have correct structure.

- [ ] **Step 3: Verify pods are Running**

```bash
kubectl get pod -A
```

Expected: all pods in `Running` state (1/1 or 2/2 for sidecar-injected). No pods in `Pending`, `CrashLoopBackOff`, or `Error`.

- [ ] **Step 4: Verify EnvoyFilters**

```bash
kubectl get envoyfilter -A
```

Expected: exactly 2 EnvoyFilters — `documents/documents-ext-authz` and `wiki/wiki-ext-authz`. None in `istio-system`.

- [ ] **Step 5: Wait for dashboard-client logs showing 200/403 cycle**

```bash
kubectl -n documents logs deploy/dashboard-client -c dashboard-client --tail=30
```

Expected: alternating 200 OK and 403 Forbidden across `documents-api`, `documents-search`, and `wiki-api`. If logs are empty, the container may still be starting — wait 30 seconds and retry.

- [ ] **Step 6: Check mallory=0 for all three workloads**

```bash
kubectl -n documents logs deploy/documents-api      -c documents-api      | grep -c mallory || true
kubectl -n documents logs deploy/documents-search   -c documents-search   | grep -c mallory || true
kubectl -n wiki      logs deploy/wiki-api           -c wiki-api           | grep -c wiki-api || true
```

Expected: all return `0`. Any non-zero means the EnvoyFilter is not blocking mallory requests from reaching the app container.

- [ ] **Step 7: External curl verification**

Ensure `/etc/hosts` has `127.0.0.1 documents.local wiki.local`.

```bash
curl -s -o /dev/null -w "%{http_code}" \
  -H "x-workspace-user-id: alice@workspace.test" \
  http://documents.local:8080/hello
# Expected: 200

curl -s -o /dev/null -w "%{http_code}" \
  -H "x-workspace-user-id: mallory@workspace.test" \
  http://documents.local:8080/hello
# Expected: 403

curl -s -o /dev/null -w "%{http_code}" \
  --resolve wiki.local:8081:127.0.0.1 \
  -H "x-workspace-user-id: alice@workspace.test" \
  http://wiki.local:8081/hello
# Expected: 200

curl -s -o /dev/null -w "%{http_code}" \
  --resolve wiki.local:8081:127.0.0.1 \
  -H "x-workspace-user-id: mallory@workspace.test" \
  http://wiki.local:8081/hello
# Expected: 403
```

If any of these return an unexpected code, do not proceed. Debug first.

---

## Task 19: Cleanup — delete superseded files

Only perform this task after Task 18 passes completely.

**Files:**
- Delete: `kind/manifests/documents/` (entire directory)
- Delete: `kind/manifests/wiki/` (entire directory)
- Delete: `kind/chart-values/` (entire directory)

- [ ] **Step 1: Delete the superseded directories**

```bash
cd /Users/joe/ashwini-repos/workspace
rm -rf kind/manifests/documents
rm -rf kind/manifests/wiki
rm -rf kind/chart-values
```

- [ ] **Step 2: Verify nothing in setup.sh references the deleted paths**

```bash
grep -r "manifests\|chart-values" /Users/joe/ashwini-repos/workspace/kind/setup.sh || echo "No references found"
```

Expected: `No references found`

- [ ] **Step 3: Commit the cleanup**

```bash
cd /Users/joe/ashwini-repos/workspace
git add -A
git commit -m "chore(kind): remove manifests/ and chart-values/ superseded by kind/demo umbrella chart"
```

---

## Task 20: Final commit and push

- [ ] **Step 1: Verify clean working tree**

```bash
cd /Users/joe/ashwini-repos/workspace
git status
git log --oneline -6
```

Expected: working tree clean. Recent commits should include all the commits from Tasks 15, 16, 17, 19.

- [ ] **Step 2: Create the unified commit (squash if needed)**

If the history is messy with many small commits, squash to one:

```bash
cd /Users/joe/ashwini-repos/workspace
# Count how many commits since HEAD before our changes:
git log --oneline origin/kind-demo..HEAD
```

If there are multiple commits to squash (e.g., 5 commits), rebase interactively:
```bash
git rebase -i HEAD~5
# Change all but the first 'pick' to 'squash'
# Edit the final commit message to: feat(kind): wrap demo in kind/demo umbrella Helm chart with central image config
```

If the commits are already clean, just amend the final commit message:
```bash
git commit --allow-empty --amend -m "feat(kind): wrap demo in kind/demo umbrella Helm chart with central image config"
```

Or create a clean single commit from the start — preferred. If working from scratch, all changes can be committed in one shot at the end:
```bash
git add kind/
git commit -m "feat(kind): wrap demo in kind/demo umbrella Helm chart with central image config"
```

- [ ] **Step 3: Push to origin**

```bash
cd /Users/joe/ashwini-repos/workspace
git push origin kind-demo
```

Expected: push succeeds. If rejected (non-fast-forward), check `git log origin/kind-demo..HEAD` to see what diverged.

---

## Self-Review Against Spec

**Spec coverage check:**

| Spec requirement | Task |
|---|---|
| `kind/demo/` directory structure | Tasks 1–14 |
| `Chart.yaml` with base + istiod deps (NOT gateway) | Task 1 |
| `values.yaml` single source of truth for all images | Task 1 |
| `_helpers.tpl` with `extAuthzPatch` and `optInLabel` | Task 3 |
| Namespace templates with `istio-injection: enabled` | Task 4 |
| PCS Deployment+Service combined | Task 5 |
| documents-api, documents-search with opt-in label | Tasks 6, 7 |
| documents-ext-authz with `subFilter.name: envoy.filters.http.router` | Task 8 (via helper) |
| documents-gateway.yaml = Gateway + VirtualService | Task 9 |
| dashboard-client.yaml with cluster-DNS URLs | Task 10 |
| wiki-api.yaml with opt-in label | Task 11 |
| wiki-ext-authz.yaml with `subFilter.name: envoy.filters.http.router` | Task 12 (via helper) |
| wiki-gateway.yaml = Gateway + VirtualService | Task 13 |
| `kind/demo/README.md` with registry-swap + gateway limitation | Task 14 |
| Vendored tarballs copied to `kind/demo/charts/` (base + istiod only) | Task 2 |
| Gateway values files in `kind/demo/` | Task 2 |
| `setup.sh` rewritten | Task 16 |
| `kind/README.md` updated | Task 17 |
| End-to-end verification run | Task 18 |
| Delete `kind/manifests/`, `kind/chart-values/` | Task 19 |
| Push to `kind-demo` branch | Task 20 |

**EnvoyFilter subFilter preservation:** The `extAuthzPatch` helper in `_helpers.tpl` (Task 3) contains the full `subFilter.name: envoy.filters.http.router` match. Both `documents-ext-authz.yaml` and `wiki-ext-authz.yaml` use `include "ext-authz-demo.extAuthzPatch"` — so the fix is DRY'd and cannot be accidentally dropped from one but not the other.

**Gateway NodePorts:** values-gateway-documents.yaml uses 30080; values-gateway-wiki.yaml uses 30081. These match the existing chart-values files.

**No placeholder issues found.**

**Type consistency:** `extAuthzPatch` helper is defined in Task 3 and used in Tasks 8 and 12. `optInLabel` defined in Task 3, used in Tasks 6, 7, 8, 11, 12. `sidecarAnnotations` defined in Task 3, used in Tasks 5, 6, 7, 10, 11. All consistent.
