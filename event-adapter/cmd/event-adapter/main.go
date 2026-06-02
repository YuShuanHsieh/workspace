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

	"go.opentelemetry.io/otel/sdk/metric"

	"event-adapter/internal/config"
	"event-adapter/internal/consumer"
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

	sub, err := js.SubscribeWildcard(cfg.NATS)
	if err != nil {
		fmt.Fprintf(stderr, "subscribe %s: %v\n", cfg.NATS.FilterSubject, err)
		return 1
	}

	cons := consumer.New(sub, proc, matcher, js, m, *cfg, cfg.NATS.FetchBatch, cfg.NATS.WorkerPoolSize, stderr)
	fmt.Fprintf(stdout, "event-adapter consuming %q with %d workers (batch %d)\n", cfg.NATS.FilterSubject, cfg.NATS.WorkerPoolSize, cfg.NATS.FetchBatch)
	cons.Run(ctx)
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
