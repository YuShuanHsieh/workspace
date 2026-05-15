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
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	stream, err := c.Process(ctx)
	require.NoError(t, err)

	hv := make([]*core_v3.HeaderValue, 0, len(hdrs))
	for k, v := range hdrs {
		hv = append(hv, &core_v3.HeaderValue{Key: k, RawValue: []byte(v)})
	}
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
		"authorization":   "Bearer t",
		"x-auth-context": "a:b:c",
	})
	require.NotNil(t, r.GetRequestHeaders())
	require.Equal(t, ext_proc_v3.CommonResponse_CONTINUE, r.GetRequestHeaders().Response.Status)
}

func TestServer_Deny(t *testing.T) {
	c, stop := startServer(t, &fixedPCS{d: pcs.DecisionDeny})
	defer stop()
	r := sendHeaders(t, c, map[string]string{
		"authorization":   "Bearer t",
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
