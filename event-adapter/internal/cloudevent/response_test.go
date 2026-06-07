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
	if a.Subject() != route.Response.Subject {
		t.Fatalf("unexpected response subject: %q", a.Subject())
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

func mustEvent(t *testing.T, s string) *Event {
	t.Helper()
	ev, err := Parse([]byte(s))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	return ev
}

func TestBuildReplySuccess(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"req-1","source":"client","type":"com.x.request","datacontenttype":"application/json","data":{"a":1},"correlationid":"corr-9"}`)
	reply := config.ReplyConfig{Source: "upload-service", Type: "com.x.reply"}
	out, err := BuildReply(in, reply, "upload-presign", 200, "application/json", []byte(`{"url":"https://s3/put"}`))
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	if out.Type() != "com.x.reply" {
		t.Errorf("type = %q", out.Type())
	}
	if out.Source() != "upload-service" {
		t.Errorf("source = %q", out.Source())
	}
	if out.Subject() != "" {
		t.Errorf("reply must have no subject, got %q", out.Subject())
	}
	if got := out.Extensions()["httpstatus"]; got != int32(200) {
		t.Errorf("httpstatus = %v", got)
	}
	if got := out.Extensions()["causationid"]; got != "req-1" {
		t.Errorf("causationid = %v", got)
	}
	if got := out.Extensions()["correlationid"]; got != "corr-9" {
		t.Errorf("correlationid = %v", got)
	}
}

func TestBuildErrorReply(t *testing.T) {
	out := BuildErrorReply("upload-service", 400, "bad cloudevent")
	if out.Type() != ErrorReplyType {
		t.Errorf("type = %q, want %q", out.Type(), ErrorReplyType)
	}
	if out.Source() != "upload-service" {
		t.Errorf("source = %q", out.Source())
	}
	if got := out.Extensions()["httpstatus"]; got != int32(400) {
		t.Errorf("httpstatus = %v", got)
	}
	var data map[string]string
	if err := out.DataAs(&data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data["error"] != "bad cloudevent" {
		t.Errorf("error body = %v", data)
	}
}
