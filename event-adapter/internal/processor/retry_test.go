package processor

import (
	"testing"
	"time"
)

func TestBackoffCapsAtMax(t *testing.T) {
	p := RetryPolicy{MaxAttempts: 4, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 250 * time.Millisecond}
	got := p.Delay(4)
	if got != 250*time.Millisecond {
		t.Fatalf("expected max backoff, got %s", got)
	}
}
