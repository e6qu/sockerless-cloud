package main

import (
	"strings"
	"testing"
)

// TestGCPFilterDepthGuard: deeply-nested parens must not overflow the stack.
func TestGCPFilterDepthGuard(t *testing.T) {
	_ = gcpParseFilterExpr(strings.Repeat("(", 2000000) + "a = b")
}
