package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunHelpExitsZero(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--help"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("expected 0, got %d", code)
	}
	if !strings.Contains(stderr.String(), "NATS JetStream") {
		t.Fatalf("help missing service description: %q", stderr.String())
	}
}

func TestRunRejectsEmptyConfigPath(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", ""}, &stdout, &stderr)
	if code != 2 {
		t.Fatalf("expected 2, got %d", code)
	}
	if !strings.Contains(stderr.String(), "config path is required") {
		t.Fatalf("stderr missing config error: %q", stderr.String())
	}
}

func TestRunRejectsMissingConfigFile(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"--config", "/no/such/file.yaml"}, &stdout, &stderr)
	if code != 1 {
		t.Fatalf("expected 1, got %d", code)
	}
	if !strings.Contains(stderr.String(), "read config") {
		t.Fatalf("stderr missing read config error: %q", stderr.String())
	}
}
