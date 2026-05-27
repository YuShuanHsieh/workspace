package processor

import (
	"context"
	"fmt"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "client-to-server/internal/cloudevent"
	"client-to-server/internal/config"
	"client-to-server/internal/dispatcher"
)

type Dispatcher interface {
	Dispatch(context.Context, config.RouteConfig, *clevent.Event) (dispatcher.Result, error)
}

type Publisher interface {
	PublishResponse(context.Context, string, *ce.Event) error
	PublishDLQ(context.Context, string, DLQEvent) error
}

type Acker interface {
	Ack(context.Context) error
}

type Processor struct {
	dispatcher Dispatcher
	publisher  Publisher
}

type DLQEvent struct {
	OriginalEvent *clevent.Event
	FailureReason string
	HTTPStatus    int
	AttemptCount  int
	SidecarAppID  string
	Timestamp     time.Time
}

func New(d Dispatcher, p Publisher) *Processor {
	return &Processor{dispatcher: d, publisher: p}
}

func (p *Processor) Process(ctx context.Context, subject string, ev *clevent.Event, route config.RouteConfig, ack Acker) error {
	policy := RetryPolicy{MaxAttempts: route.Retry.MaxAttempts, InitialBackoff: route.Retry.InitialBackoff, MaxBackoff: route.Retry.MaxBackoff}
	var lastErr error
	var lastStatus int
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		res, err := p.dispatcher.Dispatch(ctx, route, ev)
		lastStatus = res.StatusCode
		if err == nil && res.StatusCode >= 200 && res.StatusCode < 300 {
			resp, err := clevent.BuildResponse(ev, route, res.ContentType, res.Body)
			if err != nil {
				lastErr = err
			} else if err := p.publisher.PublishResponse(ctx, route.Response.Subject, resp); err != nil {
				lastErr = err
			} else {
				return ack.Ack(ctx)
			}
		} else if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf("non-success status %d", res.StatusCode)
		}
		if attempt < policy.MaxAttempts {
			timer := time.NewTimer(policy.Delay(attempt))
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("dispatch failed")
	}
	dlq := DLQEvent{
		OriginalEvent: ev,
		FailureReason: lastErr.Error(),
		HTTPStatus:    lastStatus,
		AttemptCount:  policy.MaxAttempts,
		Timestamp:     time.Now().UTC(),
	}
	if err := p.publisher.PublishDLQ(ctx, route.DLQ.Subject, dlq); err != nil {
		return fmt.Errorf("publish dlq: %w", err)
	}
	return ack.Ack(ctx)
}
