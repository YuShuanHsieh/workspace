package dispatcher

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
)

func TestDispatchForwardsDataAndHeaders(t *testing.T) {
	var gotBody string
	var gotID string
	var gotIdempotency string
	var gotActor string
	var gotTenant string
	var gotIgnored string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		gotID = r.Header.Get("ce-id")
		gotIdempotency = r.Header.Get("Idempotency-Key")
		gotActor = r.Header.Get("X-Workspace-Actor-Id")
		gotTenant = r.Header.Get("X-Workspace-Tenant-Id")
		gotIgnored = r.Header.Get("X-Not-Allowlisted")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchheaders":{"X-Workspace-Actor-Id":"user-1","X-Workspace-Tenant-Id":"tenant-a","X-Not-Allowlisted":"drop-me"},"data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.RouteConfig{
		Dispatch: config.DispatchConfig{
			Method: "POST", Path: "/", Timeout: time.Second,
			ForwardHeaders: []string{"X-Workspace-Actor-Id", "X-Workspace-Tenant-Id"},
		},
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.StatusCode != 200 || string(res.Body) != `{"ok":true}` {
		t.Fatalf("unexpected response: %+v", res)
	}
	if gotBody != `{"taskId":"t1"}` {
		t.Fatalf("unexpected body: %s", gotBody)
	}
	if gotID != "evt-1" || gotIdempotency != "evt-1" {
		t.Fatalf("missing id headers: ce-id=%q idempotency=%q", gotID, gotIdempotency)
	}
	if gotActor != "user-1" || gotTenant != "tenant-a" {
		t.Fatalf("missing publisher headers: actor=%q tenant=%q", gotActor, gotTenant)
	}
	if gotIgnored != "" {
		t.Fatalf("non-allowlisted publisher header was forwarded: %q", gotIgnored)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
