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
	require.True(t, strings.Contains(string(b), "ext_proc"))
}
