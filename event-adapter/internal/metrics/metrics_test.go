package metrics

import (
	"context"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
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
	m.DeliverySuccess(ctx, "task-created")
	m.DeliveryFailure(ctx, "task-created", "nats down")
	m.ConversionDuration(ctx, "task-created", 10*time.Millisecond)
	m.DeliveryLatency(ctx, "task-created", 20*time.Millisecond)
	m.BackpressureTriggered(ctx)
	m.PanicRecovered(ctx, "responder")
}

func TestPanicRecoveredCountsByComponent(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	m := New(mp.Meter("event-adapter"))
	ctx := context.Background()

	m.PanicRecovered(ctx, "responder")
	m.PanicRecovered(ctx, "consumer")
	m.PanicRecovered(ctx, "consumer")

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	sum, ok := collectByName(t, &rm, "event_adapter_panics_recovered").Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("panics_recovered is %T, want Sum[int64]", collectByName(t, &rm, "event_adapter_panics_recovered").Data)
	}
	byComponent := map[string]int64{}
	for _, dp := range sum.DataPoints {
		c, _ := dp.Attributes.Value("component")
		byComponent[c.AsString()] = dp.Value
	}
	if byComponent["responder"] != 1 {
		t.Fatalf("responder panics = %d, want 1", byComponent["responder"])
	}
	if byComponent["consumer"] != 2 {
		t.Fatalf("consumer panics = %d, want 2", byComponent["consumer"])
	}
}

// collectByName returns the collected metric for name, failing if absent.
func collectByName(t *testing.T, rm *metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, md := range sm.Metrics {
			if md.Name == name {
				return md
			}
		}
	}
	t.Fatalf("metric %q not found in collection", name)
	return metricdata.Metrics{}
}

func TestSLIMetricsRecordExpectedValues(t *testing.T) {
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	m := New(mp.Meter("event-adapter"))
	ctx := context.Background()

	m.DeliverySuccess(ctx, "task-created")
	m.DeliverySuccess(ctx, "task-created")
	m.DeliveryFailure(ctx, "task-created", "nats unavailable")
	m.ConversionDuration(ctx, "task-created", 12*time.Millisecond)
	m.DeliveryLatency(ctx, "task-created", 30*time.Millisecond)
	m.BackpressureTriggered(ctx)
	m.SetBacklogProvider(func(context.Context) int64 { return 42 })

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}

	// delivery_total: success=2, failed=1, keyed by status attribute.
	delivery := collectByName(t, &rm, "event_adapter_delivery")
	sum, ok := delivery.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("delivery is %T, want Sum[int64]", delivery.Data)
	}
	bySatus := map[string]int64{}
	for _, dp := range sum.DataPoints {
		status, _ := dp.Attributes.Value("status")
		bySatus[status.AsString()] = dp.Value
	}
	if bySatus["success"] != 2 {
		t.Fatalf("delivery success = %d, want 2", bySatus["success"])
	}
	if bySatus["failed"] != 1 {
		t.Fatalf("delivery failed = %d, want 1", bySatus["failed"])
	}

	// conversion + latency histograms each recorded once.
	for _, name := range []string{"event_adapter_conversion_duration", "event_adapter_delivery_latency"} {
		h, ok := collectByName(t, &rm, name).Data.(metricdata.Histogram[float64])
		if !ok {
			t.Fatalf("%s is not a float histogram", name)
		}
		var count uint64
		for _, dp := range h.DataPoints {
			count += dp.Count
		}
		if count != 1 {
			t.Fatalf("%s count = %d, want 1", name, count)
		}
	}

	// backpressure counter incremented once.
	bp, ok := collectByName(t, &rm, "event_adapter_backpressure_triggered").Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatal("backpressure is not Sum[int64]")
	}
	var bpTotal int64
	for _, dp := range bp.DataPoints {
		bpTotal += dp.Value
	}
	if bpTotal != 1 {
		t.Fatalf("backpressure total = %d, want 1", bpTotal)
	}

	// pending_backlog gauge reflects the provider.
	backlog, ok := collectByName(t, &rm, "event_adapter_pending_backlog").Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatal("pending_backlog is not Gauge[int64]")
	}
	if len(backlog.DataPoints) != 1 || backlog.DataPoints[0].Value != 42 {
		t.Fatalf("pending_backlog = %v, want single value 42", backlog.DataPoints)
	}

	// throughput gauge is registered and observable.
	if _, ok := collectByName(t, &rm, "event_adapter_events_processed_per_second").Data.(metricdata.Gauge[float64]); !ok {
		t.Fatal("events_processed_per_second is not Gauge[float64]")
	}
}
