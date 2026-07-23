// Package requesttarget validates the HTTP target properties accepted by
// configured dispatch routes.
package requesttarget

import (
	"fmt"
	"net/http"
	"strings"
)

var supportedMethods = map[string]string{
	http.MethodGet:    http.MethodGet,
	http.MethodPost:   http.MethodPost,
	http.MethodPut:    http.MethodPut,
	http.MethodPatch:  http.MethodPatch,
	http.MethodDelete: http.MethodDelete,
}

// NormalizeMethod trims whitespace, canonicalizes case, and verifies that the
// dispatch method is supported.
func NormalizeMethod(method string) (string, error) {
	normalized := strings.ToUpper(strings.TrimSpace(method))
	if normalized, ok := supportedMethods[normalized]; ok {
		return normalized, nil
	}
	return "", fmt.Errorf("unsupported HTTP method %q", method)
}
