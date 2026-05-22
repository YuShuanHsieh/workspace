package routes

import (
	"testing"

	"github.com/stretchr/testify/require"

	"permission-validation/internal/config"
)

func tableFrom(t *testing.T, defaultBehavior string, rules ...config.RouteRule) *Table {
	t.Helper()
	rc := &config.RouteConfig{
		Version:         "v1",
		AppID:           "test-app",
		DefaultBehavior: defaultBehavior,
		Routes:          rules,
	}
	tbl, err := Compile(rc)
	require.NoError(t, err)
	return tbl
}

func TestCompile_NilConfigReturnsError(t *testing.T) {
	_, err := Compile(nil)
	require.Error(t, err)
}

func TestLookup_ExactMethodAndPath_Skipped(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/health", Behavior: "skipped"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/health"))
}

func TestLookup_ExactMethodAndPath_Protected(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "POST", Path: "/api/orders", Behavior: "protected"},
	)
	require.Equal(t, DecisionProtected, tbl.Lookup("POST", "/api/orders"))
}

func TestLookup_WildcardMethodMatches(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "*", Path: "/api/admin/**", Behavior: "protected"},
	)
	require.Equal(t, DecisionProtected, tbl.Lookup("DELETE", "/api/admin/users/42"))
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/api/admin/users/42"))
}

func TestLookup_MethodMismatchSkipsRule(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "POST", Path: "/api/orders", Behavior: "protected"},
	)
	// GET /api/orders does not match POST rule; falls through to default-deny.
	require.Equal(t, DecisionDeny, tbl.Lookup("GET", "/api/orders"))
}

func TestLookup_SingleStarMatchesOneSegment(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/api/orders/123"))
	// Two segments past /api/orders → does not match single-star.
	require.Equal(t, DecisionDeny, tbl.Lookup("GET", "/api/orders/123/items"))
}

func TestLookup_DoubleStarMatchesAnySuffix(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/assets/**", Behavior: "skipped"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/assets/img/logo.png"))
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/assets/"))
}

func TestLookup_FirstMatchWins(t *testing.T) {
	// Two overlapping rules: skipped wins because it's listed first.
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/api/orders/health", Behavior: "skipped"},
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/api/orders/health"))
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/api/orders/42"))
}

func TestLookup_NoMatch_DefaultDeny(t *testing.T) {
	tbl := tableFrom(t, "deny",
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionDeny, tbl.Lookup("GET", "/unrelated"))
}

func TestLookup_NoMatch_DefaultSkipped(t *testing.T) {
	tbl := tableFrom(t, "skipped",
		config.RouteRule{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
	)
	require.Equal(t, DecisionAllow, tbl.Lookup("GET", "/unrelated"))
}

func TestLookup_NilTable_TreatedAsProtected(t *testing.T) {
	var tbl *Table
	require.Equal(t, DecisionProtected, tbl.Lookup("GET", "/anything"))
}
