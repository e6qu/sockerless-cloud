package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_MLTransformLifecycle_SDK exercises the full ML transform CRUD family:
// Create -> Get -> GetMLTransforms -> ListMLTransforms -> Update -> Delete.
func TestGlue_MLTransformLifecycle_SDK(t *testing.T) {
	c := glueClient()

	create, err := c.CreateMLTransform(ctx, &glue.CreateMLTransformInput{
		Name: aws.String("glue-sdk-mlt"),
		InputRecordTables: []gluetypes.GlueTable{
			{DatabaseName: aws.String("db"), TableName: aws.String("tbl")},
		},
		Parameters: &gluetypes.TransformParameters{
			TransformType: gluetypes.TransformTypeFindMatches,
		},
		Role:        aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		Description: aws.String("sdk ml transform"),
	})
	require.NoError(t, err)
	id := aws.ToString(create.TransformId)
	require.NotEmpty(t, id)
	t.Cleanup(func() {
		_, _ = c.DeleteMLTransform(ctx, &glue.DeleteMLTransformInput{TransformId: aws.String(id)})
	})

	get, err := c.GetMLTransform(ctx, &glue.GetMLTransformInput{TransformId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "glue-sdk-mlt", aws.ToString(get.Name))
	assert.Equal(t, gluetypes.TransformStatusTypeReady, get.Status)
	assert.Equal(t, "sdk ml transform", aws.ToString(get.Description))

	gets, err := c.GetMLTransforms(ctx, &glue.GetMLTransformsInput{})
	require.NoError(t, err)
	foundT := false
	for _, tr := range gets.Transforms {
		if aws.ToString(tr.TransformId) == id {
			foundT = true
		}
	}
	assert.True(t, foundT, "GetMLTransforms should return created transform")

	list, err := c.ListMLTransforms(ctx, &glue.ListMLTransformsInput{})
	require.NoError(t, err)
	assert.Contains(t, list.TransformIds, id)

	upd, err := c.UpdateMLTransform(ctx, &glue.UpdateMLTransformInput{
		TransformId: aws.String(id),
		Description: aws.String("updated"),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(upd.TransformId))

	get2, err := c.GetMLTransform(ctx, &glue.GetMLTransformInput{TransformId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, "updated", aws.ToString(get2.Description))

	del, err := c.DeleteMLTransform(ctx, &glue.DeleteMLTransformInput{TransformId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(del.TransformId))

	_, err = c.GetMLTransform(ctx, &glue.GetMLTransformInput{TransformId: aws.String(id)})
	assert.Error(t, err, "deleted transform should not be found")
}

// TestGlue_DataQualityLifecycle_SDK exercises the Data Quality ruleset family
// plus evaluation/recommendation runs and the results they settle into.
func TestGlue_DataQualityLifecycle_SDK(t *testing.T) {
	c := glueClient()

	const rulesetName = "glue-sdk-dqr"
	ruleset := "Rules = [ ColumnExists \"id\", IsComplete \"id\" ]"

	_, err := c.CreateDataQualityRuleset(ctx, &glue.CreateDataQualityRulesetInput{
		Name:        aws.String(rulesetName),
		Ruleset:     aws.String(ruleset),
		Description: aws.String("sdk dq ruleset"),
		TargetTable: &gluetypes.DataQualityTargetTable{
			DatabaseName: aws.String("db"),
			TableName:    aws.String("tbl"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDataQualityRuleset(ctx, &glue.DeleteDataQualityRulesetInput{Name: aws.String(rulesetName)})
	})

	get, err := c.GetDataQualityRuleset(ctx, &glue.GetDataQualityRulesetInput{Name: aws.String(rulesetName)})
	require.NoError(t, err)
	assert.Equal(t, rulesetName, aws.ToString(get.Name))
	assert.Equal(t, ruleset, aws.ToString(get.Ruleset))
	require.NotNil(t, get.TargetTable)
	assert.Equal(t, "tbl", aws.ToString(get.TargetTable.TableName))

	list, err := c.ListDataQualityRulesets(ctx, &glue.ListDataQualityRulesetsInput{})
	require.NoError(t, err)
	foundR := false
	for _, rs := range list.Rulesets {
		if aws.ToString(rs.Name) == rulesetName {
			foundR = true
			assert.NotNil(t, rs.RuleCount)
		}
	}
	assert.True(t, foundR)

	_, err = c.UpdateDataQualityRuleset(ctx, &glue.UpdateDataQualityRulesetInput{
		Name:        aws.String(rulesetName),
		Description: aws.String("updated dq"),
	})
	require.NoError(t, err)

	// Evaluation run settles SUCCEEDED synchronously and produces a result.
	start, err := c.StartDataQualityRulesetEvaluationRun(ctx, &glue.StartDataQualityRulesetEvaluationRunInput{
		DataSource: &gluetypes.DataSource{
			GlueTable: &gluetypes.GlueTable{
				DatabaseName: aws.String("db"),
				TableName:    aws.String("tbl"),
			},
		},
		Role:         aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		RulesetNames: []string{rulesetName},
	})
	require.NoError(t, err)
	runID := aws.ToString(start.RunId)
	require.NotEmpty(t, runID)

	evalRun, err := c.GetDataQualityRulesetEvaluationRun(ctx, &glue.GetDataQualityRulesetEvaluationRunInput{RunId: aws.String(runID)})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.TaskStatusTypeSucceeded, evalRun.Status)
	require.NotEmpty(t, evalRun.ResultIds)
	resultID := evalRun.ResultIds[0]

	listEval, err := c.ListDataQualityRulesetEvaluationRuns(ctx, &glue.ListDataQualityRulesetEvaluationRunsInput{})
	require.NoError(t, err)
	foundE := false
	for _, run := range listEval.Runs {
		if aws.ToString(run.RunId) == runID {
			foundE = true
		}
	}
	assert.True(t, foundE)

	_, err = c.CancelDataQualityRulesetEvaluationRun(ctx, &glue.CancelDataQualityRulesetEvaluationRunInput{RunId: aws.String(runID)})
	require.NoError(t, err)

	// The result row is empty/zero-but-shaped over no real data.
	res, err := c.GetDataQualityResult(ctx, &glue.GetDataQualityResultInput{ResultId: aws.String(resultID)})
	require.NoError(t, err)
	assert.Equal(t, resultID, aws.ToString(res.ResultId))
	assert.Equal(t, rulesetName, aws.ToString(res.RulesetName))
	assert.Empty(t, res.RuleResults)

	batch, err := c.BatchGetDataQualityResult(ctx, &glue.BatchGetDataQualityResultInput{
		ResultIds: []string{resultID, "missing-id"},
	})
	require.NoError(t, err)
	require.Len(t, batch.Results, 1)
	assert.Contains(t, batch.ResultsNotFound, "missing-id")

	listRes, err := c.ListDataQualityResults(ctx, &glue.ListDataQualityResultsInput{})
	require.NoError(t, err)
	foundRes := false
	for _, d := range listRes.Results {
		if aws.ToString(d.ResultId) == resultID {
			foundRes = true
		}
	}
	assert.True(t, foundRes)

	// Recommendation run settles SUCCEEDED and recommends a (shaped) ruleset.
	rec, err := c.StartDataQualityRuleRecommendationRun(ctx, &glue.StartDataQualityRuleRecommendationRunInput{
		DataSource: &gluetypes.DataSource{
			GlueTable: &gluetypes.GlueTable{
				DatabaseName: aws.String("db"),
				TableName:    aws.String("tbl"),
			},
		},
		Role: aws.String("arn:aws:iam::123456789012:role/GlueRole"),
	})
	require.NoError(t, err)
	recID := aws.ToString(rec.RunId)
	require.NotEmpty(t, recID)

	recRun, err := c.GetDataQualityRuleRecommendationRun(ctx, &glue.GetDataQualityRuleRecommendationRunInput{RunId: aws.String(recID)})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.TaskStatusTypeSucceeded, recRun.Status)
	assert.NotEmpty(t, aws.ToString(recRun.RecommendedRuleset))

	listRec, err := c.ListDataQualityRuleRecommendationRuns(ctx, &glue.ListDataQualityRuleRecommendationRunsInput{})
	require.NoError(t, err)
	foundRec := false
	for _, run := range listRec.Runs {
		if aws.ToString(run.RunId) == recID {
			foundRec = true
		}
	}
	assert.True(t, foundRec)

	_, err = c.CancelDataQualityRuleRecommendationRun(ctx, &glue.CancelDataQualityRuleRecommendationRunInput{RunId: aws.String(recID)})
	require.NoError(t, err)

	// Statistics / model endpoints return shaped-but-empty bodies.
	stats, err := c.ListDataQualityStatistics(ctx, &glue.ListDataQualityStatisticsInput{})
	require.NoError(t, err)
	assert.Empty(t, stats.Statistics)

	model, err := c.GetDataQualityModel(ctx, &glue.GetDataQualityModelInput{
		ProfileId: aws.String("profile-1"),
	})
	require.NoError(t, err)
	assert.Equal(t, gluetypes.DataQualityModelStatusSucceeded, model.Status)

	modelRes, err := c.GetDataQualityModelResult(ctx, &glue.GetDataQualityModelResultInput{
		StatisticId: aws.String("stat-1"),
		ProfileId:   aws.String("profile-1"),
	})
	require.NoError(t, err)
	assert.Empty(t, modelRes.Model)
}

// TestGlue_ColumnStatisticsTaskLifecycle_SDK exercises the column-statistics
// task settings + runs family, keyed by database+table.
func TestGlue_ColumnStatisticsTaskLifecycle_SDK(t *testing.T) {
	c := glueClient()

	const dbName = "glue-sdk-cst-db"
	const tblName = "glue-sdk-cst-tbl"

	_, err := c.CreateDatabase(ctx, &glue.CreateDatabaseInput{
		DatabaseInput: &gluetypes.DatabaseInput{Name: aws.String(dbName)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDatabase(ctx, &glue.DeleteDatabaseInput{Name: aws.String(dbName)})
	})
	_, err = c.CreateTable(ctx, &glue.CreateTableInput{
		DatabaseName: aws.String(dbName),
		TableInput:   &gluetypes.TableInput{Name: aws.String(tblName)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteTable(ctx, &glue.DeleteTableInput{DatabaseName: aws.String(dbName), Name: aws.String(tblName)})
	})

	_, err = c.CreateColumnStatisticsTaskSettings(ctx, &glue.CreateColumnStatisticsTaskSettingsInput{
		DatabaseName:   aws.String(dbName),
		TableName:      aws.String(tblName),
		Role:           aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		ColumnNameList: []string{"id", "name"},
		Schedule:       aws.String("cron(0 0 * * ? *)"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteColumnStatisticsTaskSettings(ctx, &glue.DeleteColumnStatisticsTaskSettingsInput{
			DatabaseName: aws.String(dbName), TableName: aws.String(tblName),
		})
	})

	getS, err := c.GetColumnStatisticsTaskSettings(ctx, &glue.GetColumnStatisticsTaskSettingsInput{
		DatabaseName: aws.String(dbName), TableName: aws.String(tblName),
	})
	require.NoError(t, err)
	require.NotNil(t, getS.ColumnStatisticsTaskSettings)
	assert.Equal(t, dbName, aws.ToString(getS.ColumnStatisticsTaskSettings.DatabaseName))
	assert.Equal(t, []string{"id", "name"}, getS.ColumnStatisticsTaskSettings.ColumnNameList)

	_, err = c.UpdateColumnStatisticsTaskSettings(ctx, &glue.UpdateColumnStatisticsTaskSettingsInput{
		DatabaseName:   aws.String(dbName),
		TableName:      aws.String(tblName),
		ColumnNameList: []string{"id"},
	})
	require.NoError(t, err)

	getS2, err := c.GetColumnStatisticsTaskSettings(ctx, &glue.GetColumnStatisticsTaskSettingsInput{
		DatabaseName: aws.String(dbName), TableName: aws.String(tblName),
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"id"}, getS2.ColumnStatisticsTaskSettings.ColumnNameList)

	// Start a run; it settles SUCCEEDED synchronously.
	start, err := c.StartColumnStatisticsTaskRun(ctx, &glue.StartColumnStatisticsTaskRunInput{
		DatabaseName:   aws.String(dbName),
		TableName:      aws.String(tblName),
		Role:           aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		ColumnNameList: []string{"id"},
	})
	require.NoError(t, err)
	runID := aws.ToString(start.ColumnStatisticsTaskRunId)
	require.NotEmpty(t, runID)

	getR, err := c.GetColumnStatisticsTaskRun(ctx, &glue.GetColumnStatisticsTaskRunInput{
		ColumnStatisticsTaskRunId: aws.String(runID),
	})
	require.NoError(t, err)
	require.NotNil(t, getR.ColumnStatisticsTaskRun)
	assert.Equal(t, gluetypes.ColumnStatisticsStateSucceeded, getR.ColumnStatisticsTaskRun.Status)
	assert.Equal(t, dbName, aws.ToString(getR.ColumnStatisticsTaskRun.DatabaseName))

	getRuns, err := c.GetColumnStatisticsTaskRuns(ctx, &glue.GetColumnStatisticsTaskRunsInput{
		DatabaseName: aws.String(dbName), TableName: aws.String(tblName),
	})
	require.NoError(t, err)
	foundRun := false
	for _, run := range getRuns.ColumnStatisticsTaskRuns {
		if aws.ToString(run.ColumnStatisticsTaskRunId) == runID {
			foundRun = true
		}
	}
	assert.True(t, foundRun)

	listRuns, err := c.ListColumnStatisticsTaskRuns(ctx, &glue.ListColumnStatisticsTaskRunsInput{})
	require.NoError(t, err)
	assert.Contains(t, listRuns.ColumnStatisticsTaskRunIds, runID)

	// The run already settled, so Stop reports it is not running.
	_, err = c.StopColumnStatisticsTaskRun(ctx, &glue.StopColumnStatisticsTaskRunInput{
		DatabaseName: aws.String(dbName), TableName: aws.String(tblName),
	})
	assert.Error(t, err, "a terminal run cannot be stopped")
}
