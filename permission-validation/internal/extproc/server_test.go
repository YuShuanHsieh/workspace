package extproc

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"

	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

type fixedPCS struct {
	d   pcs.Decision
	err error
}

func (f *fixedPCS) Check(_ context.Context, _ pcs.CheckRequest) (pcs.Decision, error) {
	return f.d, f.err
}

func startServer(t *testing.T, p PCS) (ext_proc_v3.ExternalProcessorClient, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	gs := grpc.NewServer()
	reader := metric.NewManualReader()
	mp := metric.NewMeterProvider(metric.WithReader(reader))
	h := New(p, metrics.New(mp.Meter("test")))
	RegisterServer(gs, h)
	go func() { _ = gs.Serve(lis) }()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err)
	return ext_proc_v3.NewExternalProcessorClient(conn), func() {
		_ = conn.Close()
		gs.Stop()
	}
}

func sendHeaders(t *testing.T, c ext_proc_v3.ExternalProcessorClient, hdrs map[string]string) *ext_proc_v3.ProcessingResponse {
	t.Helper()
	hv := make([]*core_v3.HeaderValue, 0, len(hdrs))
	for k, v := range hdrs {
		hv = append(hv, &core_v3.HeaderValue{Key: k, RawValue: []byte(v)})
	}
	return sendHeaderValues(t, c, hv)
}

func sendHeaderValues(t *testing.T, c ext_proc_v3.ExternalProcessorClient, hv []*core_v3.HeaderValue) *ext_proc_v3.ProcessingResponse {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := c.Process(ctx)
	require.NoError(t, err)

	require.NoError(t, stream.Send(&ext_proc_v3.ProcessingRequest{
		Request: &ext_proc_v3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HttpHeaders{Headers: &core_v3.HeaderMap{Headers: hv}},
		},
	}))
	resp, err := stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.CloseSend())
	return resp
}

func TestServer_Allow(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()
	r := sendHeaders(t, c, map[string]string{
		"authorization":  "Bearer t",
		"x-auth-context": "a:b:c",
	})
	require.NotNil(t, r.GetRequestHeaders())
	require.Equal(t, ext_proc_v3.CommonResponse_CONTINUE, r.GetRequestHeaders().Response.Status)
}

func TestServer_Deny(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionDeny})
	defer stop()
	r := sendHeaders(t, c, map[string]string{
		"authorization":  "Bearer t",
		"x-auth-context": "a:b:c",
	})
	imm := r.GetImmediateResponse()
	require.NotNil(t, imm)
	require.EqualValues(t, 403, imm.Status.Code)
}

func TestServer_MissingHeader(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()
	r := sendHeaders(t, c, map[string]string{}) // no auth, no ctx
	imm := r.GetImmediateResponse()
	require.NotNil(t, imm)
	require.EqualValues(t, 403, imm.Status.Code)
	var reason string
	for _, h := range imm.Headers.SetHeaders {
		if h.Header.Key == "x-pv-reject-reason" {
			reason = string(h.Header.RawValue)
		}
	}
	require.Equal(t, "missing_authz", reason)
}

func TestServer_DuplicateCriticalHeaderRejected(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()
	r := sendHeaderValues(t, c, []*core_v3.HeaderValue{
		{Key: "Authorization", RawValue: []byte("Bearer one")},
		{Key: "authorization", RawValue: []byte("Bearer two")},
		{Key: "x-auth-context", RawValue: []byte("a:b:c")},
	})
	imm := r.GetImmediateResponse()
	require.NotNil(t, imm)
	require.EqualValues(t, 403, imm.Status.Code)
	var reason string
	for _, h := range imm.Headers.SetHeaders {
		if h.Header.Key == "x-pv-reject-reason" {
			reason = string(h.Header.RawValue)
		}
	}
	require.Equal(t, "duplicate_header", reason)
}

func TestServer_NormalizesHeaderNames(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()
	r := sendHeaderValues(t, c, []*core_v3.HeaderValue{
		{Key: "Authorization", RawValue: []byte("Bearer t")},
		{Key: "X-Auth-Context", RawValue: []byte("a:b:c")},
	})
	require.NotNil(t, r.GetRequestHeaders())
	require.Equal(t, ext_proc_v3.CommonResponse_CONTINUE, r.GetRequestHeaders().Response.Status)
}

func TestServer_ContinueOnNonRequestHeadersPhase(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionAllow})
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := c.Process(ctx)
	require.NoError(t, err)

	// First, send RequestHeaders so the handler decides.
	require.NoError(t, stream.Send(&ext_proc_v3.ProcessingRequest{
		Request: &ext_proc_v3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HttpHeaders{Headers: &core_v3.HeaderMap{Headers: []*core_v3.HeaderValue{
				{Key: "authorization", RawValue: []byte("Bearer t")},
				{Key: "x-auth-context", RawValue: []byte("a:b:c")},
			}}},
		},
	}))
	r1, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, r1.GetRequestHeaders())

	// Now send ResponseHeaders. The server should reply with a ResponseHeaders
	// CONTINUE oneof variant, not a RequestHeaders variant.
	require.NoError(t, stream.Send(&ext_proc_v3.ProcessingRequest{
		Request: &ext_proc_v3.ProcessingRequest_ResponseHeaders{
			ResponseHeaders: &ext_proc_v3.HttpHeaders{Headers: &core_v3.HeaderMap{Headers: []*core_v3.HeaderValue{
				{Key: ":status", RawValue: []byte("200")},
			}}},
		},
	}))
	r2, err := stream.Recv()
	require.NoError(t, err)
	require.NotNil(t, r2.GetResponseHeaders(), "expected ResponseHeaders oneof variant; got %+v", r2.Response)
	require.Equal(t, ext_proc_v3.CommonResponse_CONTINUE, r2.GetResponseHeaders().Response.Status)

	require.NoError(t, stream.CloseSend())
}
