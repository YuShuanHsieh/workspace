package pathtemplate_test

import (
	"errors"
	"strings"
	"testing"

	"event-adapter/internal/pathtemplate"
)

func TestValidateAcceptsStaticPath(t *testing.T) {
	if err := pathtemplate.Validate("/events/task-created"); err != nil {
		t.Fatalf("Validate static path: %v", err)
	}
}

func TestValidateAcceptsSingleToken(t *testing.T) {
	if err := pathtemplate.Validate("/api/tasks/{taskId}/complete"); err != nil {
		t.Fatalf("Validate single-token path: %v", err)
	}
}

func TestValidateAcceptsMultipleTokens(t *testing.T) {
	if err := pathtemplate.Validate("/api/tenants/{tenantId}/tasks/{taskId}"); err != nil {
		t.Fatalf("Validate multi-token path: %v", err)
	}
}

func TestValidateAcceptsSameTokenTwice(t *testing.T) {
	if err := pathtemplate.Validate("/{taskId}/x/{taskId}/y"); err != nil {
		t.Fatalf("Validate same-token-twice: %v", err)
	}
}

func TestValidateRejectsTokenStartingWithDigit(t *testing.T) {
	err := pathtemplate.Validate("/api/{123bad}/x")
	if err == nil {
		t.Fatal("expected error for {123bad}")
	}
	if !strings.Contains(err.Error(), "123bad") {
		t.Fatalf("error should name the bad token, got: %v", err)
	}
}

func TestValidateRejectsEmptyToken(t *testing.T) {
	if err := pathtemplate.Validate("/api/{}/x"); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestValidateRejectsTokenWithHyphen(t *testing.T) {
	if err := pathtemplate.Validate("/api/{a-b}/x"); err == nil {
		t.Fatal("expected error for {a-b}")
	}
}

func TestValidateRejectsUnclosedToken(t *testing.T) {
	if err := pathtemplate.Validate("/api/{taskId/x"); err == nil {
		t.Fatal("expected error for unclosed brace")
	}
}

func TestValidateRejectsDoubleBraces(t *testing.T) {
	// Regression for an unanchored-regex bug: tokenRegex.MatchString would
	// return true for "{{taskId}}" because the inner {taskId} matched as a
	// substring. Validate must require each {...} pair to be a strict full
	// token, not merely contain a valid token within it.
	cases := []string{
		"/api/{{taskId}}/x",
		"/api/{{taskId}/x",
		"/api/{taskId}}/x",
	}
	for _, p := range cases {
		if err := pathtemplate.Validate(p); err == nil {
			t.Errorf("Validate(%q) = nil, want error for malformed token", p)
		}
	}
}

func TestValidateErrorIsNotPermanent(t *testing.T) {
	// Validation errors happen at config-load, not at dispatch.
	// They MUST NOT wrap ErrPermanent — the processor only checks for ErrPermanent
	// to bypass retry, and a config error should never reach the processor.
	err := pathtemplate.Validate("/api/{123}/x")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if errors.Is(err, pathtemplate.ErrPermanent) {
		t.Fatal("Validate errors must not wrap ErrPermanent")
	}
}

func TestResolveStaticPathReturnsUnchanged(t *testing.T) {
	got, err := pathtemplate.Resolve("/events/task-created", map[string]string{"taskId": "x"})
	if err != nil {
		t.Fatalf("Resolve static: %v", err)
	}
	if got != "/events/task-created" {
		t.Fatalf("Resolve static = %q, want unchanged", got)
	}
}

func TestResolveStaticPathIgnoresNilParams(t *testing.T) {
	// Static paths must short-circuit before touching params, so nil is fine.
	got, err := pathtemplate.Resolve("/events/task-created", nil)
	if err != nil {
		t.Fatalf("Resolve static with nil params: %v", err)
	}
	if got != "/events/task-created" {
		t.Fatalf("Resolve = %q, want unchanged", got)
	}
}

func TestResolveSingleToken(t *testing.T) {
	got, err := pathtemplate.Resolve("/api/tasks/{taskId}/complete", map[string]string{"taskId": "task-42"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tasks/task-42/complete" {
		t.Fatalf("Resolve = %q, want /api/tasks/task-42/complete", got)
	}
}

func TestResolveMultipleTokens(t *testing.T) {
	got, err := pathtemplate.Resolve("/api/tenants/{tenantId}/tasks/{taskId}", map[string]string{"tenantId": "acme", "taskId": "task-42"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tenants/acme/tasks/task-42" {
		t.Fatalf("Resolve = %q, want /api/tenants/acme/tasks/task-42", got)
	}
}

func TestResolveSameTokenTwice(t *testing.T) {
	got, err := pathtemplate.Resolve("/{taskId}/x/{taskId}/y", map[string]string{"taskId": "abc"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/abc/x/abc/y" {
		t.Fatalf("Resolve = %q, want /abc/x/abc/y", got)
	}
}

func TestResolveURLEscapesValues(t *testing.T) {
	// Spaces and slashes in param values must be path-escaped so they don't
	// reshape the URL.
	got, err := pathtemplate.Resolve("/api/tasks/{taskId}", map[string]string{"taskId": "a b/c"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tasks/a%20b%2Fc" {
		t.Fatalf("Resolve = %q, want path-escaped a%%20b%%2Fc", got)
	}
}

func TestResolveMissingFieldIsPermanent(t *testing.T) {
	_, err := pathtemplate.Resolve("/api/tasks/{taskId}/complete", map[string]string{"status": "done"})
	if err == nil {
		t.Fatal("expected permanent error for missing field")
	}
	if !errors.Is(err, pathtemplate.ErrPermanent) {
		t.Fatalf("error must wrap ErrPermanent, got %v", err)
	}
	if !strings.Contains(err.Error(), "taskId") {
		t.Fatalf("error should name the missing field, got %v", err)
	}
}

func TestResolveNilParamsIsPermanentWhenTokensPresent(t *testing.T) {
	_, err := pathtemplate.Resolve("/api/tasks/{taskId}", nil)
	if err == nil {
		t.Fatal("expected permanent error when params are nil and tokens present")
	}
	if !errors.Is(err, pathtemplate.ErrPermanent) {
		t.Fatalf("error must wrap ErrPermanent, got %v", err)
	}
}

func TestResolveBadConfigDoesNotWrapPermanent(t *testing.T) {
	// If Resolve is somehow called with a bad path (config validation missed
	// it), the error must NOT wrap ErrPermanent — that would silently DLQ
	// the event when the real fix is to correct the config.
	_, err := pathtemplate.Resolve("/api/{123bad}/x", map[string]string{"taskId": "x"})
	if err == nil {
		t.Fatal("expected config error")
	}
	if errors.Is(err, pathtemplate.ErrPermanent) {
		t.Fatal("config errors must not wrap ErrPermanent")
	}
}

