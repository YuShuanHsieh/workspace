package natsjs

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/processor"
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
	if got["lastHTTPStatus"].(float64) != 503 {
		t.Fatalf("missing http status: %v", got)
	}
	if got["sidecarAppID"] != "task-service" {
		t.Fatalf("missing sidecar app id: %v", got)
	}
	if got["timestamp"] == "" {
		t.Fatalf("missing timestamp: %v", got)
	}
	if _, err := time.Parse(time.RFC3339Nano, got["timestamp"].(string)); err != nil {
		t.Fatalf("timestamp is not RFC3339Nano: %v", err)
	}
}

func TestPublishResponseRejectsNilEvent(t *testing.T) {
	err := (&Client{}).PublishResponse(context.Background(), "response.subject", nil)
	if err == nil || !strings.Contains(err.Error(), "nil event") {
		t.Fatalf("expected nil event error, got %v", err)
	}
}

func TestFetchOneRejectsNilSubscription(t *testing.T) {
	_, err := FetchOne(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "subscription is nil") {
		t.Fatalf("expected nil subscription error, got %v", err)
	}
}
