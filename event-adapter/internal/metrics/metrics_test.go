package metrics

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
)

func TestMetricsMethodsDoNotPanic(t *testing.T) {
	mp := metric.NewMeterProvider()
	m := New(mp.Meter("test"))
	ctx := context.Background()
	m.EventConsumed(ctx, "task-created")
	m.Dispatched(ctx, "task-created", 200)
	m.DispatchLatency(ctx, "task-created", 10*time.Millisecond)
	m.RetryAttempt(ctx, "task-created")
	m.DLQPublished(ctx, "task-created")
	m.ResponsePublished(ctx, "task-created")
	m.NATSPublishFailure(ctx, "response")
	m.NATSAckFailure(ctx)
	m.JetStreamRedelivery(ctx, "task-created")
	m.DuplicateEventID(ctx, "task-created")
	m.RouteMatchFailure(ctx)
	m.InvalidCloudEvent(ctx, "missing_id")
}
