package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidate_ValidExits0(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"validate", "../../testdata/routes/valid-minimal.yaml"},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
}

func TestValidate_InvalidExits1(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"validate", "../../testdata/routes/invalid-bad-method.yaml"},
		&stdout, &stderr)
	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "method")
}

func TestTranslate_WritesFile(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envoy.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"-o", out, "--sidecar-host", "127.0.0.1", "--sidecar-port", "50051",
			"--backend-host", "127.0.0.1", "--backend-port", "8080"},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(b)
	require.True(t, strings.Contains(s, "ext_proc"))
	// Production-safe defaults: admin bound to loopback, no access log.
	require.Contains(t, s, "address: 127.0.0.1, port_value: 9901")
	require.NotContains(t, s, "access_log")
}

func TestTranslate_AdminHostAndAccessLogFlags(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "envoy.yaml")
	var stdout, stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml",
			"-o", out,
			"--sidecar-host", "sidecar", "--sidecar-port", "50051",
			"--backend-host", "backend", "--backend-port", "8080",
			"--admin-host", "0.0.0.0",
			"--access-log"},
		&stdout, &stderr)
	require.Equal(t, 0, code, "stderr=%s", stderr.String())
	b, err := os.ReadFile(out)
	require.NoError(t, err)
	s := string(b)
	require.Contains(t, s, "address: 0.0.0.0, port_value: 9901")
	require.Contains(t, s, "StdoutAccessLog")
}
