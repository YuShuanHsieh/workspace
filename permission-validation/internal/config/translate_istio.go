package config

import "errors"

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
