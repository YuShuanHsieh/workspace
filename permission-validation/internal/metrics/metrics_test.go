package metrics

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func setup(t *testing.T) (*Metrics, *metric.ManualReader) {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	m := New(mp.Meter("test"))
	return m, reader
}

func collect(t *testing.T, r *metric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, r.Collect(context.Background(), &rm))
	return rm
}

func sumByLabel(t *testing.T, rm metricdata.ResourceMetrics, instrument string, labelKey attribute.Key) map[string]int64 {
	t.Helper()
	out := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != instrument {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			require.True(t, ok, "metric %s not int64 Sum", instrument)
			for _, dp := range sum.DataPoints {
				v, _ := dp.Attributes.Value(labelKey)
				out[v.AsString()] += dp.Value
			}
		}
	}
	return out
}

func TestDecisionsCounter(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.Decision(ctx, "allow")
	m.Decision(ctx, "allow")
	m.Decision(ctx, "deny")
	m.Decision(ctx, "error")

	rm := collect(t, r)
	got := sumByLabel(t, rm, "pv.decisions.total", attribute.Key("outcome"))
	require.Equal(t, map[string]int64{"allow": 2, "deny": 1, "error": 1}, got)
}

func TestHeaderInvalidCounter(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.HeaderInvalid(ctx, "missing_authz")
	m.HeaderInvalid(ctx, "missing_ctx")
	m.HeaderInvalid(ctx, "missing_ctx")

	rm := collect(t, r)
	got := sumByLabel(t, rm, "pv.header_invalid.total", attribute.Key("reason"))
	require.Equal(t, map[string]int64{"missing_authz": 1, "missing_ctx": 2}, got)
}

func TestCtxParseFailureCounter(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.CtxParseFailure(ctx, "wrong_segment_count")
	m.CtxParseFailure(ctx, "over_length")

	rm := collect(t, r)
	got := sumByLabel(t, rm, "pv.ctx_parse_failure.total", attribute.Key("reason"))
	require.Equal(t, map[string]int64{"wrong_segment_count": 1, "over_length": 1}, got)
}

func TestLatencyHistograms(t *testing.T) {
	m, r := setup(t)
	ctx := context.Background()
	m.SidecarLatency(ctx, 3*time.Millisecond)
	m.PCSLatency(ctx, 2*time.Millisecond)

	rm := collect(t, r)
	seen := map[string]bool{}
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			seen[mt.Name] = true
		}
	}
	require.True(t, seen["pv.sidecar.latency"], "missing pv.sidecar.latency")
	require.True(t, seen["pv.pcs.latency"], "missing pv.pcs.latency")
}
