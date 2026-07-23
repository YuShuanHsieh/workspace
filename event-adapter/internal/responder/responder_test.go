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
	"event-adapter/internal/router"
)

type fakeDispatcher struct {
	res      dispatcher.Result
	err      error
	captured *config.DispatchConfig
	calls    *atomic.Int32
}

func (f fakeDispatcher) Dispatch(_ context.Context, cfg config.DispatchConfig, _ *clevent.Event) (dispatcher.Result, error) {
	if f.captured != nil {
		*f.captured = cfg
	}
	if f.calls != nil {
		f.calls.Add(1)
	}
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
	lastRoute, invalidReason                        string
}

func (f *fakeMetrics) RequestReceived(_ context.Context, route string) {
	f.mu.Lock()
	f.received++
	f.lastRoute = route
	f.mu.Unlock()
}
func (f *fakeMetrics) RequestReplyLatency(context.Context, string, time.Duration) {}
func (f *fakeMetrics) RequestDispatchError(_ context.Context, route string) {
	f.mu.Lock()
	f.dispatchErr++
	f.lastRoute = route
	f.mu.Unlock()
}
func (f *fakeMetrics) RequestNoReply(context.Context) {
	f.mu.Lock()
	f.noReply++
	f.mu.Unlock()
}
func (f *fakeMetrics) InvalidRequestEvent(_ context.Context, reason string) {
	f.mu.Lock()
	f.invalid++
	f.invalidReason = reason
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

func newDirectResponder(t *testing.T, d Dispatcher, m Metrics, routes []config.RequestRouteConfig) *Responder {
	t.Helper()
	matcher, err := router.NewRequests(routes)
	if err != nil {
		t.Fatalf("new request matcher: %v", err)
	}
	return New(matcher, d, m, "order-service", &config.RequestsConfig{
		Subject:        "s",
		QueueGroup:     "g",
		WorkerPoolSize: 2,
		DirectDispatch: config.DirectDispatchConfig{
			Enabled:             true,
			Timeout:             3 * time.Second,
			AllowedPathPrefixes: []string{"/orders/"},
		},
		Routes: routes,
	}, io.Discard)
}

func directMessage(t *testing.T, eventType, method, path string) (natsjs.RequestMsg, *[]byte) {
	t.Helper()
	envelope := map[string]any{
		"specversion":   "1.0",
		"id":            "req-direct",
		"source":        "caller",
		"type":          eventType,
		"correlationid": "corr-1",
		"data":          map[string]any{"cleanup": true},
	}
	if method != "" {
		envelope["dispatchmethod"] = method
	}
	if path != "" {
		envelope["dispatchpath"] = path
	}
	data, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal direct message: %v", err)
	}
	var out []byte
	return natsjs.RequestMsg{
		ReplyTo: "_INBOX.direct",
		Data:    data,
		Respond: func(b []byte) error { out = b; return nil },
	}, &out
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

func TestHandleDirectDispatchesUnmatchedRequest(t *testing.T) {
	var captured config.DispatchConfig
	met := &fakeMetrics{}
	r := newDirectResponder(t, fakeDispatcher{
		res: dispatcher.Result{
			StatusCode:  202,
			ContentType: "application/json",
			Body:        []byte(`{"accepted":true}`),
		},
		captured: &captured,
	}, met, nil)
	m, out := directMessage(t, "com.workspace.orders.cleanup.request", "delete", "/orders/ord-456?hard=true")

	r.handle(context.Background(), m)

	if captured.Method != "DELETE" {
		t.Errorf("dispatch method = %q, want DELETE", captured.Method)
	}
	if captured.Path != "/orders/ord-456?hard=true" {
		t.Errorf("dispatch path = %q, want /orders/ord-456?hard=true", captured.Path)
	}
	if captured.Timeout != 3*time.Second {
		t.Errorf("dispatch timeout = %v, want 3s", captured.Timeout)
	}
	if captured.TelemetryRoute != clevent.DirectRouteName {
		t.Errorf("telemetry route = %q, want %q", captured.TelemetryRoute, clevent.DirectRouteName)
	}
	if len(captured.Headers) != 0 {
		t.Errorf("dispatch headers = %v, want empty", captured.Headers)
	}
	if len(captured.ForwardHeaders) != 0 {
		t.Errorf("dispatch forward headers = %v, want empty", captured.ForwardHeaders)
	}

	reply := decode(t, *out)
	if reply["type"] != clevent.DirectReplyType {
		t.Errorf("type = %v, want %s", reply["type"], clevent.DirectReplyType)
	}
	if reply["source"] != "order-service" {
		t.Errorf("source = %v, want order-service", reply["source"])
	}
	if reply["httpstatus"].(float64) != 202 {
		t.Errorf("httpstatus = %v, want 202", reply["httpstatus"])
	}
	if reply["causationid"] != "req-direct" {
		t.Errorf("causationid = %v, want req-direct", reply["causationid"])
	}
	if reply["correlationid"] != "corr-1" {
		t.Errorf("correlationid = %v, want corr-1", reply["correlationid"])
	}
	if met.lastRoute != clevent.DirectRouteName {
		t.Errorf("metrics route = %q, want %q", met.lastRoute, clevent.DirectRouteName)
	}
}

func TestHandleExactRouteTakesPrecedenceOverInvalidDirectControls(t *testing.T) {
	const configuredTimeout = 7 * time.Second
	routes := []config.RequestRouteConfig{{
		Name:  "controlled-order",
		Match: config.RequestMatchConfig{Type: "com.workspace.orders.controlled.request"},
		Dispatch: config.DispatchConfig{
			Method:         "POST",
			Path:           "/controlled",
			Timeout:        configuredTimeout,
			Headers:        map[string]string{"X-Mode": "configured"},
			ForwardHeaders: []string{"X-Trace-ID"},
		},
		Reply: config.ReplyConfig{
			Source:     "controlled-service",
			Type:       "com.workspace.orders.controlled.reply",
			DataSchema: "https://schemas.example/controlled-reply.json",
		},
	}}
	var captured config.DispatchConfig
	r := newDirectResponder(t, fakeDispatcher{
		res:      dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)},
		captured: &captured,
	}, &fakeMetrics{}, routes)
	m, out := directMessage(t, routes[0].Match.Type, "OPTIONS", "http://example.com/admin")

	r.handle(context.Background(), m)

	if captured.Method != "POST" || captured.Path != "/controlled" || captured.Timeout != configuredTimeout {
		t.Fatalf("captured dispatch = %+v, want configured POST /controlled with 7s timeout", captured)
	}
	if captured.TelemetryRoute != routes[0].Name {
		t.Errorf("telemetry route = %q, want %q", captured.TelemetryRoute, routes[0].Name)
	}
	if captured.Headers["X-Mode"] != "configured" {
		t.Errorf("configured headers not preserved: %v", captured.Headers)
	}
	if len(captured.ForwardHeaders) != 1 || captured.ForwardHeaders[0] != "X-Trace-ID" {
		t.Errorf("configured forward headers not preserved: %v", captured.ForwardHeaders)
	}

	reply := decode(t, *out)
	if reply["type"] != routes[0].Reply.Type {
		t.Errorf("type = %v, want %s", reply["type"], routes[0].Reply.Type)
	}
	if reply["source"] != routes[0].Reply.Source {
		t.Errorf("source = %v, want %s", reply["source"], routes[0].Reply.Source)
	}
	if reply["dataschema"] != routes[0].Reply.DataSchema {
		t.Errorf("dataschema = %v, want %s", reply["dataschema"], routes[0].Reply.DataSchema)
	}
}

func TestHandleDirectRejectsInvalidDispatchTarget(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "missing method", path: "/orders/ord-456"},
		{name: "missing path", method: "DELETE"},
		{name: "unsupported method", method: "OPTIONS", path: "/orders/ord-456"},
		{name: "outside allowed prefix", method: "DELETE", path: "/admin/users"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			met := &fakeMetrics{}
			r := newDirectResponder(t, fakeDispatcher{calls: &calls}, met, nil)
			m, out := directMessage(t, "com.workspace.orders.cleanup.request", tt.method, tt.path)

			r.handle(context.Background(), m)

			if calls.Load() != 0 {
				t.Fatalf("dispatch calls = %d, want 0", calls.Load())
			}
			reply := decode(t, *out)
			if reply["httpstatus"].(float64) != 400 {
				t.Errorf("httpstatus = %v, want 400", reply["httpstatus"])
			}
			if reply["type"] != clevent.ErrorReplyType {
				t.Errorf("type = %v, want %s", reply["type"], clevent.ErrorReplyType)
			}
			if met.invalid != 1 {
				t.Errorf("invalid metric = %d, want 1", met.invalid)
			}
			if met.invalidReason != "invalid_dispatch_target" {
				t.Errorf("invalid reason = %q, want invalid_dispatch_target", met.invalidReason)
			}
		})
	}
}

func TestHandleDirectDispatchErrorsUseGenericReply(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status float64
	}{
		{name: "network error", err: errors.New("connection refused"), status: 502},
		{name: "deadline exceeded", err: context.DeadlineExceeded, status: 504},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			met := &fakeMetrics{}
			r := newDirectResponder(t, fakeDispatcher{err: tt.err}, met, nil)
			m, out := directMessage(t, "com.workspace.orders.cleanup.request", "DELETE", "/orders/ord-456")

			r.handle(context.Background(), m)

			reply := decode(t, *out)
			if reply["httpstatus"].(float64) != tt.status {
				t.Errorf("httpstatus = %v, want %.0f", reply["httpstatus"], tt.status)
			}
			if reply["type"] != clevent.DirectReplyType {
				t.Errorf("type = %v, want %s", reply["type"], clevent.DirectReplyType)
			}
			if met.dispatchErr != 1 {
				t.Errorf("dispatch error metric = %d, want 1", met.dispatchErr)
			}
			if met.lastRoute != clevent.DirectRouteName {
				t.Errorf("metrics route = %q, want %q", met.lastRoute, clevent.DirectRouteName)
			}
		})
	}
}

func TestHandleDirect3xxForwardsLocation(t *testing.T) {
	r := newDirectResponder(t, fakeDispatcher{res: dispatcher.Result{
		StatusCode: 307,
		Location:   "/orders/ord-456/confirm",
	}}, &fakeMetrics{}, nil)
	m, out := directMessage(t, "com.workspace.orders.cleanup.request", "GET", "/orders/ord-456")

	r.handle(context.Background(), m)

	reply := decode(t, *out)
	if reply["httpstatus"].(float64) != 307 {
		t.Fatalf("httpstatus = %v, want 307", reply["httpstatus"])
	}
	if reply["httplocation"] != "/orders/ord-456/confirm" {
		t.Errorf("httplocation = %v, want /orders/ord-456/confirm", reply["httplocation"])
	}
	if reply["type"] != clevent.DirectReplyType {
		t.Errorf("type = %v, want %s", reply["type"], clevent.DirectReplyType)
	}
}

func TestHandleDirectReplyIDVariesByRequestID(t *testing.T) {
	r := newDirectResponder(t, fakeDispatcher{res: dispatcher.Result{StatusCode: 204}}, &fakeMetrics{}, nil)
	first, firstOut := directMessage(t, "com.workspace.orders.cleanup.request", "DELETE", "/orders/ord-456")
	second, secondOut := directMessage(t, "com.workspace.orders.cleanup.request", "DELETE", "/orders/ord-456")
	var secondEnvelope map[string]any
	if err := json.Unmarshal(second.Data, &secondEnvelope); err != nil {
		t.Fatalf("decode second request: %v", err)
	}
	secondEnvelope["id"] = "req-direct-2"
	var err error
	second.Data, err = json.Marshal(secondEnvelope)
	if err != nil {
		t.Fatalf("encode second request: %v", err)
	}

	r.handle(context.Background(), first)
	r.handle(context.Background(), second)

	firstReply := decode(t, *firstOut)
	secondReply := decode(t, *secondOut)
	if firstReply["id"] == "" || secondReply["id"] == "" {
		t.Fatalf("reply IDs must be nonempty: first=%v second=%v", firstReply["id"], secondReply["id"])
	}
	if firstReply["id"] == secondReply["id"] {
		t.Errorf("reply IDs must vary by request ID, both were %v", firstReply["id"])
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
	var calls atomic.Int32
	met := &fakeMetrics{}
	r := New(matcher, fakeDispatcher{calls: &calls}, met, "upload-service", &config.RequestsConfig{
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
	if calls.Load() != 0 {
		t.Errorf("dispatch calls = %d, want 0", calls.Load())
	}
	if met.invalid != 1 || met.invalidReason != "no_route" {
		t.Errorf("invalid metric = %d reason = %q, want 1/no_route", met.invalid, met.invalidReason)
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
