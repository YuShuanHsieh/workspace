package config

import (
	"strings"
	"testing"
	"time"
)

func TestObservabilityWithDefaults(t *testing.T) {
	got := ObservabilityConfig{}.WithDefaults()
	want := ObservabilityConfig{
		ServiceName:           "event-adapter",
		ServiceVersion:        "0.1.0",
		Environment:           "", // deployment-distinguishing: required, not defaulted
		ServiceNamespace:      "workspace",
		HealthAddr:            ":8080",
		MetricsAddr:           ":8200",
		BackpressureThreshold: 1000,
	}
	if got != want {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

func TestObservabilityKeepsOverrides(t *testing.T) {
	in := ObservabilityConfig{ServiceName: "custom", Environment: "production", BackpressureThreshold: 500}
	got := in.WithDefaults()
	if got.ServiceName != "custom" || got.Environment != "production" || got.BackpressureThreshold != 500 {
		t.Fatalf("overrides not preserved: %+v", got)
	}
	if got.MetricsAddr != ":8200" {
		t.Fatalf("unset field not defaulted: %+v", got)
	}
}

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

func TestParseRequestsBlock(t *testing.T) {
	raw := []byte(`
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: upload-responders
  workerPoolSize: 8
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request
      dispatch:
        method: POST
        path: /requests/upload-presign
        timeout: 3s
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Requests == nil {
		t.Fatal("Requests is nil")
	}
	if cfg.Requests.Subject != "q.tenant-a.app.uploads.request" {
		t.Errorf("subject = %q", cfg.Requests.Subject)
	}
	if cfg.Requests.QueueGroup != "upload-responders" {
		t.Errorf("queueGroup = %q", cfg.Requests.QueueGroup)
	}
	if cfg.Requests.WorkerPoolSize != 8 {
		t.Errorf("workerPoolSize = %d", cfg.Requests.WorkerPoolSize)
	}
	if len(cfg.Requests.Routes) != 1 {
		t.Fatalf("routes len = %d", len(cfg.Requests.Routes))
	}
	r := cfg.Requests.Routes[0]
	if r.Match.Type != "com.workspace.uploads.presign.request" {
		t.Errorf("match.type = %q", r.Match.Type)
	}
	if r.Dispatch.Timeout != 3*time.Second {
		t.Errorf("dispatch.timeout = %v", r.Dispatch.Timeout)
	}
	if r.Reply.Type != "com.workspace.uploads.presign.reply" || r.Reply.Source != "upload-service" {
		t.Errorf("reply = %+v", r.Reply)
	}
}

func TestParseDirectOnlyRequestsBlock(t *testing.T) {
	raw := []byte(`
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: upload-responders
  workerPoolSize: 8
  directDispatch:
    enabled: true
    timeout: 3s
    allowedPathPrefixes:
      - /orders/
`)
	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.Requests == nil {
		t.Fatal("Requests is nil")
	}
	if !cfg.Requests.DirectDispatch.Enabled {
		t.Error("directDispatch.enabled = false, want true")
	}
	if cfg.Requests.DirectDispatch.Timeout != 3*time.Second {
		t.Errorf("directDispatch.timeout = %v, want 3s", cfg.Requests.DirectDispatch.Timeout)
	}
	if got := cfg.Requests.DirectDispatch.AllowedPathPrefixes; len(got) != 1 || got[0] != "/orders/" {
		t.Errorf("directDispatch.allowedPathPrefixes = %#v, want [/orders/]", got)
	}
	if len(cfg.Requests.Routes) != 0 {
		t.Errorf("routes len = %d, want 0", len(cfg.Requests.Routes))
	}
}

func TestParseRequestRouteRejectsResponseKey(t *testing.T) {
	// KnownFields(true) means response/retry/dlq under a request route is a parse error.
	raw := []byte(`
app:
  id: x
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: s
  queueGroup: g
  workerPoolSize: 1
  routes:
    - name: r
      match: {type: t}
      dispatch: {method: POST, path: /x, timeout: 1s}
      reply: {source: s, type: t.reply}
      retry: {maxAttempts: 3}
`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected parse error for retry key on request route, got nil")
	}
}

func TestParseDirectDispatchRejectsUnknownField(t *testing.T) {
	raw := []byte(`
app:
  id: x
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: s
  queueGroup: g
  workerPoolSize: 1
  directDispatch:
    enabled: true
    timeout: 1s
    unknown: true
`)
	if _, err := Parse(raw); err == nil || !strings.Contains(err.Error(), "field unknown") {
		t.Fatalf("expected unknown-field error, got: %v", err)
	}
}

func TestParseRequestRouteRejectsSubjectAndSourceKeys(t *testing.T) {
	raw := []byte(`
app:
  id: upload-service
  httpBaseURL: http://127.0.0.1:8080
nats:
  url: nats://127.0.0.1:4222
requests:
  subject: q.tenant-a.app.uploads.request
  queueGroup: upload-responders
  workerPoolSize: 8
  routes:
    - name: upload-presign
      match:
        type: com.workspace.uploads.presign.request
        subject: q.tenant-a.app.uploads.request
        source: workspace/uploads-client
      dispatch:
        method: POST
        path: /requests/upload-presign
        timeout: 3s
      reply:
        source: upload-service
        type: com.workspace.uploads.presign.reply
`)
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected parse error for request-route match.subject/source, got nil")
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
