package natscreds

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunRefreshesAndForcesReconnect(t *testing.T) {
	var mints int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&mints, 1)
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		// short-lived JWT so the refresh loop fires quickly
		token := mintTestJWT(t, body["publicKey"], time.Now().Add(3*time.Second))
		_ = json.NewEncoder(w).Encode(map[string]string{"natsToken": token})
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	cfg.RefreshBuffer = 1 * time.Second // wake ~1-2s before expiry
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Mint(context.Background()); err != nil {
		t.Fatalf("initial Mint: %v", err)
	}

	reconnected := make(chan struct{}, 1)
	forceReconnect := func() error {
		select {
		case reconnected <- struct{}{}:
		default:
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx, forceReconnect) }()

	select {
	case <-reconnected:
		if atomic.LoadInt32(&mints) < 2 {
			t.Fatalf("expected a re-mint, only %d mint(s) seen", mints)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("refresh loop did not re-mint and force a reconnect")
	}
}

func TestRunRetriesForceReconnectFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		token := mintTestJWT(t, body["publicKey"], time.Now().Add(3*time.Second))
		_ = json.NewEncoder(w).Encode(map[string]string{"natsToken": token})
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	cfg.RefreshBuffer = 1 * time.Second
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.retryBackoff = 20 * time.Millisecond // keep the test fast
	if err := p.Mint(context.Background()); err != nil {
		t.Fatalf("initial Mint: %v", err)
	}

	var calls int32
	forceReconnect := func() error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return fmt.Errorf("simulated reconnect failure")
		}
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = p.Run(ctx, forceReconnect) }()

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&calls) >= 2 {
			return // retried after the first failure — success
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("forceReconnect was not retried after failure (calls=%d)", atomic.LoadInt32(&calls))
}

func TestRunStopsOnContextCancel(t *testing.T) {
	p, err := New(validConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	p.expiry = time.Now().Add(time.Hour) // far off, so Run parks in its timer
	cfg := p.cfg
	cfg.RefreshBuffer = time.Minute
	p.cfg = cfg

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- p.Run(ctx, func() error { return nil }) }()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
