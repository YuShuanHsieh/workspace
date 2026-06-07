package metrics

import (
	"context"
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
}

func New(meter metric.Meter) *Metrics {
	mustC := func(name string) metric.Int64Counter {
		c, err := meter.Int64Counter(name)
		if err != nil {
			panic(err)
		}
		return c
	}
	h, err := meter.Float64Histogram("cts.dispatch.latency", metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}
	rh, err := meter.Float64Histogram("cts.request.reply_latency", metric.WithUnit("ms"))
	if err != nil {
		panic(err)
	}
	return &Metrics{
		eventsConsumed:      mustC("cts.events.consumed"),
		dispatched:          mustC("cts.events.dispatched"),
		dispatchLatency:     h,
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
		requestReplyLatency: rh,
		requestDispatchErr:  mustC("cts.requests.dispatch_errors"),
		requestNoReply:      mustC("cts.requests.no_reply"),
		invalidRequests:     mustC("cts.requests.invalid"),
	}
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
