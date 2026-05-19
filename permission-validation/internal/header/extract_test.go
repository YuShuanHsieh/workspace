package header

import (
	"testing"
)

func TestExtractAuth(t *testing.T) {
	cases := []struct {
		name       string
		headers    map[string]string
		wantToken  string
		wantReason string
	}{
		{"valid bearer", map[string]string{"authorization": "Bearer abc.def.ghi"}, "abc.def.ghi", ""},
		{"missing", map[string]string{}, "", "missing_authz"},
		{"wrong scheme", map[string]string{"authorization": "Basic xyz"}, "", "malformed_authz"},
		{"empty token", map[string]string{"authorization": "Bearer "}, "", "malformed_authz"},
		{"lowercase scheme rejected", map[string]string{"authorization": "bearer abc"}, "", "malformed_authz"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tok, err := ExtractAuth(c.headers)
			if c.wantReason == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if tok != c.wantToken {
					t.Fatalf("token: got %q want %q", tok, c.wantToken)
				}
				return
			}
			he, ok := err.(*HeaderError)
			if !ok {
				t.Fatalf("expected *HeaderError, got %T", err)
			}
			if he.Reason != c.wantReason {
				t.Fatalf("reason: got %q want %q", he.Reason, c.wantReason)
			}
		})
	}
}

func TestExtractContext(t *testing.T) {
	if v, err := ExtractContext(map[string]string{"x-auth-context": "doc-42:document:edit"}); err != nil || v != "doc-42:document:edit" {
		t.Fatalf("got (%q, %v)", v, err)
	}
	_, err := ExtractContext(map[string]string{})
	if err == nil {
		t.Fatal("expected missing_ctx error")
	}
	if he := err.(*HeaderError); he.Reason != "missing_ctx" {
		t.Fatalf("reason: got %q want missing_ctx", he.Reason)
	}
}
