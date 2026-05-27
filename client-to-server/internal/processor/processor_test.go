package processor

import (
	"context"
	"errors"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "client-to-server/internal/cloudevent"
	"client-to-server/internal/config"
	"client-to-server/internal/dispatcher"
)

type fakeDispatcher struct {
	result dispatcher.Result
	err    error
}

func (f fakeDispatcher) Dispatch(context.Context, config.RouteConfig, *clevent.Event) (dispatcher.Result, error) {
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

type fakeAck struct {
	acked bool
}

func (f *fakeAck) Ack(context.Context) error {
	f.acked = true
	return nil
}

func TestProcessorAcksAfterResponsePublish(t *testing.T) {
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	_ = ev.SetData("application/json", map[string]string{"taskId": "t1"})
	pub := &fakePublisher{}
	ack := &fakeAck{}
	p := New(fakeDispatcher{result: dispatcher.Result{StatusCode: 200, ContentType: "application/json", Body: []byte(`{"ok":true}`)}}, pub)
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "processed", Source: "task-service", Subject: "processed.subject"},
		Retry:    config.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ:      config.DLQConfig{Subject: "dlq.subject"},
	}
	if err := p.Process(context.Background(), "input.subject", &clevent.Event{Event: &ev}, route, ack); err != nil {
		t.Fatalf("Process returned error: %v", err)
	}
	if !ack.acked || pub.responses != 1 || pub.dlqs != 0 {
		t.Fatalf("unexpected state ack=%v responses=%d dlqs=%d", ack.acked, pub.responses, pub.dlqs)
	}
}

func TestProcessorDoesNotAckWhenDLQPublishFails(t *testing.T) {
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	_ = ev.SetData("application/json", map[string]string{"taskId": "t1"})
	pub := &fakePublisher{dlqErr: errors.New("nats down")}
	ack := &fakeAck{}
	p := New(fakeDispatcher{err: errors.New("backend down")}, pub)
	route := config.RouteConfig{
		Name:     "task-created",
		Response: config.ResponseConfig{Type: "processed", Source: "task-service", Subject: "processed.subject"},
		Retry:    config.RetryConfig{MaxAttempts: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond},
		DLQ:      config.DLQConfig{Subject: "dlq.subject"},
	}
	if err := p.Process(context.Background(), "input.subject", &clevent.Event{Event: &ev}, route, ack); err == nil {
		t.Fatal("expected process error")
	}
	if ack.acked {
		t.Fatal("must not ack when DLQ publish fails")
	}
}
