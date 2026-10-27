package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGlueDataQualityResultsV2CLI(t *testing.T) {
	t.Parallel()
	evaluationContext := "dq-v2-cli-context"
	publishArgs := []string{"glue", "publish-data-quality-result-v2",
		"--dimensions", `{"platform":"emr","cluster":"j-CLI"}`,
		"--score", "0.75",
		"--evaluation-context", evaluationContext,
		"--ruleset-name", "orders-cli-checks",
		"--client-token", "dq-v2-cli-token",
		"--rule-results", `[{"Name":"Rule_1","Result":"PASS"},{"Name":"Rule_2","Result":"PASS"},{"Name":"Rule_3","Result":"PASS"},{"Name":"Rule_4","Result":"FAIL"}]`,
	}

	out := runCLI(t, awsCLI(publishArgs...))
	var published struct {
		ResultId string `json:"ResultId"`
	}
	parseJSON(t, out, &published)
	require.NotEmpty(t, published.ResultId)

	out = runCLI(t, awsCLI(publishArgs...))
	var retried struct {
		ResultId string `json:"ResultId"`
	}
	parseJSON(t, out, &retried)
	assert.Equal(t, published.ResultId, retried.ResultId, "a retry with the same client token names the same result")

	mismatch := append(append([]string{}, publishArgs...), "--tags", `{"team":"other"}`)
	assert.Contains(t, runCLIExpectError(t, awsCLI(mismatch...)), "IdempotentParameterMismatchException")

	out = runCLI(t, awsCLI("glue", "get-data-quality-result-v2", "--result-id", published.ResultId))
	var got struct {
		ResultId          string            `json:"ResultId"`
		Score             float64           `json:"Score"`
		EvaluationContext string            `json:"EvaluationContext"`
		RulesetName       string            `json:"RulesetName"`
		Dimensions        map[string]string `json:"Dimensions"`
		RuleResults       []struct {
			Name   string `json:"Name"`
			Result string `json:"Result"`
		} `json:"RuleResults"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, published.ResultId, got.ResultId)
	assert.Equal(t, 0.75, got.Score)
	assert.Equal(t, evaluationContext, got.EvaluationContext)
	assert.Equal(t, "orders-cli-checks", got.RulesetName)
	assert.Equal(t, "j-CLI", got.Dimensions["cluster"])
	require.Len(t, got.RuleResults, 4)
	assert.Equal(t, "FAIL", got.RuleResults[3].Result)

	out = runCLI(t, awsCLI("glue", "batch-get-data-quality-result-v2",
		"--result-ids", published.ResultId, "dqresult-missing"))
	var batch struct {
		Results []struct {
			ResultId string `json:"ResultId"`
		} `json:"Results"`
		ResultsNotFound []string `json:"ResultsNotFound"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Results, 1)
	assert.Equal(t, published.ResultId, batch.Results[0].ResultId)
	assert.Equal(t, []string{"dqresult-missing"}, batch.ResultsNotFound)

	out = runCLI(t, awsCLI("glue", "list-data-quality-results-v2",
		"--filter", `{"EvaluationContext":"`+evaluationContext+`","Dimensions":{"platform":"emr"}}`))
	var listed struct {
		Results []struct {
			ResultId          string `json:"ResultId"`
			EvaluationContext string `json:"EvaluationContext"`
		} `json:"Results"`
	}
	parseJSON(t, out, &listed)
	require.Len(t, listed.Results, 1)
	assert.Equal(t, published.ResultId, listed.Results[0].ResultId)
}
