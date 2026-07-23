package natscreds

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/jwt/v2"
	"github.com/nats-io/nkeys"
)

// mintTestJWT returns a real NATS user JWT for userPub, signed by a fresh
// account key, with the given expiry — decodable by the provider.
func mintTestJWT(t *testing.T, userPub string, exp time.Time) string {
	t.Helper()
	akp, err := nkeys.CreateAccount()
	if err != nil {
		t.Fatalf("create account key: %v", err)
	}
	uc := jwt.NewUserClaims(userPub)
	uc.Expires = exp.Unix()
	token, err := uc.Encode(akp)
	if err != nil {
		t.Fatalf("encode user jwt: %v", err)
	}
	return token
}

func TestMintPostsBodyAndParsesExpiry(t *testing.T) {
	var gotBody map[string]string
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		token := mintTestJWT(t, gotBody["publicKey"], time.Now().Add(time.Hour))
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"natsToken": token, "expiresIn": 3600})
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if err := p.Mint(context.Background()); err != nil {
		t.Fatalf("Mint: %v", err)
	}

	if gotPath != "/api/v1/auth" {
		t.Errorf("path = %q, want /api/v1/auth", gotPath)
	}
	if gotBody["token_type"] != "app" {
		t.Errorf("token_type = %q, want app", gotBody["token_type"])
	}
	if gotBody["token"] != "app-token-xyz" {
		t.Errorf("token = %q, want app-token-xyz", gotBody["token"])
	}
	if gotBody["namespace"] != "task-service" {
		t.Errorf("namespace = %q, want task-service", gotBody["namespace"])
	}
	if gotBody["publicKey"] != p.publicKey {
		t.Errorf("publicKey = %q, want %q", gotBody["publicKey"], p.publicKey)
	}

	gotJWT, gotExp := p.jwt, p.expiry
	if gotJWT == "" {
		t.Fatal("expected a cached JWT after Mint")
	}
	if d := time.Until(gotExp); d < 50*time.Minute || d > 70*time.Minute {
		t.Fatalf("parsed expiry ~%v away, want ~1h", d)
	}
}

func TestMintRejectsJWTForWrongPublicKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Mint a JWT whose subject is some *other* user key, not ours.
		otherKP, _ := nkeys.CreateUser()
		otherPub, _ := otherKP.PublicKey()
		token := mintTestJWT(t, otherPub, time.Now().Add(time.Hour))
		_ = json.NewEncoder(w).Encode(map[string]string{"natsToken": token})
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	p, _ := New(cfg)
	if err := p.Mint(context.Background()); err == nil {
		t.Fatal("expected error: JWT subject does not match our public key")
	}
}

func TestMintRejectsTokenExpiringWithinRefreshBuffer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		token := mintTestJWT(t, body["publicKey"], time.Now().Add(10*time.Second))
		_ = json.NewEncoder(w).Encode(map[string]string{"natsToken": token})
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	cfg.RefreshBuffer = time.Minute // 60s > the 10s token lifetime
	p, _ := New(cfg)
	if err := p.Mint(context.Background()); err == nil {
		t.Fatal("expected error: token expires within the refresh buffer")
	}
}

func TestMintSurfacesAuthError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	p, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := p.Mint(context.Background()); err == nil {
		t.Fatal("expected an error when /auth returns 401")
	}
}

func TestMintErrorDoesNotLeakSecret(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "bad", http.StatusBadRequest)
	}))
	defer srv.Close()

	cfg := validConfig()
	cfg.AuthURL = srv.URL
	cfg.AppToken = "super-secret-token"
	p, _ := New(cfg)
	err := p.Mint(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "super-secret-token") {
		t.Fatalf("error leaked the app token: %v", err)
	}
}
