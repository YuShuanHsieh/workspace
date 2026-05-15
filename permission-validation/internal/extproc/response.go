package extproc

import (
	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	type_v3 "github.com/envoyproxy/go-control-plane/envoy/type/v3"
)

// continueReply tells Envoy to keep the request unchanged and forward to the upstream.
func continueReply() *ext_proc_v3.ProcessingResponse {
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
