//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	core_v3 "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	ext_proc_v3 "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	envoyURL   = "http://127.0.0.1:8000"
	pcsURL     = "http://127.0.0.1:9000"
	backendURL = "http://127.0.0.1:8080"
)

func waitReady(t *testing.T) {
	t.Helper()
	require.NoError(t, waitReadyError())
}

func waitReadyError() error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(envoyURL + "/health")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("envoy not ready after 20s")
}

func resetFixtures(t *testing.T) {
	t.Helper()
	for _, u := range []string{pcsURL + "/_admin/reset", backendURL + "/_admin/reset"} {
		req, err := http.NewRequest("POST", u, nil)
		require.NoError(t, err, u)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err, u)
		body, readErr := io.ReadAll(resp.Body)
		require.NoError(t, resp.Body.Close())
		require.NoError(t, readErr, u)
		require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300, "%s status=%d body=%s", u, resp.StatusCode, string(body))
	}
}

func setRule(t *testing.T, key string, allowed bool) {
	t.Helper()
	body, _ := json.Marshal(map[string]bool{key: allowed})
	resp, err := http.Post(pcsURL+"/_admin/rules", "application/json", bytes.NewReader(body))
	require.NoError(t, err)
	respBody, readErr := io.ReadAll(resp.Body)
	require.NoError(t, resp.Body.Close())
	require.NoError(t, readErr)
	require.True(t, resp.StatusCode >= 200 && resp.StatusCode < 300, "status=%d body=%s", resp.StatusCode, string(respBody))
}

func backendCallCount(t *testing.T) int {
	t.Helper()
	resp, err := http.Get(backendURL + "/_admin/calls")
	require.NoError(t, err)
	defer resp.Body.Close()
	var calls []any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&calls))
	return len(calls)
}

func do(t *testing.T, method, path string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, envoyURL+path, nil)
	require.NoError(t, err)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

func TestMain(m *testing.M) {
	// e2e build tag is the gate; CI brings the stack up via `make -C test/e2e up`.
	if os.Getenv("E2E_SKIP_WAIT") != "1" {
		// Just-in-case readiness probe.
		if err := waitReadyError(); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	os.Exit(m.Run())
}

func TestE2E_GrantedReachesBackend(t *testing.T) {
	resetFixtures(t)
	setRule(t, "doc-1|document|view", true)
	before := backendCallCount(t)
	code, body := do(t, "GET", "/api/orders/123", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "doc-1:document:view",
	})
	require.Equal(t, 200, code, "body=%s", body)
	require.Equal(t, before+1, backendCallCount(t))
}

func TestE2E_DeniedReturns403(t *testing.T) {
	resetFixtures(t)
	setRule(t, "doc-2|document|edit", false)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/2", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "doc-2:document:edit",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t), "denied request must not reach backend")
}

func TestE2E_MissingAuthRejected(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/1", map[string]string{
		"X-Auth-Context": "doc-1:document:view",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_MalformedContextRejected(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/1", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "doc-1:document",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_OverLengthContextRejected(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	tooLong := strings.Repeat("a", 1024) + ":document:view"
	code, _ := do(t, "GET", "/api/orders/1", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": tooLong,
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_PCSErrorFailClosed(t *testing.T) {
	resetFixtures(t)
	// No rule registered → fake-pcs returns 503.
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/api/orders/no-rule", map[string]string{
		"Authorization":  "Bearer sso-tok",
		"X-Auth-Context": "no-rule:document:view",
	})
	require.Equal(t, 403, code)
	require.Equal(t, before, backendCallCount(t))
}

func TestE2E_SkippedRouteBypassesSidecar(t *testing.T) {
	resetFixtures(t)
	before := backendCallCount(t)
	code, _ := do(t, "GET", "/health", nil)
	require.Equal(t, 200, code)
	require.Equal(t, before+1, backendCallCount(t))
}

// pcsCallCount returns the number of /permission-check calls recorded by fake-pcs.
func pcsCallCount(t *testing.T) int {
	t.Helper()
	resp, err := http.Get(pcsURL + "/_admin/calls")
	require.NoError(t, err)
	defer resp.Body.Close()
	var calls []any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&calls))
	return len(calls)
}

// extprocClient opens a gRPC Process stream to a permission-validation sidecar
// at addr, sends a single RequestHeaders message, reads the first
// ProcessingResponse, and returns (status code, was-ImmediateResponse).
// A CONTINUE response is reported as (200, false).
func extprocClient(t *testing.T, addr string, hdrs map[string]string) (int, bool) {
	t.Helper()
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	require.NoError(t, err, "dial")
	defer conn.Close()
	client := ext_proc_v3.NewExternalProcessorClient(conn)
	stream, err := client.Process(context.Background())
	require.NoError(t, err, "open stream")
	hm := &core_v3.HeaderMap{}
	for k, v := range hdrs {
		hm.Headers = append(hm.Headers, &core_v3.HeaderValue{Key: k, RawValue: []byte(v)})
	}
	require.NoError(t, stream.Send(&ext_proc_v3.ProcessingRequest{
		Request: &ext_proc_v3.ProcessingRequest_RequestHeaders{
			RequestHeaders: &ext_proc_v3.HttpHeaders{Headers: hm},
		},
	}), "send")
	resp, err := stream.Recv()
	require.NoError(t, err, "recv")
	if ir, ok := resp.Response.(*ext_proc_v3.ProcessingResponse_ImmediateResponse); ok {
		return int(ir.ImmediateResponse.Status.Code), true
	}
	return 200, false
}

const sidecarRoutesAddr = "127.0.0.1:50052"

func TestE2E_RoutesFile_SkippedRouteAllowsWithoutPCS(t *testing.T) {
	resetFixtures(t)
	status, immediate := extprocClient(t, sidecarRoutesAddr, map[string]string{
		":method": "GET",
		":path":   "/health",
	})
	require.False(t, immediate, "skipped route should produce CONTINUE, not ImmediateResponse")
	require.Equal(t, 200, status)
	require.Equal(t, 0, pcsCallCount(t), "PCS must not be called for skipped routes")
}

func TestE2E_RoutesFile_DefaultDenyNoMatch_403WithoutPCS(t *testing.T) {
	resetFixtures(t)
	status, immediate := extprocClient(t, sidecarRoutesAddr, map[string]string{
		":method": "GET",
		":path":   "/totally-unknown-path",
	})
	require.True(t, immediate, "default-deny no-match should produce ImmediateResponse, not CONTINUE")
	require.Equal(t, 403, status)
	require.Equal(t, 0, pcsCallCount(t), "PCS must not be called for default-deny no-match")
}
