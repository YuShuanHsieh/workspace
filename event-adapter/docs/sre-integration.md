# SRE / observability package integration

How the event-adapter integrates the shared observability SDK
(`github.com/flywindy/o11y`) and what an SRE team needs to wire up to consume it.

---

## What the o11y SDK provides

The adapter initializes the SDK once at startup (`cmd/event-adapter/main.go`):

```go
obs, err := o11y.Init(ctx,
    o11y.WithServiceName(obsCfg.ServiceName),         // default "event-adapter"
    o11y.WithServiceVersion(obsCfg.ServiceVersion),   // default "0.1.0"
    o11y.WithEnvironment(obsCfg.Environment),         // REQUIRED — no default
    o11y.WithServiceNamespace(obsCfg.ServiceNamespace), // default "workspace"
    o11y.WithMetricsAddr(obsCfg.MetricsAddr),         // default ":8200"
)
m := metrics.New(obs.Meter("event-adapter"))
```

`o11y.Init` returns a handle that:

- starts an HTTP server serving **`/metrics`** on `MetricsAddr` (default `:8200`)
- exposes an OpenTelemetry **`Meter`** used by `metrics.New` to create all
  instruments
- exports via the OpenTelemetry → Prometheus exporter (`_total` on counters,
  unit suffix on histograms; see [metrics-reference.md](metrics-reference.md))
- emits a `target_info` series carrying the service identity and tags every
  metric with `otel_scope_name="event-adapter"`
- is drained on shutdown via `obs.Shutdown(ctx)`

Health checks are **not** part of the SDK — they run on a separate HTTP server
(`healthAddr`, default `:8080`); see [health-endpoints.md](health-endpoints.md).

---

## Ports

| Port | Path | Served by | Purpose |
|---|---|---|---|
| `:8200` | `/metrics` | o11y SDK | Prometheus scrape target |
| `:8080` | `/ready`, `/live` | adapter health server | Kubernetes probes |

---

## Configuration

The `observability:` block in the adapter config. `environment` is **required**
(deployment-distinguishing, so it is intentionally not defaulted — startup fails
fast if it is missing). All other fields are optional; defaults shown:

```yaml
observability:
  environment: production       # REQUIRED — one of: production, staging, development, testing
  serviceName: event-adapter    # default
  serviceVersion: 0.1.0         # default
  serviceNamespace: workspace   # default
  healthAddr: ":8080"           # default
  metricsAddr: ":8200"          # default (Prometheus pull endpoint)
  metricsOTLPEndpoint: ""       # when set, metrics push via OTLP instead of pull
  backpressureThreshold: 1000   # default; pending backlog at which consumption pauses
```

### Metrics transport modes

Metrics export is selectable; the mode is resolved at startup and logged:

| Mode | How to select | Behaviour |
|---|---|---|
| **Pull** (default) | leave `metricsOTLPEndpoint` empty | serves `/metrics` on `metricsAddr` (`:8200`) for Prometheus scrape |
| **Push** | set `metricsOTLPEndpoint` | exports via OTLP to the given collector endpoint; `metricsAddr` is ignored |
| **Off** | pass `--otel-disabled` | no metrics server and no OTLP push (for local dev without infra); wins over the others |

> Note: the o11y SDK's metrics push uses OTLP/**HTTP** (`otlpmetrichttp`), not
> OTLP/gRPC. This satisfies "export via OTLP" but differs from the workspace
> convention's literal "OTLP/gRPC"; strict gRPC would require bypassing the SDK.

---

## What SRE needs to wire up

### 1. Scraping the metrics

**With Prometheus Operator (kube-prometheus-stack):** a `Service` exposing port
8200 plus a `ServiceMonitor` selecting it.

```yaml
apiVersion: v1
kind: Service
metadata:
  name: event-adapter
  labels:
    app: event-adapter
spec:
  selector:
    app: event-adapter
  ports:
    - name: metrics
      port: 8200
      targetPort: 8200
---
apiVersion: monitoring.coreos.com/v1
kind: ServiceMonitor
metadata:
  name: event-adapter
  labels:
    release: kube-prometheus-stack   # match your operator's selector
spec:
  selector:
    matchLabels:
      app: event-adapter
  endpoints:
    - port: metrics
      path: /metrics
      interval: 15s
```

**Without the operator (plain Prometheus):** a `Service` (or pod annotations)
plus a scrape config targeting `:8200/metrics` — no `ServiceMonitor` needed.

### 2. Health probes

Wire `readinessProbe` → `/ready` and `livenessProbe` → `/live` on port **8080**
(see [health-endpoints.md](health-endpoints.md)).

### 3. Alerting

Alert rules are currently deferred pending SRE review of thresholds and
coverage. The SLI metrics needed to build them (delivery success rate, pending
backlog, latency histograms) are emitted today.

---

## Local reference stack

`deploy/local-observability/` runs the adapter, a mock app, NATS, Prometheus,
and Grafana via Docker Compose for local verification of the full
metrics → Prometheus → Grafana path.
