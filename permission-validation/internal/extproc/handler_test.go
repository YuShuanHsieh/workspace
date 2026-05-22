package extproc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"

	"permission-validation/internal/config"
	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
	"permission-validation/internal/routes"
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
	return New(p, metrics.New(mp.Meter("test")), nil)
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
	require.Equal(t, "pcs_error", out.Reason)
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

func TestDecide_NilDependenciesFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *Handler
	}{
		{name: "nil pcs", h: New(nil, metrics.New(metric.NewMeterProvider().Meter("test")), nil)},
		{name: "nil metrics", h: New(&stubPCS{decision: pcs.DecisionAllow}, nil, nil)},
		{name: "nil handler", h: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := tc.h.Decide(context.Background(), map[string]string{
				"authorization":  "Bearer sso-tok",
				"x-auth-context": "doc-1:document:edit",
			})
			require.Equal(t, OutcomeRejectError, out.Kind)
			require.Equal(t, "internal_error", out.Reason)
		})
	}
}

// mockPCS is a controllable PCS stub for route-table tests.
type mockPCS struct {
	calls        int
	nextDecision pcs.Decision
}

func (m *mockPCS) Check(_ context.Context, _ pcs.CheckRequest) (pcs.Decision, error) {
	m.calls++
	return m.nextDecision, nil
}

func TestDecide_SkippedRoute_AllowsWithoutPCS(t *testing.T) {
	mock := &mockPCS{}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{{Method: "GET", Path: "/healthz", Behavior: "skipped"}},
	}
	tbl, _ := routes.Compile(rc)
	h := New(mock, m, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/healthz",
	})
	if out.Kind != OutcomeAllow {
		t.Fatalf("expected allow on skipped route; got kind=%v", out.Kind)
	}
	if mock.calls != 0 {
		t.Fatalf("PCS must not be called for skipped routes; got %d calls", mock.calls)
	}
}

func TestDecide_DefaultDenyNoMatch_DeniesWithoutPCS(t *testing.T) {
	mock := &mockPCS{}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{{Method: "GET", Path: "/protected", Behavior: "protected"}},
	}
	tbl, _ := routes.Compile(rc)
	h := New(mock, m, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/nothing-matches",
	})
	if out.Kind != OutcomeDeny {
		t.Fatalf("expected deny on default-deny catch-all; got kind=%v", out.Kind)
	}
	if mock.calls != 0 {
		t.Fatalf("PCS must not be called for default-deny no-match; got %d calls", mock.calls)
	}
}

func TestDecide_PathWithQueryStringMatchesRoute(t *testing.T) {
	mock := &mockPCS{}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{{Method: "GET", Path: "/healthz", Behavior: "skipped"}},
	}
	tbl, _ := routes.Compile(rc)
	h := New(mock, m, tbl)

	out := h.Decide(context.Background(), map[string]string{
		":method": "GET",
		":path":   "/healthz?verbose=1",
	})
	if out.Kind != OutcomeAllow {
		t.Fatalf("expected allow on skipped route with query string; got kind=%v", out.Kind)
	}
	if mock.calls != 0 {
		t.Fatalf("PCS must not be called for skipped route; got %d calls", mock.calls)
	}
}

func TestDecide_NilRouteTable_PreservesPhase1Behavior(t *testing.T) {
	mock := &mockPCS{nextDecision: pcs.DecisionAllow}
	m := metrics.New(metric.NewMeterProvider().Meter("test"))
	h := New(mock, m, nil)

	out := h.Decide(context.Background(), map[string]string{
		":method":        "GET",
		":path":          "/whatever",
		"authorization":  "Bearer sometoken",
		"x-auth-context": "doc-1:document:read",
	})
	if out.Kind != OutcomeAllow {
		t.Fatalf("phase-1 path expected; got kind=%v reason=%s", out.Kind, out.Reason)
	}
	if mock.calls != 1 {
		t.Fatalf("phase-1 path should call PCS exactly once; got %d", mock.calls)
	}
}
