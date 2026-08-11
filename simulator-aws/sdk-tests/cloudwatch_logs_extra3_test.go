package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogs_IntegrationRoundTrip covers PutIntegration, GetIntegration,
// ListIntegrations, and DeleteIntegration for an OpenSearch integration.
func TestLogs_IntegrationRoundTrip(t *testing.T) {
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

// TestLogs_LookupTableRoundTrip covers CreateLookupTable, GetLookupTable,
// UpdateLookupTable, DescribeLookupTables, and DeleteLookupTable.
func TestLogs_LookupTableRoundTrip(t *testing.T) {
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

// TestLogs_ScheduledQueryRoundTrip covers CreateScheduledQuery, GetScheduledQuery,
// UpdateScheduledQuery, ListScheduledQueries, GetScheduledQueryHistory, and
// DeleteScheduledQuery.
func TestLogs_ScheduledQueryRoundTrip(t *testing.T) {
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

// TestLogs_TransformerRoundTrip covers PutTransformer, GetTransformer,
// TestTransformer, and DeleteTransformer.
func TestLogs_TransformerRoundTrip(t *testing.T) {
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

// TestLogs_ImportTaskRoundTrip covers CreateImportTask, DescribeImportTasks,
// DescribeImportTaskBatches, and CancelImportTask.
func TestLogs_ImportTaskRoundTrip(t *testing.T) {
	cw := cwLogsClient()
	create, err := cw.CreateImportTask(ctx, &cloudwatchlogs.CreateImportTaskInput{
		ImportSourceArn: aws.String("arn:aws:s3:::test-import-bucket"),
		ImportRoleArn:   aws.String("arn:aws:iam::123456789012:role/import-role"),
	})
	require.NoError(t, err)
	id := aws.ToString(create.ImportId)
	require.NotEmpty(t, id)
	require.NotEmpty(t, aws.ToString(create.ImportDestinationArn))

	desc, err := cw.DescribeImportTasks(ctx, &cloudwatchlogs.DescribeImportTasksInput{
		ImportId: aws.String(id),
	})
	require.NoError(t, err)
	require.Len(t, desc.Imports, 1)
	assert.Equal(t, cwlogtypes.ImportStatusCompleted, desc.Imports[0].ImportStatus)

	batches, err := cw.DescribeImportTaskBatches(ctx, &cloudwatchlogs.DescribeImportTaskBatchesInput{
		ImportId: aws.String(id),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(batches.ImportId))
	require.NotEmpty(t, batches.ImportBatches)

	cancel, err := cw.CancelImportTask(ctx, &cloudwatchlogs.CancelImportTaskInput{
		ImportId: aws.String(id),
	})
	require.NoError(t, err)
	assert.Equal(t, id, aws.ToString(cancel.ImportId))
}

// TestLogs_LogStructureReads covers GetLogGroupFields, GetLogRecord, and
// PutLogGroupDeletionProtection. With no stored events the fields/record are
// honestly empty.
func TestLogs_LogStructureReads(t *testing.T) {
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
}

// TestLogs_AnomalyOps covers UpdateLogAnomalyDetector, ListAnomalies, and
// UpdateAnomaly over a real log anomaly detector.
func TestLogs_AnomalyOps(t *testing.T) {
	cw := cwLogsClient()
	lg := "test-anomaly-lg"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(lg)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(lg)})

	createDet, err := cw.CreateLogAnomalyDetector(ctx, &cloudwatchlogs.CreateLogAnomalyDetectorInput{
		LogGroupArnList:     []string{"arn:aws:logs:us-east-1:123456789012:log-group:" + lg},
		DetectorName:        aws.String("test-anomaly-detector"),
		EvaluationFrequency: cwlogtypes.EvaluationFrequencyOneHour,
	})
	require.NoError(t, err)
	arn := aws.ToString(createDet.AnomalyDetectorArn)
	require.NotEmpty(t, arn)
	defer cw.DeleteLogAnomalyDetector(ctx, &cloudwatchlogs.DeleteLogAnomalyDetectorInput{
		AnomalyDetectorArn: aws.String(arn),
	})

	_, err = cw.UpdateLogAnomalyDetector(ctx, &cloudwatchlogs.UpdateLogAnomalyDetectorInput{
		AnomalyDetectorArn:  aws.String(arn),
		EvaluationFrequency: cwlogtypes.EvaluationFrequencyFifteenMin,
		Enabled:             aws.Bool(true),
	})
	require.NoError(t, err)

	list, err := cw.ListAnomalies(ctx, &cloudwatchlogs.ListAnomaliesInput{
		AnomalyDetectorArn: aws.String(arn),
	})
	require.NoError(t, err)
	assert.Empty(t, list.Anomalies, "fresh detector has no anomalies")

	// UpdateAnomaly on a detector with no anomalies still validates the detector
	// exists and returns successfully.
	_, err = cw.UpdateAnomaly(ctx, &cloudwatchlogs.UpdateAnomalyInput{
		AnomalyDetectorArn: aws.String(arn),
		PatternId:          aws.String("pattern-1"),
		SuppressionType:    cwlogtypes.SuppressionTypeInfinite,
	})
	require.NoError(t, err)
}

// TestLogs_ListLogGroupsOps covers ListLogGroups and ListAggregateLogGroupSummaries.
func TestLogs_ListLogGroupsOps(t *testing.T) {
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
