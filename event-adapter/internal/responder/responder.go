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
}

// Subscriber is satisfied by *natsjs.Client.
type Subscriber interface {
	SubscribeRequests(subject, queue string, h func(natsjs.RequestMsg)) (*nats.Subscription, error)
}

type Responder struct {
	matcher Matcher
	disp    Dispatcher
	metrics Metrics
	appID   string
	cfg     *config.RequestsConfig
	stderr  io.Writer
}

func New(matcher Matcher, disp Dispatcher, metrics Metrics, appID string, cfg *config.RequestsConfig, stderr io.Writer) *Responder {
	if stderr == nil {
		stderr = io.Discard
	}
	return &Responder{matcher: matcher, disp: disp, metrics: metrics, appID: appID, cfg: cfg, stderr: stderr}
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
	ev, err := clevent.Parse(m.Data)
	if err != nil {
		r.metrics.InvalidRequestEvent(ctx, "parse_error")
		r.respond(m, clevent.BuildErrorReply(nil, r.appID, http.StatusBadRequest, err.Error()))
		return
	}
	route, ok := r.matcher.Match(ev)
	if !ok {
		r.metrics.InvalidRequestEvent(ctx, "no_route")
		r.respond(m, clevent.BuildErrorReply(ev, r.appID, http.StatusNotFound, "no matching route"))
		return
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
