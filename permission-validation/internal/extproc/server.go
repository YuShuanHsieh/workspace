package extproc

import (
	"errors"
	"io"
	"strings"

	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"google.golang.org/grpc"
)

// Server is the gRPC wrapper around Handler.
type Server struct {
	ext_proc_v3.UnimplementedExternalProcessorServer
	h *Handler
}

// RegisterServer mounts an ExternalProcessor service on gs.
func RegisterServer(gs *grpc.Server, h *Handler) {
	ext_proc_v3.RegisterExternalProcessorServer(gs, &Server{h: h})
}

// Process handles one HTTP transaction. Phase 1 reads RequestHeaders, replies once,
// then acknowledges any further messages with CONTINUE so Envoy is free to advance
// through response phases (forward-compat with Phase 1.5).
func (s *Server) Process(stream ext_proc_v3.ExternalProcessor_ProcessServer) error {
	decided := false
	for {
		msg, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch v := msg.Request.(type) {
		case *ext_proc_v3.ProcessingRequest_RequestHeaders:
			if decided {
				if err := stream.Send(continueRequestHeaders()); err != nil {
					return err
				}
				continue
			}
			decided = true
			hdrs, err := flattenHeaders(v.RequestHeaders.GetHeaders())
			if err != nil {
				if err := stream.Send(rejectReply("duplicate_header")); err != nil {
					return err
				}
				continue
			}
			out := s.h.Decide(stream.Context(), hdrs)
			reply := outcomeToReply(out)
			if err := stream.Send(reply); err != nil {
				return err
			}
		default:
			// Other phases (response_headers/body, trailers, request_body) are not
			// processed in Phase 1, but we must reply with a matching oneof variant
			// so Envoy considers the stream well-formed. Phase 1's processing_mode
			// is configured to SKIP these phases entirely; this branch is defensive.
			if err := stream.Send(continueFor(msg)); err != nil {
				return err
			}
		}
	}
}

func outcomeToReply(o Outcome) *ext_proc_v3.ProcessingResponse {
	switch o.Kind {
	case OutcomeAllow:
		return continueRequestHeaders()
	case OutcomeDeny:
		return rejectReply("deny")
	case OutcomeRejectHeader, OutcomeRejectParse, OutcomeRejectError:
		return rejectReply(o.Reason)
	default:
		return rejectReply("unknown")
	}
}

func flattenHeaders(hm *core_v3.HeaderMap) (map[string]string, error) {
	if hm == nil {
		return map[string]string{}, nil
	}
	out := make(map[string]string, len(hm.Headers))
	for _, h := range hm.Headers {
		key := strings.ToLower(h.Key)
		if isCriticalHeader(key) {
			if _, ok := out[key]; ok {
				return nil, errors.New("duplicate critical header")
			}
		}
		// Envoy may put the value in Value (string) or RawValue (bytes); prefer RawValue.
		if len(h.RawValue) > 0 {
			out[key] = string(h.RawValue)
		} else {
			out[key] = h.Value
		}
	}
	return out, nil
}

func isCriticalHeader(key string) bool {
	return key == "authorization" || key == "x-auth-context"
}
