package metrics

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Metrics struct {
	eventsConsumed      metric.Int64Counter
	dispatched          metric.Int64Counter
	dispatchLatency     metric.Float64Histogram
	retryAttempts       metric.Int64Counter
	dlqPublishes        metric.Int64Counter
	responsePublishes   metric.Int64Counter
	natsPublishFailures metric.Int64Counter
	natsAckFailures     metric.Int64Counter
	redeliveries        metric.Int64Counter
	duplicateEventIDs   metric.Int64Counter
	routeMatchFailures  metric.Int64Counter
	invalidCloudEvents  metric.Int64Counter

	requestsReceived    metric.Int64Counter
	requestReplyLatency metric.Float64Histogram
	requestDispatchErr  metric.Int64Counter
	requestNoReply      metric.Int64Counter
	invalidRequests     metric.Int64Counter

	// SLI metrics for operational readiness. The OpenTelemetry → Prometheus
	// exporter appends "_total" to counters and the unit to histograms, so the
	// emitted Prometheus names are:
	//   event_adapter_delivery               → event_adapter_delivery_total{status}
	//   event_adapter_conversion_duration[s] → event_adapter_conversion_duration_seconds
	//   event_adapter_delivery_latency[s]    → event_adapter_delivery_latency_seconds
	//   event_adapter_backpressure_triggered → event_adapter_backpressure_triggered_total
	//   event_adapter_panics_recovered       → event_adapter_panics_recovered_total{component}
	//   event_adapter_events_processed_per_second (gauge, emitted verbatim)
	//   event_adapter_pending_backlog            (gauge, emitted verbatim)
	deliveryTotal         metric.Int64Counter
	conversionDuration    metric.Float64Histogram
	deliveryLatency       metric.Float64Histogram
	backpressureTriggered metric.Int64Counter
	panicsRecovered       metric.Int64Counter

	// throughput state: events_processed_per_second is computed as the delta of
	// processedTotal divided by the wall-clock elapsed between observations.
	processedTotal atomic.Int64
	tpMu           sync.Mutex
	tpLastCount    int64
	tpLastNano     int64

	// backlogFn supplies the current pending-event backlog at scrape time.
	backlogMu sync.RWMutex
	backlogFn func(context.Context) int64
}

func New(meter metric.Meter) *Metrics {
	mustC := func(name string) metric.Int64Counter {
		c, err := meter.Int64Counter(name)
		if err != nil {
			panic(err)
		}
		return c
	}
	mustH := func(name, unit string) metric.Float64Histogram {
		h, err := meter.Float64Histogram(name, metric.WithUnit(unit))
		if err != nil {
			panic(err)
		}
		return h
	}
	// SLI latency histograms record seconds, so they need second-scale bucket
	// boundaries (the OTel default boundaries are tuned for milliseconds). The
	// 0.05 boundary lines up with the 50 ms conversion-time target.
	sliLatencyHist := func(name string) metric.Float64Histogram {
		h, err := meter.Float64Histogram(name,
			metric.WithUnit("s"),
			metric.WithExplicitBucketBoundaries(
				0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5,
			),
		)
		if err != nil {
			panic(err)
		}
		return h
	}

	m := &Metrics{
		eventsConsumed:      mustC("cts.events.consumed"),
		dispatched:          mustC("cts.events.dispatched"),
		dispatchLatency:     mustH("cts.dispatch.latency", "ms"),
		retryAttempts:       mustC("cts.retry.attempts"),
		dlqPublishes:        mustC("cts.dlq.publishes"),
		responsePublishes:   mustC("cts.response.publishes"),
		natsPublishFailures: mustC("cts.nats.publish_failures"),
		natsAckFailures:     mustC("cts.nats.ack_failures"),
		redeliveries:        mustC("cts.jetstream.redeliveries"),
		duplicateEventIDs:   mustC("cts.duplicate_event_ids"),
		routeMatchFailures:  mustC("cts.route_match_failures"),
		invalidCloudEvents:  mustC("cts.invalid_cloudevents"),
		requestsReceived:    mustC("cts.requests.received"),
		requestReplyLatency: mustH("cts.request.reply_latency", "ms"),
		requestDispatchErr:  mustC("cts.requests.dispatch_errors"),
		requestNoReply:      mustC("cts.requests.no_reply"),
		invalidRequests:     mustC("cts.requests.invalid"),

		deliveryTotal:         mustC("event_adapter_delivery"),
		conversionDuration:    sliLatencyHist("event_adapter_conversion_duration"),
		deliveryLatency:       sliLatencyHist("event_adapter_delivery_latency"),
		backpressureTriggered: mustC("event_adapter_backpressure_triggered"),
		panicsRecovered:       mustC("event_adapter_panics_recovered"),
	}

	// events_processed_per_second: observed as a rate computed from processedTotal.
	if _, err := meter.Float64ObservableGauge(
		"event_adapter_events_processed_per_second",
		metric.WithFloat64Callback(func(_ context.Context, o metric.Float64Observer) error {
			o.Observe(m.throughput())
			return nil
		}),
	); err != nil {
		panic(err)
	}

	// pending_backlog: observed from the injected backlog provider.
	if _, err := meter.Int64ObservableGauge(
		"event_adapter_pending_backlog",
		metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
			o.Observe(m.backlog(ctx))
			return nil
		}),
	); err != nil {
		panic(err)
	}

	return m
}

func (m *Metrics) EventConsumed(ctx context.Context, route string) {
	m.eventsConsumed.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) Dispatched(ctx context.Context, route string, status int) {
	m.dispatched.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route), attribute.Int("status", status)))
}

func (m *Metrics) DispatchLatency(ctx context.Context, route string, d time.Duration) {
	m.dispatchLatency.Record(ctx, float64(d.Microseconds())/1000, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RetryAttempt(ctx context.Context, route string) {
	m.retryAttempts.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) DLQPublished(ctx context.Context, route string) {
	m.dlqPublishes.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) ResponsePublished(ctx context.Context, route string) {
	m.responsePublishes.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) NATSPublishFailure(ctx context.Context, kind string) {
	m.natsPublishFailures.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind)))
}

func (m *Metrics) NATSAckFailure(ctx context.Context) {
	m.natsAckFailures.Add(ctx, 1)
}

func (m *Metrics) JetStreamRedelivery(ctx context.Context, route string) {
	m.redeliveries.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) DuplicateEventID(ctx context.Context, route string) {
	m.duplicateEventIDs.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RouteMatchFailure(ctx context.Context) {
	m.routeMatchFailures.Add(ctx, 1)
}

func (m *Metrics) InvalidCloudEvent(ctx context.Context, reason string) {
	m.invalidCloudEvents.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (m *Metrics) RequestReceived(ctx context.Context, route string) {
	m.requestsReceived.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RequestReplyLatency(ctx context.Context, route string, d time.Duration) {
	m.requestReplyLatency.Record(ctx, float64(d.Microseconds())/1000, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RequestDispatchError(ctx context.Context, route string) {
	m.requestDispatchErr.Add(ctx, 1, metric.WithAttributes(attribute.String("route", route)))
}

func (m *Metrics) RequestNoReply(ctx context.Context) {
	m.requestNoReply.Add(ctx, 1)
}

func (m *Metrics) InvalidRequestEvent(ctx context.Context, reason string) {
	m.invalidRequests.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// DeliverySuccess records one event successfully published to NATS.
func (m *Metrics) DeliverySuccess(ctx context.Context, route string) {
	m.deliveryTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("route", route), attribute.String("status", "success")))
	m.processedTotal.Add(1)
}

// DeliveryFailure records one event that failed to publish to NATS. reason is
// retained for the structured log emitted by the caller; only the status label
// is carried on the metric to keep cardinality bounded.
func (m *Metrics) DeliveryFailure(ctx context.Context, route, _ string) {
	m.deliveryTotal.Add(ctx, 1, metric.WithAttributes(
		attribute.String("route", route), attribute.String("status", "failed")))
	m.processedTotal.Add(1)
}

// ConversionDuration records the HTTP-response → CloudEvent conversion latency.
func (m *Metrics) ConversionDuration(ctx context.Context, route string, d time.Duration) {
	m.conversionDuration.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("route", route)))
}

// DeliveryLatency records the total time from receiving an event to publishing
// its response to NATS.
func (m *Metrics) DeliveryLatency(ctx context.Context, route string, d time.Duration) {
	m.deliveryLatency.Record(ctx, d.Seconds(), metric.WithAttributes(attribute.String("route", route)))
}

// BackpressureTriggered records one backpressure activation.
func (m *Metrics) BackpressureTriggered(ctx context.Context) {
	m.backpressureTriggered.Add(ctx, 1)
}

// PanicRecovered records one panic caught by a handler backstop. component
// identifies where it was recovered ("responder" or "consumer").
func (m *Metrics) PanicRecovered(ctx context.Context, component string) {
	m.panicsRecovered.Add(ctx, 1, metric.WithAttributes(attribute.String("component", component)))
}

// SetBacklogProvider installs the function used to observe the pending backlog.
func (m *Metrics) SetBacklogProvider(fn func(context.Context) int64) {
	m.backlogMu.Lock()
	m.backlogFn = fn
	m.backlogMu.Unlock()
}

func (m *Metrics) backlog(ctx context.Context) int64 {
	m.backlogMu.RLock()
	fn := m.backlogFn
	m.backlogMu.RUnlock()
	if fn == nil {
		return 0
	}
	return fn(ctx)
}

// throughput returns events processed per second since the previous observation.
func (m *Metrics) throughput() float64 {
	m.tpMu.Lock()
	defer m.tpMu.Unlock()
	now := time.Now().UnixNano()
	count := m.processedTotal.Load()
	if m.tpLastNano == 0 {
		m.tpLastNano = now
		m.tpLastCount = count
		return 0
	}
	elapsed := float64(now-m.tpLastNano) / float64(time.Second)
	m.tpLastNano = now
	prev := m.tpLastCount
	m.tpLastCount = count
	if elapsed <= 0 {
		return 0
	}
	return float64(count-prev) / elapsed
}
