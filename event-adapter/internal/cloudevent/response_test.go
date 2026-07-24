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
	wantID := deterministicID(in.ID(), route.Name, route.Response.Type, route.Response.Subject)
	if a.ID() != wantID {
		t.Errorf("response id = %q, want legacy id %q", a.ID(), wantID)
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

func TestBuildResponsePreservesLegacyIDAcrossIncomingSources(t *testing.T) {
	first := mustEvent(t, `{"specversion":"1.0","id":"evt-shared","source":"workspace/tasks-a","type":"com.workspace.task.created","data":{}}`)
	second := mustEvent(t, `{"specversion":"1.0","id":"evt-shared","source":"workspace/tasks-b","type":"com.workspace.task.created","data":{}}`)
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "com.workspace.task.processed", Source: "task-service", Subject: "tasks.processed"},
	}

	a, err := BuildResponse(first, route, 200, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildResponse first: %v", err)
	}
	b, err := BuildResponse(second, route, 200, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildResponse second: %v", err)
	}
	if a.ID() != b.ID() {
		t.Errorf("legacy response IDs must ignore incoming source: %q != %q", a.ID(), b.ID())
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
	out, err := BuildReply(in, reply, "upload-presign", 200, "application/json", []byte(`{"url":"https://s3/put"}`), "")
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
	wantID := deterministicID(in.ID(), "upload-presign", reply.Type)
	if out.ID() != wantID {
		t.Errorf("static reply id = %q, want legacy id %q", out.ID(), wantID)
	}
}

func TestBuildDirectReplyUsesGenericEnvelope(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"req-direct","source":"client","type":"orders.delete","correlationid":"corr-1","data":{}}`)

	a, err := BuildDirectReply(in, DirectReplyConfig("order-service"), DirectRouteName, 204, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildDirectReply: %v", err)
	}
	b, err := BuildDirectReply(in, DirectReplyConfig("order-service"), DirectRouteName, 204, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildDirectReply: %v", err)
	}

	if a.Type() != DirectReplyType {
		t.Errorf("type = %q, want %q", a.Type(), DirectReplyType)
	}
	if a.Source() != "order-service" {
		t.Errorf("source = %q, want %q", a.Source(), "order-service")
	}
	if a.Subject() != "" {
		t.Errorf("reply must have no subject, got %q", a.Subject())
	}
	if got := a.Extensions()["httpstatus"]; got != int32(204) {
		t.Errorf("httpstatus = %v, want %v", got, int32(204))
	}
	if got := a.Extensions()["causationid"]; got != "req-direct" {
		t.Errorf("causationid = %v, want %q", got, "req-direct")
	}
	if got := a.Extensions()["correlationid"]; got != "corr-1" {
		t.Errorf("correlationid = %v, want %q", got, "corr-1")
	}
	if a.ID() == "" || a.ID() != b.ID() {
		t.Errorf("direct reply id must be nonempty and deterministic: %q != %q", a.ID(), b.ID())
	}
}

func TestBuildDirectReplyIDIncludesIncomingSource(t *testing.T) {
	first := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-a","type":"orders.delete","data":{}}`)
	second := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-b","type":"orders.delete","data":{}}`)
	reply := DirectReplyConfig("order-service")

	a, err := BuildDirectReply(first, reply, DirectRouteName, 204, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildDirectReply first: %v", err)
	}
	b, err := BuildDirectReply(second, reply, DirectRouteName, 204, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildDirectReply second: %v", err)
	}
	if a.ID() == b.ID() {
		t.Errorf("direct reply IDs collide for distinct CloudEvent sources")
	}
}

func TestBuildReplyRequiresDirectRouteAndTypeForSourceAwareID(t *testing.T) {
	first := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-a","type":"orders.delete","data":{}}`)
	second := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-b","type":"orders.delete","data":{}}`)
	tests := []struct {
		name      string
		routeName string
		replyType string
	}{
		{name: "direct route with static reply type", routeName: DirectRouteName, replyType: "orders.configured.reply"},
		{name: "static route with direct reply type", routeName: "configured-route", replyType: DirectReplyType},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reply := config.ReplyConfig{Source: "order-service", Type: tt.replyType}
			a, err := BuildReply(first, reply, tt.routeName, 204, "application/json", nil, "")
			if err != nil {
				t.Fatalf("BuildReply first: %v", err)
			}
			b, err := BuildReply(second, reply, tt.routeName, 204, "application/json", nil, "")
			if err != nil {
				t.Fatalf("BuildReply second: %v", err)
			}
			wantID := deterministicID(first.ID(), tt.routeName, tt.replyType)
			if a.ID() != wantID || b.ID() != wantID {
				t.Errorf("non-direct reply IDs must preserve legacy formula")
			}
		})
	}
}

func TestBuildReplyPreservesLegacyIDWhenStaticRouteUsesDirectMetadataValues(t *testing.T) {
	first := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-a","type":"orders.delete","data":{}}`)
	second := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-b","type":"orders.delete","data":{}}`)
	reply := config.ReplyConfig{Source: "order-service", Type: DirectReplyType}

	a, err := BuildReply(first, reply, DirectRouteName, 204, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildReply first: %v", err)
	}
	b, err := BuildReply(second, reply, DirectRouteName, 204, "application/json", nil, "")
	if err != nil {
		t.Fatalf("BuildReply second: %v", err)
	}
	wantID := deterministicID(first.ID(), DirectRouteName, DirectReplyType)
	if a.ID() != wantID || b.ID() != wantID {
		t.Errorf("static reply IDs must preserve legacy formula: got %q and %q, want %q", a.ID(), b.ID(), wantID)
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

func TestBuildReplySetsHTTPLocationWhenNonEmpty(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"req-loc-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	reply := config.ReplyConfig{Type: "x.reply", Source: "upload-service"}

	out, err := BuildReply(in, reply, "upload-presign", 307, "application/json", []byte(""), "/elsewhere")
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	got, ok := out.Extensions()["httplocation"]
	if !ok {
		t.Fatal("expected httplocation extension on reply")
	}
	if got != "/elsewhere" {
		t.Fatalf("httplocation = %v, want /elsewhere", got)
	}
}

func TestBuildReplyOmitsHTTPLocationWhenEmpty(t *testing.T) {
	in := mustEvent(t, `{"specversion":"1.0","id":"req-loc-2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`)
	reply := config.ReplyConfig{Type: "x.reply", Source: "upload-service"}

	out, err := BuildReply(in, reply, "upload-presign", 200, "application/json", []byte(`{"ok":true}`), "")
	if err != nil {
		t.Fatalf("BuildReply: %v", err)
	}
	if _, present := out.Extensions()["httplocation"]; present {
		t.Fatal("httplocation extension must be absent when location is empty")
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
	wantID := deterministicID(in.ID(), "upload-service", ErrorReplyType)
	if out.ID() != wantID {
		t.Errorf("error reply id = %q, want legacy id %q", out.ID(), wantID)
	}

	other := mustEvent(t, `{"specversion":"1.0","id":"req-405","source":"client","type":"com.x.request","data":{"a":1}}`)
	out2 := BuildErrorReply(other, "upload-service", 404, "no matching route")
	if out.ID() == out2.ID() {
		t.Fatalf("error-reply IDs must vary by triggering request, got %q", out.ID())
	}
}

func TestBuildErrorReplyPreservesLegacyIDAcrossIncomingSources(t *testing.T) {
	first := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-a","type":"com.x.request","data":{}}`)
	second := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-b","type":"com.x.request","data":{}}`)

	a := BuildErrorReply(first, "upload-service", 404, "no matching route")
	b := BuildErrorReply(second, "upload-service", 404, "no matching route")
	if a.ID() != b.ID() {
		t.Errorf("legacy error reply IDs must ignore incoming source: %q != %q", a.ID(), b.ID())
	}
}

func TestBuildDirectErrorReplyIncludesIncomingSource(t *testing.T) {
	first := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-a","type":"orders.delete","correlationid":"corr-1","data":{}}`)
	second := mustEvent(t, `{"specversion":"1.0","id":"req-shared","source":"client-b","type":"orders.delete","correlationid":"corr-1","data":{}}`)

	a := BuildDirectErrorReply(first, "order-service", 400, "invalid target")
	aAgain := BuildDirectErrorReply(first, "order-service", 400, "invalid target")
	b := BuildDirectErrorReply(second, "order-service", 400, "invalid target")
	if a.ID() == b.ID() {
		t.Errorf("direct error reply IDs collide for distinct CloudEvent sources")
	}
	if a.ID() != aAgain.ID() {
		t.Errorf("direct error reply ID is not deterministic: %q != %q", a.ID(), aAgain.ID())
	}
	if a.Type() != ErrorReplyType || a.Source() != "order-service" {
		t.Errorf("unexpected envelope type=%q source=%q", a.Type(), a.Source())
	}
	if got := a.Extensions()["httpstatus"]; got != int32(400) {
		t.Errorf("httpstatus = %v, want 400", got)
	}
	if got := a.Extensions()["causationid"]; got != "req-shared" {
		t.Errorf("causationid = %v, want req-shared", got)
	}
	if got := a.Extensions()["correlationid"]; got != "corr-1" {
		t.Errorf("correlationid = %v, want corr-1", got)
	}
	var data map[string]string
	if err := a.DataAs(&data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data["error"] != "invalid target" {
		t.Errorf("error body = %v", data)
	}

	directNil := BuildDirectErrorReply(nil, "order-service", 400, "invalid target")
	legacyNil := BuildErrorReply(nil, "order-service", 400, "invalid target")
	if directNil.ID() != legacyNil.ID() {
		t.Errorf("nil direct error ID must retain legacy message-based formula")
	}
}
