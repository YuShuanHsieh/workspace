//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
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

	ensureEmptyStream(t, js)

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed")
	if err != nil {
		t.Fatalf("subscribe response subject: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	// Read the fixture and extract its id so the causation assertion derives
	// from the same source of truth rather than a duplicated string literal.
	fixture, err := os.ReadFile("fixtures/task-created.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var input map[string]any
	if err := json.Unmarshal(fixture, &input); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	inputID, _ := input["id"].(string)

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
	if got := response["causationid"]; got != inputID {
		t.Errorf("causationid = %v, want %v (from fixture id)", got, inputID)
	}
}

func TestEventDispatchHandlesBurst(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	ensureEmptyStream(t, js)

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed")
	if err != nil {
		t.Fatalf("subscribe responses: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	const burst = 25
	for i := 0; i < burst; i++ {
		payload := []byte(`{"specversion":"1.0","id":"burst-` + strconv.Itoa(i) +
			`","source":"workspace/task","type":"com.workspace.task.created",` +
			`"datacontenttype":"application/json","data":{"taskId":"t1"}}`)
		if _, err := js.Publish("t.tenant-a.app.task.event.created", payload); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	received := 0
	deadline := time.Now().Add(15 * time.Second)
	for received < burst && time.Now().Before(deadline) {
		msg, err := sub.NextMsg(2 * time.Second)
		if err != nil {
			continue
		}
		var got map[string]any
		if jErr := json.Unmarshal(msg.Data, &got); jErr != nil {
			t.Fatalf("response not json: %v", jErr)
		}
		received++
	}
	if received != burst {
		t.Fatalf("expected %d responses, got %d", burst, received)
	}
}

// ensureEmptyStream guarantees the workspace-events stream exists and is empty.
// nats-setup in docker compose creates it on first run; this is a safety net
// for reruns and partial teardowns.
func ensureEmptyStream(t *testing.T, js nats.JetStreamContext) {
	t.Helper()
	_, err := js.AddStream(&nats.StreamConfig{
		Name: "workspace-events",
		Subjects: []string{
			"t.tenant-a.app.task.event.created",
			"t.tenant-a.app.task.event.processed",
			"dlq.tenant-a.task-service",
		},
	})
	if err != nil && !strings.Contains(err.Error(), "stream name already in use") {
		t.Fatalf("ensure stream: %v", err)
	}
	if err := js.PurgeStream("workspace-events"); err != nil {
		t.Fatalf("purge stream: %v", err)
	}
}
