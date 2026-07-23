package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/flywindy/o11y"
	o11yhttp "github.com/flywindy/o11y/http"
	"github.com/nats-io/nats.go"

	"event-adapter/internal/config"
	"event-adapter/internal/consumer"
	"event-adapter/internal/dispatcher"
	"event-adapter/internal/health"
	"event-adapter/internal/metrics"
	"event-adapter/internal/natscreds"
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

	// Observability settings may be overridden by environment variables, falling
	// back to the values from the config file. This lets a deployment tune the
	// sidecar's o11y (service identity, metrics transport, signal enablement, and
	// log level) without editing the mounted route config.
	serviceName := getEnvOrDefault("O11Y_SERVICE_NAME", obsCfg.ServiceName)
	serviceVersion := getEnvOrDefault("O11Y_SERVICE_VERSION", obsCfg.ServiceVersion)
	environment := getEnvOrDefault("O11Y_ENVIRONMENT", obsCfg.Environment)
	serviceNamespace := getEnvOrDefault("O11Y_SERVICE_NAMESPACE", obsCfg.ServiceNamespace)
	metricsAddr := getEnvOrDefault("METRICS_ADDR", obsCfg.MetricsAddr)
	otlpEndpoint := getEnvOrDefault("OTLP_ENDPOINT", obsCfg.OTLPEndpoint)
	traceEnabled := getEnvBool("O11Y_TRACE_ENABLED", tracingEnabled(opts.otelDisabled, otlpEndpoint))
	logEnabled := getEnvBool("O11Y_LOG_ENABLED", tracingEnabled(opts.otelDisabled, otlpEndpoint))
	logLevel := getEnvOrDefault("O11Y_LOG_LEVEL", "info")

	// Metrics transport is config-driven: Prometheus pull (default), OTLP push
	// when an OTLP endpoint is set, or fully disabled via --otel-disabled.
	mode := resolveMetricsMode(opts.otelDisabled, otlpEndpoint)

	// Dynamic NATS credentials: when configured (natsAuth in the config or the
	// EVENT_ADAPTER_* env vars), mint a NATS JWT from the auth-service instead of
	// using a static creds file. Values resolve env-first, then routes.yaml.
	authCfg, dynamicAuth, err := resolveNatsAuth(cfg, os.Getenv)
	if err != nil {
		fmt.Fprintf(stderr, "nats auth config: %v\n", err)
		return 1
	}
	var credsProvider *natscreds.Provider
	var natsOpts []nats.Option
	if dynamicAuth {
		credsProvider, err = natscreds.New(authCfg)
		if err != nil {
			fmt.Fprintf(stderr, "nats auth: %v\n", err)
			return 1
		}
		if err := credsProvider.Mint(ctx); err != nil {
			fmt.Fprintf(stderr, "nats auth: initial mint: %v\n", err)
			return 1
		}
		natsOpts = []nats.Option{
			nats.UserJWT(credsProvider.JWT, credsProvider.Sign),
			nats.MaxReconnects(-1),
		}
	}

	js, err := natsjs.Connect(cfg.NATS, natsOpts...)
	if err != nil {
		fmt.Fprintf(stderr, "connect nats: %v\n", err)
		return 1
	}
	defer js.Close()

	o11yOpts := []o11y.Option{
		o11y.WithServiceName(serviceName),
		o11y.WithServiceVersion(serviceVersion),
		o11y.WithEnvironment(environment),
		o11y.WithServiceNamespace(serviceNamespace),
		// Metrics defaults on here; the mode switch below turns it off for
		// --otel-disabled (last option wins). Trace and log enablement come from
		// the O11Y_*_ENABLED env vars, defaulting to on.
		o11y.WithMetricsEnabled(true),
		o11y.WithTraceEnabled(traceEnabled),
		o11y.WithLogEnabled(logEnabled),
		o11y.WithLogLevel(parseLogLevel(logLevel)),
	}
	// When an OTLP endpoint is set, all signals (including metrics) export via
	// OTLP; otherwise traces are not sampled (local dev / Prometheus-pull metrics).
	if otlpEndpoint != "" {
		o11yOpts = append(o11yOpts, o11y.WithOTLPEndpoint(otlpEndpoint))
	} else {
		o11yOpts = append(o11yOpts, o11y.WithSamplingRatio(0))
	}
	// Metrics transport: disabled under --otel-disabled, pushed via the OTLP
	// endpoint appended above, or served for Prometheus pull on metricsAddr.
	switch mode {
	case metricsOff:
		o11yOpts = append(o11yOpts, o11y.WithMetricsEnabled(false))
	case metricsPush:
		// metrics export through the OTLP endpoint configured above
	default:
		o11yOpts = append(o11yOpts, o11y.WithMetricsAddr(metricsAddr))
	}
	obs, err := o11y.Init(ctx, o11yOpts...)
	if err != nil {
		fmt.Fprintf(stderr, "init o11y: %v\n", err)
		return 1
	}
	defer func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if sErr := obs.Shutdown(shutdownCtx); sErr != nil {
			obs.Logger.ErrorContext(shutdownCtx, "o11y shutdown", slog.Any("error", sErr))
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

	// Proactively refresh the NATS JWT before it expires, reconnecting with the
	// fresh creds. Tied to the cancellable ctx and wg so it is cancelled and
	// joined on shutdown (before the deferred js.Close), and is never left
	// running by a later startup failure.
	if dynamicAuth {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = credsProvider.Run(ctx, js.ForceReconnect)
		}()
	}

	// Health-check server: /ready (NATS connectivity) and /live (event-loop
	// heartbeat). Metrics are served separately by the o11y SDK on metricsAddr.
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
		metricsDesc = "metrics push (OTLP) to " + otlpEndpoint
	case metricsOff:
		metricsDesc = "metrics disabled"
	default:
		metricsDesc = "metrics pull on " + metricsAddr + "/metrics"
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
	raw, err := os.ReadFile(path) // #nosec G304 -- config path is the trusted operator-supplied --config flag
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

// getEnvOrDefault returns the value of the environment variable named key, or
// defaultValue when the variable is unset or empty.
func getEnvOrDefault(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// getEnvBool parses the environment variable named key as a boolean, returning
// defaultValue when the variable is unset, empty, or not a valid boolean.
func getEnvBool(key string, defaultValue bool) bool {
	if value := os.Getenv(key); value != "" {
		if b, err := strconv.ParseBool(value); err == nil {
			return b
		}
	}
	return defaultValue
}

// tracingEnabled is the default enablement for the trace and log signals when
// no explicit O11Y_*_ENABLED override is set: signals default on unless OTel is
// disabled for local development, and stay on regardless when an OTLP endpoint
// is configured to receive them.
func tracingEnabled(otelDisabled bool, otlpEndpoint string) bool {
	return !otelDisabled || otlpEndpoint != ""
}

// parseLogLevel maps a textual log level (debug, info, warn, error) to an
// slog.Level, defaulting to info for empty or unrecognized values.
func parseLogLevel(level string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
