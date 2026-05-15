package extproc

import (
	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	type_v3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
)

// continueRequestHeaders is the reply when Envoy sent us a RequestHeaders message
// and the sidecar wants Envoy to forward the request unchanged.
func continueRequestHeaders() *ext_proc_v3.ProcessingResponse {
	return &ext_proc_v3.ProcessingResponse{
		Response: &ext_proc_v3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HeadersResponse{
				Response: &ext_proc_v3.CommonResponse{
					Status: ext_proc_v3.CommonResponse_CONTINUE,
				},
			},
		},
	}
}

// continueFor builds a CONTINUE reply whose oneof variant matches the inbound
// request phase. Used by the server to acknowledge any non-RequestHeaders phase
// without changing semantics — important for Phase 1.5 forward-compat.
func continueFor(req *ext_proc_v3.ProcessingRequest) *ext_proc_v3.ProcessingResponse {
	common := &ext_proc_v3.CommonResponse{Status: ext_proc_v3.CommonResponse_CONTINUE}
	switch req.Request.(type) {
	case *ext_proc_v3.ProcessingRequest_RequestHeaders:
		return &ext_proc_v3.ProcessingResponse{Response: &ext_proc_v3.ProcessingResponse_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HeadersResponse{Response: common},
		}}
	case *ext_proc_v3.ProcessingRequest_ResponseHeaders:
		return &ext_proc_v3.ProcessingResponse{Response: &ext_proc_v3.ProcessingResponse_ResponseHeaders{
			ResponseHeaders: &ext_proc_v3.HeadersResponse{Response: common},
		}}
	case *ext_proc_v3.ProcessingRequest_RequestBody:
		return &ext_proc_v3.ProcessingResponse{Response: &ext_proc_v3.ProcessingResponse_RequestBody{
			RequestBody: &ext_proc_v3.BodyResponse{Response: common},
		}}
	case *ext_proc_v3.ProcessingRequest_ResponseBody:
		return &ext_proc_v3.ProcessingResponse{Response: &ext_proc_v3.ProcessingResponse_ResponseBody{
			ResponseBody: &ext_proc_v3.BodyResponse{Response: common},
		}}
	case *ext_proc_v3.ProcessingRequest_RequestTrailers:
		return &ext_proc_v3.ProcessingResponse{Response: &ext_proc_v3.ProcessingResponse_RequestTrailers{
			RequestTrailers: &ext_proc_v3.TrailersResponse{HeaderMutation: nil},
		}}
	case *ext_proc_v3.ProcessingRequest_ResponseTrailers:
		return &ext_proc_v3.ProcessingResponse{Response: &ext_proc_v3.ProcessingResponse_ResponseTrailers{
			ResponseTrailers: &ext_proc_v3.TrailersResponse{HeaderMutation: nil},
		}}
	default:
		// Unknown phase: fall back to a RequestHeaders CONTINUE. The only way this
		// branch is hit is a future protobuf field that this build doesn't know about;
		// returning *something* keeps the stream alive.
		return continueRequestHeaders()
	}
}

// rejectReply terminates the request with a 403 and a short reason body.
// reasonCode is included in a response header so SREs can correlate.
func rejectReply(reasonCode string) *ext_proc_v3.ProcessingResponse {
	return &ext_proc_v3.ProcessingResponse{
		Response: &ext_proc_v3.ProcessingResponse_ImmediateResponse{
			ImmediateResponse: &ext_proc_v3.ImmediateResponse{
				Status: &type_v3.HttpStatus{Code: type_v3.StatusCode_Forbidden},
				Headers: &ext_proc_v3.HeaderMutation{
					SetHeaders: []*core_v3.HeaderValueOption{{
						Header: &core_v3.HeaderValue{
							Key:      "x-pv-reject-reason",
							RawValue: []byte(reasonCode),
						},
					}},
				},
				Body: []byte("forbidden\n"),
			},
		},
	}
}
