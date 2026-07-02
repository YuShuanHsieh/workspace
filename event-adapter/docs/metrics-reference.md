# Metrics reference

All metrics are exposed in Prometheus text format at `http://<host>:8200/metrics`
(see [sre-integration.md](sre-integration.md) for how the endpoint is wired).

Naming follows the OpenTelemetry → Prometheus exporter conventions:

- counters get a `_total` suffix
- histograms get a unit suffix (`_seconds` or `_milliseconds`) and emit three
  series each: `_bucket`, `_sum`, `_count`
- every series also carries an `otel_scope_name="event-adapter"` label, and a
  `target_info` metric exposes the service identity (name, version, environment,
  namespace)

---

## Async metrics (JetStream consumer + processor)

| Prometheus name | Type | Labels | Description |
|---|---|---|---|
| `cts_events_consumed_total` | counter | `route` | events pulled from JetStream and matched to a route |
| `cts_dispatch_latency_milliseconds` | histogram | `route` | per-event handling time (legacy, milliseconds) |
| `event_adapter_delivery_total` | counter | `route`, `status` (`success`\|`failed`) | delivery outcomes; the success-rate SLI |
| `event_adapter_conversion_duration_seconds` | histogram | `route` | time to build the response CloudEvent from the HTTP response (conversion only, excludes dispatch) |
| `event_adapter_delivery_latency_seconds` | histogram | `route` | total time from receiving an event to publishing its response |
| `event_adapter_events_processed_per_second` | gauge | — | throughput, computed per scrape from the processed-event delta |
| `event_adapter_pending_backlog` | gauge | — | JetStream `NumPending` plus events currently in flight |
| `event_adapter_backpressure_triggered_total` | counter | — | edge-triggered: +1 each time the backlog crosses the threshold from below |
| `cts_route_match_failures_total` | counter | — | events with no matching route |
| `cts_invalid_cloudevents_total` | counter | `reason` (e.g. `parse_error`) | messages that could not be parsed as a CloudEvent |

## Sync metrics (request-reply responder)

| Prometheus name | Type | Labels | Description |
|---|---|---|---|
| `cts_requests_received_total` | counter | `route` | requests received and matched to a route |
| `cts_request_reply_latency_milliseconds` | histogram | `route` | time from receiving a request to sending the reply |
| `cts_requests_dispatch_errors_total` | counter | `route` | requests where the HTTP dispatch failed (→ 502/504 reply) |
| `cts_requests_no_reply_total` | counter | — | requests arriving without a reply inbox (sent via publish, not request) |
| `cts_requests_invalid_total` | counter | `reason` (`parse_error`\|`no_route`) | malformed or unroutable requests |

## Shared (both consumer and responder)

| Prometheus name | Type | Labels | Description |
|---|---|---|---|
| `event_adapter_panics_recovered_total` | counter | `component` (`consumer`\|`responder`) | panics caught by the handler backstop instead of crashing the process |

---

## Key SLI semantics

- **`event_adapter_delivery_total`** — `status="success"` when the response
  CloudEvent is published to NATS (recorded just before the message is acked);
  `status="failed"` on a failed HTTP dispatch (app down/timeout/permanent error
  → DLQ) or a failed NATS publish. Counting dispatch failures makes the
  success-rate ratio reflect an app outage instead of silently dropping DLQ'd
  events.
- **`event_adapter_conversion_duration_seconds`** — the response→CloudEvent
  conversion step only, measured after the HTTP dispatch returns (so it captures
  the sidecar's own overhead, not the app's HTTP latency). Target: p99 < 50 ms.
- **`event_adapter_delivery_latency_seconds`** — the full per-event journey
  (receive → dispatch → convert → publish).
- **`event_adapter_pending_backlog`** — sustained growth indicates the adapter
  cannot drain fast enough; backpressure engages at the configured threshold
  (default 1000).

### Useful PromQL

```promql
# Delivery success rate over 5m
sum(rate(event_adapter_delivery_total{status="success"}[5m]))
  / sum(rate(event_adapter_delivery_total[5m]))

# Conversion time p99 (target < 0.05s)
histogram_quantile(0.99, sum(rate(event_adapter_conversion_duration_seconds_bucket[5m])) by (le))

# Current backlog
event_adapter_pending_backlog

# Recovered panics by component
sum(rate(event_adapter_panics_recovered_total[5m])) by (component)
```

---

## Backpressure

The async consumer protects itself by **pausing consumption** rather than
rejecting work: when the backlog is too high it stops fetching new batches, so
messages stay safely in the JetStream stream until it catches up.

- **Backlog** = JetStream `NumPending` + events currently in flight, surfaced as
  the `event_adapter_pending_backlog` gauge. `NumPending` is cached for ~1s so
  the fetch loop and metric scrapes don't hammer the NATS server.
- **Engage** — when the backlog reaches `backpressureThreshold` (default
  `1000`), the fetch loop pauses for ~200 ms at a time and re-checks, instead of
  pulling more messages.
- **Release (hysteresis)** — backpressure clears only once the backlog falls
  back below **90%** of the threshold (e.g. 900 for a threshold of 1000). The
  gap between engage and release prevents rapid flapping around the boundary.
- **Metric** — `event_adapter_backpressure_triggered_total` is **edge-triggered**:
  it increments once each time the backlog crosses the threshold from below, not
  on every scrape while it stays high.

Tune the threshold via the `observability.backpressureThreshold` config field.

---

## Defined but not emitted

A first-generation set of `cts_*` instruments is defined in the code but never
recorded, so it does **not** appear on `/metrics` (OpenTelemetry emits no series
for an instrument with no data points). These are retained for now and should
not be relied on:

`cts_events_dispatched_total`, `cts_retry_attempts_total`,
`cts_dlq_publishes_total`, `cts_response_publishes_total`,
`cts_nats_publish_failures_total`, `cts_nats_ack_failures_total`,
`cts_jetstream_redeliveries_total`, `cts_duplicate_event_ids_total`.
