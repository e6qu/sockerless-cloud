package aws_sdk_test

import (
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogs_TestTransformerProcessors(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	out, err := cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: []cwlogtypes.Processor{
			{Grok: &cwlogtypes.Grok{Match: aws.String("%{IP:client.ip} %{WORD:method} %{URIPATHPARAM:request.uri} %{NUMBER:response.status} %{NUMBER:response.bytes}")}},
			{TypeConverter: &cwlogtypes.TypeConverter{Entries: []cwlogtypes.TypeConverterEntry{
				{Key: aws.String("response.status"), Type: cwlogtypes.TypeInteger},
			}}},
			{AddKeys: &cwlogtypes.AddKeys{Entries: []cwlogtypes.AddKeyEntry{
				{Key: aws.String("source"), Value: aws.String("edge")},
			}}},
		},
		LogEventMessages: []string{
			"192.168.1.1 GET /index.html?param=value 200 1234",
			"not an access log line",
		},
	})
	require.NoError(t, err)
	require.Len(t, out.TransformedLogs, 2)
	assert.Equal(t, int64(1), out.TransformedLogs[0].EventNumber)
	assert.Equal(t, "192.168.1.1 GET /index.html?param=value 200 1234", aws.ToString(out.TransformedLogs[0].EventMessage))
	assert.JSONEq(t,
		`{"client":{"ip":"192.168.1.1"},"method":"GET","request":{"uri":"/index.html?param=value"},"response":{"status":200,"bytes":"1234"},"source":"edge"}`,
		aws.ToString(out.TransformedLogs[0].TransformedEventMessage))
	assert.Equal(t, int64(2), out.TransformedLogs[1].EventNumber)
	assert.Nil(t, out.TransformedLogs[1].TransformedEventMessage, "a line grok does not match is not transformed")

	_, err = cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: []cwlogtypes.Processor{
			{TrimString: &cwlogtypes.TrimString{WithKeys: []string{"a"}}},
		},
		LogEventMessages: []string{"x"},
	})
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "a transformer that does not start with a parser is refused: %v", err)

	_, err = cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: []cwlogtypes.Processor{
			{ParseJSON: &cwlogtypes.ParseJSON{}},
			{ParseVPC: &cwlogtypes.ParseVPC{}},
		},
		LogEventMessages: []string{"x"},
	})
	require.True(t, errors.As(err, &invalid), "a vended-log parser after the first processor is refused: %v", err)

	_, err = cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: []cwlogtypes.Processor{
			{ParseToOCSF: &cwlogtypes.ParseToOCSF{EventSource: cwlogtypes.EventSourceVpcFlow, OcsfVersion: cwlogtypes.OCSFVersionV11}},
		},
		LogEventMessages: []string{"x"},
	})
	var invalidOp *cwlogtypes.InvalidOperationException
	require.True(t, errors.As(err, &invalidOp), "parseToOCSF: %v", err)
}

// GetLogEvents and FilterLogEvents return events as ingested; CloudWatch Logs
// Insights and metric filters see the transformed versions, and an event that
// fails to transform carries @transformationError.
func TestLogs_TransformerAppliedAtIngestion(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group, stream := "/test/transformer-ingest", "s1"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	defer cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
	require.NoError(t, err)

	groups, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(group)})
	require.NoError(t, err)
	require.Len(t, groups.LogGroups, 1)
	groupArn := aws.ToString(groups.LogGroups[0].LogGroupArn)

	_, err = cw.PutTransformer(ctx, &cloudwatchlogs.PutTransformerInput{
		LogGroupIdentifier: aws.String(groupArn),
		TransformerConfig: []cwlogtypes.Processor{
			{ParseJSON: &cwlogtypes.ParseJSON{}},
			{UpperCaseString: &cwlogtypes.UpperCaseString{WithKeys: []string{"level"}}},
			{CopyValue: &cwlogtypes.CopyValue{Entries: []cwlogtypes.CopyValueEntry{
				{Source: aws.String("@logGroupName"), Target: aws.String("group")},
			}}},
		},
	})
	require.NoError(t, err)
	got, err := cw.GetTransformer(ctx, &cloudwatchlogs.GetTransformerInput{LogGroupIdentifier: aws.String(groupArn)})
	require.NoError(t, err)
	require.Len(t, got.TransformerConfig, 3)
	require.NotNil(t, got.TransformerConfig[1].UpperCaseString)
	assert.Equal(t, []string{"level"}, got.TransformerConfig[1].UpperCaseString.WithKeys)

	_, err = cw.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName:  aws.String(group),
		FilterName:    aws.String("errors"),
		FilterPattern: aws.String(`{ $.level = "ERROR" }`),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName: aws.String("TransformedErrors"), MetricNamespace: aws.String("Test/LogsTransformer"), MetricValue: aws.String("1"),
		}},
		ApplyOnTransformedLogs: true,
	})
	require.NoError(t, err)

	now := time.Now().UnixMilli()
	ingested := []string{`{"level":"error","msg":"disk full"}`, `level=error msg=unparsed`}
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName:  aws.String(group),
		LogStreamName: aws.String(stream),
		LogEvents: []cwlogtypes.InputLogEvent{
			{Timestamp: aws.Int64(now), Message: aws.String(ingested[0])},
			{Timestamp: aws.Int64(now + 1), Message: aws.String(ingested[1])},
		},
	})
	require.NoError(t, err)

	events, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true),
	})
	require.NoError(t, err)
	require.Len(t, events.Events, 2)
	assert.Equal(t, ingested[0], aws.ToString(events.Events[0].Message), "GetLogEvents returns the original event")
	filtered, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	require.Len(t, filtered.Events, 2)
	assert.Equal(t, ingested[0], aws.ToString(filtered.Events[0].Message), "FilterLogEvents returns the original event")

	query := func(q string) [][]cwlogtypes.ResultField {
		t.Helper()
		start, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
			LogGroupName: aws.String(group), QueryString: aws.String(q),
			StartTime: aws.Int64(now/1000 - 60), EndTime: aws.Int64(now/1000 + 60),
		})
		require.NoError(t, err)
		res, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: start.QueryId})
		require.NoError(t, err)
		require.Equal(t, cwlogtypes.QueryStatusComplete, res.Status)
		return res.Results
	}
	field := func(row []cwlogtypes.ResultField, name string) string {
		for _, f := range row {
			if aws.ToString(f.Field) == name {
				return aws.ToString(f.Value)
			}
		}
		return ""
	}
	rows := query(`fields @message, level, group | filter level = "ERROR"`)
	require.Len(t, rows, 1)
	assert.Equal(t, group, field(rows[0], "group"))
	assert.JSONEq(t, `{"level":"ERROR","msg":"disk full","group":"`+group+`"}`, field(rows[0], "@message"))

	rows = query(`fields @message, @transformationError | filter ispresent(@transformationError)`)
	require.Len(t, rows, 1)
	assert.Equal(t, ingested[1], field(rows[0], "@message"))
	assert.NotEmpty(t, field(rows[0], "@transformationError"))

	cwm := cloudwatchClient()
	sum := func(namespace, metric string, dims []cwtypes.Dimension) float64 {
		t.Helper()
		stats, err := cwm.GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
			Namespace: aws.String(namespace), MetricName: aws.String(metric), Dimensions: dims,
			StartTime: aws.Time(time.UnixMilli(now).Add(-5 * time.Minute)), EndTime: aws.Time(time.Now().Add(5 * time.Minute)),
			Period: aws.Int32(600), Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
		})
		require.NoError(t, err)
		total := 0.0
		for _, dp := range stats.Datapoints {
			total += aws.ToFloat64(dp.Sum)
		}
		return total
	}
	assert.Equal(t, 1.0, sum("Test/LogsTransformer", "TransformedErrors", nil), "the metric filter matches the transformed event")
	byGroup := []cwtypes.Dimension{{Name: aws.String("LogGroupname"), Value: aws.String(group)}}
	assert.Equal(t, 1.0, sum("AWS/Logs", "TransformedLogEvents", byGroup))
	assert.Equal(t, 1.0, sum("AWS/Logs", "TransformationErrors", byGroup))

	_, err = cw.DeleteTransformer(ctx, &cloudwatchlogs.DeleteTransformerInput{LogGroupIdentifier: aws.String(group)})
	require.NoError(t, err)
	_, err = cw.GetTransformer(ctx, &cloudwatchlogs.GetTransformerInput{LogGroupIdentifier: aws.String(group)})
	var notFound *cwlogtypes.ResourceNotFoundException
	require.True(t, errors.As(err, &notFound), "%v", err)

	_, err = cw.PutTransformer(ctx, &cloudwatchlogs.PutTransformerInput{
		LogGroupIdentifier: aws.String("/test/transformer-missing-group"),
		TransformerConfig:  []cwlogtypes.Processor{{ParseJSON: &cwlogtypes.ParseJSON{}}},
	})
	require.True(t, errors.As(err, &notFound), "a transformer needs an existing log group: %v", err)
}

// substituteString compiles from as a java.util.regex.Pattern, with the
// guide's (?P<name>...) groups: lookaround, backreferences, possessive
// quantifiers and Java's ASCII \w all hold.
func TestLogs_SubstituteStringTakesJavaPatterns(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	entry := func(source, from, to string) cwlogtypes.SubstituteStringEntry {
		return cwlogtypes.SubstituteStringEntry{Source: aws.String(source), From: aws.String(from), To: aws.String(to)}
	}
	out, err := cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: []cwlogtypes.Processor{
			{ParseJSON: &cwlogtypes.ParseJSON{}},
			{SubstituteString: &cwlogtypes.SubstituteString{Entries: []cwlogtypes.SubstituteStringEntry{
				entry("price", `(?<=\$)\d+`, "N"),
				entry("doubled", `(\w)\1`, "<$1>"),
				entry("greedy", `a++a`, "X"),
				entry("arn", `^arn:aws:sts::(?P<account_id>\d{12}):assumed-role/(?P<role_name>[\w+=,.@-]+)/(?P<role_session_name>[\w+=,.@-]+)$`,
					"${account_id}:${role_name}:${role_session_name}"),
				entry("word", `\w+`, "W"),
			}}},
		},
		LogEventMessages: []string{`{"price":"cost $15 and 20","doubled":"aabbcd","greedy":"aaaa",` +
			`"arn":"arn:aws:sts::123456789012:assumed-role/MyImportantRole/MySession","word":"café"}`},
	})
	require.NoError(t, err)
	require.Len(t, out.TransformedLogs, 1)
	assert.JSONEq(t, `{"price":"cost $N and 20","doubled":"<a><b>cd","greedy":"aaaa",`+
		`"arn":"123456789012:MyImportantRole:MySession","word":"Wé"}`, aws.ToString(out.TransformedLogs[0].TransformedEventMessage))

	_, err = cw.TestTransformer(ctx, &cloudwatchlogs.TestTransformerInput{
		TransformerConfig: []cwlogtypes.Processor{
			{ParseJSON: &cwlogtypes.ParseJSON{}},
			{SubstituteString: &cwlogtypes.SubstituteString{Entries: []cwlogtypes.SubstituteStringEntry{entry("a", `(?<=a*)b`, "x")}}},
		},
		LogEventMessages: []string{`{"a":"b"}`},
	})
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "Pattern refuses an unbounded look-behind: %v", err)
}
