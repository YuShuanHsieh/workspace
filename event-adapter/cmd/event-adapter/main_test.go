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

func TestResolveMetricsMode(t *testing.T) {
	cases := []struct {
		name         string
		otelDisabled bool
		otlpEndpoint string
		want         metricsMode
	}{
		{"default is pull", false, "", metricsPull},
		{"otlp endpoint set is push", false, "collector:4318", metricsPush},
		{"disabled wins over endpoint", true, "collector:4318", metricsOff},
		{"disabled with no endpoint", true, "", metricsOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := resolveMetricsMode(tc.otelDisabled, tc.otlpEndpoint); got != tc.want {
				t.Fatalf("resolveMetricsMode(%v, %q) = %v, want %v", tc.otelDisabled, tc.otlpEndpoint, got, tc.want)
			}
		})
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
