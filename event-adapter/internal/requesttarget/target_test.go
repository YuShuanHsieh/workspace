package requesttarget

import (
	"net/http"
	"testing"
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
