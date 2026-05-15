package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunPrintsBanner(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if err := run(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("run returned error: %v", err)
	}
	if !strings.Contains(stdout.String(), "permission-validation") {
		t.Fatalf("expected banner in stdout, got %q", stdout.String())
	}
}
