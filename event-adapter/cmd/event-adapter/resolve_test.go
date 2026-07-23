package main

import (
	"testing"
	"time"

	"event-adapter/internal/config"
)

func baseAuthConfig() *config.Config {
	cfg := &config.Config{}
	cfg.App.Namespace = "cfg-ns"
	cfg.NatsAuth = &config.NATSAuthConfig{
		AuthURL:       "https://cfg-auth",
		RefreshBuffer: 90 * time.Second,
	}
	return cfg
}

func envFunc(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestResolveNatsAuthDisabledWhenNoAuthURL(t *testing.T) {
	_, enabled, err := resolveNatsAuth(&config.Config{}, envFunc(nil))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if enabled {
		t.Fatal("expected dynamic auth disabled when no authURL anywhere")
	}
}

func TestResolveNatsAuthEnvWinsOverConfig(t *testing.T) {
	got, enabled, err := resolveNatsAuth(baseAuthConfig(), envFunc(map[string]string{
		"EVENT_ADAPTER_AUTH_URL":  "https://env-auth",
		"EVENT_ADAPTER_APP_TOKEN": "tok",
		"EVENT_ADAPTER_NAMESPACE": "env-ns",
	}))
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if got.AuthURL != "https://env-auth" {
		t.Errorf("AuthURL = %q, want env value", got.AuthURL)
	}
	if got.Namespace != "env-ns" {
		t.Errorf("Namespace = %q, want env value", got.Namespace)
	}
	if got.AppToken != "tok" {
		t.Errorf("AppToken = %q", got.AppToken)
	}
}

func TestResolveNatsAuthConfigFallback(t *testing.T) {
	got, enabled, err := resolveNatsAuth(baseAuthConfig(), envFunc(map[string]string{
		"EVENT_ADAPTER_APP_TOKEN": "tok",
	}))
	if err != nil || !enabled {
		t.Fatalf("enabled=%v err=%v", enabled, err)
	}
	if got.AuthURL != "https://cfg-auth" {
		t.Errorf("AuthURL = %q, want config value", got.AuthURL)
	}
	if got.Namespace != "cfg-ns" {
		t.Errorf("Namespace = %q, want config value", got.Namespace)
	}
	if got.RefreshBuffer != 90*time.Second {
		t.Errorf("RefreshBuffer = %v, want 90s", got.RefreshBuffer)
	}
}

func TestResolveNatsAuthDefaults(t *testing.T) {
	cfg := &config.Config{NatsAuth: &config.NATSAuthConfig{AuthURL: "https://a"}}
	got, _, err := resolveNatsAuth(cfg, envFunc(map[string]string{
		"EVENT_ADAPTER_APP_TOKEN": "tok",
	}))
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got.RefreshBuffer != time.Minute {
		t.Errorf("RefreshBuffer default = %v, want 1m", got.RefreshBuffer)
	}
}

func TestResolveNatsAuthRejectsInvalidRefreshBuffer(t *testing.T) {
	cfg := &config.Config{NatsAuth: &config.NATSAuthConfig{AuthURL: "https://a"}}
	_, _, err := resolveNatsAuth(cfg, envFunc(map[string]string{
		"EVENT_ADAPTER_APP_TOKEN":      "tok",
		"EVENT_ADAPTER_REFRESH_BUFFER": "not-a-duration",
	}))
	if err == nil {
		t.Fatal("expected error for invalid EVENT_ADAPTER_REFRESH_BUFFER duration")
	}
}

func TestResolveNatsAuthRejectsBothCredsFileAndDynamic(t *testing.T) {
	cfg := &config.Config{}
	cfg.NATS.CredsFilePath = "/etc/x.creds"
	_, _, err := resolveNatsAuth(cfg, envFunc(map[string]string{
		"EVENT_ADAPTER_AUTH_URL":  "https://a",
		"EVENT_ADAPTER_APP_TOKEN": "tok",
	}))
	if err == nil {
		t.Fatal("expected error: credsFilePath and dynamic auth are mutually exclusive")
	}
}
