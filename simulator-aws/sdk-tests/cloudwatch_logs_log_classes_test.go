package aws_sdk_test

import (
	"errors"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The log classes guide: read paths answer ESSENTIALS for the Infrequent
// Access class from December 2026, and INFREQUENT_ACCESS before.
func essentialsReadClass() cwlogtypes.LogGroupClass {
	if time.Now().UTC().Before(time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)) {
		return cwlogtypes.LogGroupClassInfrequentAccess
	}
	return cwlogtypes.LogGroupClassEssentials
}

func createClassedLogGroup(t *testing.T, name string, class cwlogtypes.LogGroupClass) {
	t.Helper()
	cw := cwLogsClient()
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(name), LogGroupClass: class})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(name)})
	})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(name), LogStreamName: aws.String("s")})
	require.NoError(t, err)
}

func logsMetricSum(t *testing.T, namespace, metric string, dims []cwtypes.Dimension, since time.Time) float64 {
	t.Helper()
	stats, err := cloudwatchClient().GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace: aws.String(namespace), MetricName: aws.String(metric), Dimensions: dims,
		StartTime: aws.Time(since.Add(-5 * time.Minute)), EndTime: aws.Time(time.Now().Add(5 * time.Minute)),
		Period: aws.Int32(600), Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
	})
	require.NoError(t, err)
	total := 0.0
	for _, dp := range stats.Datapoints {
		total += aws.ToFloat64(dp.Sum)
	}
	return total
}

func dims(pairs ...string) []cwtypes.Dimension {
	out := make([]cwtypes.Dimension, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, cwtypes.Dimension{Name: aws.String(pairs[i]), Value: aws.String(pairs[i+1])})
	}
	return out
}

// The log classes guide offers an Essentials (formerly Infrequent Access)
// group managed ingestion and CloudWatch Logs Insights, but no metric filters,
// subscription filters, GetLogEvents or FilterLogEvents, Live Tail, anomaly
// detection, field indexes or embedded metric format extraction; ESSENTIALS
// and INFREQUENT_ACCESS name one class. A Delivery group keeps its events two
// days, a retention nothing changes, and offers no Insights queries.
func TestLogs_LogClassesLimitFeatures(t *testing.T) {
	t.Parallel()
	cw, stream := cwLogsClient(), cwLogsStreamClient()
	prefix := uniquename.New("/test/classes")
	essentials, infrequent, standard, deliveryGroup := prefix+"/essentials", prefix+"/infrequent", prefix+"/standard", prefix+"/delivery"
	createClassedLogGroup(t, essentials, cwlogtypes.LogGroupClassEssentials)
	createClassedLogGroup(t, infrequent, cwlogtypes.LogGroupClassInfrequentAccess)
	createClassedLogGroup(t, standard, cwlogtypes.LogGroupClassStandard)
	createClassedLogGroup(t, deliveryGroup, cwlogtypes.LogGroupClassDelivery)

	listed, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(prefix), LogGroupClass: cwlogtypes.LogGroupClassEssentials,
	})
	require.NoError(t, err)
	require.Len(t, listed.LogGroups, 2, "ESSENTIALS and INFREQUENT_ACCESS name one class")
	for _, g := range listed.LogGroups {
		assert.Equal(t, essentialsReadClass(), g.LogGroupClass)
	}
	delivery, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(deliveryGroup)})
	require.NoError(t, err)
	require.Len(t, delivery.LogGroups, 1)
	assert.Equal(t, cwlogtypes.LogGroupClassDelivery, delivery.LogGroups[0].LogGroupClass)
	assert.Equal(t, int32(2), aws.ToInt32(delivery.LogGroups[0].RetentionInDays))

	var invalidParam *cwlogtypes.InvalidParameterException
	var invalidOp *cwlogtypes.InvalidOperationException
	_, err = cw.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(deliveryGroup), RetentionInDays: aws.Int32(30)})
	require.True(t, errors.As(err, &invalidParam), "a Delivery group's retention is fixed: %v", err)
	_, err = cw.DeleteRetentionPolicy(ctx, &cloudwatchlogs.DeleteRetentionPolicyInput{LogGroupName: aws.String(deliveryGroup)})
	require.True(t, errors.As(err, &invalidParam), "a Delivery group's retention is fixed: %v", err)
	_, err = cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(deliveryGroup), QueryString: aws.String("fields @message"),
		StartTime: aws.Int64(time.Now().Add(-time.Hour).Unix()), EndTime: aws.Int64(time.Now().Unix()),
	})
	require.True(t, errors.As(err, &invalidParam), "a Delivery group offers no Insights queries: %v", err)

	metric := map[string]string{essentials: uniquename.New("EssentialsLines"), standard: uniquename.New("StandardLines")}
	start := time.Now()
	for _, group := range []string{essentials, standard} {
		emf := fmt.Sprintf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"Test/LogClasses","Dimensions":[[]],"Metrics":[{"Name":%q}]}]},%q:1}`,
			time.Now().UnixMilli(), metric[group], metric[group])
		_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
			LogGroupName: aws.String(group), LogStreamName: aws.String("s"),
			LogEvents: []cwlogtypes.InputLogEvent{{Timestamp: aws.Int64(time.Now().UnixMilli()), Message: aws.String(emf)}},
		})
		require.NoError(t, err, "every class ingests")
	}
	assert.Equal(t, 1.0, logsMetricSum(t, "Test/LogClasses", metric[standard], nil, start), "a Standard group extracts EMF metrics")
	assert.Zero(t, logsMetricSum(t, "Test/LogClasses", metric[essentials], nil, start), "an Essentials group extracts none")

	for _, group := range []string{essentials, infrequent} {
		_, err = cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("s")})
		require.True(t, errors.As(err, &invalidParam), "GetLogEvents on %s: %v", group, err)
		_, err = cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group)})
		require.True(t, errors.As(err, &invalidParam), "FilterLogEvents on %s: %v", group, err)
		_, err = cw.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
			LogGroupName: aws.String(group), FilterName: aws.String("f"), FilterPattern: aws.String(""),
			MetricTransformations: []cwlogtypes.MetricTransformation{{
				MetricName: aws.String("M"), MetricNamespace: aws.String("Test/LogClasses"), MetricValue: aws.String("1"),
			}},
		})
		require.True(t, errors.As(err, &invalidOp), "a metric filter on %s: %v", group, err)
		_, err = cw.PutIndexPolicy(ctx, &cloudwatchlogs.PutIndexPolicyInput{
			LogGroupIdentifier: aws.String(group), PolicyDocument: aws.String(`{"Fields":["RequestId"]}`),
		})
		require.True(t, errors.As(err, &invalidParam), "a field index policy on %s: %v", group, err)
		_, err = cw.CreateLogAnomalyDetector(ctx, &cloudwatchlogs.CreateLogAnomalyDetectorInput{
			LogGroupArnList: []string{"arn:aws:logs:us-east-1:123456789012:log-group:" + group},
		})
		require.True(t, errors.As(err, &invalidParam), "an anomaly detector on %s: %v", group, err)
		_, err = stream.StartLiveTail(ctx, &cloudwatchlogs.StartLiveTailInput{
			LogGroupIdentifiers: []string{"arn:aws:logs:us-east-1:123456789012:log-group:" + group},
		})
		require.True(t, errors.As(err, &invalidOp), "Live Tail on %s: %v", group, err)
	}

	events, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(standard), LogStreamName: aws.String("s")})
	require.NoError(t, err)
	require.Len(t, events.Events, 1)
}

// FilterLogEvents answers each stored event's one ID on every call; the
// subscription guide's IDs carry the event's timestamp above its ingestion
// time above 80 bits that number it within its batch.
func TestLogs_FilterLogEventsKeepsEventIDs(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := uniquename.New("/test/event-ids")
	createLogGroupWithStream(t, group, "s")
	at := time.Now().UnixMilli()
	_, err := cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String("s"),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(at), Message: aws.String("first")},
			{Timestamp: aws.Int64(at + 1), Message: aws.String("second")},
		},
	})
	require.NoError(t, err)
	read := func() []cwlogtypes.FilteredLogEvent {
		out, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group)})
		require.NoError(t, err)
		require.Len(t, out.Events, 2)
		return out.Events
	}
	first, second := read(), read()
	mask64 := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 64), big.NewInt(1))
	for i := range first {
		id := aws.ToString(first[i].EventId)
		assert.Equal(t, id, aws.ToString(second[i].EventId), "an event keeps its ID")
		assert.Regexp(t, subscriptionEventID, id)
		n, ok := new(big.Int).SetString(id, 10)
		require.True(t, ok)
		assert.Equal(t, aws.ToInt64(first[i].Timestamp), new(big.Int).Rsh(n, 144).Int64())
		ingested := new(big.Int).And(new(big.Int).Rsh(n, 80), mask64)
		assert.Equal(t, aws.ToInt64(first[i].IngestionTime), ingested.Int64())
	}
	assert.NotEqual(t, aws.ToString(first[0].EventId), aws.ToString(first[1].EventId))
}
