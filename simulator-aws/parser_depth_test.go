package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestParserDepthGuards holds the three recursive-descent parsers to refusing
// pathologically nested input rather than recursing into it. A Go stack
// overflow is a fatal error that takes the whole test process with it, so
// merely reaching the end of this function proves the parsers did not overflow
// — but that alone would also pass if a guard were removed and the parser
// happened to accept the input, or answered success on nonsense. Each guard's
// own refusal is asserted, which is what the guards are for: they exist to turn
// unbounded recursion into a named error.
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

	// CloudWatch metric-filter pattern (structured JSON).
	compiled, err := cwCompileLogPattern("{" + opens + "$.a = 1" + strings.Repeat(")", depth) + "}")
	require.Error(t, err, "a filter pattern nested %d deep must be refused", depth)
	require.Contains(t, err.Error(), "nesting too deep")
	require.Nil(t, compiled, "a refused filter pattern compiles to nothing")
}
