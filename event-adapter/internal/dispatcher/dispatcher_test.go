package dispatcher

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	pathtemplate "event-adapter/internal/pathtemplate"
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
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/", Timeout: time.Second,
		ForwardHeaders: []string{"X-Workspace-Actor-Id", "X-Workspace-Tenant-Id"},
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
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev)
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
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	// Authorization is intentionally not reserved: publishers may forward it to
	// the dispatch backend.
	if got := headers.Get("Authorization"); got != "Bearer attacker" {
		t.Fatalf("Authorization should be forwarded from publisher: %q", got)
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
	_, err := d.Dispatch(context.Background(), config.DispatchConfig{}, nil)
	if !errors.Is(err, ErrNilEvent) {
		t.Fatalf("expected ErrNilEvent, got %v", err)
	}
}

func TestDispatchForwardsPublisherCookies(t *testing.T) {
	var gotSession, gotCSRF, gotLeak string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if c, err := r.Cookie("session"); err == nil {
			gotSession = c.Value
		}
		if c, err := r.Cookie("csrf-token"); err == nil {
			gotCSRF = c.Value
		}
		gotLeak = r.Header.Get("ce-dispatchcookies")
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
	if _, err := d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotSession != "abc123" || gotCSRF != "xyz789" {
		t.Fatalf("cookies not forwarded: session=%q csrf=%q", gotSession, gotCSRF)
	}
	if gotLeak != "" {
		t.Fatalf("dispatchcookies leaked as ce-dispatchcookies header: %q", gotLeak)
	}
}

func TestDispatchAddsNoCookieHeaderWhenDispatchCookiesAbsent(t *testing.T) {
	var gotCookies []*http.Cookie
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotCookies = r.Cookies()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-nock","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	if _, err := d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if len(gotCookies) != 0 {
		t.Fatalf("expected no cookies on request, got %d: %+v", len(gotCookies), gotCookies)
	}
}

func TestDispatchPopulatesLocationOn3xx(t *testing.T) {
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			h := http.Header{}
			h.Set("Location", "/new-path")
			return &http.Response{
				StatusCode: 307,
				Header:     h,
				Body:       io.NopCloser(bytes.NewBufferString("")),
			}, nil
		}),
	}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-loc","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.StatusCode != 307 {
		t.Fatalf("StatusCode = %d, want 307", res.StatusCode)
	}
	if res.Location != "/new-path" {
		t.Fatalf("Location = %q, want /new-path", res.Location)
	}
}

func TestDispatchLeavesLocationEmptyWhen3xxHasNoLocationHeader(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 304,
			Header:     http.Header{},
			Body:       io.NopCloser(bytes.NewBufferString("")),
		}, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-nl","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Location != "" {
		t.Fatalf("Location = %q, want empty (no Location header on 304)", res.Location)
	}
}

func TestDispatchLeavesLocationEmptyOnNon3xxEvenIfLocationHeaderPresent(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		h := http.Header{}
		h.Set("Location", "/should-be-ignored")
		return &http.Response{
			StatusCode: 200,
			Header:     h,
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-200loc","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.Location != "" {
		t.Fatalf("Location = %q on 200 response, want empty (3xx-only rule)", res.Location)
	}
}

func TestDispatchDefaultClientDoesNotFollowRedirects(t *testing.T) {
	var targetHits int64

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", target.URL+"/landed")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer origin.Close()

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-noredir","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	// Pass nil client so dispatcher uses its default.
	d := New(origin.URL, nil)
	res, err := d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/", Timeout: 2 * time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if res.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("StatusCode = %d, want 307 (must not have followed)", res.StatusCode)
	}
	if got := atomic.LoadInt64(&targetHits); got != 0 {
		t.Fatalf("targetHits = %d, want 0 (default client must not follow redirects)", got)
	}
	if res.Location != target.URL+"/landed" {
		t.Fatalf("Location = %q, want %q", res.Location, target.URL+"/landed")
	}
}

func TestDispatchResolvesPathTemplate(t *testing.T) {
	var gotURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.Path
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchpathparams":{"taskId":"task-42"},"data":{"title":"Buy milk"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/api/tasks/{taskId}/complete", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotURL != "/api/tasks/task-42/complete" {
		t.Fatalf("URL.Path = %q, want /api/tasks/task-42/complete", gotURL)
	}
}

func TestDispatchStaticPathUnchanged(t *testing.T) {
	var gotURL string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotURL = r.URL.Path
		return &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{"ok":true}`)),
		}, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-2","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"x"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/events/task-created", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if gotURL != "/events/task-created" {
		t.Fatalf("URL.Path = %q, want /events/task-created", gotURL)
	}
}

func TestDispatchMissingFieldReturnsErrPermanent(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatal("transport must not be invoked when path resolution fails")
		return nil, nil
	})}
	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-3","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"status":"done"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "POST", Path: "/api/tasks/{taskId}/complete", Timeout: time.Second,
	}, ev)
	if err == nil {
		t.Fatal("expected error for missing field")
	}
	if !errors.Is(err, pathtemplate.ErrPermanent) {
		t.Fatalf("error must wrap pathtemplate.ErrPermanent, got %v", err)
	}
}

func TestDispatchGetSendsNoBody(t *testing.T) {
	var gotBodyBytes []byte
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Body != nil {
			gotBodyBytes, _ = io.ReadAll(r.Body)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-get-nobody","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"userId":"u1","taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "GET", Path: "/api/users", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if len(gotBodyBytes) != 0 {
		t.Fatalf("GET dispatch must send no body, got %d bytes: %s", len(gotBodyBytes), gotBodyBytes)
	}
}

func TestDispatchDeleteSendsCloudEventData(t *testing.T) {
	const wantBody = `{"reason":"cleanup"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want %q", r.Method, http.MethodDelete)
		}
		if r.URL.Path != "/orders/ord-456" {
			t.Errorf("path = %q, want %q", r.URL.Path, "/orders/ord-456")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			return
		}
		if string(body) != wantBody {
			t.Errorf("body = %q, want %q", body, wantBody)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-delete","source":"workspace/orders","type":"com.workspace.orders.deleted","datacontenttype":"application/json","data":{"reason":"cleanup"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New(server.URL, nil)
	if _, err := d.Dispatch(context.Background(), config.DispatchConfig{
		Method: http.MethodDelete, Path: "/orders/ord-456", Timeout: time.Second,
	}, ev); err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
}

func TestDispatchQueryStringPreservedInURL(t *testing.T) {
	var gotRequestURI string
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		gotRequestURI = r.URL.RequestURI()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(bytes.NewBufferString(`{}`)),
		}, nil
	})}

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-qs-1","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","dispatchpathparams":{"userId":"u1","tenantId":"t1"},"data":{}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}

	d := New("http://127.0.0.1:8080", client)
	_, err = d.Dispatch(context.Background(), config.DispatchConfig{
		Method: "GET", Path: "/api/items?userId={userId}&tenantId={tenantId}", Timeout: time.Second,
	}, ev)
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	want := "/api/items?userId=u1&tenantId=t1"
	if gotRequestURI != want {
		t.Fatalf("RequestURI = %q, want %q", gotRequestURI, want)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}
