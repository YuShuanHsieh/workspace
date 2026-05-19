//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
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
