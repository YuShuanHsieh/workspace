package config

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type ValidationError struct {
	Path string
	Msg  string
}

func (e ValidationError) Error() string {
	if e.Path == "" {
		return e.Msg
	}
	return e.Path + ": " + e.Msg
}

// IsReservedHeader reports whether name (case-insensitive) is a header the
// sidecar reserves and must never accept from a publisher's dispatchheaders.
func IsReservedHeader(name string) bool {
	return reservedHeaders[strings.ToLower(name)]
}

var reservedHeaders = map[string]bool{
	"ce-id":               true,
	"ce-type":             true,
	"ce-source":           true,
	"ce-specversion":      true,
	"ce-subject":          true,
	"ce-time":             true,
	"ce-datacontenttype":  true,
	"ce-dataschema":       true,
	"ce-correlationid":    true,
	"ce-causationid":      true,
	"idempotency-key":     true,
	"traceparent":         true,
	"authorization":       true,
	"connection":          true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailer":             true,
	"transfer-encoding":   true,
	"upgrade":             true,
	"cookie":              true,
}

func Validate(cfg *Config) []error {
	if cfg == nil {
		return []error{ValidationError{Msg: "config is nil"}}
	}
	var errs []error
	if cfg.App.ID == "" {
		errs = append(errs, ValidationError{Path: "app.id", Msg: "is required"})
	}
	if err := validateLoopbackBaseURL(cfg.App.HTTPBaseURL); err != nil {
		errs = append(errs, ValidationError{Path: "app.httpBaseURL", Msg: err.Error()})
	}
	if cfg.NATS.URL == "" {
		errs = append(errs, ValidationError{Path: "nats.url", Msg: "is required"})
	}
	if cfg.NATS.Stream == "" {
		errs = append(errs, ValidationError{Path: "nats.stream", Msg: "is required"})
	}
	if cfg.NATS.DurableConsumer == "" {
		errs = append(errs, ValidationError{Path: "nats.durableConsumer", Msg: "is required"})
	}
	if cfg.NATS.FilterSubject == "" {
		errs = append(errs, ValidationError{Path: "nats.filterSubject", Msg: "is required"})
	}
	if cfg.NATS.WorkerPoolSize <= 0 {
		errs = append(errs, ValidationError{Path: "nats.workerPoolSize", Msg: "must be positive"})
	}
	if cfg.NATS.FetchBatch <= 0 {
		errs = append(errs, ValidationError{Path: "nats.fetchBatch", Msg: "must be positive"})
	}
	if cfg.NATS.AckWait <= 0 {
		errs = append(errs, ValidationError{Path: "nats.ackWait", Msg: "must be positive"})
	}
	if cfg.NATS.MaxDeliver <= 0 {
		errs = append(errs, ValidationError{Path: "nats.maxDeliver", Msg: "must be positive"})
	}
	if cfg.NATS.MaxAckPending <= 0 {
		errs = append(errs, ValidationError{Path: "nats.maxAckPending", Msg: "must be positive"})
	}
	if cfg.NATS.FetchBatch > 0 && cfg.NATS.WorkerPoolSize > 0 && cfg.NATS.FetchBatch > cfg.NATS.WorkerPoolSize {
		errs = append(errs, ValidationError{Path: "nats.fetchBatch", Msg: "must not exceed nats.workerPoolSize"})
	}
	if cfg.NATS.WorkerPoolSize > 0 && cfg.NATS.MaxAckPending > 0 && cfg.NATS.WorkerPoolSize > cfg.NATS.MaxAckPending {
		errs = append(errs, ValidationError{Path: "nats.workerPoolSize", Msg: "must not exceed nats.maxAckPending"})
	}
	if cfg.NATS.DefaultDLQSubject == "" {
		errs = append(errs, ValidationError{Path: "nats.defaultDLQSubject", Msg: "is required"})
	}
	if len(cfg.Routes) == 0 {
		errs = append(errs, ValidationError{Path: "routes", Msg: "must contain at least one route"})
	}
	seen := make(map[string]int, len(cfg.Routes))
	for i, r := range cfg.Routes {
		errs = append(errs, validateRoute(fmt.Sprintf("routes[%d]", i), r)...)
		key := r.Match.Type
		if j, ok := seen[key]; ok {
			errs = append(errs, ValidationError{
				Path: fmt.Sprintf("routes[%d].match", i),
				Msg:  fmt.Sprintf("duplicate match type already defined at routes[%d]", j),
			})
		} else {
			seen[key] = i
		}
	}
	return errs
}

func validateRoute(prefix string, r RouteConfig) []error {
	var errs []error
	if r.Name == "" {
		errs = append(errs, ValidationError{Path: prefix + ".name", Msg: "is required"})
	}
	if r.Match.Type == "" {
		errs = append(errs, ValidationError{Path: prefix + ".match.type", Msg: "is required"})
	}
	if r.Dispatch.Method != http.MethodPost && r.Dispatch.Method != http.MethodPut && r.Dispatch.Method != http.MethodPatch {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.method", Msg: "must be POST, PUT, or PATCH"})
	}
	if !strings.HasPrefix(r.Dispatch.Path, "/") {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.path", Msg: "must start with /"})
	}
	if r.Dispatch.Timeout <= 0 {
		errs = append(errs, ValidationError{Path: prefix + ".dispatch.timeout", Msg: "must be positive"})
	}
	for name := range r.Dispatch.Headers {
		if reservedHeaders[strings.ToLower(name)] {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.headers." + name, Msg: "reserved header cannot be overridden"})
		}
	}
	for _, name := range r.Dispatch.ForwardHeaders {
		if name == "" {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.forwardHeaders", Msg: "header names must be non-empty"})
			continue
		}
		if reservedHeaders[strings.ToLower(name)] {
			errs = append(errs, ValidationError{Path: prefix + ".dispatch.forwardHeaders." + name, Msg: "reserved header cannot be forwarded from publisher"})
		}
	}
	if r.Response.Type == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.type", Msg: "is required"})
	}
	if r.Response.Source == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.source", Msg: "is required"})
	}
	if r.Response.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".response.subject", Msg: "is required"})
	}
	if r.Retry.MaxAttempts <= 0 {
		errs = append(errs, ValidationError{Path: prefix + ".retry.maxAttempts", Msg: "must be positive"})
	}
	if r.Retry.InitialBackoff <= 0 || r.Retry.MaxBackoff <= 0 || r.Retry.InitialBackoff > r.Retry.MaxBackoff {
		errs = append(errs, ValidationError{Path: prefix + ".retry", Msg: "initialBackoff and maxBackoff must be positive and ordered"})
	}
	if r.DLQ.Subject == "" {
		errs = append(errs, ValidationError{Path: prefix + ".dlq.subject", Msg: "is required"})
	}
	return errs
}

func validateLoopbackBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("must parse as URL: %w", err)
	}
	if u.Scheme != "http" {
		return fmt.Errorf("must use http scheme for local dispatch")
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("must target a loopback host")
	}
	return nil
}
