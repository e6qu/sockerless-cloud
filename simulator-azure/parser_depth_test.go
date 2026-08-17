package main

import (
	"strings"
	"testing"
)

// TestODataDepthGuard: deeply-nested parens must not overflow the stack, and
// the guard that prevents it must refuse the filter rather than fall back to a
// filter that matches everything.
//
// Surviving the call is only half the contract. A Go stack overflow is fatal
// and unrecoverable, so a crash would fail the run on its own; what a
// return-value-free test cannot see is the other failure mode — a depth guard
// that gives up and hands back a nil filter with a nil error, which every
// caller reads as "no filter", i.e. serve the whole collection to a request
// whose filter was never understood. The shallow filter alongside it keeps the
// guard from passing by refusing everything.
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

	shallow, err := azureParseODataFilter("(a eq 'b')")
	if err != nil {
		t.Fatalf("the depth guard must not refuse an ordinary parenthesised filter: %v", err)
	}
	if shallow == nil {
		t.Fatalf("an accepted filter must come back non-nil")
	}
}
