package config

import (
	"fmt"
	"strings"
)

// ValidationError is a single failure during Validate.
type ValidationError struct {
	Path string
	Msg  string
}

func (v *ValidationError) Error() string {
	if v.Path == "" {
		return v.Msg
	}
	return v.Path + ": " + v.Msg
}

var (
	allowedMethods  = map[string]bool{"GET": true, "POST": true, "PUT": true, "DELETE": true, "PATCH": true, "*": true}
	allowedBehavior = map[string]bool{"protected": true, "skipped": true}
	allowedDefault  = map[string]bool{"deny": true, "skipped": true}
)

// Validate enforces phase-1-route-config-schema.md §2 / §4 rules and returns
// every violation found (callers see all errors at once, not just the first).
//
// Out of scope: Validate does NOT check that `appId` matches the sidecar's
// provisioned appId (§2). That cross-check is the caller's responsibility
// because this package does not know the runtime appId.
func Validate(rc *RouteConfig) []error {
	if rc == nil {
		return []error{&ValidationError{Msg: "config is nil"}}
	}
	var errs []error
	if rc.Version != "v1" {
		errs = append(errs, &ValidationError{Path: "version", Msg: fmt.Sprintf("must be %q, got %q", "v1", rc.Version)})
	}
	if rc.AppID == "" {
		errs = append(errs, &ValidationError{Path: "appId", Msg: "is required"})
	}
	if !allowedDefault[rc.DefaultBehavior] {
		errs = append(errs, &ValidationError{Path: "defaultBehavior", Msg: fmt.Sprintf("must be deny or skipped, got %q", rc.DefaultBehavior)})
	}
	if len(rc.Routes) == 0 {
		errs = append(errs, &ValidationError{Path: "routes", Msg: "must be a non-empty list"})
	}
	for i, r := range rc.Routes {
		prefix := fmt.Sprintf("routes[%d]", i)
		if !allowedMethods[r.Method] {
			errs = append(errs, &ValidationError{Path: prefix + ".method", Msg: fmt.Sprintf("unsupported method %q", r.Method)})
		}
		if r.Path == "" {
			errs = append(errs, &ValidationError{Path: prefix + ".path", Msg: "must be non-empty"})
		} else if !strings.HasPrefix(r.Path, "/") {
			errs = append(errs, &ValidationError{Path: prefix + ".path", Msg: fmt.Sprintf("must start with '/', got %q", r.Path)})
		}
		if !allowedBehavior[r.Behavior] {
			errs = append(errs, &ValidationError{Path: prefix + ".behavior", Msg: fmt.Sprintf("must be protected or skipped, got %q", r.Behavior)})
		}
	}
	return errs
}
