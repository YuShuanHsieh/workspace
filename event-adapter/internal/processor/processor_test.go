package processor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	pathtemplate "event-adapter/internal/pathtemplate"
)

type fakeDispatcher struct {
	result       dispatcher.Result
	err          error
	calls        int
	lastDispatch config.DispatchConfig
}

func (f *fakeDispatcher) Dispatch(_ context.Context, cfg config.DispatchConfig, _ *clevent.Event) (dispatcher.Result, error) {
	f.calls++
	f.lastDispatch = cfg
	return f.result, f.err
}

type fakePublisher struct {
	responseErr  error
	dlqErr       error
	responses    int
	dlqs         int
	lastResponse *ce.Event
}

func (f *fakePublisher) PublishResponse(_ context.Context, _ string, ev *ce.Event) error {
	f.responses++
	f.lastResponse = ev
	return f.responseErr
}

func (f *fakePublisher) PublishDLQ(context.Context, string, DLQEvent) error {
	f.dlqs++
	return f.dlqErr
}

type fakeHandle struct {
	deliveries uint64
	acked      int
	naked      int
	nakDelay   time.Duration
}

func (f *fakeHandle) Ack(context.Context) error { f.acked++; return nil }

func (f *fakeHandle) Nak(_ context.Context, d time.Duration) error {
	f.naked++
	f.nakDelay = d
	return nil
}

func (f *fakeHandle) Deliveries() uint64 { return f.deliveries }

func newTestEvent(t *testing.T) *clevent.Event {
	t.Helper()
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	if err := ev.SetData("application/json", map[string]string{"taskId": "t1"}); err != nil {
		t.Fatal(err)
	}
	return &clevent.Event{Event: &ev}
}

func testRoute(maxAttempts int) config.RouteConfig {
	return config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "processed", Source: "task-service", Subject: "processed.subject"},
		Retry:    config.RetryConfig{MaxAttempts: maxAttempts, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ:      config.DLQConfig{Subject: "dlq.subject"},
	}
}

type recordingMetrics struct {
	success    int
	failure    int
	convs      int
	lastReason string
}

func (r *recordingMetrics) ConversionDuration(context.Context, string, time.Duration) { r.convs++ }
func (r *recordingMetrics) DeliverySuccess(context.Context, string)                   { r.success++ }
func (r *recordingMetrics) DeliveryFailure(_ context.Context, _, reason string) {
	r.failure++
	r.lastReason = reason
}

func TestProcessorRecordsDeliverySuccessAndConversion(t *testing.T) {
	rec := &recordingMetrics{}
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	p := New(disp, pub).WithObservability(rec, nil)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if rec.success != 1 || rec.failure != 0 || rec.convs != 1 {
		t.Fatalf("metrics success=%d failure=%d convs=%d, want 1/0/1", rec.success, rec.failure, rec.convs)
	}
}

func TestProcessorPassesBoundedTelemetryRouteToDispatcher(t *testing.T) {
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{result: dispatcher.Result{
		StatusCode:  http.StatusOK,
		ContentType: "application/json",
		Body:        []byte(`{"ok":true}`),
	}}
	route := testRoute(1)
	route.Dispatch = config.DispatchConfig{Method: http.MethodPost, Path: "/events/tasks/task-42", Timeout: time.Second}

	if err := New(disp, pub).Process(context.Background(), "input.subject", newTestEvent(t), route, h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if disp.lastDispatch.TelemetryRoute != route.Name {
		t.Errorf("telemetry route = %q, want %q", disp.lastDispatch.TelemetryRoute, route.Name)
	}
	if route.Dispatch.TelemetryRoute != "" {
		t.Errorf("route dispatch config was mutated: telemetry route = %q", route.Dispatch.TelemetryRoute)
	}
}

func TestProcessorRecordsDeliveryFailureWithReason(t *testing.T) {
	rec := &recordingMetrics{}
	pub := &fakePublisher{responseErr: errors.New("nats down")}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	p := New(disp, pub).WithObservability(rec, nil)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if rec.failure != 1 || rec.success != 0 {
		t.Fatalf("metrics failure=%d success=%d, want 1/0", rec.failure, rec.success)
	}
	if rec.lastReason != "nats down" {
		t.Fatalf("failure reason = %q, want %q", rec.lastReason, "nats down")
	}
}

func TestProcessorRecordsDeliveryFailureOnDispatchError(t *testing.T) {
	// A failed HTTP dispatch that exhausts retries and goes to the DLQ is a
	// delivery failure and must count toward delivery_total{status="failed"};
	// otherwise an app outage (everything DLQ'd) leaves the success-rate SLO
	// falsely reporting 100%.
	rec := &recordingMetrics{}
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 3}
	disp := &fakeDispatcher{err: errors.New("backend down")}
	p := New(disp, pub).WithObservability(rec, nil)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if rec.failure != 1 || rec.success != 0 {
		t.Fatalf("metrics failure=%d success=%d, want 1/0 (dispatch failure must count)", rec.failure, rec.success)
	}
	if rec.lastReason != "backend down" {
		t.Fatalf("failure reason = %q, want %q", rec.lastReason, "backend down")
	}
}

func TestProcessorAcksAfterResponsePublish(t *testing.T) {
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.acked != 1 || h.naked != 0 || pub.responses != 1 || pub.dlqs != 0 {
		t.Fatalf("unexpected state ack=%d nak=%d responses=%d dlqs=%d", h.acked, h.naked, pub.responses, pub.dlqs)
	}
}

func TestProcessorPublishesErrorResponseOnHTTPError(t *testing.T) {
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{result: dispatcher.Result{StatusCode: 422, ContentType: "application/json", Body: []byte(`{"error":"invalid taskId"}`)}}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.acked != 1 || h.naked != 0 || pub.responses != 1 || pub.dlqs != 0 || disp.calls != 1 {
		t.Fatalf("unexpected state ack=%d nak=%d responses=%d dlqs=%d calls=%d", h.acked, h.naked, pub.responses, pub.dlqs, disp.calls)
	}
}

func TestProcessorNetworkFailureNaksWhenDeliveriesRemain(t *testing.T) {
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{err: errors.New("connection refused")}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.naked != 1 || h.acked != 0 || pub.dlqs != 0 || disp.calls != 1 {
		t.Fatalf("expected single nak, got ack=%d nak=%d dlqs=%d calls=%d", h.acked, h.naked, pub.dlqs, disp.calls)
	}
	if h.nakDelay != time.Millisecond {
		t.Fatalf("expected nak delay = initialBackoff, got %v", h.nakDelay)
	}
}

func TestProcessorNetworkFailureExhaustsToDLQ(t *testing.T) {
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 3}
	disp := &fakeDispatcher{err: errors.New("connection refused")}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.acked != 1 || h.naked != 0 || pub.responses != 0 || pub.dlqs != 1 || disp.calls != 1 {
		t.Fatalf("unexpected state ack=%d nak=%d responses=%d dlqs=%d calls=%d", h.acked, h.naked, pub.responses, pub.dlqs, disp.calls)
	}
}

func TestProcessorDoesNotRetryNonNetworkDispatchError(t *testing.T) {
	pub := &fakePublisher{}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{err: errors.New("invalid request body")}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.acked != 1 || h.naked != 0 || pub.responses != 0 || pub.dlqs != 1 || disp.calls != 1 {
		t.Fatalf("unexpected state ack=%d nak=%d responses=%d dlqs=%d calls=%d", h.acked, h.naked, pub.responses, pub.dlqs, disp.calls)
	}
}

func TestProcessorDoesNotAckWhenDLQPublishFails(t *testing.T) {
	pub := &fakePublisher{dlqErr: errors.New("nats down")}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{err: errors.New("backend down")}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(1), h); err == nil {
		t.Fatal("expected process error")
	}
	if h.acked != 0 {
		t.Fatalf("must not ack when DLQ publish fails, got acked=%d", h.acked)
	}
}

func TestProcessorNaksWhenResponsePublishFailsWithRetriesRemaining(t *testing.T) {
	pub := &fakePublisher{responseErr: errors.New("nats down")}
	h := &fakeHandle{deliveries: 1}
	disp := &fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.naked != 1 || h.acked != 0 || pub.dlqs != 0 {
		t.Fatalf("expected nak on response publish failure, got ack=%d nak=%d dlqs=%d", h.acked, h.naked, pub.dlqs)
	}
}

func TestProcessorSendsToDLQWhenResponsePublishFailsAndExhausted(t *testing.T) {
	pub := &fakePublisher{responseErr: errors.New("nats down")}
	h := &fakeHandle{deliveries: 3}
	disp := &fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}
	p := New(disp, pub)
	if err := p.Process(context.Background(), "input.subject", newTestEvent(t), testRoute(3), h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if h.acked != 1 || h.naked != 0 || pub.dlqs != 1 {
		t.Fatalf("expected dlq+ack when exhausted, got ack=%d nak=%d dlqs=%d", h.acked, h.naked, pub.dlqs)
	}
}

func TestProcessor3xxWithLocationPublishesHTTPLocation(t *testing.T) {
	disp := &fakeDispatcher{result: dispatcher.Result{
		StatusCode:  307,
		ContentType: "",
		Body:        []byte(""),
		Location:    "/new-path",
	}}
	pub := &fakePublisher{}
	p := New(disp, pub)

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-p-3xx","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"taskId":"t1"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	route := config.RouteConfig{
		Name:     "task-created",
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/", Timeout: time.Second},
		Response: config.ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.x.processed"},
		Retry:    config.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
	}

	h := &fakeHandle{deliveries: 1}
	if err := p.Process(context.Background(), "t.x.created", ev, route, h); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if pub.lastResponse == nil {
		t.Fatal("expected publisher to receive a response event")
	}
	got, ok := pub.lastResponse.Extensions()["httplocation"]
	if !ok {
		t.Fatal("httplocation extension missing on published response event")
	}
	if got != "/new-path" {
		t.Fatalf("httplocation = %v, want /new-path", got)
	}
}

func TestProcessorPermanentPathErrorGoesStraightToDLQ(t *testing.T) {
	permErr := fmt.Errorf("dispatcher: resolve path: %w", pathtemplate.ErrPermanent)
	disp := &fakeDispatcher{err: permErr}
	pub := &fakePublisher{}
	p := New(disp, pub)

	ev, err := clevent.Parse([]byte(`{"specversion":"1.0","id":"evt-pt-proc","source":"workspace/task","type":"com.workspace.task.created","datacontenttype":"application/json","data":{"status":"done"}}`))
	if err != nil {
		t.Fatalf("parse event: %v", err)
	}
	route := config.RouteConfig{
		Name:     "task-created",
		Dispatch: config.DispatchConfig{Method: "POST", Path: "/api/tasks/{taskId}/x", Timeout: time.Second},
		Response: config.ResponseConfig{Type: "x.proc", Source: "task-service", Subject: "out"},
		Retry:    config.RetryConfig{MaxAttempts: 3, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ:      config.DLQConfig{Subject: "dlq.tenant-a.task-service"},
	}

	msg := &fakeHandle{deliveries: 1}
	if err := p.Process(context.Background(), "t.x.created", ev, route, msg); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if msg.naked != 0 {
		t.Fatalf("Nak count = %d, want 0 (permanent error must not retry)", msg.naked)
	}
	if pub.dlqs != 1 {
		t.Fatalf("DLQ count = %d, want 1", pub.dlqs)
	}
	if msg.acked != 1 {
		t.Fatalf("Ack count = %d, want 1 (after DLQ the original is acked)", msg.acked)
	}
}
