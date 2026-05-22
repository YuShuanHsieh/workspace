package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

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
		fmt.Fprintln(stderr,
			"usage: validate-routes translate <file> [--target=static|istio] [target-specific flags]")
		return 2
	}
	// First positional is the route-config file; flags follow it.
	file := args[0]
	fs := flag.NewFlagSet("translate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("o", "", "output file (defaults to stdout)")
	target := fs.String("target", "static", "render target: static | istio")

	// Static-target flags. Default values stay backwards-compatible with
	// the Phase 1 invocation; runTranslate validates the matrix below.
	sidecarHost := fs.String("sidecar-host", "127.0.0.1", "sidecar gRPC host Envoy will dial (static only)")
	sidecarPort := fs.Int("sidecar-port", 50051, "sidecar gRPC port (both targets)")
	backendHost := fs.String("backend-host", "127.0.0.1", "application backend host (static only)")
	backendPort := fs.Int("backend-port", 8080, "application backend port (static only)")
	adminHost := fs.String("admin-host", "127.0.0.1", "Envoy admin bind (static only)")
	accessLog := fs.Bool("access-log", false, "emit Envoy access logs (static only)")

	// Istio-target flags.
	namespace := fs.String("namespace", "", "metadata.namespace on the EnvoyFilter (istio only, required)")
	var workloadLabels stringMap
	fs.Var(&workloadLabels, "workload-label", "repeatable key=value pairs for workloadSelector (istio only, ≥1 required)")
	filterName := fs.String("name", "", "EnvoyFilter metadata.name (istio only; defaults to permission-validation-<appId>)")
	probePaths := fs.String("probe-paths", "", "comma-separated exact paths to bypass ext_proc (istio only; defaults to /healthz,/readyz,/livez)")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if !validPort(*sidecarPort) {
		fmt.Fprintf(stderr, "sidecar-port must be in range 1..65535 (got %d)\n", *sidecarPort)
		return 2
	}

	switch *target {
	case "static":
		// Reject istio-only flags.
		if *namespace != "" {
			fmt.Fprintln(stderr, "--namespace is only valid with --target=istio")
			return 2
		}
		if len(workloadLabels) > 0 {
			fmt.Fprintln(stderr, "--workload-label is only valid with --target=istio")
			return 2
		}
		if *filterName != "" {
			fmt.Fprintln(stderr, "--name is only valid with --target=istio")
			return 2
		}
		if *probePaths != "" {
			fmt.Fprintln(stderr, "--probe-paths is only valid with --target=istio")
			return 2
		}
		return runTranslateStatic(file, *out, *sidecarHost, *sidecarPort, *backendHost, *backendPort, *adminHost, *accessLog, stdout, stderr)
	case "istio":
		// Reject static-only flags (the ones explicitly set; detect via fs.Visit).
		var rejected []string
		fs.Visit(func(f *flag.Flag) {
			switch f.Name {
			case "backend-host", "backend-port", "admin-host", "access-log":
				rejected = append(rejected, "--"+f.Name)
			case "sidecar-host":
				rejected = append(rejected, "--sidecar-host (use --workload-label / pod-local 127.0.0.1 in istio mode)")
			}
		})
		if len(rejected) > 0 {
			fmt.Fprintf(stderr, "the following flags are not valid with --target=istio: %v\n", rejected)
			return 2
		}
		return runTranslateIstio(file, *out, *namespace, workloadLabels, *filterName, *probePaths, *sidecarPort, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "--target must be one of: static, istio (got %q)\n", *target)
		return 2
	}
}

// stringMap is a repeatable flag.Value that accumulates key=value pairs.
type stringMap map[string]string

func (m *stringMap) String() string {
	if m == nil || *m == nil {
		return ""
	}
	parts := make([]string, 0, len(*m))
	for k, v := range *m {
		parts = append(parts, k+"="+v)
	}
	return strings.Join(parts, ",")
}

func (m *stringMap) Set(s string) error {
	i := strings.IndexByte(s, '=')
	if i <= 0 || i == len(s)-1 {
		return fmt.Errorf("expected key=value, got %q", s)
	}
	if *m == nil {
		*m = map[string]string{}
	}
	(*m)[s[:i]] = s[i+1:]
	return nil
}

func runTranslateStatic(file, out, sidecarHost string, sidecarPort int, backendHost string, backendPort int, adminHost string, accessLog bool, stdout, stderr io.Writer) int {
	if !validPort(backendPort) {
		fmt.Fprintf(stderr, "backend-port must be in range 1..65535 (got %d)\n", backendPort)
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
		SidecarHost: sidecarHost, SidecarPort: sidecarPort,
		AppBackendHost: backendHost, AppBackendPort: backendPort,
		AdminHost: adminHost, AccessLogStdout: accessLog,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOut(out, b, stdout, stderr)
}

func runTranslateIstio(file, out, namespace string, workloadLabels stringMap, name, probePathsCSV string, sidecarPort int, stdout, stderr io.Writer) int {
	if namespace == "" {
		fmt.Fprintln(stderr, "--namespace is required with --target=istio")
		return 2
	}
	if len(workloadLabels) == 0 {
		fmt.Fprintln(stderr, "--workload-label is required with --target=istio (at least one)")
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
	opts := config.IstioOptions{
		Namespace:      namespace,
		WorkloadLabels: workloadLabels,
		Name:           name,
		SidecarPort:    sidecarPort,
	}
	if probePathsCSV != "" {
		opts.ProbePaths = strings.Split(probePathsCSV, ",")
	}
	b, err := config.TranslateIstio(rc, opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeOut(out, b, stdout, stderr)
}

func writeOut(out string, b []byte, stdout, stderr io.Writer) int {
	if out == "" {
		n, err := stdout.Write(b)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if n != len(b) {
			fmt.Fprintf(stderr, "short write: wrote %d of %d bytes\n", n, len(b))
			return 1
		}
		return 0
	}
	if err := os.WriteFile(out, b, 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func validPort(p int) bool {
	return p >= 1 && p <= 65535
}

func readConfig(path string) (*config.RouteConfig, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return config.Parse(b)
}
