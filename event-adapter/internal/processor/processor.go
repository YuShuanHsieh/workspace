package processor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"syscall"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	pathtemplate "event-adapter/internal/pathtemplate"
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

// Metrics records delivery-related SLI metrics. A no-op implementation is used
// when none is supplied via WithObservability.
type Metrics interface {
	ConversionDuration(ctx context.Context, route string, d time.Duration)
	DeliverySuccess(ctx context.Context, route string)
	DeliveryFailure(ctx context.Context, route, reason string)
}

type noopMetrics struct{}

func (noopMetrics) ConversionDuration(context.Context, string, time.Duration) {}
func (noopMetrics) DeliverySuccess(context.Context, string)                   {}
func (noopMetrics) DeliveryFailure(context.Context, string, string)           {}

type MessageHandle interface {
	Acker
	Nak(context.Context, time.Duration) error
	Deliveries() uint64
}

type Processor struct {
	dispatcher Dispatcher
	publisher  Publisher
	metrics    Metrics
	logger     *slog.Logger
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
	return &Processor{
		dispatcher: d,
		publisher:  p,
		metrics:    noopMetrics{},
		logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// WithObservability attaches SLI metrics and a structured logger. Nil arguments
// leave the existing (no-op) values in place. Returns the receiver for chaining.
func (p *Processor) WithObservability(m Metrics, l *slog.Logger) *Processor {
	if m != nil {
		p.metrics = m
	}
	if l != nil {
		p.logger = l
	}
	return p
}

func (p *Processor) Process(ctx context.Context, subject string, ev *clevent.Event, route config.RouteConfig, msg MessageHandle) error {
	policy := RetryPolicy{MaxAttempts: route.Retry.MaxAttempts, InitialBackoff: route.Retry.InitialBackoff, MaxBackoff: route.Retry.MaxBackoff}
	delivery := int(msg.Deliveries()) // #nosec G115 -- redelivery count, bounded by MaxDeliver
	if delivery < 1 {
		delivery = 1
	}

	dispatchConfig := route.Dispatch
	dispatchConfig.TelemetryRoute = route.Name
	res, dispatchErr := p.dispatcher.Dispatch(ctx, dispatchConfig, ev)
	if dispatchErr != nil {
		// A failed dispatch is a delivery failure regardless of whether it
		// retries or dead-letters; count it so the success-rate SLO reflects
		// app outages instead of silently ignoring DLQ'd events.
		p.metrics.DeliveryFailure(ctx, route.Name, dispatchErr.Error())
		if errors.Is(dispatchErr, pathtemplate.ErrPermanent) {
			return p.toDLQ(ctx, route, ev, dispatchErr.Error(), 0, delivery, msg)
		}
		if isNetworkError(dispatchErr) && delivery < policy.MaxAttempts {
			return msg.Nak(ctx, policy.Delay(delivery))
		}
		return p.toDLQ(ctx, route, ev, dispatchErr.Error(), 0, delivery, msg)
	}

	// Time only the response→CloudEvent conversion, not the HTTP dispatch above,
	// so conversion_duration measures the sidecar's own overhead.
	convStart := time.Now()
	resp, buildErr := clevent.BuildResponse(ev, route, res.StatusCode, res.ContentType, res.Body, res.Location)
	if buildErr != nil {
		if delivery < policy.MaxAttempts {
			return msg.Nak(ctx, policy.Delay(delivery))
		}
		return p.toDLQ(ctx, route, ev, buildErr.Error(), res.StatusCode, delivery, msg)
	}
	// The HTTP response has been converted into a publishable CloudEvent.
	p.metrics.ConversionDuration(ctx, route.Name, time.Since(convStart))

	if pubErr := p.publisher.PublishResponse(ctx, route.Response.Subject, resp); pubErr != nil {
		p.metrics.DeliveryFailure(ctx, route.Name, pubErr.Error())
		p.logger.ErrorContext(ctx, "event delivery to nats failed",
			slog.String("route", route.Name),
			slog.String("subject", route.Response.Subject),
			slog.String("error", pubErr.Error()))
		if delivery < policy.MaxAttempts {
			return msg.Nak(ctx, policy.Delay(delivery))
		}
		return p.toDLQ(ctx, route, ev, pubErr.Error(), res.StatusCode, delivery, msg)
	}

	p.metrics.DeliverySuccess(ctx, route.Name)
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
