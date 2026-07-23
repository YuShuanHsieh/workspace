package consumer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/health"
	"event-adapter/internal/natsjs"
	"event-adapter/internal/processor"
)

const (
	// backpressurePause is how long the fetch loop sleeps while the backlog is
	// above the threshold before re-checking.
	backpressurePause = 200 * time.Millisecond
	// backlogCacheTTL bounds how often ConsumerInfo is queried for the backlog.
	backlogCacheTTL = time.Second
	// backpressureReleaseRatio is the hysteresis factor: backpressure releases
	// once the backlog falls below threshold*ratio, avoiding flapping.
	backpressureReleaseNum = 9
	backpressureReleaseDen = 10
)

type Processor interface {
	Process(ctx context.Context, subject string, ev *clevent.Event, route config.RouteConfig, msg processor.MessageHandle) error
}

type Matcher interface {
	Match(ev *clevent.Event) (config.RouteConfig, bool)
}

type DLQPublisher interface {
	PublishDLQ(ctx context.Context, subject string, dlq processor.DLQEvent) error
}

type Metrics interface {
	EventConsumed(ctx context.Context, route string)
	DispatchLatency(ctx context.Context, route string, d time.Duration)
	DeliveryLatency(ctx context.Context, route string, d time.Duration)
	InvalidCloudEvent(ctx context.Context, reason string)
	RouteMatchFailure(ctx context.Context)
	BackpressureTriggered(ctx context.Context)
	PanicRecovered(ctx context.Context, component string)
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

	heartbeat   *health.Heartbeat
	pending     func(context.Context) (int64, error)
	bpThreshold int
	inFlight    atomic.Int64

	cacheMu       sync.Mutex
	cachedPending int64
	cachedAtNano  int64
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

// WithHeartbeat installs the liveness heartbeat updated on every fetch loop
// iteration. Returns the receiver for chaining.
func (c *Consumer) WithHeartbeat(hb *health.Heartbeat) *Consumer {
	c.heartbeat = hb
	return c
}

// WithBackpressure enables backlog-based backpressure. When the backlog reaches
// threshold the fetch loop pauses (rejecting new work) until it drains; pending
// supplies the current JetStream NumPending. Returns the receiver for chaining.
func (c *Consumer) WithBackpressure(threshold int, pending func(context.Context) (int64, error)) *Consumer {
	c.bpThreshold = threshold
	c.pending = pending
	return c
}

// InFlight reports how many events are currently being processed.
func (c *Consumer) InFlight() int64 {
	return c.inFlight.Load()
}

// Backlog reports the current pending-event backlog: JetStream NumPending plus
// the events currently in flight.
func (c *Consumer) Backlog(ctx context.Context) int64 {
	return c.currentPending(ctx) + c.inFlight.Load()
}

// pauseForBackpressure reports whether the fetch loop should pause. It engages
// at the threshold; once engaged it stays paused until the backlog falls below
// the release point (threshold * release ratio), so intake does not resume in
// the hysteresis band between the release point and the threshold.
func (c *Consumer) pauseForBackpressure(backlog int64, engaged bool) bool {
	if c.bpThreshold <= 0 {
		return false
	}
	if backlog >= int64(c.bpThreshold) {
		return true
	}
	releaseAt := int64(c.bpThreshold) * backpressureReleaseNum / backpressureReleaseDen
	return engaged && backlog >= releaseAt
}

// currentPending returns NumPending, cached for backlogCacheTTL so the fetch
// loop and metric scrapes do not hammer the NATS server with ConsumerInfo calls.
func (c *Consumer) currentPending(ctx context.Context) int64 {
	c.cacheMu.Lock()
	defer c.cacheMu.Unlock()
	now := time.Now().UnixNano()
	if c.cachedAtNano != 0 && now-c.cachedAtNano < int64(backlogCacheTTL) {
		return c.cachedPending
	}
	if c.pending != nil {
		if p, err := c.pending(ctx); err == nil {
			c.cachedPending = p
			c.cachedAtNano = now
		}
	}
	return c.cachedPending
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

	backpressured := false
	for ctx.Err() == nil {
		c.heartbeat.Beat()

		if c.bpThreshold > 0 {
			backlog := c.Backlog(ctx)
			if c.pauseForBackpressure(backlog, backpressured) {
				if !backpressured {
					c.metrics.BackpressureTriggered(ctx)
					backpressured = true
					fmt.Fprintf(c.stderr, "backpressure engaged: backlog=%d threshold=%d\n", backlog, c.bpThreshold)
				}
				select {
				case <-time.After(backpressurePause):
				case <-ctx.Done():
				}
				continue
			}
			if backpressured {
				backpressured = false
				fmt.Fprintf(c.stderr, "backpressure released: backlog=%d\n", backlog)
			}
		}

		msgs, err := natsjs.FetchBatch(ctx, c.sub, c.batch)
		if err != nil {
			if ctx.Err() != nil || isEmptyPoll(err) {
				continue
			}
			fmt.Fprintf(c.stderr, "fetch batch: %v\n", err)
			time.Sleep(100 * time.Millisecond)
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

// isEmptyPoll reports whether err signals a pull-fetch that returned no messages
// before its deadline — normal behavior when the JetStream queue is idle, not a
// real failure. NATS surfaces this as context.DeadlineExceeded or nats.ErrTimeout.
func isEmptyPoll(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, nats.ErrTimeout)
}

func (c *Consumer) work(ctx context.Context, jobs <-chan job, wg *sync.WaitGroup) {
	defer wg.Done()
	for j := range jobs {
		c.handle(ctx, j)
	}
}

func (c *Consumer) handle(ctx context.Context, j job) {
	c.inFlight.Add(1)
	defer c.inFlight.Add(-1)

	// ev is declared before the recover so the backstop can DLQ the offending
	// event when the panic occurs after parsing. A panic is treated as a
	// permanent failure (DLQ + ack) so a poison event cannot wedge a worker or
	// redeliver forever; recovering also keeps one bad event from crashing the
	// whole sidecar.
	var ev *clevent.Event
	defer func() {
		if rec := recover(); rec != nil {
			c.metrics.PanicRecovered(ctx, "consumer")
			fmt.Fprintf(c.stderr, "panic recovered processing %s: %v\n", j.subject, rec)
			c.toDefaultDLQ(ctx, ev, fmt.Sprintf("panic: %v", rec), j.subject, j.handle)
		}
	}()

	parsed, err := clevent.Parse(j.data)
	if err != nil {
		c.metrics.InvalidCloudEvent(ctx, "parse_error")
		c.toDefaultDLQ(ctx, nil, err.Error(), j.subject, j.handle)
		return
	}
	ev = parsed
	route, ok := c.matcher.Match(ev)
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
	elapsed := time.Since(start)
	c.metrics.DispatchLatency(ctx, route.Name, elapsed)
	c.metrics.DeliveryLatency(ctx, route.Name, elapsed)
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
