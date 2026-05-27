//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

func TestEventDispatchPublishesResponse(t *testing.T) {
	app := httptestApp(t)
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	_ = js.DeleteStream("workspace-events")
	if _, err := js.AddStream(&nats.StreamConfig{
		Name: "workspace-events",
		Subjects: []string{
			"t.tenant-a.app.task.event.created",
			"t.tenant-a.app.task.event.processed",
			"dlq.tenant-a.task-service",
		},
	}); err != nil {
		t.Fatalf("add stream: %v", err)
	}
	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed")
	if err != nil {
		t.Fatalf("subscribe response: %v", err)
	}
	cfgPath := writeE2EConfig(t, app)
	binPath := filepath.Join(t.TempDir(), "event-adapter")
	build := exec.Command("/usr/local/go/bin/go", "build", "-o", binPath, "./cmd/event-adapter")
	build.Dir = "../.."
	build.Env = append(os.Environ(), "GOCACHE=/tmp/go-build")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build sidecar: %v\n%s", err, out)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd := exec.CommandContext(ctx, binPath, "--config", cfgPath)
	cmd.Dir = "../.."
	output := &safeBuffer{}
	cmd.Stdout = output
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sidecar: %v", err)
	}
	t.Cleanup(func() {
		cancel()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})
	waitForOutput(t, output, "processing 1 route")
	input := []byte(`{"specversion":"1.0","id":"evt-e2e-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchheaders":{"X-Workspace-Actor-Id":"user-1","X-Workspace-Tenant-Id":"tenant-a"},"data":{"taskId":"task-1"}}`)
	if _, err := js.Publish("t.tenant-a.app.task.event.created", input); err != nil {
		t.Fatalf("publish input: %v", err)
	}
	msg, err := sub.NextMsg(10 * time.Second)
	if err != nil {
		t.Fatalf("waiting for response: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["type"] != "com.workspace.task.created.processed" {
		t.Fatalf("unexpected response type: %v", response["type"])
	}
	if response["causationid"] != "evt-e2e-1" {
		t.Fatalf("missing causation extension: %#v", response)
	}
}

func httptestApp(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:18080")
	if err != nil {
		t.Fatalf("listen fake app: %v", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/events/task-created", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Workspace-Actor-Id") != "user-1" {
			http.Error(w, "missing actor", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Shutdown(context.Background()) })
	return "http://127.0.0.1:18080"
}

func writeE2EConfig(t *testing.T, appURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "routes.yaml")
	content := fmt.Sprintf(`app:
  id: task-service
  httpBaseURL: %s
nats:
  url: nats://127.0.0.1:4222
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
`, appURL)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

type safeBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func waitForOutput(t *testing.T, output *safeBuffer, want string) {
	t.Helper()
	deadline := time.After(15 * time.Second)
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for sidecar output containing %q; output=%q", want, output.String())
		case <-ticker.C:
			if strings.Contains(output.String(), want) {
				return
			}
		}
	}
}
