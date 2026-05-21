package routes

import (
	"testing"

	"permission-validation/internal/config"
)

func TestLookup_ExactPathExactMethod_Match(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/api/orders", Behavior: "protected"},
		},
	}
	tbl, err := Compile(rc)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	behavior, matched := tbl.Lookup("GET", "/api/orders")
	if !matched || behavior != "protected" {
		t.Fatalf("got (%q, matched=%v); want (protected, true)", behavior, matched)
	}
}

func TestLookup_PathPatterns(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/exact", Behavior: "protected"},
			{Method: "GET", Path: "/wild/*", Behavior: "protected"},
			{Method: "GET", Path: "/deep/**", Behavior: "protected"},
		},
	}
	tbl, err := Compile(rc)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	cases := []struct {
		path        string
		wantMatched bool
	}{
		{"/exact", true},
		{"/exactsuffix", false}, // exact must not prefix-match
		{"/wild/foo", true},
		{"/wild/foo/bar", false}, // single * does not cross slash
		{"/deep/anything/here", true},
		{"/unrelated", false},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			_, matched := tbl.Lookup("GET", c.path)
			if matched != c.wantMatched {
				t.Fatalf("path=%s: matched=%v, want %v", c.path, matched, c.wantMatched)
			}
		})
	}
}

func TestLookup_MethodMatching(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/get-only", Behavior: "protected"},
			{Method: "*", Path: "/any-method", Behavior: "protected"},
		},
	}
	tbl, _ := Compile(rc)

	if _, m := tbl.Lookup("GET", "/get-only"); !m {
		t.Fatalf("GET /get-only should match")
	}
	if _, m := tbl.Lookup("POST", "/get-only"); m {
		t.Fatalf("POST /get-only should NOT match a GET-only rule")
	}
	if _, m := tbl.Lookup("DELETE", "/any-method"); !m {
		t.Fatalf("DELETE /any-method should match a wildcard-method rule")
	}
	if _, m := tbl.Lookup("get", "/get-only"); !m {
		t.Fatalf("lowercase 'get' should match GET rule")
	}
}

func TestLookup_FirstMatchWins(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes: []config.RouteRule{
			{Method: "GET", Path: "/api/orders/admin", Behavior: "skipped"},
			{Method: "GET", Path: "/api/orders/*", Behavior: "protected"},
		},
	}
	tbl, _ := Compile(rc)

	if b, m := tbl.Lookup("GET", "/api/orders/admin"); !m || b != "skipped" {
		t.Fatalf("expected skipped on first-match-wins; got (%s, matched=%v)", b, m)
	}
	if b, m := tbl.Lookup("GET", "/api/orders/other"); !m || b != "protected" {
		t.Fatalf("expected protected for non-admin under /api/orders/*; got (%s, matched=%v)", b, m)
	}
}

func TestLookup_DefaultDeny(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "deny",
		Routes:  []config.RouteRule{{Method: "GET", Path: "/protected", Behavior: "protected"}},
	}
	tbl, _ := Compile(rc)
	b, m := tbl.Lookup("GET", "/nothing-matches")
	if m {
		t.Fatalf("expected no match")
	}
	if b != "deny" {
		t.Fatalf("expected default deny; got %q", b)
	}
}

func TestLookup_DefaultSkipped(t *testing.T) {
	rc := &config.RouteConfig{
		Version: "v1", AppID: "x", DefaultBehavior: "skipped",
		Routes:  []config.RouteRule{{Method: "GET", Path: "/protected", Behavior: "protected"}},
	}
	tbl, _ := Compile(rc)
	b, m := tbl.Lookup("GET", "/nothing-matches")
	if m {
		t.Fatalf("expected no match")
	}
	if b != "skipped" {
		t.Fatalf("expected default skipped; got %q", b)
	}
}
