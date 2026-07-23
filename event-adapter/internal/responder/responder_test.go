package responder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/natsjs"
	pathtemplate "event-adapter/internal/pathtemplate"
)

type fakeDispatcher struct {
	res dispatcher.Result
	err error
}

func (f fakeDispatcher) Dispatch(_ context.Context, _ config.DispatchConfig, _ *clevent.Event) (dispatcher.Result, error) {
	return f.res, f.err
}

// panicDispatcher simulates a handler that panics mid-dispatch (e.g. a nil-map
// access in downstream code), exercising the responder's panic backstop.
type panicDispatcher struct{ msg string }

func (p panicDispatcher) Dispatch(context.Context, config.DispatchConfig, *clevent.Event) (dispatcher.Result, error) {
	panic(p.msg)
}

type fakeMetrics struct {
	mu                                              sync.Mutex
	received, dispatchErr, noReply, invalid, panics int
}

func (f *fakeMetrics) RequestReceived(context.Context, string) {
	f.mu.Lock()
	f.received++
	f.mu.Unlock()
}
func (f *fakeMetrics) RequestReplyLatency(context.Context, string, time.Duration) {}
func (f *fakeMetrics) RequestDispatchError(context.Context, string) {
	f.mu.Lock()
	f.dispatchErr++
	f.mu.Unlock()
}
func (f *fakeMetrics) RequestNoReply(context.Context) {
	f.mu.Lock()
	f.noReply++
	f.mu.Unlock()
}
func (f *fakeMetrics) InvalidRequestEvent(context.Context, string) {
	f.mu.Lock()
	f.invalid++
	f.mu.Unlock()
}
func (f *fakeMetrics) PanicRecovered(context.Context, string) {
	f.mu.Lock()
	f.panics++
	f.mu.Unlock()
}

func newResponder(d Dispatcher, m Metrics) *Responder {
	matcher, _ := newTestMatcher()
	return New(matcher, d, m, "upload-service", &config.RequestsConfig{
		Subject: "s", QueueGroup: "g", WorkerPoolSize: 2,
		Routes: []config.RequestRouteConfig{{
			Name:     "upload-presign",
			Match:    config.RequestMatchConfig{Type: "com.x.request"},
			Dispatch: config.DispatchConfig{Method: "POST", Path: "/r", Timeout: time.Second},
			Reply:    config.ReplyConfig{Source: "upload-service", Type: "com.x.reply"},
		}},
	}, io.Discard)
}

func capture() (*natsjs.RequestMsg, *[]byte) {
	var out []byte
	m := &natsjs.RequestMsg{
		ReplyTo: "_INBOX.1",
		Data:    []byte(`{"specversion":"1.0","id":"req-1","source":"c","type":"com.x.request","data":{"k":1}}`),
		Respond: func(b []byte) error { out = b; return nil },
	}
	return m, &out
}

func decode(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("decode reply: %v", err)
	}
	return v
}

func TestHandleSuccess(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"url":"https://s3/put"}`)}}
	met := &fakeMetrics{}
	r := newResponder(d, met)
	m, out := capture()
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	if reply["type"] != "com.x.reply" {
		t.Errorf("type = %v", reply["type"])
	}
	if reply["httpstatus"].(float64) != 200 {
		t.Errorf("httpstatus = %v", reply["httpstatus"])
	}
	if reply["causationid"] != "req-1" {
		t.Errorf("causationid = %v", reply["causationid"])
	}
	if met.received != 1 {
		t.Errorf("received = %d", met.received)
	}
}

func TestHandleAppRejectionIsNormalReply(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 400, ContentType: "application/json", Body: []byte(`{"error":"bad type"}`)}}
	r := newResponder(d, &fakeMetrics{})
	m, out := capture()
	r.handle(context.Background(), *m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 400 {
		t.Errorf("httpstatus = %v, want 400 forwarded", reply["httpstatus"])
	}
	if reply["type"] != "com.x.reply" {
		t.Errorf("type = %v, want normal reply type", reply["type"])
	}
}

func TestHandleDispatchErrorReplies502(t *testing.T) {
	d := fakeDispatcher{err: errors.New("connection refused")}
	met := &fakeMetrics{}
	r := newResponder(d, met)
	m, out := capture()
	r.handle(context.Background(), *m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 502 {
		t.Errorf("httpstatus = %v, want 502", reply["httpstatus"])
	}
	if met.dispatchErr != 1 {
		t.Errorf("dispatchErr = %d", met.dispatchErr)
	}
}

func TestHandleTimeoutReplies504(t *testing.T) {
	d := fakeDispatcher{err: context.DeadlineExceeded}
	r := newResponder(d, &fakeMetrics{})
	m, out := capture()
	r.handle(context.Background(), *m)
	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 504 {
		t.Errorf("httpstatus = %v, want 504", reply["httpstatus"])
	}
}

func TestHandlePermanentPathErrorRepliesWith400(t *testing.T) {
	permErr := fmt.Errorf("dispatcher: resolve path: %w", pathtemplate.ErrPermanent)
	d := fakeDispatcher{err: permErr}
	met := &fakeMetrics{}
	r := newResponder(d, met)
	m, out := capture()
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	status, ok := reply["httpstatus"].(float64)
	if !ok || status != 400 {
		t.Fatalf("httpstatus = %v, want 400", reply["httpstatus"])
	}
	if met.dispatchErr != 1 {
		t.Fatalf("dispatchErr metric = %d, want 1", met.dispatchErr)
	}
}

func TestHandle3xxWithLocationRepliesWithHTTPLocation(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{
		StatusCode:  307,
		ContentType: "",
		Body:        []byte(""),
		Location:    "/moved",
	}}
	r := newResponder(d, &fakeMetrics{})
	m, out := capture()
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 307 {
		t.Fatalf("httpstatus = %v, want 307", reply["httpstatus"])
	}
	got, ok := reply["httplocation"].(string)
	if !ok {
		t.Fatalf("httplocation missing from reply: %v", reply)
	}
	if got != "/moved" {
		t.Fatalf("httplocation = %q, want /moved", got)
	}
}

func TestHandleParseErrorReplies400(t *testing.T) {
	r := newResponder(fakeDispatcher{}, &fakeMetrics{})
	out := []byte(nil)
	m := natsjs.RequestMsg{ReplyTo: "_INBOX.1", Data: []byte(`not json`), Respond: func(b []byte) error { out = b; return nil }}
	r.handle(context.Background(), m)
	reply := decode(t, out)
	if reply["httpstatus"].(float64) != 400 {
		t.Errorf("httpstatus = %v, want 400", reply["httpstatus"])
	}
	if reply["type"] != clevent.ErrorReplyType {
		t.Errorf("type = %v, want error reply type", reply["type"])
	}
}

func TestHandleNoRoutePreservesRequestIdentity(t *testing.T) {
	matcher, _ := newEmptyTestMatcher()
	r := New(matcher, fakeDispatcher{}, &fakeMetrics{}, "upload-service", &config.RequestsConfig{
		Subject: "s", QueueGroup: "g", WorkerPoolSize: 2,
	}, io.Discard)
	var out []byte
	m := natsjs.RequestMsg{
		ReplyTo: "_INBOX.1",
		Data:    []byte(`{"specversion":"1.0","id":"req-404","source":"c","type":"com.x.unknown","correlationid":"corr-404","data":{"k":1}}`),
		Respond: func(b []byte) error { out = b; return nil },
	}

	r.handle(context.Background(), m)
	reply := decode(t, out)
	if reply["httpstatus"].(float64) != 404 {
		t.Errorf("httpstatus = %v, want 404", reply["httpstatus"])
	}
	if reply["causationid"] != "req-404" {
		t.Errorf("causationid = %v, want req-404", reply["causationid"])
	}
	if reply["correlationid"] != "corr-404" {
		t.Errorf("correlationid = %v, want corr-404", reply["correlationid"])
	}
}

func TestHandleRecoversFromPanicAndReplies500(t *testing.T) {
	met := &fakeMetrics{}
	r := newResponder(panicDispatcher{msg: "boom"}, met)
	m, out := capture()

	// Must not propagate the panic; a regressed handler would crash the worker
	// goroutine and take down the whole sidecar.
	r.handle(context.Background(), *m)

	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 500 {
		t.Errorf("httpstatus = %v, want 500", reply["httpstatus"])
	}
	if reply["type"] != clevent.ErrorReplyType {
		t.Errorf("type = %v, want error reply type", reply["type"])
	}
	if reply["causationid"] != "req-1" {
		t.Errorf("causationid = %v, want req-1", reply["causationid"])
	}
	if met.panics != 1 {
		t.Errorf("panics = %d, want 1", met.panics)
	}
}

func TestHandleNoReplyToIsDropped(t *testing.T) {
	met := &fakeMetrics{}
	r := newResponder(fakeDispatcher{}, met)
	responded := false
	m := natsjs.RequestMsg{ReplyTo: "", Data: []byte(`{}`), Respond: func([]byte) error { responded = true; return nil }}
	r.handle(context.Background(), m)
	if responded {
		t.Error("must not respond when ReplyTo is empty")
	}
	if met.noReply != 1 {
		t.Errorf("noReply = %d", met.noReply)
	}
}

type countBeater struct{ beats atomic.Int32 }

func (c *countBeater) Beat() { c.beats.Add(1) }

// A responder-only deployment runs no JetStream consumer, so the responder
// itself must drive the liveness heartbeat — even with zero incoming requests,
// otherwise /live goes stale and k8s restarts an idle-but-healthy pod.
func TestRunBeatsHeartbeatWithoutRequests(t *testing.T) {
	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	hb := &countBeater{}
	r := newResponder(d, &fakeMetrics{}).WithHeartbeat(hb)

	sub := newFakeSubscriber()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, sub) }()

	select {
	case <-sub.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("responder did not start")
	}

	// No request is ever sent. The heartbeat must still be beaten.
	deadline := time.Now().Add(2 * time.Second)
	for hb.beats.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if hb.beats.Load() == 0 {
		t.Fatal("responder did not beat the heartbeat without requests")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not shut down")
	}
}

// fakeSubscriber records the handler passed to SubscribeRequests and signals
// readiness once it has been captured.
type fakeSubscriber struct {
	mu      sync.Mutex
	handler func(natsjs.RequestMsg)
	ready   chan struct{}
}

func newFakeSubscriber() *fakeSubscriber {
	return &fakeSubscriber{ready: make(chan struct{})}
}

func (f *fakeSubscriber) SubscribeRequests(_, _ string, h func(natsjs.RequestMsg)) (*nats.Subscription, error) {
	f.mu.Lock()
	f.handler = h
	f.mu.Unlock()
	close(f.ready)
	return &nats.Subscription{}, nil
}

func TestRunDispatchesAndShutsDown(t *testing.T) {
	const n = 5

	d := fakeDispatcher{res: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	r := newResponder(d, &fakeMetrics{})

	sub := newFakeSubscriber()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- r.Run(ctx, sub) }()

	// Wait for the handler to be captured.
	select {
	case <-sub.ready:
	case <-time.After(2 * time.Second):
		t.Fatal("handler not captured within 2s")
	}
	sub.mu.Lock()
	handler := sub.handler
	sub.mu.Unlock()
	if handler == nil {
		t.Fatal("handler is nil after ready")
	}

	// Drive n requests concurrently through the captured handler. The handler
	// only enqueues onto the worker pool, so synchronize on the replies (which
	// the workers emit) rather than on the enqueue returning.
	validReq := []byte(`{"specversion":"1.0","id":"req-1","source":"c","type":"com.x.request","data":{"k":1}}`)
	var (
		replied  int64
		repliedG sync.WaitGroup
		mu       sync.Mutex
		sample   []byte
	)
	repliedG.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			handler(natsjs.RequestMsg{
				ReplyTo: "_INBOX.x",
				Data:    validReq,
				Respond: func(b []byte) error {
					mu.Lock()
					if sample == nil {
						cp := make([]byte, len(b))
						copy(cp, b)
						sample = cp
					}
					mu.Unlock()
					atomic.AddInt64(&replied, 1)
					repliedG.Done()
					return nil
				},
			})
		}()
	}
	// Wait until every request has been processed and replied to by a worker,
	// with a timeout so a regressed worker path fails the test fast rather than
	// hanging CI forever.
	waitDone := make(chan struct{})
	go func() {
		repliedG.Wait()
		close(waitDone)
	}()
	select {
	case <-waitDone:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for all replies")
	}

	if got := atomic.LoadInt64(&replied); got != n {
		t.Fatalf("replied = %d, want %d", got, n)
	}

	mu.Lock()
	s := sample
	mu.Unlock()
	reply := decode(t, s)
	if reply["httpstatus"].(float64) != 200 {
		t.Errorf("httpstatus = %v, want 200", reply["httpstatus"])
	}

	// Trigger shutdown.
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not shut down")
	}
}
