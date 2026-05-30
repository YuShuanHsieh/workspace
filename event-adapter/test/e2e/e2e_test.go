//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
)

// TestEventDispatchPublishesResponse verifies the full sidecar round-trip:
//
//  1. A CloudEvent is published to the input NATS subject.
//  2. The event-adapter dispatches it to the mock-app HTTP handler.
//  3. The event-adapter wraps the HTTP response as a new CloudEvent and
//     publishes it to the response subject.
//  4. The response CloudEvent has the expected type and causation extension.
//
// Prerequisites: run `docker compose up --build -d` from this directory before
// executing the test.
func TestEventDispatchPublishesResponse(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}

	// Ensure the stream exists (nats-setup in compose creates it; this is a
	// safety net for reruns or partial teardowns).
	_, err = js.AddStream(&nats.StreamConfig{
		Name: "workspace-events",
		Subjects: []string{
			"t.tenant-a.app.task.event.created",
			"t.tenant-a.app.task.event.processed",
			"dlq.tenant-a.task-service",
		},
	})
	if err != nil {
		t.Fatalf("ensure stream: %v", err)
	}

	// Purge leftover messages so each test run starts clean.
	if err := js.PurgeStream("workspace-events"); err != nil {
		t.Fatalf("purge stream: %v", err)
	}

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed")
	if err != nil {
		t.Fatalf("subscribe response subject: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Publish the fixture CloudEvent. The id "evt-manual-1" must match the
	// causation assertion below so both the test and the fixture file stay in sync.
	fixture, err := os.ReadFile("fixtures/task-created.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if _, err := js.Publish("t.tenant-a.app.task.event.created", fixture); err != nil {
		t.Fatalf("publish input event: %v", err)
	}

	msg, err := sub.NextMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("waiting for response CloudEvent: %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := response["type"]; got != "com.workspace.task.created.processed" {
		t.Errorf("response type = %v, want com.workspace.task.created.processed", got)
	}
	if got := response["causationid"]; got != "evt-manual-1" {
		t.Errorf("causationid = %v, want evt-manual-1", got)
	}
}
