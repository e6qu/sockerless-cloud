package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/glue"
	gluetypes "github.com/aws/aws-sdk-go-v2/service/glue/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGlue_DataQualityResultsV2_SDK publishes a result the way an external
// data platform does, reads it back through the V2 reads, and finds an
// evaluation run's result through the same reads.
func TestGlue_DataQualityResultsV2_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	evaluationContext := uniqueName("dq-v2-context")
	token := uniqueName("dq-v2-token")

	publish := &glue.PublishDataQualityResultV2Input{
		Dimensions:        map[string]string{"platform": "emr", "cluster": "j-ABC"},
		Tags:              map[string]string{"team": "data"},
		Score:             aws.Float64(0.5),
		EvaluationContext: aws.String(evaluationContext),
		Descriptors:       map[string]string{"table": "orders"},
		RulesetName:       aws.String("orders-checks"),
		ClientToken:       aws.String(token),
		RuleResults: []gluetypes.DataQualityRuleResult{
			{Name: aws.String("Rule_1"), Result: gluetypes.DataQualityRuleResultStatusPass},
			{Name: aws.String("Rule_2"), Result: gluetypes.DataQualityRuleResultStatusFail},
		},
	}
	published, err := c.PublishDataQualityResultV2(ctx, publish)
	require.NoError(t, err)
	resultID := aws.ToString(published.ResultId)
	require.NotEmpty(t, resultID)

	again, err := c.PublishDataQualityResultV2(ctx, publish)
	require.NoError(t, err, "a retry with the same ClientToken and body is accepted")
	assert.Equal(t, resultID, aws.ToString(again.ResultId), "a retry names the result the first attempt created")

	changed := *publish
	changed.Score = aws.Float64(0.9)
	_, err = c.PublishDataQualityResultV2(ctx, &changed)
	var mismatch *gluetypes.IdempotentParameterMismatchException
	require.True(t, errors.As(err, &mismatch), "reusing a ClientToken with another body is refused, got %v", err)

	invalid := *publish
	invalid.ClientToken = nil
	invalid.Score = aws.Float64(1.5)
	_, err = c.PublishDataQualityResultV2(ctx, &invalid)
	var invalidInput *gluetypes.InvalidInputException
	require.True(t, errors.As(err, &invalidInput), "a score above 1 is refused, got %v", err)

	got, err := c.GetDataQualityResultV2(ctx, &glue.GetDataQualityResultV2Input{ResultId: aws.String(resultID)})
	require.NoError(t, err)
	assert.Equal(t, 0.5, aws.ToFloat64(got.Score))
	assert.Equal(t, evaluationContext, aws.ToString(got.EvaluationContext))
	assert.Equal(t, "orders-checks", aws.ToString(got.RulesetName))
	assert.Equal(t, map[string]string{"platform": "emr", "cluster": "j-ABC"}, got.Dimensions)
	assert.Equal(t, map[string]string{"team": "data"}, got.Tags)
	assert.Equal(t, map[string]string{"table": "orders"}, got.Descriptors)
	require.Len(t, got.RuleResults, 2)
	assert.Equal(t, gluetypes.DataQualityRuleResultStatusFail, got.RuleResults[1].Result)
	require.NotNil(t, got.ResultPublishedOn)

	_, err = c.GetDataQualityResultV2(ctx, &glue.GetDataQualityResultV2Input{ResultId: aws.String("dqresult-missing")})
	var notFound *gluetypes.EntityNotFoundException
	require.True(t, errors.As(err, &notFound), "an unknown result is not found, got %v", err)

	batch, err := c.BatchGetDataQualityResultV2(ctx, &glue.BatchGetDataQualityResultV2Input{
		ResultIds: []string{resultID, "dqresult-missing"},
	})
	require.NoError(t, err)
	require.Len(t, batch.Results, 1)
	assert.Equal(t, resultID, aws.ToString(batch.Results[0].ResultId))
	assert.Equal(t, []string{"dqresult-missing"}, batch.ResultsNotFound)

	listed, err := c.ListDataQualityResultsV2(ctx, &glue.ListDataQualityResultsV2Input{
		Filter: &gluetypes.DataQualityResultsV2FilterCriteria{
			EvaluationContext: aws.String(evaluationContext),
			Dimensions:        map[string]string{"platform": "emr"},
		},
	})
	require.NoError(t, err)
	require.Len(t, listed.Results, 1)
	assert.Equal(t, resultID, aws.ToString(listed.Results[0].ResultId))

	none, err := c.ListDataQualityResultsV2(ctx, &glue.ListDataQualityResultsV2Input{
		Filter: &gluetypes.DataQualityResultsV2FilterCriteria{
			EvaluationContext: aws.String(evaluationContext),
			Dimensions:        map[string]string{"platform": "databricks"},
		},
	})
	require.NoError(t, err)
	assert.Empty(t, none.Results, "a dimension the result does not carry filters it out")
}

// TestGlue_DataQualityResultsV2_EvaluationRun_SDK finds the result a ruleset
// evaluation run wrote through the V2 reads, filtered by its Glue table.
func TestGlue_DataQualityResultsV2_EvaluationRun_SDK(t *testing.T) {
	t.Parallel()
	c := glueClient()
	rulesetName := uniqueName("dq-v2-ruleset")
	database, table := uniqueName("dq-v2-db"), uniqueName("dq-v2-tbl")

	_, err := c.CreateDataQualityRuleset(ctx, &glue.CreateDataQualityRulesetInput{
		Name:    aws.String(rulesetName),
		Ruleset: aws.String(`Rules = [ RowCount > 0 ]`),
		TargetTable: &gluetypes.DataQualityTargetTable{
			DatabaseName: aws.String(database),
			TableName:    aws.String(table),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = c.DeleteDataQualityRuleset(ctx, &glue.DeleteDataQualityRulesetInput{Name: aws.String(rulesetName)})
	})

	start, err := c.StartDataQualityRulesetEvaluationRun(ctx, &glue.StartDataQualityRulesetEvaluationRunInput{
		DataSource: &gluetypes.DataSource{
			GlueTable: &gluetypes.GlueTable{DatabaseName: aws.String(database), TableName: aws.String(table)},
		},
		Role:         aws.String("arn:aws:iam::123456789012:role/GlueRole"),
		RulesetNames: []string{rulesetName},
	})
	require.NoError(t, err)
	run, err := c.GetDataQualityRulesetEvaluationRun(ctx, &glue.GetDataQualityRulesetEvaluationRunInput{RunId: start.RunId})
	require.NoError(t, err)
	require.Len(t, run.ResultIds, 1)

	listed, err := c.ListDataQualityResultsV2(ctx, &glue.ListDataQualityResultsV2Input{
		Filter: &gluetypes.DataQualityResultsV2FilterCriteria{
			DataSource: &gluetypes.DataSource{
				GlueTable: &gluetypes.GlueTable{DatabaseName: aws.String(database), TableName: aws.String(table)},
			},
		},
	})
	require.NoError(t, err)
	require.Len(t, listed.Results, 1)
	assert.Equal(t, run.ResultIds[0], aws.ToString(listed.Results[0].ResultId))

	got, err := c.GetDataQualityResultV2(ctx, &glue.GetDataQualityResultV2Input{ResultId: aws.String(run.ResultIds[0])})
	require.NoError(t, err)
	assert.Equal(t, rulesetName, aws.ToString(got.RulesetName))
	require.NotNil(t, got.DataSource)
	require.NotNil(t, got.DataSource.GlueTable)
	assert.Equal(t, table, aws.ToString(got.DataSource.GlueTable.TableName))
}
