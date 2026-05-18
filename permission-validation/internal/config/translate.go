package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"regexp"
	"strings"
	"text/template"
)

//go:embed envoy-static.tmpl.yaml
var envoyTemplate string

// TranslateOptions are environment-specific values that the YAML schema does not carry.
type TranslateOptions struct {
	SidecarHost      string
	SidecarPort      int
	AppBackendHost   string
	AppBackendPort   int
	FailureModeAllow bool // must stay false in Phase 1; exposed for tests
	// AdminHost binds Envoy's admin listener on port 9901. Defaults to 127.0.0.1
	// so production renders are safe by default; the e2e harness overrides it
	// to 0.0.0.0 so the host can curl admin endpoints from outside the container.
	AdminHost string
	// AccessLogStdout adds an http_connection_manager access_log that writes to
	// the container's stdout. Off by default; production renders should turn it
	// on so SREs see traffic alongside the sidecar's own logs.
	AccessLogStdout bool
}

type routeView struct {
	Method   string
	Behavior string
	Path     string
	Regex    string
	PathKind string // "exact" | "prefix" | "regex"
}

type translateView struct {
	Routes           []routeView
	DefaultBehavior  string
	SidecarHost      string
	SidecarPort      int
	AppBackendHost   string
	AppBackendPort   int
	FailureModeAllow bool
	AdminHost        string
	AccessLogStdout  bool
}

// Translate renders the embedded Envoy template using rc + opts.
func Translate(rc *RouteConfig, opts TranslateOptions) ([]byte, error) {
	adminHost := opts.AdminHost
	if adminHost == "" {
		adminHost = "127.0.0.1"
	}
	tv := translateView{
		DefaultBehavior:  rc.DefaultBehavior,
		SidecarHost:      opts.SidecarHost,
		SidecarPort:      opts.SidecarPort,
		AppBackendHost:   opts.AppBackendHost,
		AppBackendPort:   opts.AppBackendPort,
		FailureModeAllow: opts.FailureModeAllow,
		AdminHost:        adminHost,
		AccessLogStdout:  opts.AccessLogStdout,
	}
	for _, r := range rc.Routes {
		rv, err := routeToView(r)
		if err != nil {
			return nil, err
		}
		tv.Routes = append(tv.Routes, rv)
	}
	tmpl, err := template.New("envoy").Parse(envoyTemplate)
	if err != nil {
		return nil, fmt.Errorf("translate: parse template: %w", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, tv); err != nil {
		return nil, fmt.Errorf("translate: execute template: %w", err)
	}
	return out.Bytes(), nil
}

// routeToView resolves the path glob into an Envoy matcher kind.
//
//	literal             → exact
//	literal + trailing /** → prefix
//	anything else containing * or ** → safe_regex
func routeToView(r RouteRule) (routeView, error) {
	rv := routeView{Method: r.Method, Behavior: r.Behavior, Path: r.Path}
	hasStar := strings.Contains(r.Path, "*")
	switch {
	case !hasStar:
		rv.PathKind = "exact"
	case strings.HasSuffix(r.Path, "/**") && strings.Count(r.Path, "*") == 2:
		rv.PathKind = "prefix"
		rv.Path = strings.TrimSuffix(r.Path, "/**")
	default:
		rv.PathKind = "regex"
		rv.Regex = globToRegex(r.Path)
	}
	return rv, nil
}

// globToRegex converts a gitignore-style glob (§2.1) to an Envoy safe_regex (RE2):
// "*" → one path segment ([^/]+); "**" → zero or more segments (.*); literal
// characters are escaped via regexp.QuoteMeta.
func globToRegex(p string) string {
	var b strings.Builder
	b.WriteString("^")
	i := 0
	for i < len(p) {
		switch {
		case strings.HasPrefix(p[i:], "**"):
			b.WriteString(".*")
			i += 2
		case p[i] == '*':
			b.WriteString("[^/]+")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(p[i])))
			i++
		}
	}
	b.WriteString("$")
	return b.String()
}
