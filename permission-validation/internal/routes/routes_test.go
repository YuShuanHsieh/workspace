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
