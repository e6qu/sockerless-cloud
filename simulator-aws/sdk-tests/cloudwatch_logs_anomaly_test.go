package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func anomalyLogGroup(t *testing.T, group, stream string) string {
	t.Helper()
	cw := cwLogsClient()
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
	require.NoError(t, err)
	return cwLogGroupArnForTest(t, group)
}

func putAnomalyLines(t *testing.T, group, stream string, at time.Time, lines ...string) {
	t.Helper()
	events := make([]cwlogtypes.InputLogEvent, 0, len(lines))
	for i, l := range lines {
		events = append(events, cwlogtypes.InputLogEvent{Message: aws.String(l), Timestamp: aws.Int64(at.Add(time.Duration(i) * time.Second).UnixMilli())})
	}
	_, err := cwLogsClient().PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: events})
	require.NoError(t, err)
}

func detectorStatus(t *testing.T, arn string) cwlogtypes.AnomalyDetectorStatus {
	t.Helper()
	got, err := cwLogsClient().GetLogAnomalyDetector(ctx, &cloudwatchlogs.GetLogAnomalyDetectorInput{AnomalyDetectorArn: aws.String(arn)})
	require.NoError(t, err)
	return got.AnomalyDetectorStatus
}

// TestLogs_AnomalyDetectorFindsAndSuppressesAnomalies trains a detector on
// INFO lines, sends an ERROR line it has never seen, reads the anomaly back,
// and suppresses it and its pattern.
func TestLogs_AnomalyDetectorFindsAndSuppressesAnomalies(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := uniquename.New("/sdk/anomaly")
	groupArn := anomalyLogGroup(t, group, "app")
	var baseline []string
	for i := range 20 {
		baseline = append(baseline, fmt.Sprintf("2024-05-01 10:00:%02d [INFO] Served request in %d ms", i, 100+i))
	}
	putAnomalyLines(t, group, "app", time.Now().Add(-time.Hour), baseline...)

	created, err := cw.CreateLogAnomalyDetector(ctx, &cloudwatchlogs.CreateLogAnomalyDetectorInput{
		DetectorName:        aws.String("sdk-anomalies"),
		LogGroupArnList:     []string{groupArn},
		EvaluationFrequency: cwlogtypes.EvaluationFrequencyOneMin,
	})
	require.NoError(t, err)
	arn := aws.ToString(created.AnomalyDetectorArn)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogAnomalyDetector(ctx, &cloudwatchlogs.DeleteLogAnomalyDetectorInput{AnomalyDetectorArn: aws.String(arn)})
	})
	got, err := cw.GetLogAnomalyDetector(ctx, &cloudwatchlogs.GetLogAnomalyDetectorInput{AnomalyDetectorArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, int64(21), aws.ToInt64(got.AnomalyVisibilityTime), "the visibility time defaults to 21 days")
	require.Eventually(t, func() bool { return detectorStatus(t, arn) == cwlogtypes.AnomalyDetectorStatusAnalyzing },
		30*time.Second, 250*time.Millisecond, "the detector trains, then analyzes")

	putAnomalyLines(t, group, "app", time.Now(),
		"2024-05-01 11:00:00 [INFO] Served request in 120 ms",
		"2024-05-01 11:00:01 [ERROR] Connection refused by database replica",
		"2024-05-01 11:00:02 [ERROR] Connection refused by database replica")

	// The detector runs once a minute, its evaluationFrequency.
	var anomaly cwlogtypes.Anomaly
	require.Eventually(t, func() bool {
		out, err := cw.ListAnomalies(ctx, &cloudwatchlogs.ListAnomaliesInput{AnomalyDetectorArn: aws.String(arn)})
		require.NoError(t, err)
		if len(out.Anomalies) != 1 {
			return false
		}
		anomaly = out.Anomalies[0]
		return true
	}, 3*time.Minute, time.Second)
	assert.Equal(t, "[ERROR] Connection refused by database replica", aws.ToString(anomaly.PatternString)[len("<*> <*> "):])
	assert.Equal(t, "HIGH", aws.ToString(anomaly.Priority))
	assert.True(t, aws.ToBool(anomaly.Active))
	assert.Equal(t, cwlogtypes.StateActive, anomaly.State)
	assert.Equal(t, []string{groupArn}, anomaly.LogGroupArnList)
	require.NotEmpty(t, anomaly.LogSamples)
	assert.Contains(t, aws.ToString(anomaly.LogSamples[0].Message), "Connection refused")
	var total int64
	for _, n := range anomaly.Histogram {
		total += n
	}
	assert.Equal(t, int64(2), total)
	require.Len(t, anomaly.PatternTokens, 8)
	assert.True(t, aws.ToBool(anomaly.PatternTokens[0].IsDynamic))
	assert.Equal(t, "Date-1", aws.ToString(anomaly.PatternTokens[0].InferredTokenName))
	assert.Equal(t, "Time-2", aws.ToString(anomaly.PatternTokens[1].InferredTokenName))

	list := func(state cwlogtypes.SuppressionState) []cwlogtypes.Anomaly {
		out, err := cw.ListAnomalies(ctx, &cloudwatchlogs.ListAnomaliesInput{AnomalyDetectorArn: aws.String(arn), SuppressionState: state})
		require.NoError(t, err)
		return out.Anomalies
	}
	_, err = cw.UpdateAnomaly(ctx, &cloudwatchlogs.UpdateAnomalyInput{
		AnomalyDetectorArn: aws.String(arn),
		AnomalyId:          anomaly.AnomalyId,
		SuppressionType:    cwlogtypes.SuppressionTypeLimited,
		SuppressionPeriod:  &cwlogtypes.SuppressionPeriod{Value: 2, SuppressionUnit: cwlogtypes.SuppressionUnitHours},
	})
	require.NoError(t, err)
	suppressed := list(cwlogtypes.SuppressionStateSuppressed)
	require.Len(t, suppressed, 1)
	assert.Equal(t, cwlogtypes.StateSuppressed, suppressed[0].State)
	assert.False(t, aws.ToBool(suppressed[0].IsPatternLevelSuppression))
	assert.InDelta(t, time.Now().Add(2*time.Hour).UnixMilli(), suppressed[0].SuppressedUntil, float64(time.Minute.Milliseconds()))
	assert.Empty(t, list(cwlogtypes.SuppressionStateUnsuppressed))

	_, err = cw.UpdateAnomaly(ctx, &cloudwatchlogs.UpdateAnomalyInput{AnomalyDetectorArn: aws.String(arn), AnomalyId: anomaly.AnomalyId})
	require.NoError(t, err)
	assert.Len(t, list(cwlogtypes.SuppressionStateUnsuppressed), 1, "omitting the suppression ends it")

	_, err = cw.UpdateAnomaly(ctx, &cloudwatchlogs.UpdateAnomalyInput{
		AnomalyDetectorArn: aws.String(arn), PatternId: anomaly.PatternId, SuppressionType: cwlogtypes.SuppressionTypeInfinite,
	})
	require.NoError(t, err)
	suppressed = list(cwlogtypes.SuppressionStateSuppressed)
	require.Len(t, suppressed, 1)
	assert.True(t, aws.ToBool(suppressed[0].IsPatternLevelSuppression))
	assert.Zero(t, suppressed[0].SuppressedUntil, "an INFINITE suppression has no end")

	_, err = cw.UpdateAnomaly(ctx, &cloudwatchlogs.UpdateAnomalyInput{
		AnomalyDetectorArn: aws.String(arn), AnomalyId: anomaly.AnomalyId, PatternId: anomaly.PatternId,
	})
	requireAWSErrorCode(t, err, "InvalidParameterException")

	_, err = cw.UpdateLogAnomalyDetector(ctx, &cloudwatchlogs.UpdateLogAnomalyDetectorInput{AnomalyDetectorArn: aws.String(arn), Enabled: aws.Bool(false)})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.AnomalyDetectorStatusPaused, detectorStatus(t, arn))
	_, err = cw.UpdateLogAnomalyDetector(ctx, &cloudwatchlogs.UpdateLogAnomalyDetectorInput{AnomalyDetectorArn: aws.String(arn), Enabled: aws.Bool(true)})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.AnomalyDetectorStatusAnalyzing, detectorStatus(t, arn), "restarting resumes analysis")
}

func TestLogs_AnomalyDetectorRefusesUnknownGroup(t *testing.T) {
	t.Parallel()
	_, err := cwLogsClient().CreateLogAnomalyDetector(ctx, &cloudwatchlogs.CreateLogAnomalyDetectorInput{
		LogGroupArnList: []string{"arn:aws:logs:us-east-1:123456789012:log-group:" + uniquename.New("/sdk/missing")},
	})
	requireAWSErrorCode(t, err, "ResourceNotFoundException")
}

// TestLogs_ScheduledQueryRunsAndRecordsHistory schedules a query every
// minute, waits for its first run in the history, and reads what it
// delivered to Amazon S3 and to a lookup table.
func TestLogs_ScheduledQueryRunsAndRecordsHistory(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := uniquename.New("/sdk/scheduled")
	anomalyLogGroup(t, group, "app")
	putAnomalyLines(t, group, "app", time.Now().Add(-time.Minute), `{"level":"ERROR","code":"E1"}`, `{"level":"INFO","code":"I1"}`, `{"level":"ERROR","code":"E2"}`)

	bucket := strings.ToLower(uniquename.New("sdk-sq-results"))
	s3c := s3Client()
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	execRole := createServiceRole(t, uniquename.New("sq-exec"), "logs.amazonaws.com", "logs:StartQuery", "logs:GetQueryResults", "logs:StopQuery", "logs:DescribeLogGroups")
	s3Role := createServiceRole(t, uniquename.New("sq-s3"), "logs.amazonaws.com", "s3:PutObject")
	tableRole := createServiceRole(t, uniquename.New("sq-table"), "logs.amazonaws.com", "logs:CreateLookupTable", "logs:UpdateLookupTable", "logs:GetQueryResults")
	table := strings.ReplaceAll(uniquename.New("sq_errors"), "-", "_")

	created, err := cw.CreateScheduledQuery(ctx, &cloudwatchlogs.CreateScheduledQueryInput{
		Name:                aws.String(uniquename.New("sdk-every-minute")),
		QueryLanguage:       cwlogtypes.QueryLanguageCwli,
		QueryString:         aws.String("filter level = 'ERROR' | fields code | sort code asc"),
		LogGroupIdentifiers: []string{group},
		ScheduleExpression:  aws.String("cron(* * * * ? *)"),
		StartTimeOffset:     aws.Int64(3600),
		ExecutionRoleArn:    aws.String(execRole),
		DestinationConfiguration: &cwlogtypes.DestinationConfiguration{
			S3Configuration:          &cwlogtypes.S3Configuration{DestinationIdentifier: aws.String("s3://" + bucket + "/results/"), RoleArn: aws.String(s3Role)},
			LookupTableConfiguration: &cwlogtypes.LookupTableConfiguration{TableName: aws.String(table), RoleArn: aws.String(tableRole)},
		},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.ScheduledQueryArn)
	t.Cleanup(func() {
		_, _ = cw.DeleteScheduledQuery(ctx, &cloudwatchlogs.DeleteScheduledQueryInput{Identifier: aws.String(arn)})
	})

	var record cwlogtypes.TriggerHistoryRecord
	require.Eventually(t, func() bool {
		hist, err := cw.GetScheduledQueryHistory(ctx, &cloudwatchlogs.GetScheduledQueryHistoryInput{
			Identifier: aws.String(arn), StartTime: aws.Int64(0), EndTime: aws.Int64(time.Now().Add(time.Hour).UnixMilli()),
		})
		require.NoError(t, err)
		if len(hist.TriggerHistory) == 0 {
			return false
		}
		record = hist.TriggerHistory[0]
		return true
	}, 2*time.Minute, time.Second, "the query runs at the next minute")
	require.Equal(t, cwlogtypes.ExecutionStatusComplete, record.ExecutionStatus, aws.ToString(record.ErrorMessage))
	require.Len(t, record.Destinations, 2)
	for _, d := range record.Destinations {
		assert.Equal(t, cwlogtypes.ActionStatusComplete, d.Status, aws.ToString(d.ErrorMessage))
	}

	results, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: record.QueryId})
	require.NoError(t, err)
	require.Len(t, results.Results, 2)

	obj, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("results/" + aws.ToString(record.QueryId) + ".json")})
	require.NoError(t, err)
	body, err := io.ReadAll(obj.Body)
	require.NoError(t, err)
	var rows []map[string]string
	require.NoError(t, json.Unmarshal(body, &rows))
	assert.Equal(t, []map[string]string{{"code": "E1"}, {"code": "E2"}}, rows)

	lt, err := cw.GetLookupTable(ctx, &cloudwatchlogs.GetLookupTableInput{LookupTableArn: aws.String("arn:aws:logs:us-east-1:123456789012:lookup-table:" + table)})
	require.NoError(t, err)
	assert.Equal(t, "code\nE1\nE2\n", aws.ToString(lt.TableBody))

	got, err := cw.GetScheduledQuery(ctx, &cloudwatchlogs.GetScheduledQueryInput{Identifier: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.ExecutionStatusComplete, got.LastExecutionStatus)
	assert.Equal(t, aws.ToInt64(record.TriggeredTimestamp), aws.ToInt64(got.LastTriggeredTime))
}

// TestLogs_ImportTaskReadsCloudTrailLake imports the events an event data
// store holds into a managed log group.
func TestLogs_ImportTaskReadsCloudTrailLake(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	ct := cloudTrailClient()
	created, err := ct.CreateEventDataStore(ctx, &cloudtrail.CreateEventDataStoreInput{
		Name: aws.String(uniquename.New("import-eds")), RetentionPeriod: aws.Int32(7), TerminationProtectionEnabled: aws.Bool(false),
	})
	require.NoError(t, err)
	eds := aws.ToString(created.EventDataStoreArn)
	t.Cleanup(func() {
		_, _ = ct.DeleteEventDataStore(ctx, &cloudtrail.DeleteEventDataStoreInput{EventDataStore: aws.String(eds)})
	})
	ec2c := ec2.NewFromConfig(sdkConfig(), func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	_, err = ec2c.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{})
	require.NoError(t, err)

	_, err = cw.CreateImportTask(ctx, &cloudwatchlogs.CreateImportTaskInput{
		ImportSourceArn: aws.String(eds),
		ImportRoleArn:   aws.String(createServiceRole(t, uniquename.New("import-untrusted"), "logs.amazonaws.com", "cloudtrail:GetEventDataStoreData")),
	})
	requireAWSErrorCode(t, err, "AccessDeniedException")

	role := createServiceRole(t, uniquename.New("import-role"), "cloudtrail.amazonaws.com", "cloudtrail:GetEventDataStoreData", "logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutResourcePolicy")
	task, err := cw.CreateImportTask(ctx, &cloudwatchlogs.CreateImportTaskInput{ImportSourceArn: aws.String(eds), ImportRoleArn: aws.String(role)})
	require.NoError(t, err)
	id := aws.ToString(task.ImportId)
	edsID := eds[strings.LastIndex(eds, "/")+1:]
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:aws/cloudtrail/"+edsID, aws.ToString(task.ImportDestinationArn))

	var imp cwlogtypes.Import
	require.Eventually(t, func() bool {
		out, err := cw.DescribeImportTasks(ctx, &cloudwatchlogs.DescribeImportTasksInput{ImportId: aws.String(id)})
		require.NoError(t, err)
		require.Len(t, out.Imports, 1)
		imp = out.Imports[0]
		return imp.ImportStatus != cwlogtypes.ImportStatusInProgress
	}, 30*time.Second, 250*time.Millisecond)
	require.Equal(t, cwlogtypes.ImportStatusCompleted, imp.ImportStatus, aws.ToString(imp.ErrorMessage))
	require.NotNil(t, imp.ImportStatistics)
	assert.Positive(t, aws.ToInt64(imp.ImportStatistics.BytesImported))

	batches, err := cw.DescribeImportTaskBatches(ctx, &cloudwatchlogs.DescribeImportTaskBatchesInput{ImportId: aws.String(id)})
	require.NoError(t, err)
	require.NotEmpty(t, batches.ImportBatches)
	assert.Equal(t, cwlogtypes.ImportStatusCompleted, batches.ImportBatches[0].Status)

	events, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{
		LogGroupName: aws.String("aws/cloudtrail/" + edsID), FilterPattern: aws.String(`{ $.eventName = "DescribeVpcs" }`),
	})
	require.NoError(t, err)
	require.NotEmpty(t, events.Events, "the import wrote the store's DescribeVpcs records")
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String("aws/cloudtrail/" + edsID)})
	})

	cancelled, err := cw.CancelImportTask(ctx, &cloudwatchlogs.CancelImportTaskInput{ImportId: aws.String(id)})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.ImportStatusCompleted, cancelled.ImportStatus, "a finished import does not cancel")

	_, err = cw.CreateImportTask(ctx, &cloudwatchlogs.CreateImportTaskInput{
		ImportSourceArn: aws.String("arn:aws:cloudtrail:us-east-1:123456789012:eventdatastore/" + uniquename.New("missing")),
		ImportRoleArn:   aws.String(role),
	})
	requireAWSErrorCode(t, err, "ResourceNotFoundException")
}
