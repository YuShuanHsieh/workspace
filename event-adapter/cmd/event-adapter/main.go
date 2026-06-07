package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"go.opentelemetry.io/otel/sdk/metric"

	"event-adapter/internal/config"
	"event-adapter/internal/consumer"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/metrics"
	"event-adapter/internal/natsjs"
	"event-adapter/internal/processor"
	"event-adapter/internal/responder"
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
	httpDispatcher := dispatcher.New(cfg.App.HTTPBaseURL, nil)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	if len(cfg.Routes) > 0 {
		matcher, err := router.New(cfg.Routes)
		if err != nil {
			fmt.Fprintf(stderr, "build router: %v\n", err)
			return 1
		}
		proc := processor.New(httpDispatcher, js)
		sub, err := js.SubscribeWildcard(cfg.NATS)
		if err != nil {
			fmt.Fprintf(stderr, "subscribe %s: %v\n", cfg.NATS.FilterSubject, err)
			return 1
		}
		cons := consumer.New(sub, proc, matcher, js, m, *cfg, cfg.NATS.FetchBatch, cfg.NATS.WorkerPoolSize, stderr)
		fmt.Fprintf(stdout, "event-adapter consuming %q with %d workers (batch %d)\n", cfg.NATS.FilterSubject, cfg.NATS.WorkerPoolSize, cfg.NATS.FetchBatch)
		wg.Add(1)
		go func() {
			defer wg.Done()
			cons.Run(ctx)
		}()
	}

	if cfg.Requests != nil {
		rmatcher, err := router.NewRequests(cfg.Requests.Routes)
		if err != nil {
			fmt.Fprintf(stderr, "build request router: %v\n", err)
			return 1
		}
		resp := responder.New(rmatcher, httpDispatcher, m, cfg.App.ID, cfg.Requests, stderr)
		fmt.Fprintf(stdout, "event-adapter responding to %q (queue %q) with %d workers\n", cfg.Requests.Subject, cfg.Requests.QueueGroup, cfg.Requests.WorkerPoolSize)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := resp.Run(ctx, js); err != nil {
				select {
				case errCh <- fmt.Errorf("responder: %w", err):
				default:
				}
				cancel()
			}
		}()
	}

	wg.Wait()
	select {
	case err := <-errCh:
		fmt.Fprintln(stderr, err)
		return 1
	default:
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
