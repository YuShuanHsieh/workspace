package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/sdk/metric"
	"google.golang.org/grpc"

	"permission-validation/internal/extproc"
	"permission-validation/internal/metrics"
	"permission-validation/internal/pcs"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("permission-validation", flag.ContinueOnError)
	fs.SetOutput(stderr)
	listen := fs.String("listen", "0.0.0.0:50051", "gRPC listen address")
	pcsEndpoint := fs.String("pcs-endpoint", "http://permission-checking:8080", "base URL for PCS")
	pcsTimeout := fs.Duration("pcs-timeout", 250*time.Millisecond, "per-call PCS timeout")
	otelEndpoint := fs.String("otel-endpoint", "127.0.0.1:4317", "OTLP/gRPC metrics endpoint")
	otelDisabled := fs.Bool("otel-disabled", false, "disable OTLP export (uses no-op meter)")

	fs.Usage = func() {
		fmt.Fprintf(stderr, "permission-validation — Phase 1 Envoy ext_proc sidecar\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		// flag.ContinueOnError already printed; map --help to 0.
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	mp, shutdownMeter, err := buildMeterProvider(ctx, *otelEndpoint, *otelDisabled)
	if err != nil {
		fmt.Fprintln(stderr, "metrics:", err)
		return 1
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdownMeter(shutdownCtx)
	}()

	m := metrics.New(mp.Meter("permission-validation"))
	pcsClient := pcs.NewClient(*pcsEndpoint, *pcsTimeout)
	h := extproc.New(pcsClient, m)

	lis, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintln(stderr, "listen:", err)
		return 1
	}
	gs := grpc.NewServer()
	extproc.RegisterServer(gs, h)
	fmt.Fprintf(stdout, "permission-validation listening on %s\n", lis.Addr())

	serveErr := make(chan error, 1)
	go func() { serveErr <- gs.Serve(lis) }()

	select {
	case <-ctx.Done():
		gs.GracefulStop()
		return 0
	case err := <-serveErr:
		if err != nil {
			fmt.Fprintln(stderr, "serve:", err)
			return 1
		}
		return 0
	}
}

func buildMeterProvider(ctx context.Context, endpoint string, disabled bool) (*metric.MeterProvider, func(context.Context) error, error) {
	if disabled {
		mp := metric.NewMeterProvider()
		return mp, mp.Shutdown, nil
	}
	exp, err := otlpmetricgrpc.New(ctx,
		otlpmetricgrpc.WithEndpoint(endpoint),
		otlpmetricgrpc.WithInsecure(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("otlp: %w", err)
	}
	mp := metric.NewMeterProvider(
		metric.WithReader(metric.NewPeriodicReader(exp, metric.WithInterval(15*time.Second))),
	)
	return mp, mp.Shutdown, nil
}
