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
	a, err := BuildResponse(wrapped, route, 200, "application/json", []byte(`{"ok":true}`), "")
	if err != nil {
		t.Fatalf("BuildResponse returned error: %v", err)
	}
	b, err := BuildResponse(wrapped, route, 200, "application/json", []byte(`{"ok":true}`), "")
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
	out, err := BuildResponse(wrapped, route, 422, "application/json", []byte(`{"error":"invalid taskId"}`), "")
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

func TestBuildResponseSetsHTTPLocationWhenNonEmpty(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"evt-loc-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "x.processed", Source: "task-service", Subject: "out"},
	}

	out, err := BuildResponse(in, route, 307, "application/json", []byte(""), "/new-path")
	if err != nil {
		t.Fatalf("BuildResponse: %v", err)
	}
	got, ok := out.Extensions()["httplocation"]
	if !ok {
		t.Fatalf("expected httplocation extension to be set")
	}
	if got != "/new-path" {
		t.Fatalf("httplocation = %v, want /new-path", got)
	}
}

func TestBuildResponseOmitsHTTPLocationWhenEmpty(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"evt-loc-2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "x.processed", Source: "task-service", Subject: "out"},
	}

	out, err := BuildResponse(in, route, 200, "application/json", []byte(`{"ok":true}`), "")
	if err != nil {
		t.Fatalf("BuildResponse: %v", err)
	}
	if _, present := out.Extensions()["httplocation"]; present {
		t.Fatalf("httplocation extension must not be set when location is empty")
	}
}

func TestBuildErrorReply(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"req-404","source":"client","type":"com.x.request","correlationid":"corr-404","data":{"a":1}}`)
	out := BuildErrorReply(in, "upload-service", 404, "no matching route")
	if out.Type() != ErrorReplyType {
		t.Errorf("type = %q, want %q", out.Type(), ErrorReplyType)
	}
	if out.Source() != "upload-service" {
		t.Errorf("source = %q", out.Source())
	}
	if got := out.Extensions()["httpstatus"]; got != int32(404) {
		t.Errorf("httpstatus = %v", got)
	}
	if got := out.Extensions()["causationid"]; got != "req-404" {
		t.Errorf("causationid = %v", got)
	}
	if got := out.Extensions()["correlationid"]; got != "corr-404" {
		t.Errorf("correlationid = %v", got)
	}
	var data map[string]string
	if err := out.DataAs(&data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data["error"] != "no matching route" {
		t.Errorf("error body = %v", data)
	}

	other := mustEvent(t, `{"specversion":"1.0","id":"req-405","source":"client","type":"com.x.request","data":{"a":1}}`)
	out2 := BuildErrorReply(other, "upload-service", 404, "no matching route")
	if out.ID() == out2.ID() {
		t.Fatalf("error-reply IDs must vary by triggering request, got %q", out.ID())
	}
}
