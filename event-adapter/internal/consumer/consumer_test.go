package consumer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/nats-io/nats.go"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/processor"
)

type fakeProcessor struct {
	calls int32
	err   error
}

func (f *fakeProcessor) Process(_ context.Context, _ string, _ *clevent.Event, _ config.RouteConfig, _ processor.MessageHandle) error {
	atomic.AddInt32(&f.calls, 1)
	return f.err
}

type fakeMatcher struct {
	route config.RouteConfig
	ok    bool
}

func (f *fakeMatcher) Match(*clevent.Event) (config.RouteConfig, bool) {
	return f.route, f.ok
}

type fakeDLQ struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeDLQ) PublishDLQ(context.Context, string, processor.DLQEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return nil
}

type noopMetrics struct{}

func (noopMetrics) EventConsumed(context.Context, string)               {}
func (noopMetrics) DispatchLatency(context.Context, string, time.Duration) {}
func (noopMetrics) InvalidCloudEvent(context.Context, string)           {}
func (noopMetrics) RouteMatchFailure(context.Context)                   {}

type fakeHandle struct {
	mu    sync.Mutex
	acked int
}

func (f *fakeHandle) Ack(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.acked++
	return nil
}
func (f *fakeHandle) Nak(context.Context, time.Duration) error { return nil }
func (f *fakeHandle) Deliveries() uint64                       { return 1 }

func validEventBytes(t *testing.T) []byte {
	t.Helper()
	ev := ce.New()
	ev.SetID("evt-1")
	ev.SetSource("workspace/task")
	ev.SetType("com.workspace.task.created")
	if err := ev.SetData("application/json", map[string]string{"taskId": "t1"}); err != nil {
		t.Fatal(err)
	}
	b, err := ev.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func testConsumer(proc Processor, matcher Matcher, dlq DLQPublisher) *Consumer {
	return New(nil, proc, matcher, dlq, noopMetrics{}, config.Config{}, 4, 4, nil)
}

func TestIsEmptyPoll(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"context deadline exceeded", context.DeadlineExceeded, true},
		{"nats timeout", nats.ErrTimeout, true},
		{"wrapped deadline exceeded", fmt.Errorf("fetch batch: %w", context.DeadlineExceeded), true},
		{"wrapped nats timeout", fmt.Errorf("fetch batch: %w", nats.ErrTimeout), true},
		{"real error", errors.New("connection refused"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isEmptyPoll(tc.err); got != tc.want {
				t.Fatalf("isEmptyPoll(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestHandleParseErrorGoesToDefaultDLQAndAcks(t *testing.T) {
	dlq := &fakeDLQ{}
	h := &fakeHandle{}
	c := testConsumer(&fakeProcessor{}, &fakeMatcher{ok: true}, dlq)
	c.handle(context.Background(), job{subject: "input.subject", data: []byte("not json"), handle: h})
	if dlq.calls != 1 || h.acked != 1 {
		t.Fatalf("expected dlq+ack, got dlq=%d ack=%d", dlq.calls, h.acked)
	}
}

func TestHandleNoRouteGoesToDefaultDLQAndAcks(t *testing.T) {
	dlq := &fakeDLQ{}
	h := &fakeHandle{}
	proc := &fakeProcessor{}
	c := testConsumer(proc, &fakeMatcher{ok: false}, dlq)
	c.handle(context.Background(), job{subject: "input.subject", data: validEventBytes(t), handle: h})
	if dlq.calls != 1 || h.acked != 1 || proc.calls != 0 {
		t.Fatalf("expected dlq+ack and no process, got dlq=%d ack=%d proc=%d", dlq.calls, h.acked, proc.calls)
	}
}

func TestHandleMatchedRouteCallsProcessor(t *testing.T) {
	dlq := &fakeDLQ{}
	proc := &fakeProcessor{}
	c := testConsumer(proc, &fakeMatcher{ok: true, route: config.RouteConfig{Name: "r"}}, dlq)
	c.handle(context.Background(), job{subject: "input.subject", data: validEventBytes(t), handle: &fakeHandle{}})
	if proc.calls != 1 || dlq.calls != 0 {
		t.Fatalf("expected process call and no dlq, got proc=%d dlq=%d", proc.calls, dlq.calls)
	}
}

func TestWorkerPoolProcessesAllJobs(t *testing.T) {
	proc := &fakeProcessor{}
	c := testConsumer(proc, &fakeMatcher{ok: true, route: config.RouteConfig{Name: "r"}}, &fakeDLQ{})
	data := validEventBytes(t)

	jobs := make(chan job, c.workers)
	var wg sync.WaitGroup
	wg.Add(c.workers)
	for i := 0; i < c.workers; i++ {
		go c.work(context.Background(), jobs, &wg)
	}
	const total = 50
	for i := 0; i < total; i++ {
		jobs <- job{subject: "input.subject", data: data, handle: &fakeHandle{}}
	}
	close(jobs)
	wg.Wait()

	if got := atomic.LoadInt32(&proc.calls); got != total {
		t.Fatalf("expected %d processed, got %d", total, got)
	}
}
