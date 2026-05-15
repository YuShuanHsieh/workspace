package header

import (
	"strings"
	"testing"
)

func TestParseContextHeader_Valid(t *testing.T) {
	got, err := ParseContextHeader("doc-42:document:edit")
	if err != nil {
		t.Fatalf("expected ok, got error: %v", err)
	}
	if got.ObjectID != "doc-42" || got.ObjectType != "document" || got.Action != "edit" {
		t.Fatalf("unexpected parse result: %#v", got)
	}
}

func TestParseContextHeader_Rejections(t *testing.T) {
	cases := []struct {
		name       string
		input      string
		wantReason string
	}{
		{"wrong segment count - too few", "doc-42:document", "wrong_segment_count"},
		{"wrong segment count - too many", "a:b:c:d", "wrong_segment_count"},
		{"empty leading segment", ":document:edit", "empty_segment"},
		{"empty middle segment", "doc-42::edit", "empty_segment"},
		{"empty trailing segment", "doc-42:document:", "empty_segment"},
		{"leading whitespace", " doc-42:document:edit", "whitespace"},
		{"trailing whitespace", "doc-42:document:edit ", "whitespace"},
		{"interior whitespace", "doc-42:doc type:edit", "whitespace"},
		{"control character", "doc-42:document:edit\x01", "control_char"},
		{"non-printable", "doc-42:document:edit\xff", "non_printable"},
		{"over length", strings.Repeat("a", 1024-2) + ":b:c", "over_length"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseContextHeader(c.input)
			if err == nil {
				t.Fatalf("expected error, got nil")
			}
			pe, ok := err.(*ParseError)
			if !ok {
				t.Fatalf("expected *ParseError, got %T: %v", err, err)
			}
			if pe.Reason != c.wantReason {
				t.Fatalf("reason: got %q want %q", pe.Reason, c.wantReason)
			}
		})
	}
}
