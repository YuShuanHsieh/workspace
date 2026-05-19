package header

import "strings"

// HeaderError carries the metric reason label defined in phase-1-request-contract.md §5.
type HeaderError struct{ Reason string }

func (e *HeaderError) Error() string { return "header invalid: " + e.Reason }

// ExtractAuth returns the bearer token from the lowercase `authorization` header.
// Envoy lower-cases header names on the wire (HTTP/2), so callers should pass a
// map keyed by lowercase header names.
func ExtractAuth(h map[string]string) (string, error) {
	v, ok := h["authorization"]
	if !ok || v == "" {
		return "", &HeaderError{Reason: "missing_authz"}
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(v, prefix) {
		return "", &HeaderError{Reason: "malformed_authz"}
	}
	tok := v[len(prefix):]
	if tok == "" {
		return "", &HeaderError{Reason: "malformed_authz"}
	}
	return tok, nil
}

// ExtractContext returns the raw X-Auth-Context value, or a missing_ctx HeaderError.
// Format validation happens in ParseContextHeader.
func ExtractContext(h map[string]string) (string, error) {
	v, ok := h["x-auth-context"]
	if !ok || v == "" {
		return "", &HeaderError{Reason: "missing_ctx"}
	}
	return v, nil
}
