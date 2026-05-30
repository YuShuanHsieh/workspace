package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/nats-io/nats.go"
	"go.opentelemetry.io/otel/sdk/metric"

	clevent "event-adapter/internal/cloudevent"
	"event-adapter/internal/config"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/metrics"
	"event-adapter/internal/natsjs"
	"event-adapter/internal/processor"
	"event-adapter/internal/router"
)

type options struct {
	configPath string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("event-adapter", flag.ContinueOnError)
	fs.SetOutput(stderr)
	opts := options{}
	fs.StringVar(&opts.configPath, "config", "routes.yaml", "path to sidecar route configuration")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "event-adapter - NATS JetStream to local HTTP event sidecar")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if opts.configPath == "" {
		fmt.Fprintln(stderr, "config path is required")
		return 2
	}
	cfg, err := loadConfig(opts.configPath)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	js, err := natsjs.Connect(cfg.NATS)
	if err != nil {
		fmt.Fprintf(stderr, "connect nats: %v\n", err)
		return 1
	}
	defer js.Close()

	mp := metric.NewMeterProvider()
	m := metrics.New(mp.Meter("event-adapter"))
	matcher := router.New(cfg.Routes)
	httpDispatcher := dispatcher.New(cfg.App.HTTPBaseURL, nil)
	proc := processor.New(httpDispatcher, js)

	subs := make([]*nats.Subscription, 0, len(cfg.Routes))
	for _, route := range cfg.Routes {
		sub, err := js.PullSubscribe(route.Match.Subject, cfg.NATS.DurableConsumer+"-"+route.Name)
		if err != nil {
			fmt.Fprintf(stderr, "subscribe %s: %v\n", route.Match.Subject, err)
			return 1
		}
		subs = append(subs, sub)
	}
	fmt.Fprintf(stdout, "event-adapter processing %d route(s)\n", len(subs))

	for ctx.Err() == nil {
		for _, sub := range subs {
			msg, err := natsjs.FetchOne(ctx, sub)
			if err != nil {
				continue
			}
			ev, err := clevent.Parse(msg.Data)
			if err != nil {
				m.InvalidCloudEvent(ctx, "parse_error")
				if err := publishDefaultDLQAndAck(ctx, js, cfg, nil, err.Error(), msg, msg.Subject, stderr); err != nil {
					fmt.Fprintf(stderr, "publish dlq %s: %v\n", msg.Subject, err)
				}
				continue
			}
			route, ok := matcher.Match(msg.Subject, ev)
			if !ok {
				m.RouteMatchFailure(ctx)
				if err := publishDefaultDLQAndAck(ctx, js, cfg, ev, "no matching route", msg, msg.Subject, stderr); err != nil {
					fmt.Fprintf(stderr, "publish dlq %s: %v\n", msg.Subject, err)
				}
				continue
			}
			m.EventConsumed(ctx, route.Name)
			start := time.Now()
			if err := proc.Process(ctx, msg.Subject, ev, route, msg); err != nil {
				fmt.Fprintf(stderr, "process %s: %v\n", msg.Subject, err)
				continue
			}
			m.DispatchLatency(ctx, route.Name, time.Since(start))
		}
	}
	return 0
}

func loadConfig(path string) (*config.Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	cfg, err := config.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if errs := config.Validate(cfg); len(errs) > 0 {
		return nil, fmt.Errorf("validate config: %w", errs[0])
	}
	return cfg, nil
}

type dlqPublisher interface {
	PublishDLQ(context.Context, string, processor.DLQEvent) error
}

func publishDefaultDLQAndAck(ctx context.Context, pub dlqPublisher, cfg *config.Config, ev *clevent.Event, reason string, ack processor.Acker, subject string, stderr io.Writer) error {
	if err := publishDefaultDLQ(ctx, pub, cfg, ev, reason); err != nil {
		return err
	}
	if err := ack.Ack(ctx); err != nil {
		fmt.Fprintf(stderr, "ack %s: %v\n", subject, err)
	}
	return nil
}

func publishDefaultDLQ(ctx context.Context, pub dlqPublisher, cfg *config.Config, ev *clevent.Event, reason string) error {
	dlq := processor.DLQEvent{
		OriginalEvent: ev,
		FailureReason: reason,
		Timestamp:     time.Now().UTC(),
		SidecarAppID:  cfg.App.ID,
	}
	return pub.PublishDLQ(ctx, cfg.NATS.DefaultDLQSubject, dlq)
}
