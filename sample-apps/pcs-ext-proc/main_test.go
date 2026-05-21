package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecide_AliceCanEditDoc1(t *testing.T) {
	got := decide("alice@workspace.test", "doc-1", "document", "edit")
	if got != true {
		t.Fatalf("expected allow, got deny")
	}
}

func TestDecide_AliceCannotEditDoc2(t *testing.T) {
	if decide("alice@workspace.test", "doc-2", "document", "edit") {
		t.Fatalf("expected deny, got allow")
	}
}

func TestDecide_BobCannotEditDoc1(t *testing.T) {
	if decide("bob@workspace.test", "doc-1", "document", "edit") {
		t.Fatalf("expected deny, got allow")
	}
}

func TestDecide_UnknownUserAlwaysDenied(t *testing.T) {
	if decide("mallory@workspace.test", "doc-1", "document", "read") {
		t.Fatalf("expected deny for unknown user, got allow")
	}
}

func TestDecide_UnknownObjectAlwaysDenied(t *testing.T) {
	if decide("alice@workspace.test", "doc-99", "document", "read") {
		t.Fatalf("expected deny for unknown object, got allow")
	}
}

func TestCheckHandler_AllowReturns200WithAllowedTrue(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodPost,
		"/permission-check/v1/check",
		strings.NewReader(`{"objectId":"doc-1","objectType":"document","permission":"edit"}`),
	)
	r.Header.Set("Authorization", "Bearer alice@workspace.test")
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	checkHandler(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"allowed":true`) {
		t.Fatalf("body: got %q, want allowed:true", w.Body.String())
	}
}

func TestCheckHandler_DenyReturns200WithAllowedFalse(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodPost,
		"/permission-check/v1/check",
		strings.NewReader(`{"objectId":"doc-2","objectType":"document","permission":"edit"}`),
	)
	r.Header.Set("Authorization", "Bearer alice@workspace.test")
	w := httptest.NewRecorder()
	checkHandler(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status: got %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), `"allowed":false`) {
		t.Fatalf("body: got %q, want allowed:false", w.Body.String())
	}
}

func TestCheckHandler_MalformedJSONReturns400(t *testing.T) {
	r := httptest.NewRequest(
		http.MethodPost,
		"/permission-check/v1/check",
		strings.NewReader(`not json`),
	)
	r.Header.Set("Authorization", "Bearer alice@workspace.test")
	w := httptest.NewRecorder()
	checkHandler(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: got %d, want 400", w.Code)
	}
}
