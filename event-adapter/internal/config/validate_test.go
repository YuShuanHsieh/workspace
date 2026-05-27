package config

import (
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	return &Config{
		App: AppConfig{ID: "task-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{
			URL: "nats://nats:4222", Stream: "workspace-events", DurableConsumer: "task-service-dispatcher",
			AckWait: 30 * time.Second, MaxDeliver: 5, MaxAckPending: 1024, DefaultDLQSubject: "dlq.tenant-a.task-service",
		},
		Routes: []RouteConfig{{
			Name:     "task-created",
			Match:    MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
			Dispatch: DispatchConfig{Method: "POST", Path: "/events/task-created", Timeout: 2 * time.Second},
			Response: ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.tenant-a.app.task.event.processed"},
			Retry:    RetryConfig{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second},
			DLQ:      DLQConfig{Subject: "dlq.tenant-a.task-service"},
		}},
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	if errs := Validate(validConfig()); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRejectsExternalHTTPBaseURL(t *testing.T) {
	cfg := validConfig()
	cfg.App.HTTPBaseURL = "http://example.com"
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", errs[0])
	}
}

func TestValidateRejectsStaticHeaderOverride(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Headers = map[string]string{"ce-id": "bad"}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "reserved header") {
		t.Fatalf("expected reserved header error, got %v", errs[0])
	}
}

func TestValidateRejectsReservedForwardHeader(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.ForwardHeaders = []string{"Authorization"}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "reserved header") {
		t.Fatalf("expected reserved header error, got %v", errs[0])
	}
}
