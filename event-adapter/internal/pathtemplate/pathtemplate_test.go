package pathtemplate

import (
	"errors"
	"strings"
	"testing"

	clevent "event-adapter/internal/cloudevent"
)

func TestValidateAcceptsStaticPath(t *testing.T) {
	if err := Validate("/events/task-created"); err != nil {
		t.Fatalf("Validate static path: %v", err)
	}
}

func TestValidateAcceptsSingleToken(t *testing.T) {
	if err := Validate("/api/tasks/{taskId}/complete"); err != nil {
		t.Fatalf("Validate single-token path: %v", err)
	}
}

func TestValidateAcceptsMultipleTokens(t *testing.T) {
	if err := Validate("/api/tenants/{tenantId}/tasks/{taskId}"); err != nil {
		t.Fatalf("Validate multi-token path: %v", err)
	}
}

func TestValidateAcceptsSameTokenTwice(t *testing.T) {
	if err := Validate("/{taskId}/x/{taskId}/y"); err != nil {
		t.Fatalf("Validate same-token-twice: %v", err)
	}
}

func TestValidateRejectsTokenStartingWithDigit(t *testing.T) {
	err := Validate("/api/{123bad}/x")
	if err == nil {
		t.Fatal("expected error for {123bad}")
	}
	if !strings.Contains(err.Error(), "123bad") {
		t.Fatalf("error should name the bad token, got: %v", err)
	}
}

func TestValidateRejectsEmptyToken(t *testing.T) {
	if err := Validate("/api/{}/x"); err == nil {
		t.Fatal("expected error for empty token")
	}
}

func TestValidateRejectsTokenWithHyphen(t *testing.T) {
	if err := Validate("/api/{a-b}/x"); err == nil {
		t.Fatal("expected error for {a-b}")
	}
}

func TestValidateRejectsUnclosedToken(t *testing.T) {
	if err := Validate("/api/{taskId/x"); err == nil {
		t.Fatal("expected error for unclosed brace")
	}
}

func TestValidateErrorIsNotPermanent(t *testing.T) {
	// Validation errors happen at config-load, not at dispatch.
	// They MUST NOT wrap ErrPermanent — the processor only checks for ErrPermanent
	// to bypass retry, and a config error should never reach the processor.
	err := Validate("/api/{123}/x")
	if err == nil {
		t.Fatal("expected validation error")
	}
	if errors.Is(err, ErrPermanent) {
		t.Fatal("Validate errors must not wrap ErrPermanent")
	}
}

func TestResolveStaticPathReturnsUnchanged(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e1","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"x"}}`)
	got, err := Resolve("/events/task-created", ev)
	if err != nil {
		t.Fatalf("Resolve static: %v", err)
	}
	if got != "/events/task-created" {
		t.Fatalf("Resolve static = %q, want unchanged", got)
	}
}

func TestResolveSingleToken(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e2","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"task-42"}}`)
	got, err := Resolve("/api/tasks/{taskId}/complete", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tasks/task-42/complete" {
		t.Fatalf("Resolve = %q, want /api/tasks/task-42/complete", got)
	}
}

func TestResolveMultipleTokens(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e3","source":"s","type":"t","datacontenttype":"application/json","data":{"tenantId":"acme","taskId":"task-42"}}`)
	got, err := Resolve("/api/tenants/{tenantId}/tasks/{taskId}", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tenants/acme/tasks/task-42" {
		t.Fatalf("Resolve = %q, want /api/tenants/acme/tasks/task-42", got)
	}
}

func TestResolveSameTokenTwice(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e4","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"abc"}}`)
	got, err := Resolve("/{taskId}/x/{taskId}/y", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/abc/x/abc/y" {
		t.Fatalf("Resolve = %q, want /abc/x/abc/y", got)
	}
}

func TestResolveURLEscapesValues(t *testing.T) {
	// Spaces and slashes in field values must be path-escaped so they don't
	// reshape the URL.
	ev := mustParse(t, `{"specversion":"1.0","id":"e5","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"a b/c"}}`)
	got, err := Resolve("/api/tasks/{taskId}", ev)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != "/api/tasks/a%20b%2Fc" {
		t.Fatalf("Resolve = %q, want path-escaped a%%20b%%2Fc", got)
	}
}

func TestResolveMissingFieldIsPermanent(t *testing.T) {
	ev := mustParse(t, `{"specversion":"1.0","id":"e6","source":"s","type":"t","datacontenttype":"application/json","data":{"status":"done"}}`)
	_, err := Resolve("/api/tasks/{taskId}/complete", ev)
	if err == nil {
		t.Fatal("expected permanent error for missing field")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("error must wrap ErrPermanent, got %v", err)
	}
	if !strings.Contains(err.Error(), "taskId") {
		t.Fatalf("error should name the missing field, got %v", err)
	}
}

func TestResolveDataNotAnObjectIsPermanent(t *testing.T) {
	// data is a JSON array, not an object.
	ev := mustParse(t, `{"specversion":"1.0","id":"e7","source":"s","type":"t","datacontenttype":"application/json","data":["not","an","object"]}`)
	_, err := Resolve("/api/tasks/{taskId}", ev)
	if err == nil {
		t.Fatal("expected permanent error for non-object data")
	}
	if !errors.Is(err, ErrPermanent) {
		t.Fatalf("error must wrap ErrPermanent, got %v", err)
	}
}

func TestResolveBadConfigDoesNotWrapPermanent(t *testing.T) {
	// If Resolve is somehow called with a bad path (config validation missed
	// it), the error must NOT wrap ErrPermanent — that would silently DLQ
	// the event when the real fix is to correct the config.
	ev := mustParse(t, `{"specversion":"1.0","id":"e8","source":"s","type":"t","datacontenttype":"application/json","data":{"taskId":"x"}}`)
	_, err := Resolve("/api/{123bad}/x", ev)
	if err == nil {
		t.Fatal("expected config error")
	}
	if errors.Is(err, ErrPermanent) {
		t.Fatal("config errors must not wrap ErrPermanent")
	}
}

func mustParse(t *testing.T, raw string) *clevent.Event {
	t.Helper()
	ev, err := clevent.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	return ev
}
