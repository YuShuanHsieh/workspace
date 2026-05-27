package cloudevent

import (
	"testing"

	ce "github.com/cloudevents/sdk-go/v2/event"

	"event-adapter/internal/config"
)

func TestBuildResponseUsesDeterministicIDAndCausation(t *testing.T) {
	in := ce.New()
	in.SetID("evt-1")
	in.SetSource("workspace/task")
	in.SetType("com.workspace.task.created")
	in.SetExtension("correlationid", "corr-1")

	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.tenant-a.app.task.event.processed"},
	}
	wrapped := &Event{Event: &in}
	a, err := BuildResponse(wrapped, route, 200, "application/json", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("BuildResponse returned error: %v", err)
	}
	b, err := BuildResponse(wrapped, route, 200, "application/json", []byte(`{"ok":true}`))
	if err != nil {
		t.Fatalf("BuildResponse returned error: %v", err)
	}
	if a.ID() != b.ID() {
		t.Fatalf("response id must be deterministic: %q != %q", a.ID(), b.ID())
	}
	if a.Type() != route.Response.Type || a.Source() != route.Response.Source {
		t.Fatalf("unexpected response metadata: type=%q source=%q", a.Type(), a.Source())
	}
	if got := a.Extensions()["causationid"]; got != "evt-1" {
		t.Fatalf("unexpected causationid: %v", got)
	}
	if got := a.Extensions()["correlationid"]; got != "corr-1" {
		t.Fatalf("unexpected correlationid: %v", got)
	}
	if got := a.Extensions()["httpstatus"]; got != int32(200) {
		t.Fatalf("unexpected httpstatus: %v", got)
	}
}

func TestBuildResponseStampsErrorStatus(t *testing.T) {
	in := ce.New()
	in.SetID("evt-1")
	in.SetSource("workspace/task")
	in.SetType("com.workspace.task.created")

	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.tenant-a.app.task.event.processed"},
	}
	wrapped := &Event{Event: &in}
	out, err := BuildResponse(wrapped, route, 422, "application/json", []byte(`{"error":"invalid taskId"}`))
	if err != nil {
		t.Fatalf("BuildResponse returned error: %v", err)
	}
	if got := out.Extensions()["httpstatus"]; got != int32(422) {
		t.Fatalf("unexpected httpstatus: %v", got)
	}
}
