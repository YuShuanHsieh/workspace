package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func loadFile(t *testing.T, name string) *RouteConfig {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "routes", name))
	require.NoError(t, err)
	rc, err := Parse(b)
	require.NoError(t, err)
	return rc
}

func TestValidate_MinimalOK(t *testing.T) {
	rc := loadFile(t, "valid-minimal.yaml")
	require.Empty(t, Validate(rc))
}

func TestValidate_ComprehensiveOK(t *testing.T) {
	rc := loadFile(t, "valid-comprehensive.yaml")
	require.Empty(t, Validate(rc))
}

func TestValidate_WrongVersion(t *testing.T) {
	rc := loadFile(t, "invalid-wrong-version.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "version")
}

func TestValidate_EmptyRoutes(t *testing.T) {
	rc := loadFile(t, "invalid-empty-routes.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "routes")
}

func TestValidate_BadMethod(t *testing.T) {
	rc := loadFile(t, "invalid-bad-method.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "method")
}

func TestValidate_BadBehavior(t *testing.T) {
	rc := loadFile(t, "invalid-bad-behavior.yaml")
	errs := Validate(rc)
	require.Len(t, errs, 1)
	require.Contains(t, errs[0].Error(), "behavior")
}

func TestValidate_BadDefault(t *testing.T) {
	rc := &RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "protected",
		Routes: []RouteRule{{Method: "GET", Path: "/x", Behavior: "protected"}},
	}
	errs := Validate(rc)
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), "defaultBehavior")
}

func TestValidate_PathMustStartWithSlash(t *testing.T) {
	rc := &RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []RouteRule{{Method: "GET", Path: "api/x", Behavior: "protected"}},
	}
	errs := Validate(rc)
	require.NotEmpty(t, errs)
	require.Contains(t, errs[0].Error(), "path")
}
