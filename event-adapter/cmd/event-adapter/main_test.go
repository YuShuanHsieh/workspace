package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/processor"
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

type fakeDLQPublisher struct {
	err       error
	publishes int
}

func (f *fakeDLQPublisher) PublishDLQ(context.Context, string, processor.DLQEvent) error {
	f.publishes++
	return f.err
}

type fakeDefaultAcker struct {
	acks int
}

func (f *fakeDefaultAcker) Ack(context.Context) error {
	f.acks++
	return nil
}

func TestPublishDefaultDLQAndAckAcksAfterPublish(t *testing.T) {
	pub := &fakeDLQPublisher{}
	ack := &fakeDefaultAcker{}
	cfg := &config.Config{
		App:  config.AppConfig{ID: "task-service"},
		NATS: config.NATSConfig{DefaultDLQSubject: "dlq.subject"},
	}
	if err := publishDefaultDLQAndAck(context.Background(), pub, cfg, (*clevent.Event)(nil), "parse error", ack, "input.subject", &bytes.Buffer{}); err != nil {
		t.Fatalf("publishDefaultDLQAndAck returned error: %v", err)
	}
	if pub.publishes != 1 || ack.acks != 1 {
		t.Fatalf("unexpected state publishes=%d acks=%d", pub.publishes, ack.acks)
	}
}

func TestPublishDefaultDLQAndAckDoesNotAckWhenPublishFails(t *testing.T) {
	pub := &fakeDLQPublisher{err: errors.New("nats down")}
	ack := &fakeDefaultAcker{}
	cfg := &config.Config{
		App:  config.AppConfig{ID: "task-service"},
		NATS: config.NATSConfig{DefaultDLQSubject: "dlq.subject"},
	}
	if err := publishDefaultDLQAndAck(context.Background(), pub, cfg, nil, "parse error", ack, "input.subject", &bytes.Buffer{}); err == nil {
		t.Fatal("expected publishDefaultDLQAndAck error")
	}
	if ack.acks != 0 {
		t.Fatalf("must not ack when DLQ publish fails, got %d acks", ack.acks)
	}
}
