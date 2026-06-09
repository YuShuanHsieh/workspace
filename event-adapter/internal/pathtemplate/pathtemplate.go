// Package pathtemplate parses and resolves {field} tokens in HTTP path
// templates against the top-level fields of a CloudEvent's data payload.
package pathtemplate

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
)

// ErrPermanent wraps payload-related Resolve failures that cannot succeed on
// retry because the event data does not change between attempts. The processor
// and responder use errors.Is to bypass their retry/error paths.
var ErrPermanent = errors.New("pathtemplate: permanent failure")

// tokenRegex matches a single {fieldName} token anywhere in a path. Used by
// Resolve to find all tokens to substitute. Token names must start with a
// letter and contain only letters, digits, and underscores.
var tokenRegex = regexp.MustCompile(`\{([a-zA-Z][a-zA-Z0-9_]*)\}`)

// strictTokenRegex anchors the same pattern with ^...$ for Validate, which must
// reject malformed pairs like "{{taskId}}" where the inner "{taskId}" would
// otherwise match as a substring of an outer "{{taskId}" looseBraceRegex hit.
var strictTokenRegex = regexp.MustCompile(`^\{[a-zA-Z][a-zA-Z0-9_]*\}$`)

// looseBraceRegex matches anything between { and } — used to find candidate
// tokens (well-formed or not) so Validate can report bad ones.
var looseBraceRegex = regexp.MustCompile(`\{([^}]*)\}`)

// Validate parses a path string at config-load time and rejects malformed
// tokens. It does not require any event data — it checks only the path itself.
// Errors returned by Validate do NOT wrap ErrPermanent (those are reserved for
// dispatch-time payload failures).
func Validate(path string) error {
	// Reject unclosed braces (e.g. "/api/{x").
	for i := 0; i < len(path); i++ {
		if path[i] == '{' {
			closing := indexFromOffset(path, i, '}')
			if closing == -1 {
				return fmt.Errorf("pathtemplate: unclosed { in path %q", path)
			}
		}
	}
	// Every {...} match must satisfy the strict, anchored token regex. We also
	// reject any '{' or '}' immediately adjacent to the match (i.e. doubled
	// braces like "{{x}}") since looseBraceRegex would not catch them alone.
	for _, idx := range looseBraceRegex.FindAllStringSubmatchIndex(path, -1) {
		start, end := idx[0], idx[1]
		raw := path[start:end]
		if !strictTokenRegex.MatchString(raw) {
			inner := path[idx[2]:idx[3]]
			return fmt.Errorf("pathtemplate: invalid token %q in path %q (must match {[a-zA-Z][a-zA-Z0-9_]*})", inner, path)
		}
		// Adjacent extra brace ⇒ malformed (e.g. "{{taskId}" or "{taskId}}").
		if start > 0 && path[start-1] == '{' {
			return fmt.Errorf("pathtemplate: malformed token in path %q (unexpected '{' before %q)", path, raw)
		}
		if end < len(path) && path[end] == '}' {
			return fmt.Errorf("pathtemplate: malformed token in path %q (unexpected '}' after %q)", path, raw)
		}
	}
	return nil
}

// tokenNames returns the names of every token in path, in order of appearance,
// without de-duplication. Callers that want unique names should de-dup themselves.
// Returns an error if any token is malformed (delegates to Validate).
func tokenNames(path string) ([]string, error) {
	if err := Validate(path); err != nil {
		return nil, err
	}
	matches := tokenRegex.FindAllStringSubmatch(path, -1)
	names := make([]string, 0, len(matches))
	for _, m := range matches {
		names = append(names, m[1])
	}
	return names, nil
}

func indexFromOffset(s string, off int, c byte) int {
	for i := off + 1; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

// Resolve substitutes {field} tokens in path against the top-level fields of
// data (parsed as a JSON object). Returns the resolved path on success, or an
// error wrapping ErrPermanent if any token cannot be resolved. Static paths
// (no tokens) short-circuit without parsing JSON.
//
// data is the raw JSON bytes of the CloudEvent's data payload — typically
// obtained from ev.Data() at the call site. Taking bytes instead of *Event
// avoids an import cycle through cloudevent.
//
// Validation failures (bad token syntax) do NOT wrap ErrPermanent — those are
// config bugs, not payload bugs.
func Resolve(path string, data []byte) (string, error) {
	names, err := tokenNames(path)
	if err != nil {
		return "", err
	}
	if len(names) == 0 {
		return path, nil
	}
	values, err := decodeDataAsObject(data)
	if err != nil {
		return "", err
	}

	out := path
	for _, name := range uniqueNames(names) {
		raw, ok := values[name]
		if !ok {
			return "", fmt.Errorf("%w: field %q not found in event data", ErrPermanent, name)
		}
		s, ok := raw.(string)
		if !ok {
			return "", fmt.Errorf("%w: field %q is not a string (got %T)", ErrPermanent, name, raw)
		}
		out = replaceAllToken(out, name, url.PathEscape(s))
	}
	return out, nil
}

func decodeDataAsObject(data []byte) (map[string]any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("%w: data is empty", ErrPermanent)
	}
	var values map[string]any
	if err := json.Unmarshal(data, &values); err != nil {
		return nil, fmt.Errorf("%w: data is not a JSON object: %v", ErrPermanent, err)
	}
	return values, nil
}

func uniqueNames(in []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(in))
	for _, n := range in {
		if _, ok := seen[n]; ok {
			continue
		}
		seen[n] = struct{}{}
		out = append(out, n)
	}
	return out
}

func replaceAllToken(path, name, value string) string {
	token := "{" + name + "}"
	for {
		idx := indexOf(path, token)
		if idx < 0 {
			return path
		}
		path = path[:idx] + value + path[idx+len(token):]
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
