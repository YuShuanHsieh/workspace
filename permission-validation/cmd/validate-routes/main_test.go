package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type failWriter struct{}

func (failWriter) Write(_ []byte) (int, error) {
	return 0, errors.New("write failed")
}

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
	require.Contains(t, s, `address: "127.0.0.1", port_value: 9901`)
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
	require.Contains(t, s, `address: "0.0.0.0", port_value: 9901`)
	require.Contains(t, s, "StdoutAccessLog")
}

func TestTranslate_RejectsInvalidPorts(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "sidecar port zero",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml", "--sidecar-port", "0"},
			want: "sidecar-port",
		},
		{
			name: "backend port above range",
			args: []string{"translate", "../../testdata/routes/valid-minimal.yaml", "--backend-port", "65536"},
			want: "backend-port",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := run(context.Background(), tc.args, &bytes.Buffer{}, &stderr)
			require.Equal(t, 2, code)
			require.Contains(t, stderr.String(), tc.want)
		})
	}
}

func TestTranslate_StdoutWriteFailureExitsNonZero(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(),
		[]string{"translate", "../../testdata/routes/valid-minimal.yaml"},
		failWriter{}, &stderr)
	require.Equal(t, 1, code)
	require.Contains(t, stderr.String(), "write failed")
}

func TestTranslate_TargetIstio_WritesFile(t *testing.T) {
	tmp := t.TempDir()
	out := filepath.Join(tmp, "envoyfilter.yaml")
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"translate", "../../testdata/routes/valid-minimal.yaml",
		"--target=istio",
		"--namespace=orders",
		"--workload-label=app=orders-app",
		"-o", out,
	}, &bytes.Buffer{}, &stderr)
	if code != 0 {
		t.Fatalf("exit %d; stderr=%s", code, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(b, []byte("kind: EnvoyFilter")) {
		t.Fatalf("expected EnvoyFilter output; got:\n%s", b)
	}
	if !bytes.Contains(b, []byte("namespace: orders")) {
		t.Fatalf("expected namespace: orders; got:\n%s", b)
	}
}

func TestTranslate_TargetIstio_RequiresNamespace(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"translate", "../../testdata/routes/valid-minimal.yaml",
		"--target=istio",
		"--workload-label=app=x",
	}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "namespace") {
		t.Fatalf("stderr should mention 'namespace'; got: %s", stderr.String())
	}
}

func TestTranslate_TargetIstio_RequiresWorkloadLabel(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"translate", "../../testdata/routes/valid-minimal.yaml",
		"--target=istio",
		"--namespace=orders",
	}, &bytes.Buffer{}, &stderr)
	if code != 2 {
		t.Fatalf("exit: got %d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "workload-label") {
		t.Fatalf("stderr should mention 'workload-label'; got: %s", stderr.String())
	}
}
