package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestIstioOptions_Validate_RequiresNamespace(t *testing.T) {
	opts := IstioOptions{WorkloadLabels: map[string]string{"app": "x"}}
	if err := opts.Validate(); err == nil {
		t.Fatalf("expected error for missing namespace, got nil")
	}
}

func TestIstioOptions_Validate_RequiresOneWorkloadLabel(t *testing.T) {
	opts := IstioOptions{Namespace: "orders"}
	if err := opts.Validate(); err == nil {
		t.Fatalf("expected error for missing workload labels, got nil")
	}
}

func TestIstioOptions_Validate_HappyPath(t *testing.T) {
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	if err := opts.Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestTranslateIstio_MinimalProducesValidYAML(t *testing.T) {
	rc := &RouteConfig{
		Version:         "v1",
		AppID:           "orders-app",
		DefaultBehavior: "deny",
		Routes:          []RouteRule{{Method: "GET", Path: "/api/orders", Behavior: "protected"}},
	}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}

	b, err := TranslateIstio(rc, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("rendered YAML did not parse: %v\n%s", err, b)
	}
	if got := doc["apiVersion"]; got != "networking.istio.io/v1alpha3" {
		t.Fatalf("apiVersion: got %v, want networking.istio.io/v1alpha3", got)
	}
	if got := doc["kind"]; got != "EnvoyFilter" {
		t.Fatalf("kind: got %v, want EnvoyFilter", got)
	}
	if !strings.Contains(string(b), "namespace: orders") {
		t.Fatalf("expected namespace: orders in output\n%s", b)
	}
}
