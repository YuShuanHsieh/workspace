package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParse_ValidMinimal(t *testing.T) {
	src := []byte(`
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /health
    behavior: skipped
`)
	rc, err := Parse(src)
	require.NoError(t, err)
	require.Equal(t, "v1", rc.Version)
	require.Equal(t, "orders-app", rc.AppID)
	require.Equal(t, "deny", rc.DefaultBehavior)
	require.Len(t, rc.Routes, 1)
}

func TestParse_MalformedYAML(t *testing.T) {
	_, err := Parse([]byte("version: v1\nappId: [unterminated"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "yaml decode")
}

func TestParse_EmptyBytes(t *testing.T) {
	// An empty document decodes to a zero-value RouteConfig without error;
	// Validate then surfaces the missing-fields errors. This documents the
	// boundary: Parse only catches structural problems, not missing values.
	_, err := Parse(nil)
	require.Error(t, err) // EOF on empty stream
}

func TestParse_RejectsUnknownTopLevelField(t *testing.T) {
	src := []byte(`
version: v1
appId: orders-app
defaultBehavior: deny
routes: []
unknownField: oops
`)
	_, err := Parse(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "unknownField")
}

func TestParse_RejectsUnknownRouteField(t *testing.T) {
	src := []byte(`
version: v1
appId: orders-app
defaultBehavior: deny
routes:
  - method: GET
    path: /x
    behavior: protected
    behaviur: typo
`)
	_, err := Parse(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "behaviur")
}

func TestParse_CamelCaseKeysAreRequired(t *testing.T) {
	// `defaultbehavior` (lowercase) is an unknown key under strict mode.
	src := []byte(`
version: v1
appId: orders-app
defaultbehavior: deny
routes: []
`)
	_, err := Parse(src)
	require.Error(t, err)
	require.Contains(t, err.Error(), "defaultbehavior")
}
