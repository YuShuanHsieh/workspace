package natscreds

import "testing"

func TestNewRejectsInvalidNamespace(t *testing.T) {
	for _, ns := range []string{"a.b", "a*", "a>", "a b", "a\tb", ".", "*"} {
		cfg := validConfig()
		cfg.Namespace = ns
		if _, err := New(cfg); err == nil {
			t.Errorf("expected error for invalid namespace %q", ns)
		}
	}
}

func TestNewAcceptsValidNamespace(t *testing.T) {
	for _, ns := range []string{"task-service", "task_service", "app123", "a"} {
		cfg := validConfig()
		cfg.Namespace = ns
		if _, err := New(cfg); err != nil {
			t.Errorf("unexpected error for valid namespace %q: %v", ns, err)
		}
	}
}
