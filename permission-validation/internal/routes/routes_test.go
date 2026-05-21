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
