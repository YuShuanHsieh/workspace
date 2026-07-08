package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/flywindy/o11y"
	o11yhttp "github.com/flywindy/o11y/http"

	"event-adapter/internal/config"
	"event-adapter/internal/consumer"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/health"
	"event-adapter/internal/metrics"
	"event-adapter/internal/natsjs"
	"event-adapter/internal/processor"
	"event-adapter/internal/responder"
	"event-adapter/internal/router"
)

// maxHeartbeatAge is how stale the consumer heartbeat may become before the
// /live probe reports the event loop wedged.
const maxHeartbeatAge = 60 * time.Second

type options struct {
	configPath   string
	otelDisabled bool
}

// metricsMode selects how metrics leave the process.
type metricsMode int

const (
	metricsPull metricsMode = iota // serve /metrics for Prometheus scrape (default)
	metricsPush                    // export via OTLP to a collector
	metricsOff                     // no metrics export (--otel-disabled, local dev)
)

func (m metricsMode) String() string {
	switch m {
	case metricsPush:
		return "push"
	case metricsOff:
		return "off"
	default:
		return "pull"
	}
}

// resolveMetricsMode picks the metrics export mode: --otel-disabled wins, then a
// configured OTLP endpoint means push, otherwise Prometheus pull (the default).
func resolveMetricsMode(otelDisabled bool, otlpEndpoint string) metricsMode {
	switch {
	case otelDisabled:
		return metricsOff
	case otlpEndpoint != "":
		return metricsPush
	default:
		return metricsPull
	}
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
	fs.BoolVar(&opts.otelDisabled, "otel-disabled", false, "disable metrics export (no Prometheus server, no OTLP push) for local development")
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
	obsCfg := cfg.Observability.WithDefaults()

	js, err := natsjs.Connect(cfg.NATS)
	if err != nil {
		fmt.Fprintf(stderr, "connect nats: %v\n", err)
		return 1
	}
	defer js.Close()

	// Metrics transport is config-driven: Prometheus pull (default), OTLP push
	// when an OTLP endpoint is set, or fully disabled via --otel-disabled.
	mode := resolveMetricsMode(opts.otelDisabled, obsCfg.MetricsOTLPEndpoint)
	o11yOpts := []o11y.Option{
		o11y.WithServiceName(obsCfg.ServiceName),
		o11y.WithServiceVersion(obsCfg.ServiceVersion),
		o11y.WithEnvironment(obsCfg.Environment),
		o11y.WithServiceNamespace(obsCfg.ServiceNamespace),
		// Enable all three signals by default, independent of any ambient
		// O11Y_*_ENABLED env vars. Metrics can still be turned off below: the
		// mode switch appends after these, and the last option wins, so
		// --otel-disabled (WithMetricsEnabled(false)) overrides this default.
		o11y.WithMetricsEnabled(true),
		o11y.WithTraceEnabled(true),
		o11y.WithLogEnabled(true),
	}
	switch mode {
	case metricsOff:
		o11yOpts = append(o11yOpts, o11y.WithMetricsEnabled(false))
	case metricsPush:
		o11yOpts = append(o11yOpts, o11y.WithMetricsOTLPEndpoint(obsCfg.MetricsOTLPEndpoint))
	default:
		o11yOpts = append(o11yOpts, o11y.WithMetricsAddr(obsCfg.MetricsAddr))
	}
	obs, err := o11y.Init(ctx, o11yOpts...)
	if err != nil {
		fmt.Fprintf(stderr, "init o11y: %v\n", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelShutdown()
		if sErr := obs.Shutdown(shutdownCtx); sErr != nil {
			fmt.Fprintf(stderr, "o11y shutdown: %v\n", sErr)
		}
	}()
	m := metrics.New(obs.Meter("event-adapter"))
	// Dispatch over an o11y-instrumented transport so outbound calls to the app
	// produce client spans named "METHOD /path" (e.g. "POST /orders") instead of
	// the default "HTTP POST". CheckRedirect mirrors dispatcher.New's own default.
	dispatchClient := &http.Client{
		Transport: o11yhttp.NewTransport(
			http.DefaultTransport,
			obs.TracerProvider(),
			obs.MeterProvider(),
			obs.Propagator,
			o11yhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
				return r.Method + " " + r.URL.Path
			}),
		),
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	httpDispatcher := dispatcher.New(cfg.App.HTTPBaseURL, dispatchClient)

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	errCh := make(chan error, 1)

	// Health-check server: /ready (NATS connectivity) and /live (event-loop
	// heartbeat). Metrics are served separately by the o11y SDK on obsCfg.MetricsAddr.
	heartbeat := &health.Heartbeat{}
	checker := &health.Checker{
		NATSConnected:   js.IsConnected,
		Heartbeat:       heartbeat,
		MaxHeartbeatAge: maxHeartbeatAge,
	}
	healthSrv := health.NewServer(obsCfg.HealthAddr, checker)
	wg.Add(1)
	go func() {
		defer wg.Done()
		if sErr := healthSrv.ListenAndServe(); sErr != nil && !errors.Is(sErr, http.ErrServerClosed) {
			select {
			case errCh <- fmt.Errorf("health server: %w", sErr):
			default:
			}
			cancel()
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancelShutdown := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelShutdown()
		_ = healthSrv.Shutdown(shutdownCtx)
	}()
	var metricsDesc string
	switch mode {
	case metricsPush:
		metricsDesc = "metrics push (OTLP) to " + obsCfg.MetricsOTLPEndpoint
	case metricsOff:
		metricsDesc = "metrics disabled"
	default:
		metricsDesc = "metrics pull on " + obsCfg.MetricsAddr + "/metrics"
	}
	fmt.Fprintf(stdout, "event-adapter health on %s (/ready, /live), %s\n", obsCfg.HealthAddr, metricsDesc)

	if len(cfg.Routes) > 0 {
		matcher, err := router.New(cfg.Routes)
		if err != nil {
			fmt.Fprintf(stderr, "build router: %v\n", err)
			return 1
		}
		proc := processor.New(httpDispatcher, js).WithObservability(m, obs.Logger)
		sub, err := js.SubscribeWildcard(cfg.NATS)
		if err != nil {
			fmt.Fprintf(stderr, "subscribe %s: %v\n", cfg.NATS.FilterSubject, err)
			return 1
		}
		cons := consumer.New(sub, proc, matcher, js, m, *cfg, cfg.NATS.FetchBatch, cfg.NATS.WorkerPoolSize, stderr)
		cons.WithHeartbeat(heartbeat).WithBackpressure(obsCfg.BackpressureThreshold, func(c context.Context) (int64, error) {
			return js.ConsumerPending(cfg.NATS.Stream, cfg.NATS.DurableConsumer)
		})
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
		resp := responder.New(rmatcher, httpDispatcher, m, cfg.App.ID, cfg.Requests, stderr).WithHeartbeat(heartbeat)
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
