package main

import (
	"strings"
	"testing"
)

// TestGCPFilterGroupingSurvivesNesting pins the part of the parenthesis
// handling that a caller can observe: grouping changes the answer. `(a OR b)
// AND c` is not `a OR (b AND c)`, so a parser that loses a group — by failing
// to consume its closing parenthesis, or by flattening the levels it descended
// — answers differently for a value the group excludes.
func TestGCPFilterGroupingSurvivesNesting(t *testing.T) {
	// The disjunction holds but the conjunct after it does not, so a correctly
	// grouped expression rejects this. A parser that dropped the group answers
	// on the disjunction alone and matches.
	failsTheConjunct := map[string]any{"a": "1", "b": "9", "c": "9"}
	// Both halves hold, so a correctly grouped expression matches.
	matchesBothHalves := map[string]any{"a": "1", "b": "9", "c": "3"}

	for _, depth := range []int{1, 2, 8, 64, maxFilterParseDepth / 2} {
		open, close := strings.Repeat("(", depth), strings.Repeat(")", depth)
		filter := open + "(a = 1 OR b = 2) AND c = 3" + close
		node := gcpParseFilterExpr(filter)
		if node == nil {
			t.Fatalf("nested %d deep: the filter must parse", depth)
		}
		if node.eval(failsTheConjunct) {
			t.Errorf("nested %d deep: the parenthesised group was lost — a value the "+
				"conjunct after it excludes must not match the filter", depth)
		}
		if !node.eval(matchesBothHalves) {
			t.Errorf("nested %d deep: a value both halves match must match the filter", depth)
		}
	}
}

// TestGCPFilterDepthGuardTerminates covers the one thing maxFilterParseDepth
// exists for: a filter nested far past any real query must terminate rather
// than recurse without bound. The oracle is that this binary reaches the
// assertion — an exhausted goroutine stack is a fatal error Go cannot recover
// from, and an unbounded parse never returns.
//
// The guard's truncation is deliberately not asserted through the returned
// node. gcpParseFilterExpr answers a refused level with the match-all node and
// then keeps consuming, so the tokens past the refused level are still parsed
// by the levels unwinding around it and the evaluated result is the same either
// way. ParseGuard's own boundary — the level at which Enter stops admitting —
// is covered in shared/parsecore_test.go.
func TestGCPFilterDepthGuardTerminates(t *testing.T) {
	node := gcpParseFilterExpr(strings.Repeat("(", 2_000_000) + "a = b")
	if node == nil {
		t.Fatalf("a filter the guard refused must still yield a node a caller can apply")
	}
}
