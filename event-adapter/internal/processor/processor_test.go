package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
)

type fakeDispatcher struct {
	result dispatcher.Result
	err    error
	calls  int
}

func (f *fakeDispatcher) Dispatch(context.Context, config.DispatchConfig, *clevent.Event) (dispatcher.Result, error) {
	f.calls++
	return f.result, f.err
}

type fakePublisher struct {
	responseErr error
	dlqErr      error
	responses   int
	dlqs        int
}

func (f *fakePublisher) PublishResponse(context.Context, string, *ce.Event) error {
	f.responses++
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
