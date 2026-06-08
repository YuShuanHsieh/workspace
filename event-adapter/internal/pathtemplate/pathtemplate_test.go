package pathtemplate

import (
	"errors"
	"strings"
	"testing"
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
