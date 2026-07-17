package natscreds

import "testing"

func validConfig() Config {
	return Config{
		AuthURL:   "http://auth.example",
		Namespace: "task-service",
		AppToken:  "app-token-xyz",
	}
}

func TestNewRejectsEmptyAppToken(t *testing.T) {
	cfg := validConfig()
	cfg.AppToken = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for empty app token")
	}
}

func TestNewRejectsEmptyAuthURL(t *testing.T) {
	cfg := validConfig()
	cfg.AuthURL = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for empty auth URL")
	}
}

func TestNewRejectsEmptyNamespace(t *testing.T) {
	cfg := validConfig()
	cfg.Namespace = ""
	if _, err := New(cfg); err == nil {
		t.Fatal("expected error for empty namespace")
	}
}

func TestNewGeneratesKeyPair(t *testing.T) {
	p, err := New(validConfig())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if p.publicKey == "" {
		t.Fatal("expected a generated public key")
	}
	if p.publicKey[0] != 'U' {
		t.Fatalf("expected a user nkey (prefix U), got %q", p.publicKey)
	}
}
