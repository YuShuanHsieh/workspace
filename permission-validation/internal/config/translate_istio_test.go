package config

import "testing"

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
