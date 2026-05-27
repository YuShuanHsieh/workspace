package config

import (
	"strings"
	"testing"
)

func TestParseValidConfig(t *testing.T) {
	raw := []byte(`
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://nats:4222
  stream: workspace-events
  durableConsumer: task-service-dispatcher
  ackWait: 30s
  maxDeliver: 5
  maxAckPending: 1024
  defaultDLQSubject: dlq.tenant-a.task-service
routes:
  - name: task-created
    match:
      subject: t.tenant-a.app.task.event.created
      type: com.workspace.task.created
      source: workspace/task
    dispatch:
      method: POST
      path: /events/task-created
      timeout: 2s
      forwardHeaders:
        - X-Workspace-Actor-Id
        - X-Workspace-Tenant-Id
    response:
      type: com.workspace.task.created.processed
      source: task-service
      subject: t.tenant-a.app.task.event.processed
    retry:
      maxAttempts: 3
      initialBackoff: 100ms
      maxBackoff: 2s
    dlq:
      subject: dlq.tenant-a.task-service
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if cfg.App.ID != "task-service" {
		t.Fatalf("unexpected app id: %q", cfg.App.ID)
	}
	if cfg.Routes[0].Dispatch.Timeout.String() != "2s" {
		t.Fatalf("unexpected timeout: %s", cfg.Routes[0].Dispatch.Timeout)
	}
	if got := cfg.Routes[0].Dispatch.ForwardHeaders; len(got) != 2 || got[0] != "X-Workspace-Actor-Id" {
		t.Fatalf("unexpected forward headers: %#v", got)
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	_, err := Parse([]byte(`
app:
  id: task-service
  httpBaseURL: http://127.0.0.1:8080
unknown: true
`))
	if err == nil {
		t.Fatal("expected unknown field error")
	}
	if !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("expected unknown-field error, got: %v", err)
	}
}
