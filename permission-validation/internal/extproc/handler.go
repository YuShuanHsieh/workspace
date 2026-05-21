package extproc

import (
	"context"
	"errors"
	"time"

	"permission-validation/internal/header"
	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
	"permission-validation/internal/routes"
)

// PCS is the dependency interface for the permission checking service.
type PCS interface {
	Check(ctx context.Context, req pcs.CheckRequest) (pcs.Decision, error)
}

// OutcomeKind enumerates the four wire-level outcomes the sidecar produces.
type OutcomeKind int

const (
	OutcomeAllow OutcomeKind = iota
	OutcomeDeny
	OutcomeRejectHeader
	OutcomeRejectParse
	OutcomeRejectError
)

// Outcome is what handler.Decide returns. Reason is set only for reject kinds and
// carries the metric label.
type Outcome struct {
	Kind   OutcomeKind
	Reason string
}

// Handler is the orchestration core: extract → parse → PCS → emit metrics → return Outcome.
type Handler struct {
	pcs    PCS
	m      *metrics.Metrics
	routes *routes.Table // nil = Phase 1 behavior (no short-circuit)
}

// New constructs a Handler. The metrics object is required.
// t may be nil (Phase 1 behavior: no route-based short-circuit).
func New(p PCS, m *metrics.Metrics, t *routes.Table) *Handler {
	return &Handler{pcs: p, m: m, routes: t}
}

// Decide consumes a lowercased header map (Envoy normalizes header casing on HTTP/2)
// and returns the wire outcome.
func (h *Handler) Decide(ctx context.Context, hdrs map[string]string) Outcome {
	if h == nil || h.m == nil || h.pcs == nil {
		return Outcome{Kind: OutcomeRejectError, Reason: "internal_error"}
	}

	if h.routes != nil {
		method := hdrs[":method"]
		path := hdrs[":path"]
		behavior, _ := h.routes.Lookup(method, path)
		switch behavior {
		case "skipped":
			h.m.Decision(ctx, "allow")
			return Outcome{Kind: OutcomeAllow}
		case "deny":
			h.m.Decision(ctx, "deny")
			return Outcome{Kind: OutcomeDeny}
		}
		// "protected" (matched rule or default-protected) → fall through to
		// existing header parse + PCS path.
	}

	start := time.Now()
	defer func() { h.m.SidecarLatency(ctx, time.Since(start)) }()

	tok, err := header.ExtractAuth(hdrs)
	if err != nil {
		var he *header.HeaderError
		if !errors.As(err, &he) {
			he = &header.HeaderError{Reason: "internal_error"}
		}
		h.m.HeaderInvalid(ctx, he.Reason)
		return Outcome{Kind: OutcomeRejectHeader, Reason: he.Reason}
	}
	ctxRaw, err := header.ExtractContext(hdrs)
	if err != nil {
		var he *header.HeaderError
		if !errors.As(err, &he) {
			he = &header.HeaderError{Reason: "internal_error"}
		}
		h.m.HeaderInvalid(ctx, he.Reason)
		return Outcome{Kind: OutcomeRejectHeader, Reason: he.Reason}
	}
	parsed, err := header.ParseContextHeader(ctxRaw)
	if err != nil {
		var pe *header.ParseError
		if !errors.As(err, &pe) {
			pe = &header.ParseError{Reason: "internal_error"}
		}
		h.m.CtxParseFailure(ctx, pe.Reason)
		return Outcome{Kind: OutcomeRejectParse, Reason: pe.Reason}
	}

	pcsStart := time.Now()
	dec, err := h.pcs.Check(ctx, pcs.CheckRequest{
		ObjectID:   parsed.ObjectID,
		ObjectType: parsed.ObjectType,
		Permission: parsed.Action,
		SSOToken:   tok,
		RequestID:  hdrs["x-request-id"],
	})
	h.m.PCSLatency(ctx, time.Since(pcsStart))
	if err != nil {
		h.m.Decision(ctx, "error")
		return Outcome{Kind: OutcomeRejectError, Reason: "pcs_error"}
	}
	if dec == pcs.DecisionAllow {
		h.m.Decision(ctx, "allow")
		return Outcome{Kind: OutcomeAllow}
	}
	h.m.Decision(ctx, "deny")
	return Outcome{Kind: OutcomeDeny}
}
