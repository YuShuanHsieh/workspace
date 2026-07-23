package responder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	ce "github.com/cloudevents/sdk-go/v2/event"
	"github.com/nats-io/nats.go"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/natsjs"
	pathtemplate "event-adapter/internal/pathtemplate"
	"event-adapter/internal/requesttarget"
)

type Dispatcher interface {
	Dispatch(context.Context, config.DispatchConfig, *clevent.Event) (dispatcher.Result, error)
}

type Matcher interface {
	Match(ev *clevent.Event) (config.RequestRouteConfig, bool)
}

type Metrics interface {
	RequestReceived(ctx context.Context, route string)
	RequestReplyLatency(ctx context.Context, route string, d time.Duration)
	RequestDispatchError(ctx context.Context, route string)
	RequestNoReply(ctx context.Context)
	InvalidRequestEvent(ctx context.Context, reason string)
	PanicRecovered(ctx context.Context, component string)
}

// Subscriber is satisfied by *natsjs.Client.
type Subscriber interface {
	SubscribeRequests(subject, queue string, h func(natsjs.RequestMsg)) (*nats.Subscription, error)
}

// heartbeatInterval is how often Run refreshes the liveness heartbeat while the
// responder serves, so /live stays fresh for request-reply-only deployments
// even when no requests arrive. Must stay well below the probe's max age (60s).
const heartbeatInterval = 10 * time.Second

// beater is the subset of *health.Heartbeat that Run drives; kept as an
// interface so the heartbeat can be observed in tests.
type beater interface{ Beat() }

type Responder struct {
	matcher Matcher
	disp    Dispatcher
	metrics Metrics
	appID   string
	cfg     *config.RequestsConfig
	stderr  io.Writer
	hb      beater
}

func New(matcher Matcher, disp Dispatcher, metrics Metrics, appID string, cfg *config.RequestsConfig, stderr io.Writer) *Responder {
	if stderr == nil {
		stderr = io.Discard
	}
	return &Responder{matcher: matcher, disp: disp, metrics: metrics, appID: appID, cfg: cfg, stderr: stderr}
}

// WithHeartbeat installs a liveness heartbeat that Run beats periodically while
// the responder is serving, so /live reflects a request-reply-only deployment
// that runs no JetStream consumer. Returns the receiver for chaining.
func (r *Responder) WithHeartbeat(hb beater) *Responder {
	r.hb = hb
	return r
}

// Run subscribes and processes requests on a bounded worker pool until ctx is
// cancelled, then drains the subscription and waits for in-flight work.
func (r *Responder) Run(ctx context.Context, sub Subscriber) error {
	jobs := make(chan natsjs.RequestMsg, r.cfg.WorkerPoolSize)
	var wg sync.WaitGroup
	wg.Add(r.cfg.WorkerPoolSize)
	for i := 0; i < r.cfg.WorkerPoolSize; i++ {
		go func() {
			defer wg.Done()
			for m := range jobs {
				r.handle(ctx, m)
			}
		}()
	}

	subscription, err := sub.SubscribeRequests(r.cfg.Subject, r.cfg.QueueGroup, func(m natsjs.RequestMsg) {
		select {
		case jobs <- m:
		case <-ctx.Done():
		}
	})
	if err != nil {
		close(jobs)
		wg.Wait()
		return err
	}

	// Drive the liveness heartbeat on a fixed interval (not per-request) so an
	// idle responder is not mistaken for a wedged one. Only started after a
	// successful subscribe so a subscribe failure does not block wg.Wait below.
	if r.hb != nil {
		r.hb.Beat()
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := time.NewTicker(heartbeatInterval)
			defer t.Stop()
			for {
				select {
				case <-t.C:
					r.hb.Beat()
				case <-ctx.Done():
					return
				}
			}
		}()
	}

	<-ctx.Done()
	// Drain stops new callbacks and waits for pending ones to finish, so no
	// goroutine sends to jobs after this returns — safe to close.
	_ = subscription.Drain()
	close(jobs)
	wg.Wait()
	return nil
}

func (r *Responder) handle(ctx context.Context, m natsjs.RequestMsg) {
	if m.Respond == nil || m.ReplyTo == "" {
		r.metrics.RequestNoReply(ctx)
		return
	}
	// ev is declared before the recover so the backstop can still build a reply
	// carrying the request's causationid when the panic occurs after parsing.
	var ev *clevent.Event
	defer func() {
		if rec := recover(); rec != nil {
			r.metrics.PanicRecovered(ctx, "responder")
			fmt.Fprintf(r.stderr, "responder: panic recovered: %v\n", rec)
			r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusInternalServerError, "internal error"))
		}
	}()
	parsed, err := clevent.Parse(m.Data)
	if err != nil {
		r.metrics.InvalidRequestEvent(ctx, "parse_error")
		r.respond(m, clevent.BuildErrorReply(nil, r.appID, http.StatusBadRequest, err.Error()))
		return
	}
	ev = parsed
	route, ok := r.matcher.Match(ev)
	if !ok {
		if !r.cfg.DirectDispatch.Enabled {
			r.metrics.InvalidRequestEvent(ctx, "no_route")
			r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusNotFound, "no matching route"))
			return
		}
		target, targetErr := requesttarget.Resolve(
			ev.DispatchMethod,
			ev.DispatchPath,
			r.cfg.DirectDispatch.AllowedPathPrefixes,
		)
		if targetErr != nil {
			r.metrics.InvalidRequestEvent(ctx, "invalid_dispatch_target")
			r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusBadRequest, targetErr.Error()))
			return
		}
		route = config.RequestRouteConfig{
			Name: clevent.DirectRouteName,
			Dispatch: config.DispatchConfig{
				Method:  target.Method,
				Path:    target.Path,
				Timeout: r.cfg.DirectDispatch.Timeout,
			},
			Reply: clevent.DirectReplyConfig(r.appID),
		}
	}
	r.metrics.RequestReceived(ctx, route.Name)
	start := time.Now()
	defer func() { r.metrics.RequestReplyLatency(ctx, route.Name, time.Since(start)) }()

	res, derr := r.disp.Dispatch(ctx, route.Dispatch, ev)
	if derr != nil {
		r.metrics.RequestDispatchError(ctx, route.Name)
		status := http.StatusBadGateway
		switch {
		case errors.Is(derr, pathtemplate.ErrPermanent):
			status = http.StatusBadRequest
		case errors.Is(derr, context.DeadlineExceeded):
			status = http.StatusGatewayTimeout
		}
		reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, status, "application/json", errorBody(derr.Error()), "")
		if berr != nil {
			r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusInternalServerError, berr.Error()))
			return
		}
		r.respond(m, reply)
		return
	}
	reply, berr := clevent.BuildReply(ev, route.Reply, route.Name, res.StatusCode, res.ContentType, res.Body, res.Location)
	if berr != nil {
		r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusInternalServerError, berr.Error()))
		return
	}
	r.respond(m, reply)
}

func (r *Responder) respond(m natsjs.RequestMsg, ev *ce.Event) {
	b, err := json.Marshal(ev)
	if err != nil {
		fmt.Fprintf(r.stderr, "responder: marshal reply: %v\n", err)
		return
	}
	if err := m.Respond(b); err != nil {
		fmt.Fprintf(r.stderr, "responder: respond: %v\n", err)
	}
}

func errorBody(message string) []byte {
	b, _ := json.Marshal(map[string]string{"error": message})
	return b
}
