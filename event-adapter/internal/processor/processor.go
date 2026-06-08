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
	Dispatch(context.Context, config.DispatchConfig, *clevent.Event) (dispatcher.Result, error)
}

type Publisher interface {
	PublishResponse(context.Context, string, *ce.Event) error
	PublishDLQ(context.Context, string, DLQEvent) error
}

type Acker interface {
	Ack(context.Context) error
}

type MessageHandle interface {
	Acker
	Nak(context.Context, time.Duration) error
	Deliveries() uint64
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

func (p *Processor) Process(ctx context.Context, subject string, ev *clevent.Event, route config.RouteConfig, msg MessageHandle) error {
	policy := RetryPolicy{MaxAttempts: route.Retry.MaxAttempts, InitialBackoff: route.Retry.InitialBackoff, MaxBackoff: route.Retry.MaxBackoff}
	delivery := int(msg.Deliveries())
	if delivery < 1 {
		delivery = 1
	}

	res, dispatchErr := p.dispatcher.Dispatch(ctx, route.Dispatch, ev)
	if dispatchErr != nil {
		if isNetworkError(dispatchErr) && delivery < policy.MaxAttempts {
			return msg.Nak(ctx, policy.Delay(delivery))
		}
		return p.toDLQ(ctx, route, ev, dispatchErr.Error(), 0, delivery, msg)
	}

	resp, buildErr := clevent.BuildResponse(ev, route, res.StatusCode, res.ContentType, res.Body, res.Location)
	if buildErr != nil {
		if delivery < policy.MaxAttempts {
			return msg.Nak(ctx, policy.Delay(delivery))
		}
		return p.toDLQ(ctx, route, ev, buildErr.Error(), res.StatusCode, delivery, msg)
	}

	if pubErr := p.publisher.PublishResponse(ctx, route.Response.Subject, resp); pubErr != nil {
		if delivery < policy.MaxAttempts {
			return msg.Nak(ctx, policy.Delay(delivery))
		}
		return p.toDLQ(ctx, route, ev, pubErr.Error(), res.StatusCode, delivery, msg)
	}

	return msg.Ack(ctx)
}

func (p *Processor) toDLQ(ctx context.Context, route config.RouteConfig, ev *clevent.Event, reason string, status, attempts int, ack Acker) error {
	dlq := DLQEvent{
		OriginalEvent: ev,
		FailureReason: reason,
		HTTPStatus:    status,
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
