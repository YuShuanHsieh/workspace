package natsjs

import (
	"encoding/json"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "client-to-server/internal/cloudevent"
	"client-to-server/internal/processor"
)

func TestBuildDLQPayloadIncludesFailureMetadata(t *testing.T) {
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	dlq := processor.DLQEvent{
		OriginalEvent: &clevent.Event{Event: &ev},
		FailureReason: "backend down",
		HTTPStatus:    503,
		AttemptCount:  3,
		SidecarAppID:  "task-service",
		Timestamp:     time.Date(2026, 5, 27, 0, 0, 0, 0, time.UTC),
	}
	body, err := BuildDLQPayload(dlq)
	if err != nil {
		t.Fatalf("BuildDLQPayload returned error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got["failureReason"] != "backend down" {
		t.Fatalf("missing failure reason: %v", got)
	}
	if got["attemptCount"].(float64) != 3 {
		t.Fatalf("missing attempt count: %v", got)
	}
}
