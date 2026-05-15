package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Metrics holds the Phase 1 SRE instruments. Names map to phase-1-request-contract.md §5
// and phase-1-context-header-format.md §4.
type Metrics struct {
	decisions       metric.Int64Counter
	headerInvalid   metric.Int64Counter
	ctxParseFailure metric.Int64Counter
	sidecarLatency  metric.Float64Histogram
	pcsLatency      metric.Float64Histogram
}

// New builds a Metrics from an OTel Meter. Panic-free; instrument creation errors are
// wrapped into a single panic at startup because a missing meter is a deploy-time bug.
func New(meter metric.Meter) *Metrics {
	mustC := func(name, desc string) metric.Int64Counter {
		c, err := meter.Int64Counter(name, metric.WithDescription(desc))
		if err != nil {
			panic(err)
		}
		return c
	}
	mustH := func(name, desc string) metric.Float64Histogram {
		h, err := meter.Float64Histogram(name, metric.WithDescription(desc), metric.WithUnit("ms"))
		if err != nil {
			panic(err)
		}
		return h
	}
	return &Metrics{
		decisions:       mustC("pv.decisions.total", "Decision outcome counts (allow/deny/error)"),
		headerInvalid:   mustC("pv.header_invalid.total", "Header-presence and well-formedness failures"),
		ctxParseFailure: mustC("pv.ctx_parse_failure.total", "X-Auth-Context parse failures"),
		sidecarLatency:  mustH("pv.sidecar.latency", "End-to-end sidecar handling latency"),
		pcsLatency:      mustH("pv.pcs.latency", "PCS HTTP call latency"),
	}
}

// Decision records one outcome. outcome ∈ {"allow","deny","error"}.
func (m *Metrics) Decision(ctx context.Context, outcome string) {
	m.decisions.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// HeaderInvalid records one rejection. reason ∈ {"missing_authz","missing_ctx","malformed_authz"}.
func (m *Metrics) HeaderInvalid(ctx context.Context, reason string) {
	m.headerInvalid.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// CtxParseFailure records one parse rejection. reason from phase-1-context-header-format.md §4.
func (m *Metrics) CtxParseFailure(ctx context.Context, reason string) {
	m.ctxParseFailure.Add(ctx, 1, metric.WithAttributes(attribute.String("reason", reason)))
}

// SidecarLatency records the wall-clock time the sidecar held the request.
func (m *Metrics) SidecarLatency(ctx context.Context, d time.Duration) {
	m.sidecarLatency.Record(ctx, float64(d.Microseconds())/1000.0)
}

// PCSLatency records the time spent waiting on PCS.
func (m *Metrics) PCSLatency(ctx context.Context, d time.Duration) {
	m.pcsLatency.Record(ctx, float64(d.Microseconds())/1000.0)
}
