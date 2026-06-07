package responder

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/natsjs"
)

type fakeDispatcher struct {
	res dispatcher.Result
	err error
}

func (f fakeDispatcher) Dispatch(_ context.Context, _ config.DispatchConfig, _ *clevent.Event) (dispatcher.Result, error) {
	return f.res, f.err
}

type fakeMetrics struct{ received, dispatchErr, noReply, invalid int }

func (f *fakeMetrics) RequestReceived(context.Context, string)                    { f.received++ }
func (f *fakeMetrics) RequestReplyLatency(context.Context, string, time.Duration) {}
func (f *fakeMetrics) RequestDispatchError(context.Context, string)               { f.dispatchErr++ }
func (f *fakeMetrics) RequestNoReply(context.Context)                             { f.noReply++ }
func (f *fakeMetrics) InvalidRequestEvent(context.Context, string)                { f.invalid++ }

func newResponder(d Dispatcher, m Metrics) *Responder {
	matcher, _ := newTestMatcher()
	return New(matcher, d, m, "upload-service", &config.RequestsConfig{
		Subject: "s", QueueGroup: "g", WorkerPoolSize: 2,
		Routes: []config.RequestRouteConfig{{
			Name:     "upload-presign",
			Match:    config.MatchConfig{Type: "com.x.request"},
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
