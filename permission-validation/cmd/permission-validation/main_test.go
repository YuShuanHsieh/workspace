package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type blockingGRPCServer struct {
	gracefulCalled chan struct{}
	stopCalled     chan struct{}
}

func newBlockingGRPCServer() *blockingGRPCServer {
	return &blockingGRPCServer{
		gracefulCalled: make(chan struct{}),
		stopCalled:     make(chan struct{}),
	}
}

func (s *blockingGRPCServer) GracefulStop() {
	close(s.gracefulCalled)
	<-s.stopCalled
}

func (s *blockingGRPCServer) Stop() {
	close(s.stopCalled)
}

func TestRun_HelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected --help to exit 0, got %d", code)
	}
	if !strings.Contains(stdout.String()+stderr.String(), "permission-validation") {
		t.Fatalf("expected --help to mention permission-validation; got %q / %q", stdout.String(), stderr.String())
	}
}

func TestRun_BadFlagExitsNonZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--no-such-flag"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit on bad flag")
	}
}

func TestRun_ServesAndShutsDownOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan int, 1)
	var stdout, stderr bytes.Buffer
	go func() {
		done <- run(ctx, []string{
			"--listen", "127.0.0.1:0",
			"--pcs-endpoint", "http://127.0.0.1:1",
			"--otel-disabled",
		}, &stdout, &stderr)
	}()
	// Let it start.
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 0 {
			t.Fatalf("expected clean shutdown, got %d; stderr=%s", code, stderr.String())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not shut down within 2s")
	}
}

func TestStopGRPCServerFallsBackWhenGracefulStopBlocks(t *testing.T) {
	s := newBlockingGRPCServer()
	stopGRPCServer(s, 10*time.Millisecond)

	select {
	case <-s.gracefulCalled:
	default:
		t.Fatal("expected graceful stop to be attempted")
	}
	select {
	case <-s.stopCalled:
	default:
		t.Fatal("expected forced stop fallback")
	}
}

func TestRun_RoutesFile_MissingFile_ExitsNonZero(t *testing.T) {
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--listen=127.0.0.1:0",
		"--routes-file=/nonexistent/routes.yaml",
		"--otel-disabled",
	}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0; stderr=%s", stderr.String())
	}
	if !strings.Contains(stderr.String(), "read routes file") {
		t.Fatalf("stderr should mention 'read routes file'; got: %s", stderr.String())
	}
}

func TestRun_RoutesFile_InvalidYAML_ExitsNonZero(t *testing.T) {
	tmp := t.TempDir()
	bad := filepath.Join(tmp, "routes.yaml")
	if err := os.WriteFile(bad, []byte("not: valid: yaml: { ["), 0o644); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	code := run(context.Background(), []string{
		"--listen=127.0.0.1:0",
		"--routes-file=" + bad,
		"--otel-disabled",
	}, &bytes.Buffer{}, &stderr)
	if code == 0 {
		t.Fatalf("expected non-zero exit; got 0; stderr=%s", stderr.String())
	}
}
