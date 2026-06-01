package consumer

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nats-io/nats.go"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/natsjs"
	"event-adapter/internal/processor"
)

type Processor interface {
	Process(ctx context.Context, subject string, ev *clevent.Event, route config.RouteConfig, msg processor.MessageHandle) error
}

type Matcher interface {
	Match(subject string, ev *clevent.Event) (config.RouteConfig, bool)
}

type DLQPublisher interface {
	PublishDLQ(ctx context.Context, subject string, dlq processor.DLQEvent) error
}

type Metrics interface {
	EventConsumed(ctx context.Context, route string)
	DispatchLatency(ctx context.Context, route string, d time.Duration)
	InvalidCloudEvent(ctx context.Context, reason string)
	RouteMatchFailure(ctx context.Context)
}

type Consumer struct {
	sub     *nats.Subscription
	proc    Processor
	matcher Matcher
	dlq     DLQPublisher
	metrics Metrics
	cfg     config.Config
	batch   int
	workers int
	stderr  io.Writer
}

type job struct {
	subject string
	data    []byte
	handle  processor.MessageHandle
}

func New(sub *nats.Subscription, proc Processor, matcher Matcher, dlq DLQPublisher, metrics Metrics, cfg config.Config, batch, workers int, stderr io.Writer) *Consumer {
	if stderr == nil {
		stderr = io.Discard
	}
	return &Consumer{
		sub:     sub,
		proc:    proc,
		matcher: matcher,
		dlq:     dlq,
		metrics: metrics,
		cfg:     cfg,
		batch:   batch,
		workers: workers,
		stderr:  stderr,
	}
}

// Run blocks until ctx is cancelled. It starts the worker pool, then loops
// fetching batches and dispatching them; on shutdown it drains the channel and
// waits for in-flight work to finish.
func (c *Consumer) Run(ctx context.Context) {
	jobs := make(chan job, c.workers)
	var wg sync.WaitGroup
	wg.Add(c.workers)
	for i := 0; i < c.workers; i++ {
		go c.work(ctx, jobs, &wg)
	}

	for ctx.Err() == nil {
		msgs, err := natsjs.FetchBatch(ctx, c.sub, c.batch)
		if err != nil {
			continue
		}
		for _, m := range msgs {
			select {
			case jobs <- job{subject: m.Subject, data: m.Data, handle: m}:
			case <-ctx.Done():
			}
		}
	}

	close(jobs)
	wg.Wait()
}

func (c *Consumer) work(ctx context.Context, jobs <-chan job, wg *sync.WaitGroup) {
	defer wg.Done()
	for j := range jobs {
		c.handle(ctx, j)
	}
}

func (c *Consumer) handle(ctx context.Context, j job) {
	ev, err := clevent.Parse(j.data)
	if err != nil {
		c.metrics.InvalidCloudEvent(ctx, "parse_error")
		c.toDefaultDLQ(ctx, nil, err.Error(), j.subject, j.handle)
		return
	}
	route, ok := c.matcher.Match(j.subject, ev)
	if !ok {
		c.metrics.RouteMatchFailure(ctx)
		c.toDefaultDLQ(ctx, ev, "no matching route", j.subject, j.handle)
		return
	}
	c.metrics.EventConsumed(ctx, route.Name)
	start := time.Now()
	if err := c.proc.Process(ctx, j.subject, ev, route, j.handle); err != nil {
		fmt.Fprintf(c.stderr, "process %s: %v\n", j.subject, err)
		return
	}
	c.metrics.DispatchLatency(ctx, route.Name, time.Since(start))
}

func (c *Consumer) toDefaultDLQ(ctx context.Context, ev *clevent.Event, reason, subject string, ack processor.Acker) {
	dlq := processor.DLQEvent{
		OriginalEvent: ev,
		FailureReason: reason,
		Timestamp:     time.Now().UTC(),
		SidecarAppID:  c.cfg.App.ID,
	}
	if err := c.dlq.PublishDLQ(ctx, c.cfg.NATS.DefaultDLQSubject, dlq); err != nil {
		fmt.Fprintf(c.stderr, "publish dlq %s: %v\n", subject, err)
		return
	}
	if err := ack.Ack(ctx); err != nil {
		fmt.Fprintf(c.stderr, "ack %s: %v\n", subject, err)
	}
}
