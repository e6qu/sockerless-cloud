package main

import (
	"strings"
	"testing"
)

// TestParserDepthGuards proves the recursive-descent parsers no longer overflow
// the stack on pathologically nested parens (a Go stack-overflow is a fatal
// error that would kill this test process — so reaching the asserts = fixed).
func TestParserDepthGuards(t *testing.T) {
	deep := strings.Repeat("(", 500000) + "a = :v"
	// DynamoDB condition expression (the depth guard makes this a loud error
	// rather than a stack overflow; either way the process must not crash).
	_, _ = ddbEvalCondition(map[string]any{}, true, deep, nil, map[string]any{":v": map[string]any{"S": "x"}})
	// CloudWatch Logs Insights filter
	_, _ = cwParseInsightsFilter(strings.Repeat("(", 500000) + "level = ERROR")
	// CloudWatch metric-filter pattern (structured JSON)
	_, _ = cwCompileLogPattern("{" + strings.Repeat("(", 500000) + "$.a = 1" + strings.Repeat(")", 500000) + "}")
	// reaching here without a crash is the assertion
}
