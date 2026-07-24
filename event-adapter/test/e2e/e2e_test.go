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

	// The fixture carries a dispatchcookies.session value, and mock-app.yaml
	// declares requireCookies: [session]. mock-app returns 400 when that cookie
	// is missing, which fails the dispatch and yields no response CloudEvent. So
	// a received response below proves the cookie survived the full round-trip.
	cookies, _ := input["dispatchcookies"].(map[string]any)
	if _, ok := cookies["session"]; !ok {
		t.Fatalf("fixture must carry dispatchcookies.session to exercise cookie forwarding")
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

	seen := make(map[string]struct{}, burst)
	deadline := time.Now().Add(15 * time.Second)
	for len(seen) < burst && time.Now().Before(deadline) {
		msg, err := sub.NextMsg(2 * time.Second)
		if err != nil {
			continue
		}
		var got map[string]any
		if jErr := json.Unmarshal(msg.Data, &got); jErr != nil {
			t.Fatalf("response not json: %v", jErr)
		}
		id, _ := got["causationid"].(string)
		if id == "" {
			t.Fatalf("missing causationid in response: %v", got)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != burst {
		t.Fatalf("expected %d unique responses, got %d", burst, len(seen))
	}
}

func TestRequestReplyPresign(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	fixture, err := os.ReadFile("fixtures/upload-presign.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	msg, err := nc.Request("q.tenant-a.app.uploads.request", fixture, 15*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var reply map[string]any
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply["type"] != "com.workspace.uploads.presign.reply" {
		t.Errorf("type = %v", reply["type"])
	}
	if reply["causationid"] != "req-presign-1" {
		t.Errorf("causationid = %v", reply["causationid"])
	}
	status, ok := reply["httpstatus"].(float64)
	if !ok || status != 200 {
		t.Errorf("httpstatus = %v, want 200", reply["httpstatus"])
	}
	data, _ := reply["data"].(map[string]any)
	if data["uploadId"] != "up-1" {
		t.Errorf("data.uploadId = %v, want up-1", data["uploadId"])
	}
}

func TestRequestReplyDirectDelete(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	fixture, err := os.ReadFile("fixtures/direct-delete.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	msg, err := nc.Request("q.tenant-a.app.uploads.request", fixture, 15*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var reply map[string]any
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply["type"] != "io.eventadapter.direct.reply" {
		t.Errorf("type = %v, want io.eventadapter.direct.reply", reply["type"])
	}
	if reply["source"] != "task-service" {
		t.Errorf("source = %v, want task-service", reply["source"])
	}
	if reply["causationid"] != "req-direct-delete-1" {
		t.Errorf("causationid = %v, want req-direct-delete-1", reply["causationid"])
	}
	if reply["correlationid"] != "corr-direct-delete-1" {
		t.Errorf("correlationid = %v, want corr-direct-delete-1", reply["correlationid"])
	}
	status, ok := reply["httpstatus"].(float64)
	if !ok || status != 200 {
		t.Errorf("httpstatus = %v, want 200", reply["httpstatus"])
	}
	data, ok := reply["data"].(map[string]any)
	if !ok {
		t.Fatalf("response.data is not an object: %v", reply["data"])
	}
	if data["deleted"] != "ord-456" {
		t.Errorf("data.deleted = %v, want ord-456", data["deleted"])
	}
	if data["hard"] != true {
		t.Errorf("data.hard = %v, want true", data["hard"])
	}
}

// TestJetStreamRedirectPublishesHTTPLocation verifies that when the dispatched
// HTTP endpoint returns a 3xx with a Location header, the response CloudEvent
// published on the response subject carries httpstatus=307 and httplocation,
// AND that the adapter does not follow the redirect (the redirect target
// handler is never invoked).
func TestJetStreamRedirectPublishesHTTPLocation(t *testing.T) {
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

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed.redirect")
	if err != nil {
		t.Fatalf("subscribe redirect response subject: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	fixture, err := os.ReadFile("fixtures/redirect-jetstream.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	if _, err := js.Publish("t.tenant-a.app.task.event.created", fixture); err != nil {
		t.Fatalf("publish input event: %v", err)
	}

	msg, err := sub.NextMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("waiting for redirect response: %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	status, ok := response["httpstatus"].(float64)
	if !ok || status != 307 {
		t.Fatalf("httpstatus = %v, want 307", response["httpstatus"])
	}
	loc, ok := response["httplocation"].(string)
	if !ok {
		t.Fatalf("httplocation missing from response event: %v", response)
	}
	if loc != "/events/post-redirect" {
		t.Fatalf("httplocation = %q, want /events/post-redirect", loc)
	}
}

func TestRequestReplyRedirectCarriesHTTPLocation(t *testing.T) {
	nc, err := nats.Connect("nats://127.0.0.1:4222")
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	defer nc.Close()

	fixture, err := os.ReadFile("fixtures/redirect-reqreply.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	msg, err := nc.Request("q.tenant-a.app.uploads.request", fixture, 15*time.Second)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	var reply map[string]any
	if err := json.Unmarshal(msg.Data, &reply); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	if reply["type"] != "com.workspace.redirect.reply" {
		t.Fatalf("type = %v, want com.workspace.redirect.reply", reply["type"])
	}
	status, ok := reply["httpstatus"].(float64)
	if !ok || status != 307 {
		t.Fatalf("httpstatus = %v, want 307", reply["httpstatus"])
	}
	loc, ok := reply["httplocation"].(string)
	if !ok {
		t.Fatalf("httplocation missing from reply: %v", reply)
	}
	if loc != "/events/post-redirect" {
		t.Fatalf("httplocation = %q, want /events/post-redirect", loc)
	}
}

func TestPathTemplateResolvesFromEventData(t *testing.T) {
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

	sub, err := js.SubscribeSync("t.tenant-a.app.task.event.processed.template")
	if err != nil {
		t.Fatalf("subscribe template response subject: %v", err)
	}
	defer func() { _ = sub.Unsubscribe() }()

	fixture, err := os.ReadFile("fixtures/path-template.json")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	if _, err := js.Publish("t.tenant-a.app.task.event.created", fixture); err != nil {
		t.Fatalf("publish input event: %v", err)
	}

	msg, err := sub.NextMsg(15 * time.Second)
	if err != nil {
		t.Fatalf("waiting for response: %v", err)
	}

	var response map[string]any
	if err := json.Unmarshal(msg.Data, &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	status, ok := response["httpstatus"].(float64)
	if !ok || status != 200 {
		t.Fatalf("httpstatus = %v, want 200", response["httpstatus"])
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatalf("response.data is not an object: %v", response["data"])
	}
	if data["path"] != "/api/tasks/e2e-task-1/complete" {
		t.Fatalf("echoed path = %v, want /api/tasks/e2e-task-1/complete", data["path"])
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
			"t.tenant-a.app.task.event.processed.redirect",
			"t.tenant-a.app.task.event.processed.template",
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
