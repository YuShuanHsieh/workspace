package routes

import (
	"fmt"
	"regexp"
	"strings"

	"permission-validation/internal/config"
)

// Table is the compiled, ordered route table for a single RouteConfig.
// Lookup tries rules in file order; first match wins. On no match the
// default behaviour from RouteConfig.DefaultBehavior applies.
type Table struct {
	rules           []compiledRule
	defaultBehavior string
}

type compiledRule struct {
	method   string         // "GET", "POST", "*" — method matcher
	path     *regexp.Regexp // compiled from globToRegex
	behavior string         // "protected" or "skipped"
}

// Compile turns rc into a Table. Returns an error if any rule fails to compile.
func Compile(rc *config.RouteConfig) (*Table, error) {
	t := &Table{defaultBehavior: rc.DefaultBehavior}
	for i, r := range rc.Routes {
		re, err := regexp.Compile(globToRegex(r.Path))
		if err != nil {
			return nil, fmt.Errorf("routes[%d]: compile path %q: %w", i, r.Path, err)
		}
		t.rules = append(t.rules, compiledRule{
			method:   strings.ToUpper(r.Method),
			path:     re,
			behavior: r.Behavior,
		})
	}
	return t, nil
}

// Lookup returns the matched rule's behavior and matched=true if a rule
// matched, or (defaultBehavior, false) if none did.
func (t *Table) Lookup(method, path string) (behavior string, matched bool) {
	method = strings.ToUpper(method)
	for _, r := range t.rules {
		if r.method != "*" && r.method != method {
			continue
		}
		if r.path.MatchString(path) {
			return r.behavior, true
		}
	}
	return t.defaultBehavior, false
}

// globToRegex is a copy of the regex compiler from internal/config/translate.go.
// We duplicate it here intentionally to keep the routes package free of
// translate.go's other concerns (Envoy template variables, etc.). If the
// glob semantics diverge later, that's a deliberate decision visible in diff.
func globToRegex(p string) string {
	if p == "" {
		return "^$"
	}
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(p) {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case p[i] == '*':
			b.WriteString("[^/]*")
			i++
		case p[i] == '.', p[i] == '+', p[i] == '(', p[i] == ')', p[i] == '|',
			p[i] == '[', p[i] == ']', p[i] == '{', p[i] == '}', p[i] == '^',
			p[i] == '$', p[i] == '?', p[i] == '\\':
			b.WriteByte('\\')
			b.WriteByte(p[i])
			i++
		default:
			b.WriteByte(p[i])
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}
