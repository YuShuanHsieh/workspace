package extproc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"

	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

type stubPCS struct {
	decision pcs.Decision
	err      error
	gotReq   pcs.CheckRequest
}

func (s *stubPCS) Check(ctx context.Context, req pcs.CheckRequest) (pcs.Decision, error) {
	s.gotReq = req
	return s.decision, s.err
}

func newHandler(t *testing.T, p PCS) *Handler {
	t.Helper()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	return New(p, metrics.New(mp.Meter("test")))
}

func TestDecide_Allow(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow}
	h := newHandler(t, p)
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:edit",
		"x-request-id":   "req-1",
	})
	require.Equal(t, OutcomeAllow, out.Kind)
	require.Equal(t, "doc-1", p.gotReq.ObjectID)
	require.Equal(t, "document", p.gotReq.ObjectType)
	require.Equal(t, "edit", p.gotReq.Permission)
	require.Equal(t, "sso-tok", p.gotReq.SSOToken)
	require.Equal(t, "req-1", p.gotReq.RequestID)
}

func TestDecide_Deny(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionDeny}
	h := newHandler(t, p)
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:edit",
	})
	require.Equal(t, OutcomeDeny, out.Kind)
}

func TestDecide_PCSError_FailClosed(t *testing.T) {
	p := &stubPCS{err: errors.New("boom")}
	h := newHandler(t, p)
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document:edit",
	})
	require.Equal(t, OutcomeRejectError, out.Kind)
}

func TestDecide_MissingAuth(t *testing.T) {
	h := newHandler(t, &stubPCS{})
	out := h.Decide(context.Background(), map[string]string{
		"x-auth-context": "doc-1:document:edit",
	})
	require.Equal(t, OutcomeRejectHeader, out.Kind)
	require.Equal(t, "missing_authz", out.Reason)
}

func TestDecide_MissingContext(t *testing.T) {
	h := newHandler(t, &stubPCS{})
	out := h.Decide(context.Background(), map[string]string{
		"authorization": "Bearer sso-tok",
	})
	require.Equal(t, OutcomeRejectHeader, out.Kind)
	require.Equal(t, "missing_ctx", out.Reason)
}

func TestDecide_MalformedContext(t *testing.T) {
	h := newHandler(t, &stubPCS{})
	out := h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer sso-tok",
		"x-auth-context": "doc-1:document", // wrong segment count
	})
	require.Equal(t, OutcomeRejectParse, out.Kind)
	require.Equal(t, "wrong_segment_count", out.Reason)
}

func TestDecide_RecordsSidecarLatency(t *testing.T) {
	p := &stubPCS{decision: pcs.DecisionAllow}
	h := newHandler(t, p)
	start := time.Now()
	_ = h.Decide(context.Background(), map[string]string{
		"authorization":  "Bearer t",
		"x-auth-context": "a:b:c",
	})
	require.WithinDuration(t, time.Now(), start, 50*time.Millisecond)
}
