//go:build integration

// Package natscreds integration test: stands up a real NATS server in
// decentralized-JWT mode plus a stub /auth endpoint that mints user JWTs, then
// verifies event-adapter's provider can mint, connect, publish/subscribe, and
// proactively refresh + reconnect with a fresh JWT.
//
// Run with:  go test -tags=integration ./internal/natscreds/ -run TestEndToEnd -v
package natscreds_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nkeys"

	"event-adapter/internal/natscreds"
)

func TestEndToEndMintConnectAndRefresh(t *testing.T) {
	// --- decentralized JWT chain: operator -> account (signs user JWTs) ---
	okp, _ := nkeys.CreateOperator()
	opub, _ := okp.PublicKey()
	akp, _ := nkeys.CreateAccount()
	apub, _ := akp.PublicKey()

	oc := jwt.NewOperatorClaims(opub)
	oc.Name = "TEST-OP"
	operJWT, err := oc.Encode(okp)
	if err != nil {
		t.Fatalf("encode operator jwt: %v", err)
	}
	opClaims, err := jwt.DecodeOperatorClaims(operJWT)
	if err != nil {
		t.Fatalf("decode operator jwt: %v", err)
	}

	ac := jwt.NewAccountClaims(apub)
	ac.Name = "APP"
	accJWT, err := ac.Encode(okp)
	if err != nil {
		t.Fatalf("encode account jwt: %v", err)
	}

	// --- embedded NATS server trusting the operator, resolving the account ---
	resolver := &natsserver.MemAccResolver{}
	if err := resolver.Store(apub, accJWT); err != nil {
		t.Fatalf("store account jwt: %v", err)
	}
	srv, err := natsserver.NewServer(&natsserver.Options{
		Host:             "127.0.0.1",
		Port:             -1,
		TrustedOperators: []*jwt.OperatorClaims{opClaims},
		AccountResolver:  resolver,
		NoLog:            true,
		NoSigs:           true,
	})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	go srv.Start()
	if !srv.ReadyForConnections(5 * time.Second) {
		t.Fatal("nats server not ready")
	}
	defer srv.Shutdown()

	// --- stub /auth: mint a short-lived user JWT signed by the account ---
	var mints int32
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		uc := jwt.NewUserClaims(body["publicKey"])
		uc.Name = body["namespace"]
		uc.Expires = time.Now().Add(4 * time.Second).Unix() // short, to exercise refresh
		token, err := uc.Encode(akp)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		atomic.AddInt32(&mints, 1)
		_ = json.NewEncoder(w).Encode(map[string]any{"natsToken": token, "expiresIn": 3})
	}))
	defer stub.Close()

	// --- provider: mint once, connect with UserJWT ---
	p, err := natscreds.New(natscreds.Config{
		AuthURL:       stub.URL,
		Namespace:     "app",
		AppToken:      "app-token",
		RefreshBuffer: 1 * time.Second, // re-mint ~3s after each mint (one calm refresh)
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Mint(ctx); err != nil {
		t.Fatalf("initial Mint: %v", err)
	}

	nc, err := nats.Connect(srv.ClientURL(),
		nats.UserJWT(p.JWT, p.Sign),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(50*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer nc.Close()
	if !nc.IsConnected() {
		t.Fatal("expected connected")
	}

	// --- pub/sub sanity ---
	sub, err := nc.SubscribeSync("test.subject")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := nc.Publish("test.subject", []byte("hello")); err != nil {
		t.Fatalf("publish: %v", err)
	}
	_ = nc.Flush()
	msg, err := sub.NextMsg(2 * time.Second)
	if err != nil || string(msg.Data) != "hello" {
		t.Fatalf("pub/sub failed: msg=%v err=%v", msg, err)
	}

	// --- refresh loop: should re-mint and ForceReconnect before expiry ---
	go func() { _ = p.Run(ctx, nc.ForceReconnect) }()

	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&mints) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if atomic.LoadInt32(&mints) < 2 {
		t.Fatalf("expected a re-mint, saw %d", mints)
	}

	// Let the ForceReconnect settle, then confirm the connection is still usable
	// with the fresh JWT (the original 4s JWT would otherwise have expired).
	time.Sleep(500 * time.Millisecond)
	if !nc.IsConnected() {
		t.Fatal("connection dropped after refresh")
	}
	// Robust re-verify: publish/receive should work post-refresh (retry to avoid
	// a race with a reconnect that may still be settling).
	var delivered bool
	for i := 0; i < 5 && !delivered; i++ {
		_ = nc.Publish("test.subject", []byte("again"))
		_ = nc.Flush()
		if _, err := sub.NextMsg(500 * time.Millisecond); err == nil {
			delivered = true
		}
	}
	if !delivered {
		t.Fatal("pub/sub after refresh failed")
	}
	t.Logf("verified: %d mints, still connected and publishing after refresh", atomic.LoadInt32(&mints))
}
