package header

import (
	"unicode/utf8"
)

const MaxContextHeaderLen = 1024

// ParsedContext is the result of splitting X-Auth-Context.
// Field semantics come from phase-1-request-contract.md §3.1.
type ParsedContext struct {
	ObjectID   string
	ObjectType string
	Action     string
}

// ParseError carries the metric reason label defined in phase-1-context-header-format.md §4.
type ParseError struct {
	Reason string
}

func (e *ParseError) Error() string { return "context header parse failure: " + e.Reason }

// ParseContextHeader applies the rules from phase-1-context-header-format.md.
// It never returns a partial result on error.
func ParseContextHeader(v string) (ParsedContext, error) {
	if len(v) > MaxContextHeaderLen {
		return ParsedContext{}, &ParseError{Reason: "over_length"}
	}
	if !utf8.ValidString(v) {
		return ParsedContext{}, &ParseError{Reason: "non_printable"}
	}
	for i := 0; i < len(v); i++ {
		b := v[i]
		if b > 0x7E {
			return ParsedContext{}, &ParseError{Reason: "non_printable"}
		}
		if b < 0x20 || b == 0x7F {
			return ParsedContext{}, &ParseError{Reason: "control_char"}
		}
	}

	segs := splitExact(v, ':', 3)
	if segs == nil {
		return ParsedContext{}, &ParseError{Reason: "wrong_segment_count"}
	}
	for _, s := range segs {
		if s == "" {
			return ParsedContext{}, &ParseError{Reason: "empty_segment"}
		}
	}
	for _, s := range segs {
		for i := 0; i < len(s); i++ {
			c := s[i]
			if c == ' ' || c == '\t' {
				return ParsedContext{}, &ParseError{Reason: "whitespace"}
			}
		}
	}
	return ParsedContext{ObjectID: segs[0], ObjectType: segs[1], Action: segs[2]}, nil
}

// splitExact returns nil when v does not have exactly n segments separated by sep.
func splitExact(v string, sep byte, n int) []string {
	out := make([]string, 0, n)
	start := 0
	for i := 0; i < len(v); i++ {
		if v[i] == sep {
			if len(out) == n-1 {
				return nil
			}
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	out = append(out, v[start:])
	if len(out) != n {
		return nil
	}
	return out
}
