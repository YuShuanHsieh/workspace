package dispatcher

import (
	"bytes"
	"context"
	"errors"
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

func TestDispatchForwardsAllDispatchHeadersWhenAllowlistEmpty(t *testing.T) {
	var headers http.Header
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers = r.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchheaders":{"X-Workspace-Actor-Id":"user-1","X-Workspace-Tenant-Id":"tenant-a","X-Custom-Trace":"abc"},"data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.RouteConfig{
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second},
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if got := headers.Get("X-Workspace-Actor-Id"); got != "user-1" {
		t.Fatalf("expected actor forwarded by default, got %q", got)
	}
	if got := headers.Get("X-Workspace-Tenant-Id"); got != "tenant-a" {
		t.Fatalf("expected tenant forwarded by default, got %q", got)
	}
	if got := headers.Get("X-Custom-Trace"); got != "abc" {
		t.Fatalf("expected custom header forwarded by default, got %q", got)
	}
}

func TestDispatchDropsReservedDispatchHeadersAtRuntime(t *testing.T) {
	var headers http.Header
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		headers = r.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-3","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchheaders":{"Authorization":"Bearer attacker","Idempotency-Key":"attacker-key","ce-id":"spoof","X-Workspace-Actor-Id":"user-1"},"data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.RouteConfig{
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second},
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if got := headers.Get("Authorization"); got != "" {
		t.Fatalf("Authorization must not be forwarded from publisher: %q", got)
	}
	if got := headers.Get("Idempotency-Key"); got != "evt-3" {
		t.Fatalf("publisher must not override Idempotency-Key: %q", got)
	}
	if got := headers.Get("ce-id"); got != "evt-3" {
		t.Fatalf("publisher must not override ce-id: %q", got)
	}
	if got := headers.Get("X-Workspace-Actor-Id"); got != "user-1" {
		t.Fatalf("non-reserved publisher header should still forward: %q", got)
	}
}

func TestDispatchRejectsNilEvent(t *testing.T) {
	d := New("http://127.0.0.1:8080", nil)
	_, err := d.Dispatch(context.Background(), config.RouteConfig{}, nil)
	if !errors.Is(err, ErrNilEvent) {
		t.Fatalf("expected ErrNilEvent, got %v", err)
	}
}

func TestDispatchForwardsPublisherCookies(t *testing.T) {
	var gotSession, gotCSRF string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if c, err := r.Cookie("session"); err == nil {
			gotSession = c.Value
		}
		if c, err := r.Cookie("csrf-token"); err == nil {
			gotCSRF = c.Value
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-ck","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchcookies":{"session":"abc123","csrf-token":"xyz789"},"data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	if _, err := d.Dispatch(context.Background(), config.RouteConfig{
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second},
	}, ev); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotSession != "abc123" || gotCSRF != "xyz789" {
		t.Fatalf("cookies not forwarded: session=%q csrf=%q", gotSession, gotCSRF)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
