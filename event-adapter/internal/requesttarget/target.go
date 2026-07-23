// Package requesttarget validates the HTTP target properties accepted by
// configured dispatch routes.
package requesttarget

import (
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

var supportedMethods = map[string]string{
	http.MethodGet:    http.MethodGet,
	http.MethodPost:   http.MethodPost,
	http.MethodPut:    http.MethodPut,
	http.MethodPatch:  http.MethodPatch,
	http.MethodDelete: http.MethodDelete,
}

// Target is a validated HTTP method and local request URI.
type Target struct {
	Method string
	Path   string
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

// Resolve validates and canonicalizes a publisher-selected local request
// target. When allowedPrefixes is nonempty, the decoded path must fall under at
// least one prefix at a path-segment boundary.
func Resolve(rawMethod, rawTarget string, allowedPrefixes []string) (Target, error) {
	method, err := NormalizeMethod(rawMethod)
	if err != nil {
		return Target{}, err
	}

	target, err := validateLocalTarget(rawTarget, true)
	if err != nil {
		return Target{}, err
	}

	if len(allowedPrefixes) > 0 {
		allowed := false
		for _, rawPrefix := range allowedPrefixes {
			prefix, prefixErr := validateLocalTarget(rawPrefix, false)
			if prefixErr != nil {
				return Target{}, fmt.Errorf("invalid allowed prefix %q: %w", rawPrefix, prefixErr)
			}
			if pathHasPrefix(target.decodedPath, prefix.decodedPath) {
				allowed = true
			}
		}
		if !allowed {
			return Target{}, fmt.Errorf("request target %q is outside allowed prefixes", rawTarget)
		}
	}

	return Target{
		Method: method,
		Path:   target.escapedPath + target.query,
	}, nil
}

// ValidatePrefix verifies that raw is a safe absolute local path without a
// query. Resolve canonicalizes valid prefixes before applying them.
func ValidatePrefix(raw string) error {
	_, err := validateLocalTarget(raw, false)
	return err
}

type localTarget struct {
	escapedPath string
	decodedPath string
	query       string
}

func validateLocalTarget(raw string, allowQuery bool) (localTarget, error) {
	if raw == "" {
		return localTarget{}, fmt.Errorf("request target path is required")
	}
	if raw[0] != '/' || strings.HasPrefix(raw, "//") {
		return localTarget{}, fmt.Errorf("request target %q must start with exactly one slash", raw)
	}
	if strings.Contains(raw, "#") {
		return localTarget{}, fmt.Errorf("request target %q must not contain a fragment", raw)
	}

	queryIndex := strings.IndexByte(raw, '?')
	rawPath := raw
	query := ""
	if queryIndex >= 0 {
		if !allowQuery {
			return localTarget{}, fmt.Errorf("request target prefix %q must not contain a query", raw)
		}
		rawPath = raw[:queryIndex]
		query = raw[queryIndex:]
	}

	parsed, err := url.ParseRequestURI(raw)
	if err != nil {
		return localTarget{}, fmt.Errorf("invalid request target %q: %w", raw, err)
	}
	if parsed.Scheme != "" || parsed.Host != "" || parsed.User != nil || parsed.Opaque != "" {
		return localTarget{}, fmt.Errorf("request target %q must be a local absolute path", raw)
	}
	if query != "" {
		if queryErr := validateRawQuery(query[1:]); queryErr != nil {
			return localTarget{}, fmt.Errorf("invalid request target query in %q: %w", raw, queryErr)
		}
	}

	lowerPath := strings.ToLower(rawPath)
	if strings.Contains(lowerPath, "%2f") {
		return localTarget{}, fmt.Errorf("request target %q contains an encoded slash", raw)
	}
	if strings.Contains(lowerPath, "%5c") {
		return localTarget{}, fmt.Errorf("request target %q contains an encoded backslash", raw)
	}

	decodedPath, err := url.PathUnescape(rawPath)
	if err != nil {
		return localTarget{}, fmt.Errorf("invalid request target escaping in %q: %w", raw, err)
	}
	if !utf8.ValidString(decodedPath) {
		return localTarget{}, fmt.Errorf("request target %q contains invalid UTF-8", raw)
	}
	if strings.ContainsRune(decodedPath, '\\') {
		return localTarget{}, fmt.Errorf("request target %q contains a backslash", raw)
	}
	for _, char := range decodedPath {
		if unicode.IsControl(char) {
			return localTarget{}, fmt.Errorf("request target %q contains a control character", raw)
		}
	}
	for _, segment := range strings.Split(decodedPath, "/") {
		if segment == "." || segment == ".." {
			return localTarget{}, fmt.Errorf("request target %q contains a traversal segment", raw)
		}
	}

	if unescapedAgain, secondErr := url.PathUnescape(decodedPath); secondErr == nil && unescapedAgain != decodedPath {
		return localTarget{}, fmt.Errorf("request target %q contains nested escaping", raw)
	}

	return localTarget{
		escapedPath: path.Clean(rawPath),
		decodedPath: path.Clean(decodedPath),
		query:       query,
	}, nil
}

func validateRawQuery(raw string) error {
	for index := 0; index < len(raw); index++ {
		char := raw[index]
		if isASCIIAlphaNumeric(char) || strings.ContainsRune("-._~!$&'()*+,;=:@/?", rune(char)) {
			continue
		}
		if char == '%' {
			if index+2 >= len(raw) || !isHexDigit(raw[index+1]) || !isHexDigit(raw[index+2]) {
				return fmt.Errorf("malformed percent escape at byte %d", index)
			}
			index += 2
			continue
		}
		return fmt.Errorf("disallowed raw character at byte %d", index)
	}
	return nil
}

func isASCIIAlphaNumeric(char byte) bool {
	return char >= 'a' && char <= 'z' ||
		char >= 'A' && char <= 'Z' ||
		char >= '0' && char <= '9'
}

func isHexDigit(char byte) bool {
	return char >= 'a' && char <= 'f' ||
		char >= 'A' && char <= 'F' ||
		char >= '0' && char <= '9'
}

func pathHasPrefix(targetPath, prefix string) bool {
	return prefix == "/" ||
		targetPath == prefix ||
		strings.HasPrefix(targetPath, prefix+"/")
}
