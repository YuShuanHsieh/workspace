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

func TestTranslateIstio_NameDefaultsFromAppID(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	b, err := TranslateIstio(rc, opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(string(b), "name: permission-validation-orders-app") {
		t.Fatalf("expected default name 'permission-validation-orders-app'; got:\n%s", b)
	}
}

func TestTranslateIstio_ExplicitNameOverridesDefault(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		Name:           "custom-filter-name",
	}
	b, _ := TranslateIstio(rc, opts)
	if !strings.Contains(string(b), "name: custom-filter-name") {
		t.Fatalf("expected explicit name; got:\n%s", b)
	}
}

func TestTranslateIstio_ProbePathsDefault(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	for _, p := range []string{`path: "/healthz"`, `path: "/readyz"`, `path: "/livez"`} {
		if !strings.Contains(s, p) {
			t.Fatalf("expected default probe-path entry %q; got:\n%s", p, b)
		}
	}
}

func TestTranslateIstio_ProbePathsOverride(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "orders-app", DefaultBehavior: "deny"}
	opts := IstioOptions{
		Namespace:      "orders",
		WorkloadLabels: map[string]string{"app": "orders-app"},
		ProbePaths:     []string{"/health", "/ready"},
	}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	if !strings.Contains(s, `path: "/health"`) || !strings.Contains(s, `path: "/ready"`) {
		t.Fatalf("expected /health and /ready probe paths; got:\n%s", b)
	}
	// Defaults must NOT appear when override given.
	for _, p := range []string{`path: "/healthz"`, `path: "/readyz"`, `path: "/livez"`} {
		if strings.Contains(s, p) {
			t.Fatalf("default probe path %q must not appear when override is given; got:\n%s", p, b)
		}
	}
}

func TestTranslateIstio_WorkloadLabelsRendered(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{
		Namespace:      "ns",
		WorkloadLabels: map[string]string{"app": "x", "tier": "api"},
	}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	if !strings.Contains(s, "app: x") || !strings.Contains(s, "tier: api") {
		t.Fatalf("expected both labels rendered; got:\n%s", b)
	}
}

func TestTranslateIstio_RoutesNotInOutput(t *testing.T) {
	rc := &RouteConfig{
		Version: "v1", AppID: "orders-app", DefaultBehavior: "deny",
		Routes: []RouteRule{
			{Method: "GET", Path: "/api/orders/secret", Behavior: "protected"},
			{Method: "POST", Path: "/api/orders/admin", Behavior: "skipped"},
		},
	}
	opts := IstioOptions{Namespace: "orders", WorkloadLabels: map[string]string{"app": "orders-app"}}
	b, _ := TranslateIstio(rc, opts)
	s := string(b)
	// Routes from routes.yaml MUST NOT appear in the EnvoyFilter — they move into
	// the sidecar via --routes-file. Probe paths are the only routes in the CRD.
	if strings.Contains(s, "/api/orders/secret") || strings.Contains(s, "/api/orders/admin") {
		t.Fatalf("routes.yaml paths must not appear in the EnvoyFilter; got:\n%s", b)
	}
}

func TestTranslateIstio_ContextIsSidecarInbound(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "ns", WorkloadLabels: map[string]string{"app": "x"}}
	b, _ := TranslateIstio(rc, opts)
	// Every configPatches[].match.context MUST be SIDECAR_INBOUND.
	// 3 patches × 1 context line each = 3.
	got := strings.Count(string(b), "context: SIDECAR_INBOUND")
	if got != 3 {
		t.Fatalf("expected 3 SIDECAR_INBOUND contexts; got %d in:\n%s", got, b)
	}
}

func TestTranslateIstio_RejectsEmptyNamespace(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{WorkloadLabels: map[string]string{"app": "x"}}
	if _, err := TranslateIstio(rc, opts); err == nil {
		t.Fatalf("expected error for empty Namespace")
	}
}

func TestTranslateIstio_RejectsEmptyWorkloadLabels(t *testing.T) {
	rc := &RouteConfig{Version: "v1", AppID: "x", DefaultBehavior: "deny"}
	opts := IstioOptions{Namespace: "ns"}
	if _, err := TranslateIstio(rc, opts); err == nil {
		t.Fatalf("expected error for empty WorkloadLabels")
	}
}
