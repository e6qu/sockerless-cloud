package aws_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlueMLTransformCLI exercises the ML transform CRUD family via the aws CLI.
func TestGlueMLTransformCLI(t *testing.T) {
	out := runCLI(t, awsCLI("glue", "create-ml-transform",
		"--name", "glue-cli-mlt",
		"--description", "cli ml transform",
		"--input-record-tables", `[{"DatabaseName":"db","TableName":"tbl"}]`,
		"--parameters", `{"TransformType":"FIND_MATCHES"}`,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
	))
	var created struct {
		TransformId string `json:"TransformId"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.TransformId)
	id := created.TransformId
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-ml-transform", "--transform-id", id))
	})

	out = runCLI(t, awsCLI("glue", "get-ml-transform", "--transform-id", id))
	var get struct {
		Name   string `json:"Name"`
		Status string `json:"Status"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, "glue-cli-mlt", get.Name)
	assert.Equal(t, "READY", get.Status)

	out = runCLI(t, awsCLI("glue", "get-ml-transforms"))
	var gets struct {
		Transforms []struct {
			TransformId string `json:"TransformId"`
		} `json:"Transforms"`
	}
	parseJSON(t, out, &gets)
	foundT := false
	for _, tr := range gets.Transforms {
		if tr.TransformId == id {
			foundT = true
		}
	}
	assert.True(t, foundT)

	out = runCLI(t, awsCLI("glue", "list-ml-transforms"))
	var listed struct {
		TransformIds []string `json:"TransformIds"`
	}
	parseJSON(t, out, &listed)
	assert.Contains(t, listed.TransformIds, id)

	out = runCLI(t, awsCLI("glue", "update-ml-transform",
		"--transform-id", id, "--description", "updated"))
	var upd struct {
		TransformId string `json:"TransformId"`
	}
	parseJSON(t, out, &upd)
	assert.Equal(t, id, upd.TransformId)

	out = runCLI(t, awsCLI("glue", "delete-ml-transform", "--transform-id", id))
	var del struct {
		TransformId string `json:"TransformId"`
	}
	parseJSON(t, out, &del)
	assert.Equal(t, id, del.TransformId)
}

// TestGlueDataQualityCLI exercises Data Quality rulesets, evaluation and
// recommendation runs, results, statistics and models via the aws CLI.
func TestGlueDataQualityCLI(t *testing.T) {
	const rulesetName = "glue-cli-dqr"
	ruleset := "Rules = [ ColumnExists \"id\" ]"

	runCLI(t, awsCLI("glue", "create-data-quality-ruleset",
		"--name", rulesetName,
		"--ruleset", ruleset,
		"--description", "cli dq ruleset",
		"--target-table", `{"DatabaseName":"db","TableName":"tbl"}`,
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-data-quality-ruleset", "--name", rulesetName))
	})

	out := runCLI(t, awsCLI("glue", "get-data-quality-ruleset", "--name", rulesetName))
	var get struct {
		Name    string `json:"Name"`
		Ruleset string `json:"Ruleset"`
	}
	parseJSON(t, out, &get)
	assert.Equal(t, rulesetName, get.Name)
	assert.Equal(t, ruleset, get.Ruleset)

	out = runCLI(t, awsCLI("glue", "list-data-quality-rulesets"))
	var listR struct {
		Rulesets []struct {
			Name string `json:"Name"`
		} `json:"Rulesets"`
	}
	parseJSON(t, out, &listR)
	foundR := false
	for _, rs := range listR.Rulesets {
		if rs.Name == rulesetName {
			foundR = true
		}
	}
	assert.True(t, foundR)

	runCLI(t, awsCLI("glue", "update-data-quality-ruleset",
		"--name", rulesetName, "--description", "updated"))

	out = runCLI(t, awsCLI("glue", "start-data-quality-ruleset-evaluation-run",
		"--data-source", `{"GlueTable":{"DatabaseName":"db","TableName":"tbl"}}`,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
		"--ruleset-names", rulesetName,
	))
	var startRun struct {
		RunId string `json:"RunId"`
	}
	parseJSON(t, out, &startRun)
	require.NotEmpty(t, startRun.RunId)
	runID := startRun.RunId

	out = runCLI(t, awsCLI("glue", "get-data-quality-ruleset-evaluation-run", "--run-id", runID))
	var evalRun struct {
		Status    string   `json:"Status"`
		ResultIds []string `json:"ResultIds"`
	}
	parseJSON(t, out, &evalRun)
	assert.Equal(t, "SUCCEEDED", evalRun.Status)
	require.NotEmpty(t, evalRun.ResultIds)
	resultID := evalRun.ResultIds[0]

	runCLI(t, awsCLI("glue", "list-data-quality-ruleset-evaluation-runs"))
	runCLI(t, awsCLI("glue", "cancel-data-quality-ruleset-evaluation-run", "--run-id", runID))

	out = runCLI(t, awsCLI("glue", "get-data-quality-result", "--result-id", resultID))
	var res struct {
		ResultId    string `json:"ResultId"`
		RulesetName string `json:"RulesetName"`
	}
	parseJSON(t, out, &res)
	assert.Equal(t, resultID, res.ResultId)
	assert.Equal(t, rulesetName, res.RulesetName)

	out = runCLI(t, awsCLI("glue", "batch-get-data-quality-result",
		"--result-ids", resultID, "missing"))
	var batch struct {
		Results []struct {
			ResultId string `json:"ResultId"`
		} `json:"Results"`
		ResultsNotFound []string `json:"ResultsNotFound"`
	}
	parseJSON(t, out, &batch)
	require.Len(t, batch.Results, 1)
	assert.Contains(t, batch.ResultsNotFound, "missing")

	runCLI(t, awsCLI("glue", "list-data-quality-results"))

	out = runCLI(t, awsCLI("glue", "start-data-quality-rule-recommendation-run",
		"--data-source", `{"GlueTable":{"DatabaseName":"db","TableName":"tbl"}}`,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
	))
	var rec struct {
		RunId string `json:"RunId"`
	}
	parseJSON(t, out, &rec)
	require.NotEmpty(t, rec.RunId)
	recID := rec.RunId

	out = runCLI(t, awsCLI("glue", "get-data-quality-rule-recommendation-run", "--run-id", recID))
	var recRun struct {
		Status             string `json:"Status"`
		RecommendedRuleset string `json:"RecommendedRuleset"`
	}
	parseJSON(t, out, &recRun)
	assert.Equal(t, "SUCCEEDED", recRun.Status)

	runCLI(t, awsCLI("glue", "list-data-quality-rule-recommendation-runs"))
	runCLI(t, awsCLI("glue", "cancel-data-quality-rule-recommendation-run", "--run-id", recID))

	runCLI(t, awsCLI("glue", "list-data-quality-statistics"))
	runCLI(t, awsCLI("glue", "get-data-quality-model", "--profile-id", "profile-1"))
	runCLI(t, awsCLI("glue", "get-data-quality-model-result",
		"--statistic-id", "stat-1", "--profile-id", "profile-1"))
}

// TestGlueColumnStatisticsTaskCLI exercises the column-statistics task settings
// and runs family via the aws CLI.
func TestGlueColumnStatisticsTaskCLI(t *testing.T) {
	const dbName = "glue-cli-cst-db"
	const tblName = "glue-cli-cst-tbl"

	runCLI(t, awsCLI("glue", "create-database", "--database-input", `{"Name":"`+dbName+`"}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-database", "--name", dbName))
	})
	runCLI(t, awsCLI("glue", "create-table",
		"--database-name", dbName,
		"--table-input", `{"Name":"`+tblName+`"}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-table", "--database-name", dbName, "--name", tblName))
	})

	runCLI(t, awsCLI("glue", "create-column-statistics-task-settings",
		"--database-name", dbName,
		"--table-name", tblName,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
		"--column-name-list", "id", "name",
		"--schedule", "cron(0 0 * * ? *)",
	))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("glue", "delete-column-statistics-task-settings",
			"--database-name", dbName, "--table-name", tblName))
	})

	out := runCLI(t, awsCLI("glue", "get-column-statistics-task-settings",
		"--database-name", dbName, "--table-name", tblName))
	var getS struct {
		ColumnStatisticsTaskSettings struct {
			DatabaseName   string   `json:"DatabaseName"`
			ColumnNameList []string `json:"ColumnNameList"`
		} `json:"ColumnStatisticsTaskSettings"`
	}
	parseJSON(t, out, &getS)
	assert.Equal(t, dbName, getS.ColumnStatisticsTaskSettings.DatabaseName)
	assert.Equal(t, []string{"id", "name"}, getS.ColumnStatisticsTaskSettings.ColumnNameList)

	runCLI(t, awsCLI("glue", "update-column-statistics-task-settings",
		"--database-name", dbName, "--table-name", tblName,
		"--column-name-list", "id"))

	out = runCLI(t, awsCLI("glue", "start-column-statistics-task-run",
		"--database-name", dbName, "--table-name", tblName,
		"--role", "arn:aws:iam::123456789012:role/GlueRole",
		"--column-name-list", "id"))
	var startRun struct {
		ColumnStatisticsTaskRunId string `json:"ColumnStatisticsTaskRunId"`
	}
	parseJSON(t, out, &startRun)
	require.NotEmpty(t, startRun.ColumnStatisticsTaskRunId)
	runID := startRun.ColumnStatisticsTaskRunId

	out = runCLI(t, awsCLI("glue", "get-column-statistics-task-run",
		"--column-statistics-task-run-id", runID))
	var getR struct {
		ColumnStatisticsTaskRun struct {
			Status       string `json:"Status"`
			DatabaseName string `json:"DatabaseName"`
		} `json:"ColumnStatisticsTaskRun"`
	}
	parseJSON(t, out, &getR)
	assert.Equal(t, "SUCCEEDED", getR.ColumnStatisticsTaskRun.Status)
	assert.Equal(t, dbName, getR.ColumnStatisticsTaskRun.DatabaseName)

	out = runCLI(t, awsCLI("glue", "get-column-statistics-task-runs",
		"--database-name", dbName, "--table-name", tblName))
	var getRuns struct {
		ColumnStatisticsTaskRuns []struct {
			ColumnStatisticsTaskRunId string `json:"ColumnStatisticsTaskRunId"`
		} `json:"ColumnStatisticsTaskRuns"`
	}
	parseJSON(t, out, &getRuns)
	foundRun := false
	for _, run := range getRuns.ColumnStatisticsTaskRuns {
		if run.ColumnStatisticsTaskRunId == runID {
			foundRun = true
		}
	}
	assert.True(t, foundRun)

	out = runCLI(t, awsCLI("glue", "list-column-statistics-task-runs"))
	var listRuns struct {
		ColumnStatisticsTaskRunIds []string `json:"ColumnStatisticsTaskRunIds"`
	}
	parseJSON(t, out, &listRuns)
	assert.Contains(t, listRuns.ColumnStatisticsTaskRunIds, runID)

	// A terminal run cannot be stopped — the CLI surfaces the service error.
	runCLIExpectError(t, awsCLI("glue", "stop-column-statistics-task-run",
		"--database-name", dbName, "--table-name", tblName))
}
