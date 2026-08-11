package main

import (
	"strings"
	"testing"
)

// TestODataDepthGuard: deeply-nested parens must not overflow the stack.
func TestODataDepthGuard(t *testing.T) {
	_, _ = azureParseODataFilter(strings.Repeat("(", 2000000) + "a eq 'b'")
}
