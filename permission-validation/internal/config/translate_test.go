package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestTranslate_MinimalProducesValidYAML(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	require.Empty(t, Validate(rc))
	got, err := Translate(rc, TranslateOptions{
		SidecarHost: "127.0.0.1", SidecarPort: 18080,
		AppBackendHost: "127.0.0.1", AppBackendPort: 8080,
		FailureModeAllow: false,
	})
	require.NoError(t, err)

	// Sanity: it's valid YAML.
	var parsed map[string]any
	require.NoError(t, yaml.Unmarshal(got, &parsed))

	s := string(got)
	require.Contains(t, s, "ext_proc")
	require.Contains(t, s, "failure_mode_allow: false")
	require.Contains(t, s, "/health")
	require.Contains(t, s, "/api/orders")
}

func TestTranslate_SkippedHasDisabled(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	got, err := Translate(rc, TranslateOptions{
		SidecarHost: "sidecar", SidecarPort: 50051,
		AppBackendHost: "backend", AppBackendPort: 8080,
	})
	require.NoError(t, err)
	s := string(got)
	healthIdx := strings.Index(s, "/health")
	require.GreaterOrEqual(t, healthIdx, 0)
	// The disabled override appears in the per-route section for /health.
	require.Contains(t, s[healthIdx:], "disabled: true")
}

func TestTranslate_DefaultDenyEmitsFallbackRoute(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	require.Equal(t, "deny", rc.DefaultBehavior)
	got, err := Translate(rc, TranslateOptions{SidecarHost: "s", SidecarPort: 1, AppBackendHost: "b", AppBackendPort: 1})
	require.NoError(t, err)
	require.Contains(t, string(got), "direct_response")
	require.Contains(t, string(got), "status: 403")
}
