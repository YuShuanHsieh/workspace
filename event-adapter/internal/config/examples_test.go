package config

import (
	"os"
	"testing"
)

func TestShippedExampleConfigsValidate(t *testing.T) {
	paths := []string{
		"../../examples/onboarding/routes.yaml",
		"../../test/e2e/routes.yaml",
	}
	for _, p := range paths {
		p := p
		t.Run(p, func(t *testing.T) {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("read %s: %v", p, err)
			}
			cfg, err := Parse(b)
			if err != nil {
				t.Fatalf("parse %s: %v", p, err)
			}
			if errs := Validate(cfg); len(errs) != 0 {
				t.Fatalf("validate %s: expected no errors, got %v", p, errs)
			}
		})
	}
}
