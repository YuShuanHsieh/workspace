package processor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
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
	attempts := 0
	for attempt := 1; attempt <= policy.MaxAttempts; attempt++ {
		attempts = attempt
		res, dispatchErr := p.dispatcher.Dispatch(ctx, route, ev)
		if dispatchErr != nil {
			lastErr = dispatchErr
			lastStatus = 0
			if attempt < policy.MaxAttempts && isNetworkError(dispatchErr) {
				timer := time.NewTimer(policy.Delay(attempt))
				select {
				case <-ctx.Done():
					timer.Stop()
					return ctx.Err()
				case <-timer.C:
				}
				continue
			}
			break
		}
		lastStatus = res.StatusCode
		resp, buildErr := clevent.BuildResponse(ev, route, res.StatusCode, res.ContentType, res.Body)
		if buildErr != nil {
			lastErr = buildErr
			break
		}
		if pubErr := p.publisher.PublishResponse(ctx, route.Response.Subject, resp); pubErr != nil {
			lastErr = pubErr
			break
		}
		return ack.Ack(ctx)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("dispatch failed")
	}
	dlq := DLQEvent{
		OriginalEvent: ev,
		FailureReason: lastErr.Error(),
		HTTPStatus:    lastStatus,
		AttemptCount:  attempts,
		Timestamp:     time.Now().UTC(),
	}
	if err := p.publisher.PublishDLQ(ctx, route.DLQ.Subject, dlq); err != nil {
		return fmt.Errorf("publish dlq: %w", err)
	}
	return ack.Ack(ctx)
}

func isNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	type temporary interface {
		Temporary() bool
	}
	var tempErr temporary
	if errors.As(err, &tempErr) && tempErr.Temporary() {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection aborted") ||
		strings.Contains(msg, "i/o timeout")
}
