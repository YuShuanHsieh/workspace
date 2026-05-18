package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"permission-validation/internal/config"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(_ context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: validate-routes (validate|translate) <file> [flags]")
		return 2
	}
	switch args[0] {
	case "validate":
		return runValidate(args[1:], stderr)
	case "translate":
		return runTranslate(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown subcommand %q\n", args[0])
		return 2
	}
}

func runValidate(args []string, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: validate-routes validate <file>")
		return 2
	}
	rc, err := readConfig(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	errs := config.Validate(rc)
	if len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, e)
		}
		return 1
	}
	return 0
}

func runTranslate(args []string, stdout, stderr io.Writer) int {
	if len(args) < 1 {
		fmt.Fprintln(stderr, "usage: validate-routes translate <file> [-o output] [--sidecar-host h] [--sidecar-port n] [--backend-host h] [--backend-port n] [--admin-host h] [--access-log]")
		return 2
	}
	// First positional is the route-config file; flags follow it.
	file := args[0]
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output file (defaults to stdout)")
	sidecarHost := fs.String("sidecar-host", "127.0.0.1", "sidecar gRPC host Envoy will dial")
	sidecarPort := fs.Int("sidecar-port", 50051, "sidecar gRPC port Envoy will dial")
	backendHost := fs.String("backend-host", "127.0.0.1", "application backend host")
	backendPort := fs.Int("backend-port", 8080, "application backend port")
	adminHost := fs.String("admin-host", "127.0.0.1", "Envoy admin listener bind address (port 9901). Override to 0.0.0.0 only for local/test stacks that need host-side access.")
	accessLog := fs.Bool("access-log", false, "emit Envoy access logs to stdout. Recommended for production renders; off by default to keep test runs quiet.")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rc, err := readConfig(file)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if errs := config.Validate(rc); len(errs) > 0 {
		for _, e := range errs {
			fmt.Fprintln(stderr, e)
		}
		return 1
	}
	b, err := config.Translate(rc, config.TranslateOptions{
		SidecarHost: *sidecarHost, SidecarPort: *sidecarPort,
		AppBackendHost: *backendHost, AppBackendPort: *backendPort,
		AdminHost: *adminHost, AccessLogStdout: *accessLog,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *out == "" {
		_, _ = stdout.Write(b)
		return 0
	}
	if err := os.WriteFile(*out, b, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func readConfig(path string) (*config.RouteConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return config.Parse(b)
}
