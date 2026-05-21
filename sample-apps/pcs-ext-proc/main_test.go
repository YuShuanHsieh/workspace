package main

import "testing"

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
