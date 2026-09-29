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
	failsTheConjunct := map[string]any{"a": "1", "b": "9", "c": "9"}
	matchesBothHalves := map[string]any{"a": "1", "b": "9", "c": "3"}

	for _, depth := range []int{1, 2, 8, 64, maxFilterParseDepth / 2} {
		open, close := strings.Repeat("(", depth), strings.Repeat(")", depth)
		filter := open + "(a = 1 OR b = 2) AND c = 3" + close
		node, err := gcpParseFilterExpr(filter)
		if err != nil {
			t.Fatalf("nested %d deep: the filter must parse: %v", depth, err)
		}
		if node.Eval(failsTheConjunct) {
			t.Errorf("nested %d deep: the parenthesised group was lost — a value the "+
				"conjunct after it excludes must not match the filter", depth)
		}
		if !node.Eval(matchesBothHalves) {
			t.Errorf("nested %d deep: a value both halves match must match the filter", depth)
		}
	}
}

// TestGCPFilterDepthGuardRejects covers what maxFilterParseDepth exists for: a
// filter nested far past any real query terminates, and Google Cloud answers
// it as the malformed filter it is rather than matching everything.
func TestGCPFilterDepthGuardRejects(t *testing.T) {
	for _, filter := range []string{
		strings.Repeat("(", 2_000_000) + "a = b",
		strings.Repeat("NOT ", 2_000_000) + "a = b",
	} {
		if node, err := gcpParseFilterExpr(filter); err == nil {
			t.Fatalf("a filter past the depth guard parsed to %v", node)
		}
	}
}
