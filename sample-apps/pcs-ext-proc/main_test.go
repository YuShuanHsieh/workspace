package main

import "testing"

func TestDecide_AliceCanEditDoc1(t *testing.T) {
	got := decide("alice@workspace.test", "doc-1", "document", "edit")
	if got != true {
		t.Fatalf("expected allow, got deny")
	}
}
