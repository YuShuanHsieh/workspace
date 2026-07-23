package config

import (
	"testing"
	"time"
)

func TestParseNATSAuthAndNamespace(t *testing.T) {
	yaml := []byte(`
app:
  id: task-service
  namespace: task-ns
  httpBaseURL: http://localhost:8080
nats:
  url: nats://localhost:4222
  stream: s
  durableConsumer: d
  filterSubject: sub
natsAuth:
  authURL: https://auth.example
  refreshBuffer: 2m
routes:
  - name: r
    match: { subject: sub }
    dispatch: { method: POST, path: / }
`)
	cfg, err := Parse(yaml)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.App.Namespace != "task-ns" {
		t.Errorf("App.Namespace = %q, want task-ns", cfg.App.Namespace)
	}
	if cfg.NatsAuth == nil {
		t.Fatal("expected NatsAuth to be set")
	}
	if cfg.NatsAuth.AuthURL != "https://auth.example" {
		t.Errorf("AuthURL = %q", cfg.NatsAuth.AuthURL)
	}
	if cfg.NatsAuth.RefreshBuffer != 2*time.Minute {
		t.Errorf("RefreshBuffer = %v, want 2m", cfg.NatsAuth.RefreshBuffer)
	}
}
