package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSTS_GetCallerIdentity(t *testing.T) {
	out := runCLI(t, awsCLI("sts", "get-caller-identity", "--output", "json"))

	var result struct {
		Account string `json:"Account"`
		Arn     string `json:"Arn"`
		UserId  string `json:"UserId"`
	}
	parseJSON(t, out, &result)

	require.NotEmpty(t, result.Account)
	assert.NotEmpty(t, result.Arn)
}
