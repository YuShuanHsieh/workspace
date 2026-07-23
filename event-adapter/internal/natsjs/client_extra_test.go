package natsjs

import (
	"testing"

	"github.com/nats-io/nats.go"

	"event-adapter/internal/config"
)

func TestBuildOptsAppendsExtraOptions(t *testing.T) {
	opts := buildOpts(config.NATSConfig{URL: "nats://localhost:4222"}, nats.Name("a"), nats.Name("b"))
	if len(opts) != 2 {
		t.Fatalf("buildOpts with 2 extra = %d, want 2", len(opts))
	}
}

func TestBuildOptsCombinesCredsAndExtra(t *testing.T) {
	cfg := config.NATSConfig{CredsFilePath: "/etc/nats/svc.creds"} //nolint:gosec // test fixture
	opts := buildOpts(cfg, nats.Name("a"))
	if len(opts) != 2 {
		t.Fatalf("creds + 1 extra = %d, want 2 (creds + extra)", len(opts))
	}
}
