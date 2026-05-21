package config

import (
	_ "embed"
	"bytes"
	"errors"
	"fmt"
	"text/template"
)

// IstioOptions are environment-specific values for the istio render target.
// They are intentionally NOT a superset of TranslateOptions — the two targets
// take disjoint flags and the CLI dispatches between them on --target.
type IstioOptions struct {
	// Namespace is metadata.namespace on the EnvoyFilter. Required.
	Namespace string
	// WorkloadLabels keys spec.workloadSelector.labels. Must have ≥1 entry.
	WorkloadLabels map[string]string
	// Name overrides metadata.name. Empty → permission-validation-<appId>.
	Name string
	// SidecarPort is the gRPC port the static cluster targets at 127.0.0.1.
	// Defaults to 50051 when zero.
	SidecarPort int
	// ProbePaths is the exact-match list of paths that bypass ext_proc at the
	// Envoy level (liveness/readiness probes). Empty → /healthz,/readyz,/livez.
	ProbePaths []string
}

// Validate returns an error if required fields are missing or malformed.
func (o IstioOptions) Validate() error {
	if o.Namespace == "" {
		return errors.New("IstioOptions.Namespace is required")
	}
	if len(o.WorkloadLabels) == 0 {
		return errors.New("IstioOptions.WorkloadLabels must have at least one entry")
	}
	return nil
}

//go:embed envoy-filter.tmpl.yaml
var envoyFilterTemplate string

var defaultProbePaths = []string{"/healthz", "/readyz", "/livez"}

// TranslateIstio renders an Istio EnvoyFilter CRD from rc + opts.
// The rendered CRD is independent of the route list — protected/skipped
// decisions migrate into the sidecar (which reads routes.yaml at startup
// via --routes-file). The CRD installs the ext_proc filter, the static
// pv_sidecar cluster, and the probe-path carve-out only.
func TranslateIstio(rc *RouteConfig, opts IstioOptions) ([]byte, error) {
	if err := opts.Validate(); err != nil {
		return nil, fmt.Errorf("istio options: %w", err)
	}
	if opts.SidecarPort == 0 {
		opts.SidecarPort = 50051
	}
	if opts.Name == "" {
		opts.Name = "permission-validation-" + rc.AppID
	}
	if len(opts.ProbePaths) == 0 {
		opts.ProbePaths = append([]string(nil), defaultProbePaths...)
	}
	tmpl, err := template.New("envoy-filter").Parse(envoyFilterTemplate)
	if err != nil {
		return nil, fmt.Errorf("istio template parse: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, opts); err != nil {
		return nil, fmt.Errorf("istio template execute: %w", err)
	}
	return buf.Bytes(), nil
}
