// Package routes compiles a permission-validation route table and answers
// short-circuit lookups by method + request path. It is the in-sidecar
// counterpart to the per-route filter config the static Envoy target embeds
// in envoy.yaml: when the Istio target is used, route-level skip / default
// decisions move into this package so the EnvoyFilter does not need them.
package routes

import (
	"errors"
	"fmt"
	"regexp"

	"permission-validation/internal/config"
)

// Decision is the outcome of a route Lookup.
type Decision int

const (
	// DecisionAllow means the request should be forwarded without PCS validation
	// (matched a skipped route, or no match + default skipped).
	DecisionAllow Decision = iota
	// DecisionDeny means the request should be rejected at the sidecar
	// (no match + default deny).
	DecisionDeny
	// DecisionProtected means the caller should fall through to the existing
	// header-parse + PCS-call path (matched a protected route).
	DecisionProtected
)

type compiledRoute struct {
	method   string // exact method or "*"
	pattern  *regexp.Regexp
	behavior string // "protected" | "skipped"
}

// Table is a compiled, immutable route lookup. Use Compile to construct one;
// Lookup is safe for concurrent use.
type Table struct {
	routes          []compiledRoute
	defaultBehavior string
}

// Compile builds a Table from a validated RouteConfig. Routes are evaluated in
// file order: the first matching rule wins.
func Compile(rc *config.RouteConfig) (*Table, error) {
	if rc == nil {
		return nil, errors.New("routes: nil route config")
	}
	t := &Table{defaultBehavior: rc.DefaultBehavior}
	for i, r := range rc.Routes {
		re, err := regexp.Compile(config.GlobToRegex(r.Path))
		if err != nil {
			return nil, fmt.Errorf("routes[%d]: compile %q: %w", i, r.Path, err)
		}
		t.routes = append(t.routes, compiledRoute{
			method:   r.Method,
			pattern:  re,
			behavior: r.Behavior,
		})
	}
	return t, nil
}

// Lookup returns the decision for (method, path). A nil receiver returns
// DecisionProtected so callers fall through to the existing PCS path.
func (t *Table) Lookup(method, path string) Decision {
	if t == nil {
		return DecisionProtected
	}
	for _, r := range t.routes {
		if r.method != "*" && r.method != method {
			continue
		}
		if !r.pattern.MatchString(path) {
			continue
		}
		if r.behavior == "skipped" {
			return DecisionAllow
		}
		return DecisionProtected
	}
	if t.defaultBehavior == "skipped" {
		return DecisionAllow
	}
	return DecisionDeny
}
