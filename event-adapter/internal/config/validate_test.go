package config

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

func validConfig() *Config {
	return &Config{
		App: AppConfig{ID: "task-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{
			URL: "nats://nats:4222", Stream: "workspace-events", DurableConsumer: "task-service-dispatcher",
			FilterSubject: "t.tenant-a.app.task.event.created", WorkerPoolSize: 16, FetchBatch: 16,
			AckWait: 30 * time.Second, MaxDeliver: 5, MaxAckPending: 1024, DefaultDLQSubject: "dlq.tenant-a.task-service",
		},
		Routes: []RouteConfig{{
			Name:     "task-created",
			Match:    MatchConfig{Subject: "t.tenant-a.app.task.event.created", Type: "com.workspace.task.created", Source: "workspace/task"},
			Dispatch: DispatchConfig{Method: "POST", Path: "/events/task-created", Timeout: 2 * time.Second},
			Response: ResponseConfig{Type: "com.workspace.task.created.processed", Source: "task-service", Subject: "t.tenant-a.app.task.event.processed"},
			Retry:    RetryConfig{MaxAttempts: 3, InitialBackoff: 100 * time.Millisecond, MaxBackoff: 2 * time.Second},
			DLQ:      DLQConfig{Subject: "dlq.tenant-a.task-service"},
		}},
		Observability: ObservabilityConfig{Environment: "testing"},
	}
}

func TestValidateRequiresObservabilityEnvironment(t *testing.T) {
	cfg := validConfig()
	cfg.Observability.Environment = ""
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for missing observability.environment")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "observability.environment") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected observability.environment error, got %v", errs)
	}
}

func TestValidateRejectsUnknownEnvironment(t *testing.T) {
	cfg := validConfig()
	cfg.Observability.Environment = "prodution" // typo, not in the allowlist
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error for invalid observability.environment")
	}
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "observability.environment") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected observability.environment error, got %v", errs)
	}
}

func TestValidateAcceptsAllValidEnvironments(t *testing.T) {
	for _, env := range []string{"production", "staging", "development", "testing"} {
		cfg := validConfig()
		cfg.Observability.Environment = env
		if errs := Validate(cfg); len(errs) != 0 {
			t.Fatalf("environment %q: expected no errors, got %v", env, errs)
		}
	}
}

func TestValidateAcceptsValidConfig(t *testing.T) {
	if errs := Validate(validConfig()); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRejectsExternalHTTPBaseURL(t *testing.T) {
	cfg := validConfig()
	cfg.App.HTTPBaseURL = "http://example.com"
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "loopback") {
		t.Fatalf("expected loopback error, got %v", errs[0])
	}
}

func TestValidateRejectsStaticHeaderOverride(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Headers = map[string]string{"ce-id": "bad"}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "reserved header") {
		t.Fatalf("expected reserved header error, got %v", errs[0])
	}
}

func TestValidateRejectsReservedForwardHeader(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.ForwardHeaders = []string{"Connection"}
	errs := Validate(cfg)
	if len(errs) == 0 {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(errs[0].Error(), "reserved header") {
		t.Fatalf("expected reserved header error, got %v", errs[0])
	}
}

// Authorization is intentionally no longer reserved: routes may forward the
// client's Authorization header to the dispatch backend.
func TestValidateAllowsForwardingAuthorization(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.ForwardHeaders = []string{"Authorization"}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected Authorization to be a valid forward header, got %v", errs)
	}
}

func TestValidateRejectsEmptyFilterSubject(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.FilterSubject = ""
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "nats.filterSubject") {
		t.Fatalf("expected filterSubject error, got %v", errs)
	}
}

func TestValidateRejectsNonPositiveWorkerPoolSize(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.WorkerPoolSize = 0
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "nats.workerPoolSize") {
		t.Fatalf("expected workerPoolSize error, got %v", errs)
	}
}

func TestValidateRejectsNonPositiveFetchBatch(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.FetchBatch = 0
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "nats.fetchBatch") {
		t.Fatalf("expected fetchBatch error, got %v", errs)
	}
}

func TestValidateRejectsFetchBatchExceedingWorkerPoolSize(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.FetchBatch = cfg.NATS.WorkerPoolSize + 1
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "nats.fetchBatch") {
		t.Fatalf("expected fetchBatch error, got %v", errs)
	}
}

func TestValidateRejectsWorkerPoolSizeExceedingMaxAckPending(t *testing.T) {
	cfg := validConfig()
	cfg.NATS.WorkerPoolSize = cfg.NATS.MaxAckPending + 1
	cfg.NATS.FetchBatch = cfg.NATS.WorkerPoolSize
	errs := Validate(cfg)
	if len(errs) == 0 || !strings.Contains(errs[0].Error(), "nats.workerPoolSize") {
		t.Fatalf("expected workerPoolSize error, got %v", errs)
	}
}

func TestValidateAllowsOptionalSubjectAndSource(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Match.Subject = ""
	cfg.Routes[0].Match.Source = ""
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors with optional subject/source, got %v", errs)
	}
}

func TestValidateRejectsDuplicateType(t *testing.T) {
	cfg := validConfig()
	dup := cfg.Routes[0]
	dup.Match.Subject = "different.subject"
	dup.Match.Source = "different/source"
	cfg.Routes = append(cfg.Routes, dup)
	errs := Validate(cfg)
	found := false
	for _, e := range errs {
		if strings.Contains(e.Error(), "duplicate match type") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected duplicate match type error, got %v", errs)
	}
}

func TestCookieIsReservedHeader(t *testing.T) {
	if !IsReservedHeader("Cookie") {
		t.Fatal("expected Cookie to be a reserved header")
	}
	if !IsReservedHeader("cookie") {
		t.Fatal("expected reserved-header check to be case-insensitive for cookie")
	}
}

func hasErr(errs []error, substr string) bool {
	for _, e := range errs {
		if strings.Contains(e.Error(), substr) {
			return true
		}
	}
	return false
}

func baseRequests() *RequestsConfig {
	return &RequestsConfig{
		Subject:        "q.tenant-a.app.uploads.request",
		QueueGroup:     "upload-responders",
		WorkerPoolSize: 4,
		Routes: []RequestRouteConfig{{
			Name:     "upload-presign",
			Match:    RequestMatchConfig{Type: "com.workspace.uploads.presign.request"},
			Dispatch: DispatchConfig{Method: "POST", Path: "/requests/upload-presign", Timeout: time.Second},
			Reply:    ReplyConfig{Source: "upload-service", Type: "com.workspace.uploads.presign.reply"},
		}},
	}
}

func directOnlyConfig() *Config {
	return &Config{
		App:  AppConfig{ID: "upload-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: &RequestsConfig{
			Subject:        "q.tenant-a.app.uploads.request",
			QueueGroup:     "upload-responders",
			WorkerPoolSize: 4,
			DirectDispatch: DirectDispatchConfig{Enabled: true, Timeout: 3 * time.Second, AllowedPathPrefixes: []string{"/orders/"}},
		},
		Observability: ObservabilityConfig{Environment: "testing"},
	}
}

func TestValidateDirectOnlyResponder(t *testing.T) {
	if errs := Validate(directOnlyConfig()); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRequestsRequiresRoutesOrDirectDispatch(t *testing.T) {
	cfg := directOnlyConfig()
	cfg.Requests.DirectDispatch.Enabled = false
	if !hasErr(Validate(cfg), "requests: must configure routes or enable directDispatch") {
		t.Fatalf("expected direct-dispatch-or-routes error, got %v", Validate(cfg))
	}
}

func TestValidateDirectDispatchRequiresPositiveTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second} {
		t.Run(timeout.String(), func(t *testing.T) {
			cfg := directOnlyConfig()
			cfg.Requests.DirectDispatch.Timeout = timeout
			if !hasErr(Validate(cfg), "requests.directDispatch.timeout: must be positive") {
				t.Fatalf("expected timeout error, got %v", Validate(cfg))
			}
		})
	}
}

func TestValidateDirectDispatchRejectsUnsafePrefix(t *testing.T) {
	for _, prefix := range []string{"/orders/../admin", "/orders/?page=1"} {
		t.Run(prefix, func(t *testing.T) {
			cfg := directOnlyConfig()
			cfg.Requests.DirectDispatch.AllowedPathPrefixes = []string{prefix}
			errs := Validate(cfg)
			if !hasErr(errs, "requests.directDispatch.allowedPathPrefixes[0]") {
				t.Fatalf("expected indexed prefix error, got %v", errs)
			}
			if !hasErr(errs, "traversal segment") && !hasErr(errs, "must not contain a query") {
				t.Fatalf("expected prefix validator reason, got %v", errs)
			}
		})
	}
}

func TestValidateStaticRoutesAndDirectDispatch(t *testing.T) {
	cfg := directOnlyConfig()
	cfg.Requests.Routes = baseRequests().Routes
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateDirectDispatchAllowsEmptyPrefixes(t *testing.T) {
	cfg := directOnlyConfig()
	cfg.Requests.DirectDispatch.AllowedPathPrefixes = nil
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidatePureResponder(t *testing.T) {
	cfg := &Config{
		App:           AppConfig{ID: "upload-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:          NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests:      baseRequests(),
		Observability: ObservabilityConfig{Environment: "testing"},
	}
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidateRejectsNoPath(t *testing.T) {
	cfg := &Config{
		App:  AppConfig{ID: "x", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS: NATSConfig{URL: "nats://127.0.0.1:4222"},
	}
	errs := Validate(cfg)
	if !hasErr(errs, "at least one of routes or requests") {
		t.Fatalf("expected at-least-one-path error, got %v", errs)
	}
}

func TestValidateRequestRouteFields(t *testing.T) {
	reqs := baseRequests()
	reqs.Routes[0].Reply.Type = ""
	reqs.Subject = ""
	cfg := &Config{
		App:      AppConfig{ID: "x", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: reqs,
	}
	errs := Validate(cfg)
	if !hasErr(errs, "requests.subject") {
		t.Errorf("expected requests.subject error, got %v", errs)
	}
	if !hasErr(errs, "reply.type") {
		t.Errorf("expected reply.type error, got %v", errs)
	}
}

func TestValidateRejectsBadPathTemplateInJetStreamRoute(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Path = "/api/{123bad}/x"
	errs := Validate(cfg)
	if !hasErr(errs, "routes[0].dispatch.path") {
		t.Fatalf("expected routes[0].dispatch.path error, got %v", errs)
	}
	if !hasErr(errs, "123bad") {
		t.Fatalf("expected error mentioning bad token 123bad, got %v", errs)
	}
}

func TestValidateRejectsBadPathTemplateInRequestReplyRoute(t *testing.T) {
	reqs := baseRequests()
	reqs.Routes[0].Dispatch.Path = "/api/{a-b}/presign"
	cfg := &Config{
		App:      AppConfig{ID: "upload-service", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: reqs,
	}
	errs := Validate(cfg)
	if !hasErr(errs, "requests.routes[0].dispatch.path") {
		t.Fatalf("expected requests.routes[0].dispatch.path error, got %v", errs)
	}
}

func TestValidateAcceptsValidPathTemplate(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Path = "/api/tasks/{taskId}/complete"
	cfg.Requests = baseRequests()
	cfg.Requests.Routes[0].Dispatch.Path = "/api/uploads/{uploadId}/presign"
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors for valid path templates, got %v", errs)
	}
}

func TestValidateAcceptsGetDispatchMethod(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Method = http.MethodGet
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors for GET dispatch method, got %v", errs)
	}
}

func TestValidateAcceptsDeleteDispatchMethodForEventAndRequestRoutes(t *testing.T) {
	cfg := validConfig()
	cfg.Routes[0].Dispatch.Method = http.MethodDelete
	cfg.Requests = baseRequests()
	cfg.Requests.Routes[0].Dispatch.Method = http.MethodDelete
	if errs := Validate(cfg); len(errs) != 0 {
		t.Fatalf("expected no errors for DELETE dispatch method, got %v", errs)
	}
}

func TestValidateRejectsNonCanonicalDispatchMethod(t *testing.T) {
	const want = "routes[0].dispatch.method: must be GET, POST, PUT, PATCH, or DELETE"
	for _, method := range []string{"delete", " DELETE "} {
		t.Run(method, func(t *testing.T) {
			cfg := validConfig()
			cfg.Routes[0].Dispatch.Method = method
			for _, err := range Validate(cfg) {
				if err.Error() == want {
					return
				}
			}
			t.Fatalf("expected validation error %q, got %v", want, Validate(cfg))
		})
	}
}

func TestValidateDuplicateRequestType(t *testing.T) {
	reqs := baseRequests()
	dup := reqs.Routes[0]
	dup.Name = "second"
	reqs.Routes = append(reqs.Routes, dup)
	cfg := &Config{
		App:      AppConfig{ID: "x", HTTPBaseURL: "http://127.0.0.1:8080"},
		NATS:     NATSConfig{URL: "nats://127.0.0.1:4222"},
		Requests: reqs,
	}
	if !hasErr(Validate(cfg), "duplicate match type") {
		t.Fatalf("expected duplicate type error")
	}
}
