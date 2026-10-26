package main

import (
	"strings"
	"testing"
)

// A depth guard that gave up with a nil filter and nil error would serve the
// whole collection, so the guard must refuse; the shallow filter keeps it from
// passing by refusing everything.
func TestODataDepthGuard(t *testing.T) {
	deep := strings.Repeat("(", 2000000) + "a eq 'b'"
	filter, err := azureParseODataFilter(deep)
	if err == nil {
		t.Fatalf("a filter nested 2,000,000 deep was accepted; the depth guard did not fire")
	}
	if !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("the refusal must name the nesting depth as the reason, got %q", err)
	}
	if filter != nil {
		t.Fatalf("a refused filter must come back nil; a non-nil filter beside an error is read as a match-everything predicate by a caller that checks only the error")
	}

	if _, err := azureParseODataFilter(strings.Repeat("not ", 2000000) + "a eq 'b'"); err == nil || !strings.Contains(err.Error(), "too deep") {
		t.Fatalf("a chain of 2,000,000 nots must meet the same guard, got %v", err)
	}

	shallow, err := azureParseODataFilter("(a eq 'b')")
	if err != nil {
		t.Fatalf("the depth guard must not refuse an ordinary parenthesised filter: %v", err)
	}
	if shallow == nil {
		t.Fatalf("an accepted filter must come back non-nil")
	}
}
