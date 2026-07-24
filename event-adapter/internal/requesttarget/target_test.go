package requesttarget

import (
	"net/http"
	"strings"
	"testing"

	"event-adapter/internal/pathtemplate"
)

func TestNormalizeMethodAcceptsSupportedMethods(t *testing.T) {
	tests := []struct {
		method string
		want   string
	}{
		{method: "get", want: http.MethodGet},
		{method: "POST", want: http.MethodPost},
		{method: "Put", want: http.MethodPut},
		{method: "patch", want: http.MethodPatch},
		{method: "delete", want: http.MethodDelete},
		{method: "  delete\t", want: http.MethodDelete},
	}

	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			got, err := NormalizeMethod(tt.method)
			if err != nil {
				t.Fatalf("NormalizeMethod(%q) returned error: %v", tt.method, err)
			}
			if got != tt.want {
				t.Fatalf("NormalizeMethod(%q) = %q, want %q", tt.method, got, tt.want)
			}
		})
	}
}

func TestNormalizeMethodRejectsUnsupportedMethods(t *testing.T) {
	for _, method := range []string{http.MethodOptions, http.MethodHead, ""} {
		t.Run(method, func(t *testing.T) {
			if _, err := NormalizeMethod(method); err == nil {
				t.Fatalf("NormalizeMethod(%q) returned nil error", method)
			}
		})
	}
}

func TestResolveCanonicalizesSafeTarget(t *testing.T) {
	got, err := Resolve("delete", "/orders//ord-456?hard=true", []string{"/orders/"})
	if err != nil {
		t.Fatalf("Resolve() returned error: %v", err)
	}

	want := Target{
		Method: http.MethodDelete,
		Path:   "/orders/ord-456?hard=true",
	}
	if got != want {
		t.Fatalf("Resolve() = %#v, want %#v", got, want)
	}
}

func TestResolveRejectsUnsafeTargets(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "full URL", target: "http://localhost/admin"},
		{name: "scheme relative", target: "//localhost/admin"},
		{name: "fragment", target: "/orders#admin"},
		{name: "literal dot traversal", target: "/orders/./ord-1"},
		{name: "literal dot dot traversal", target: "/orders/../admin"},
		{name: "encoded traversal lowercase", target: "/orders/%2e%2e/admin"},
		{name: "encoded traversal uppercase", target: "/orders/%2E%2E/admin"},
		{name: "encoded slash lowercase", target: "/orders%2fadmin"},
		{name: "encoded slash uppercase", target: "/orders%2Fadmin"},
		{name: "encoded backslash lowercase", target: "/orders%5cadmin"},
		{name: "encoded backslash uppercase", target: "/orders%5Cadmin"},
		{name: "nested traversal escape", target: "/orders/%252e%252e/admin"},
		{name: "masked nested traversal escape", target: "/orders/%25zz/%252e%252e/%252e%252e/admin"},
		{name: "encoded percent", target: "/orders/%25value"},
		{name: "literal backslash", target: `/orders\admin`},
		{name: "malformed escape", target: "/orders/%zz"},
		{name: "malformed query escape", target: "/orders?next=%zz"},
		{name: "incomplete query escape", target: "/orders?next=%2"},
		{name: "literal query space", target: "/orders?value=a b"},
		{name: "literal query tab", target: "/orders?value=a\tb"},
		{name: "literal query control", target: "/orders?value=a\x00b"},
		{name: "raw query opening bracket", target: "/orders?value=[a"},
		{name: "raw query closing bracket", target: "/orders?value=a]"},
		{name: "raw query non ASCII", target: "/orders?value=café"},
		{name: "control character", target: "/orders/\x00admin"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Resolve(http.MethodGet, tt.target, nil); err == nil {
				t.Fatalf("Resolve(%q) returned nil error", tt.target)
			}
		})
	}
}

func TestResolveNeutralizesPathTemplateTokens(t *testing.T) {
	for _, rawTarget := range []string{
		"/orders/{p}/admin",
		"/orders/%7Bp%7D/admin",
	} {
		t.Run(rawTarget, func(t *testing.T) {
			got, err := Resolve(http.MethodGet, rawTarget, []string{"/orders"})
			if err != nil {
				t.Fatalf("Resolve() returned error: %v", err)
			}

			const want = "/orders/%7Bp%7D/admin"
			if got.Path != want {
				t.Fatalf("Resolve().Path = %q, want %q", got.Path, want)
			}

			afterTemplateResolution, err := pathtemplate.Resolve(got.Path, map[string]string{"p": ".."})
			if err != nil {
				t.Fatalf("pathtemplate.Resolve() returned error: %v", err)
			}
			if afterTemplateResolution != want {
				t.Fatalf("pathtemplate.Resolve() = %q, want unchanged %q", afterTemplateResolution, want)
			}
		})
	}
}

func TestResolveCanonicalizesUnicodeAndMatchesPrefixesAfterDecoding(t *testing.T) {
	tests := []struct {
		name   string
		target string
		prefix string
	}{
		{name: "literal target encoded prefix", target: "/orders/café", prefix: "/orders/caf%C3%A9"},
		{name: "encoded target literal prefix", target: "/orders/caf%C3%A9", prefix: "/orders/café"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Resolve(http.MethodGet, tt.target, []string{tt.prefix})
			if err != nil {
				t.Fatalf("Resolve() returned error: %v", err)
			}
			if got.Path != "/orders/caf%C3%A9" {
				t.Fatalf("Resolve().Path = %q, want %q", got.Path, "/orders/caf%C3%A9")
			}
		})
	}
}

func TestResolveEnforcesPrefixAtSegmentBoundary(t *testing.T) {
	tests := []struct {
		target  string
		wantErr bool
	}{
		{target: "/orders"},
		{target: "/orders/ord-1"},
		{target: "/orders-admin", wantErr: true},
		{target: "/admin", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.target, func(t *testing.T) {
			_, err := Resolve(http.MethodGet, tt.target, []string{"/orders/"})
			if tt.wantErr && err == nil {
				t.Fatalf("Resolve(%q) returned nil error", tt.target)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("Resolve(%q) returned error: %v", tt.target, err)
			}
		})
	}
}

func TestResolveAllowsOptionalAndRootPrefixes(t *testing.T) {
	for _, tt := range []struct {
		name     string
		prefixes []string
	}{
		{name: "no prefixes", prefixes: nil},
		{name: "root prefix", prefixes: []string{"/"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Resolve(http.MethodGet, "/safe/arbitrary-path", tt.prefixes); err != nil {
				t.Fatalf("Resolve() returned error: %v", err)
			}
		})
	}
}

func TestValidatePrefix(t *testing.T) {
	for _, prefix := range []string{"/", "/orders", "/orders/"} {
		t.Run("accept "+prefix, func(t *testing.T) {
			if err := ValidatePrefix(prefix); err != nil {
				t.Fatalf("ValidatePrefix(%q) returned error: %v", prefix, err)
			}
		})
	}

	for _, tt := range []struct {
		name   string
		prefix string
	}{
		{name: "relative", prefix: "orders"},
		{name: "scheme relative", prefix: "//orders"},
		{name: "query", prefix: "/orders?active=true"},
		{name: "traversal", prefix: "/orders/../admin"},
		{name: "encoded separator", prefix: "/orders%2fadmin"},
		{name: "encoded backslash", prefix: "/orders%5cadmin"},
		{name: "nested escape", prefix: "/orders/%252e%252e/admin"},
	} {
		t.Run("reject "+tt.name, func(t *testing.T) {
			if err := ValidatePrefix(tt.prefix); err == nil {
				t.Fatalf("ValidatePrefix(%q) returned nil error", tt.prefix)
			}
		})
	}
}

func TestResolveRejectsInvalidMethodAndMissingPath(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
	}{
		{name: "missing method", target: "/orders"},
		{name: "unsupported method", method: http.MethodHead, target: "/orders"},
		{name: "missing path", method: http.MethodGet},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Resolve(tt.method, tt.target, nil); err == nil {
				t.Fatalf("Resolve(%q, %q) returned nil error", tt.method, tt.target)
			}
		})
	}
}

func TestResolvePreservesQueryAndAppliesPrefixOnlyToPath(t *testing.T) {
	for _, rawTarget := range []string{
		"/orders/ord-1?next=%2Fadmin%3Fraw%3Dtrue&token=a%252Fb",
		"/orders/ord-1?value=a%20b&next=%2Forders%3Fx%3D1",
		"/orders/ord-1?value=a+b",
	} {
		t.Run(rawTarget, func(t *testing.T) {
			got, err := Resolve(http.MethodGet, rawTarget, []string{"/orders"})
			if err != nil {
				t.Fatalf("Resolve() returned error: %v", err)
			}
			if got.Path != rawTarget {
				t.Fatalf("Resolve().Path = %q, want %q", got.Path, rawTarget)
			}
		})
	}
}

func TestResolveErrorsIdentifyInvalidInput(t *testing.T) {
	_, err := Resolve(http.MethodGet, "/admin", []string{"/orders"})
	if err == nil {
		t.Fatal("Resolve() returned nil error")
	}
	if !strings.Contains(err.Error(), "/admin") {
		t.Fatalf("Resolve() error %q does not identify supplied path", err)
	}
}
