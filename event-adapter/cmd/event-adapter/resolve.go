package main

import (
	"fmt"
	"time"

	"event-adapter/internal/config"
	"event-adapter/internal/natscreds"
)

// resolveNatsAuth merges the dynamic NATS-auth settings, taking each value
// env-first (EVENT_ADAPTER_*) then the routes.yaml fallback (natsAuth/app),
// mirroring the o11y config override pattern. Dynamic auth is enabled when a
// resolved authURL is present. The app token is env-only. It returns an error
// when both a static nats.credsFilePath and dynamic auth are configured, which
// are mutually exclusive.
func resolveNatsAuth(cfg *config.Config, getenv func(string) string) (natscreds.Config, bool, error) {
	var cfgAuthURL string
	var cfgRefresh time.Duration
	if cfg.NatsAuth != nil {
		cfgAuthURL = cfg.NatsAuth.AuthURL
		cfgRefresh = cfg.NatsAuth.RefreshBuffer
	}

	authURL := envOr(getenv, "EVENT_ADAPTER_AUTH_URL", cfgAuthURL)
	if authURL == "" {
		return natscreds.Config{}, false, nil // dynamic auth not configured
	}
	if cfg.NATS.CredsFilePath != "" {
		return natscreds.Config{}, false, fmt.Errorf(
			"nats.credsFilePath and dynamic auth (authURL) are mutually exclusive; set only one")
	}

	refreshBuffer := cfgRefresh
	if v := getenv("EVENT_ADAPTER_REFRESH_BUFFER"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return natscreds.Config{}, false, fmt.Errorf("invalid EVENT_ADAPTER_REFRESH_BUFFER %q: %w", v, err)
		}
		refreshBuffer = d
	}
	if refreshBuffer <= 0 {
		refreshBuffer = time.Minute
	}

	return natscreds.Config{
		AuthURL:       authURL,
		Namespace:     envOr(getenv, "EVENT_ADAPTER_NAMESPACE", cfg.App.Namespace),
		AppToken:      getenv("EVENT_ADAPTER_APP_TOKEN"),
		RefreshBuffer: refreshBuffer,
	}, true, nil
}

// envOr returns getenv(key) when non-empty, otherwise fallback.
func envOr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
