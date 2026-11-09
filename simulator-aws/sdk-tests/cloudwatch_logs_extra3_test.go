package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogs_IntegrationRoundTrip(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	name := "test-opensearch-integration"
	defer cw.DeleteIntegration(ctx, &cloudwatchlogs.DeleteIntegrationInput{
		IntegrationName: aws.String(name),
		Force:           true,
	})

	put, err := cw.PutIntegration(ctx, &cloudwatchlogs.PutIntegrationInput{
		IntegrationName: aws.String(name),
		IntegrationType: cwlogtypes.IntegrationTypeOpensearch,
		ResourceConfig: &cwlogtypes.ResourceConfigMemberOpenSearchResourceConfig{
			Value: cwlogtypes.OpenSearchResourceConfig{
				DataSourceRoleArn:         aws.String("arn:aws:iam::123456789012:role/cwl-opensearch"),
				DashboardViewerPrincipals: []string{"arn:aws:iam::123456789012:user/viewer"},
				RetentionDays:             aws.Int32(90),
			},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(put.IntegrationName))
	assert.Equal(t, cwlogtypes.IntegrationStatusActive, put.IntegrationStatus)

	get, err := cw.GetIntegration(ctx, &cloudwatchlogs.GetIntegrationInput{
		IntegrationName: aws.String(name),
	})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.IntegrationName))
	assert.Equal(t, cwlogtypes.IntegrationTypeOpensearch, get.IntegrationType)
	require.NotNil(t, get.IntegrationDetails)

	list, err := cw.ListIntegrations(ctx, &cloudwatchlogs.ListIntegrationsInput{
		IntegrationNamePrefix: aws.String("test-opensearch"),
	})
	require.NoError(t, err)
	found := false
	for _, s := range list.IntegrationSummaries {
		if aws.ToString(s.IntegrationName) == name {
			found = true
		}
	}
	assert.True(t, found, "integration should be listed")

	_, err = cw.DeleteIntegration(ctx, &cloudwatchlogs.DeleteIntegrationInput{
		IntegrationName: aws.String(name),
	})
	require.NoError(t, err)
}

func TestLogs_LookupTableRoundTrip(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	name := "test_lookup_table"
	body := "key,value\nfoo,bar\n"

	create, err := cw.CreateLookupTable(ctx, &cloudwatchlogs.CreateLookupTableInput{
		LookupTableName: aws.String(name),
		TableBody:       aws.String(body),
		Description:     aws.String("test lookup table"),
	})
	require.NoError(t, err)
	arn := aws.ToString(create.LookupTableArn)
	require.NotEmpty(t, arn)
	require.NotNil(t, create.CreatedAt)
	defer cw.DeleteLookupTable(ctx, &cloudwatchlogs.DeleteLookupTableInput{LookupTableArn: aws.String(arn)})

	get, err := cw.GetLookupTable(ctx, &cloudwatchlogs.GetLookupTableInput{LookupTableArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.LookupTableName))
	assert.Equal(t, body, aws.ToString(get.TableBody))
	assert.Equal(t, int64(len(body)), aws.ToInt64(get.SizeBytes))

	newBody := "key,value\nbaz,qux\nfoo,bar\n"
	upd, err := cw.UpdateLookupTable(ctx, &cloudwatchlogs.UpdateLookupTableInput{
		LookupTableArn: aws.String(arn),
		TableBody:      aws.String(newBody),
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(upd.LookupTableArn))
	require.NotNil(t, upd.LastUpdatedTime)

	desc, err := cw.DescribeLookupTables(ctx, &cloudwatchlogs.DescribeLookupTablesInput{
		LookupTableNamePrefix: aws.String("test_lookup"),
	})
	require.NoError(t, err)
	found := false
	for _, lt := range desc.LookupTables {
		if aws.ToString(lt.LookupTableArn) == arn {
			found = true
		}
	}
	assert.True(t, found, "lookup table should be described")

	_, err = cw.DeleteLookupTable(ctx, &cloudwatchlogs.DeleteLookupTableInput{LookupTableArn: aws.String(arn)})
	require.NoError(t, err)
}

func TestLogs_ScheduledQueryRoundTrip(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	name := "test-scheduled-query"
	dest := &cwlogtypes.DestinationConfiguration{
		S3Configuration: &cwlogtypes.S3Configuration{
			DestinationIdentifier: aws.String("s3://test-sq-bucket"),
			RoleArn:               aws.String("arn:aws:iam::123456789012:role/sq-role"),
		},
	}
	create, err := cw.CreateScheduledQuery(ctx, &cloudwatchlogs.CreateScheduledQueryInput{
		Name:                     aws.String(name),
		QueryLanguage:            cwlogtypes.QueryLanguageCwli,
		QueryString:              aws.String("fields @timestamp, @message | sort @timestamp desc"),
		ScheduleExpression:       aws.String("rate(1 hour)"),
		ExecutionRoleArn:         aws.String("arn:aws:iam::123456789012:role/sq-exec"),
		DestinationConfiguration: dest,
		State:                    cwlogtypes.ScheduledQueryStateEnabled,
	})
	require.NoError(t, err)
	arn := aws.ToString(create.ScheduledQueryArn)
	require.NotEmpty(t, arn)
	assert.Equal(t, cwlogtypes.ScheduledQueryStateEnabled, create.State)
	defer cw.DeleteScheduledQuery(ctx, &cloudwatchlogs.DeleteScheduledQueryInput{Identifier: aws.String(arn)})

	get, err := cw.GetScheduledQuery(ctx, &cloudwatchlogs.GetScheduledQueryInput{Identifier: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, name, aws.ToString(get.Name))
	assert.Equal(t, "rate(1 hour)", aws.ToString(get.ScheduleExpression))

	_, err = cw.UpdateScheduledQuery(ctx, &cloudwatchlogs.UpdateScheduledQueryInput{
		Identifier:         aws.String(arn),
		QueryLanguage:      cwlogtypes.QueryLanguageCwli,
		QueryString:        aws.String("fields @timestamp, @message | sort @timestamp desc"),
		ExecutionRoleArn:   aws.String("arn:aws:iam::123456789012:role/sq-exec"),
		ScheduleExpression: aws.String("rate(2 hours)"),
	})
	require.NoError(t, err)
	get2, err := cw.GetScheduledQuery(ctx, &cloudwatchlogs.GetScheduledQueryInput{Identifier: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, "rate(2 hours)", aws.ToString(get2.ScheduleExpression))

	list, err := cw.ListScheduledQueries(ctx, &cloudwatchlogs.ListScheduledQueriesInput{})
	require.NoError(t, err)
	found := false
	for _, sq := range list.ScheduledQueries {
		if aws.ToString(sq.ScheduledQueryArn) == arn {
			found = true
		}
	}
	assert.True(t, found, "scheduled query should be listed")

	hist, err := cw.GetScheduledQueryHistory(ctx, &cloudwatchlogs.GetScheduledQueryHistoryInput{
		Identifier: aws.String(arn),
		StartTime:  aws.Int64(0),
		EndTime:    aws.Int64(time.Now().UnixMilli()),
	})
	require.NoError(t, err)
	assert.Equal(t, arn, aws.ToString(hist.ScheduledQueryArn))
	assert.Empty(t, hist.TriggerHistory, "fresh scheduled query has no trigger history")

	_, err = cw.DeleteScheduledQuery(ctx, &cloudwatchlogs.DeleteScheduledQueryInput{Identifier: aws.String(arn)})
	require.NoError(t, err)
}

func TestLogs_TransformerRoundTrip(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	lg := "test-transformer-lg"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(lg)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(lg)})
	defer cw.DeleteTransformer(ctx, &cloudwatchlogs.DeleteTransformerInput{LogGroupIdentifier: aws.String(lg)})

	processors := []cwlogtypes.Processor{
		{ParseJSON: &cwlogtypes.ParseJSON{}},
	}
	_, err = cw.PutTransformer(ctx, &cloudwatchlogs.PutTransformerInput{
		LogGroupIdentifier: aws.String(lg),
		TransformerConfig:  processors,
	})
	require.NoError(t, err)

	get, err := cw.GetTransformer(ctx, &cloudwatchlogs.GetTransformerInput{LogGroupIdentifier: aws.String(lg)})
	require.NoError(t, err)
	assert.Equal(t, lg, aws.ToString(get.LogGroupIdentifier))
	require.Len(t, get.TransformerConfig, 1)
	require.NotNil(t, get.TransformerConfig[0].ParseJSON)

	test, err := cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: processors,
		LogEventMessages:  []string{`{"level":"INFO","msg":"hello"}`},
	})
	require.NoError(t, err)
	require.Len(t, test.TransformedLogs, 1)
	assert.Equal(t, int64(1), test.TransformedLogs[0].EventNumber)

	_, err = cw.DeleteTransformer(ctx, &cloudwatchlogs.DeleteTransformerInput{LogGroupIdentifier: aws.String(lg)})
	require.NoError(t, err)
}

func TestLogs_LogStructureReads(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	lg := "test-structure-lg"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(lg)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(lg)})

	fields, err := cw.GetLogGroupFields(ctx, &cloudwatchlogs.GetLogGroupFieldsInput{
		LogGroupName: aws.String(lg),
	})
	require.NoError(t, err)
	assert.Empty(t, fields.LogGroupFields, "log group with no events has no fields")

	rec, err := cw.GetLogRecord(ctx, &cloudwatchlogs.GetLogRecordInput{
		LogRecordPointer: aws.String("missing|stream|0"),
	})
	require.NoError(t, err)
	assert.Empty(t, rec.LogRecord, "unknown record pointer resolves to an empty record")

	_, err = cw.PutLogGroupDeletionProtection(ctx, &cloudwatchlogs.PutLogGroupDeletionProtectionInput{
		LogGroupIdentifier:        aws.String(lg),
		DeletionProtectionEnabled: aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(lg)})
	var refused smithy.APIError
	require.True(t, errors.As(err, &refused), "deletion protection refuses DeleteLogGroup: %v", err)
	assert.Equal(t, "ValidationException", refused.ErrorCode())
	_, err = cw.PutLogGroupDeletionProtection(ctx, &cloudwatchlogs.PutLogGroupDeletionProtectionInput{
		LogGroupIdentifier:        aws.String(lg),
		DeletionProtectionEnabled: aws.Bool(false),
	})
	require.NoError(t, err)
}

func TestLogs_ListLogGroupsOps(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	lg := "test-listlg-group"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(lg)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(lg)})

	list, err := cw.ListLogGroups(ctx, &cloudwatchlogs.ListLogGroupsInput{
		LogGroupNamePattern: aws.String("test-listlg"),
	})
	require.NoError(t, err)
	found := false
	for _, g := range list.LogGroups {
		if aws.ToString(g.LogGroupName) == lg {
			found = true
			assert.Equal(t, cwlogtypes.LogGroupClassStandard, g.LogGroupClass)
		}
	}
	assert.True(t, found, "log group should appear in ListLogGroups")

	agg, err := cw.ListAggregateLogGroupSummaries(ctx, &cloudwatchlogs.ListAggregateLogGroupSummariesInput{
		GroupBy: cwlogtypes.ListAggregateLogGroupSummariesGroupByDataSourceNameAndType,
	})
	require.NoError(t, err)
	assert.Empty(t, agg.AggregateLogGroupSummaries, "no data-source aggregates without integrations")
}
