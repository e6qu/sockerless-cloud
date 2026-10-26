package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParserDepthGuards holds the three recursive-descent parsers to refusing
// pathologically nested input with their own named error. A stack overflow would
// kill the test process, and reaching the end alone would also pass if a guard
// were removed and the parser accepted the input.
func TestParserDepthGuards(t *testing.T) {
	const depth = 500000
	opens := strings.Repeat("(", depth)

	// DynamoDB condition expression.
	matched, err := ddbEvalCondition(map[string]any{}, true, opens+"a = :v", nil,
		map[string]any{":v": map[string]any{"S": "x"}})
	require.Error(t, err, "a condition expression nested %d deep must be refused", depth)
	require.Contains(t, err.Error(), "nesting too deep")
	require.False(t, matched, "a refused condition expression matches nothing")

	// CloudWatch Logs Insights filter.
	_, err = cwParseInsightsFilter(opens + "level = ERROR")
	require.Error(t, err, "an Insights filter nested %d deep must be refused", depth)
	require.Contains(t, err.Error(), "nesting too deep")

	_, err = cwParseInsightsFilter(strings.Repeat("not ", depth) + "level = ERROR")
	require.Error(t, err, "a chain of %d nots must meet the same guard", depth)
	require.Contains(t, err.Error(), "nesting too deep")

	// CloudWatch metric-filter pattern (structured JSON).
	compiled, err := cwCompileLogPattern("{" + opens + "$.a = 1" + strings.Repeat(")", depth) + "}")
	require.Error(t, err, "a filter pattern nested %d deep must be refused", depth)
	require.Contains(t, err.Error(), "nesting too deep")
	require.Nil(t, compiled, "a refused filter pattern compiles to nothing")
}
