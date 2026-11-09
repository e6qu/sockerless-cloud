package aws_sdk_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/firehose"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newPolicyLogGroup(t *testing.T, cw *cloudwatchlogs.Client, group, stream string) {
	t.Helper()
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})
	if stream != "" {
		_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
		require.NoError(t, err)
	}
}

func putMessages(t *testing.T, cw *cloudwatchlogs.Client, group, stream string, messages ...string) {
	t.Helper()
	now := time.Now().UnixMilli()
	events := make([]cwlogtypes.InputLogEvent, 0, len(messages))
	for i, m := range messages {
		events = append(events, cwlogtypes.InputLogEvent{Timestamp: aws.Int64(now + int64(i)), Message: aws.String(m)})
	}
	_, err := cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: events})
	require.NoError(t, err)
}

func streamMessages(t *testing.T, cw *cloudwatchlogs.Client, group, stream string, unmask bool) []string {
	t.Helper()
	out, err := cw.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true), Unmask: unmask,
	})
	require.NoError(t, err)
	var messages []string
	for _, e := range out.Events {
		messages = append(messages, aws.ToString(e.Message))
	}
	return messages
}

// TestLogs_PutLogEventsBatchLimits holds PutLogEvents to the batch
// constraints its API reference lists.
func TestLogs_PutLogEventsBatchLimits(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group, stream := "/test/put-log-events-limits", "s"
	newPolicyLogGroup(t, cw, group, stream)
	now := time.Now()
	put := func(events ...cwlogtypes.InputLogEvent) (*cloudwatchlogs.PutLogEventsOutput, error) {
		return cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: events})
	}
	event := func(at time.Time, message string) cwlogtypes.InputLogEvent {
		return cwlogtypes.InputLogEvent{Timestamp: aws.Int64(at.UnixMilli()), Message: aws.String(message)}
	}
	refused := func(err error, contains string) {
		t.Helper()
		var invalid *cwlogtypes.InvalidParameterException
		require.True(t, errors.As(err, &invalid), "want InvalidParameterException, got %v", err)
		assert.Contains(t, invalid.ErrorMessage(), contains)
	}

	many := make([]cwlogtypes.InputLogEvent, 10001)
	for i := range many {
		many[i] = event(now, "x")
	}
	_, err := put(many...)
	refused(err, "10000")

	_, err = put(event(now, strings.Repeat("a", 1048576-26+1)))
	refused(err, "Log event too large: 1048577 bytes exceeds limit of 1048576")
	_, err = put(event(now, strings.Repeat("b", 600000)), event(now, strings.Repeat("c", 600000)))
	refused(err, "Upload too large: 1200052 bytes exceeds limit of 1048576")
	_, err = put(event(now, "later"), event(now.Add(-time.Second), "earlier"))
	refused(err, "chronological order")
	_, err = put(event(now.Add(-25*time.Hour), "a day ago"), event(now, "now"))
	refused(err, "24 hours")

	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String("big")})
	require.NoError(t, err)
	_, err = cw.PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String("big"),
		LogEvents: []cwlogtypes.InputLogEvent{event(now, strings.Repeat("d", 1048576-26))}})
	require.NoError(t, err, "an event of exactly 1,048,576 bytes with its overhead fits")

	out, err := put(event(now.Add(-15*24*time.Hour), "too old"), event(now, "kept"), event(now.Add(3*time.Hour), "too new"))
	require.NoError(t, err)
	require.NotNil(t, out.RejectedLogEventsInfo)
	assert.Equal(t, int32(1), aws.ToInt32(out.RejectedLogEventsInfo.TooOldLogEventEndIndex))
	assert.Equal(t, int32(2), aws.ToInt32(out.RejectedLogEventsInfo.TooNewLogEventStartIndex))
	assert.Nil(t, out.RejectedLogEventsInfo.ExpiredLogEventEndIndex)
	assert.Equal(t, []string{"kept"}, streamMessages(t, cw, group, stream, false))

	_, err = cw.PutRetentionPolicy(ctx, &cloudwatchlogs.PutRetentionPolicyInput{LogGroupName: aws.String(group), RetentionInDays: aws.Int32(1)})
	require.NoError(t, err)
	out, err = put(event(now.Add(-2*24*time.Hour), "expired"), event(now, "fresh"))
	require.NoError(t, err)
	require.NotNil(t, out.RejectedLogEventsInfo)
	assert.Equal(t, int32(1), aws.ToInt32(out.RejectedLogEventsInfo.ExpiredLogEventEndIndex))
	assert.Equal(t, []string{"kept", "fresh"}, streamMessages(t, cw, group, stream, false))
}

func dataProtectionDocument(destination string, identifiers ...string) string {
	quoted := make([]string, len(identifiers))
	custom := []string{}
	for i, id := range identifiers {
		if name, regex, ok := strings.Cut(id, "="); ok {
			custom = append(custom, fmt.Sprintf(`{"Name":%q,"Regex":%q}`, name, regex))
			id = name
		} else {
			id = "arn:aws:dataprotection::aws:data-identifier/" + id
		}
		quoted[i] = fmt.Sprintf("%q", id)
	}
	list := strings.Join(quoted, ",")
	config := ""
	if len(custom) > 0 {
		config = `"Configuration":{"CustomDataIdentifier":[` + strings.Join(custom, ",") + `]},`
	}
	return `{"Name":"data-protection","Version":"2021-06-01",` + config + `"Statement":[` +
		`{"Sid":"audit","DataIdentifier":[` + list + `],"Operation":{"Audit":{"FindingsDestination":{` + destination + `}}}},` +
		`{"Sid":"redact","DataIdentifier":[` + list + `],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`
}

type findingsReport struct {
	ResourceArn     string `json:"resourceArn"`
	DataIdentifiers []struct {
		Name       string `json:"name"`
		Count      int    `json:"count"`
		Detections []struct {
			Start int `json:"start"`
			End   int `json:"end"`
		} `json:"detections"`
	} `json:"dataIdentifiers"`
}

// TestLogs_DataProtectionMasksAuditsAndUnmasks follows the data protection
// guide end to end: a log group-level policy with managed and custom
// identifiers masks at ingestion, reports findings to CloudWatch Logs,
// Amazon S3 and Amazon Data Firehose, counts LogEventsWithFindings, and shows
// the data as ingested only to a reader with logs:Unmask.
func TestLogs_DataProtectionMasksAuditsAndUnmasks(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group, stream := "/test/data-protection-masking", "app"
	findingsGroup := "/test/data-protection-findings"
	newPolicyLogGroup(t, cw, group, stream)
	newPolicyLogGroup(t, cw, findingsGroup, "")
	firehoseName := "sdk-dp-findings"
	_, firehoseBucket := createImmediateFirehose(t, firehoseName)
	bucket := "sdk-dp-audit-findings"
	s3c := s3Client()
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		objects, _ := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket)})
		for _, object := range objects.Contents {
			_, _ = s3c.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: object.Key})
		}
		_, _ = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	destination := fmt.Sprintf(`"CloudWatchLogs":{"LogGroup":%q},"Firehose":{"DeliveryStream":%q},"S3":{"Bucket":%q}`, findingsGroup, firehoseName, bucket)
	doc := dataProtectionDocument(destination, "EmailAddress", "CreditCardNumber", `EmployeeId=EmployeeID-\d{9}`)
	_, err = cw.PutDataProtectionPolicy(ctx, &cloudwatchlogs.PutDataProtectionPolicyInput{LogGroupIdentifier: aws.String(group), PolicyDocument: aws.String(doc)})
	require.NoError(t, err)

	policy, err := s3c.GetBucketPolicy(ctx, &s3.GetBucketPolicyInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(policy.Policy), "delivery.logs.amazonaws.com")
	assert.Contains(t, aws.ToString(policy.Policy), "AWSLogDeliveryWrite")
	tags, err := firehoseClient().ListTagsForDeliveryStream(ctx, &firehose.ListTagsForDeliveryStreamInput{DeliveryStreamName: aws.String(firehoseName)})
	require.NoError(t, err)
	tagged := false
	for _, tag := range tags.Tags {
		tagged = tagged || (aws.ToString(tag.Key) == "LogDeliveryEnabled" && aws.ToString(tag.Value) == "true")
	}
	assert.True(t, tagged, "the findings stream carries LogDeliveryEnabled=true")
	resourcePolicies, err := cw.DescribeResourcePolicies(ctx, &cloudwatchlogs.DescribeResourcePoliciesInput{})
	require.NoError(t, err)
	granted := false
	for _, p := range resourcePolicies.ResourcePolicies {
		granted = granted || strings.Contains(aws.ToString(p.PolicyDocument), findingsGroup)
	}
	assert.True(t, granted, "a resource policy admits delivery.logs.amazonaws.com to the findings group")

	original := "user jane.doe@example.com paid with card 4532015112830366 as EmployeeID-123456789"
	putMessages(t, cw, group, stream, original, "nothing sensitive here")

	masked := "user ******************** paid with card **************** as ********************"
	assert.Equal(t, []string{masked, "nothing sensitive here"}, streamMessages(t, cw, group, stream, false))
	assert.Equal(t, []string{original, "nothing sensitive here"}, streamMessages(t, cw, group, stream, true))

	filtered, err := cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), FilterPattern: aws.String("paid")})
	require.NoError(t, err)
	require.Len(t, filtered.Events, 1)
	assert.Equal(t, masked, aws.ToString(filtered.Events[0].Message))
	filtered, err = cw.FilterLogEvents(ctx, &cloudwatchlogs.FilterLogEventsInput{LogGroupName: aws.String(group), FilterPattern: aws.String("paid"), Unmask: true})
	require.NoError(t, err)
	require.Len(t, filtered.Events, 1)
	assert.Equal(t, original, aws.ToString(filtered.Events[0].Message))

	query, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
		LogGroupName: aws.String(group), QueryString: aws.String("fields @message, unmask(@message) | filter @message like /paid/"),
		StartTime: aws.Int64(time.Now().Add(-time.Hour).Unix()), EndTime: aws.Int64(time.Now().Add(time.Minute).Unix()),
	})
	require.NoError(t, err)
	results, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: query.QueryId})
	require.NoError(t, err)
	require.Len(t, results.Results, 1)
	row := map[string]string{}
	for _, f := range results.Results[0] {
		row[aws.ToString(f.Field)] = aws.ToString(f.Value)
	}
	assert.Equal(t, masked, row["@message"])
	assert.Equal(t, original, row["unmask(@message)"])

	findings := streamMessages(t, cw, findingsGroup, group, false)
	require.Len(t, findings, 1, "one findings report for the one event with sensitive data")
	var report findingsReport
	require.NoError(t, json.Unmarshal([]byte(findings[0]), &report))
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:"+group+":*", report.ResourceArn)
	found := map[string][2]int{}
	for _, id := range report.DataIdentifiers {
		require.Len(t, id.Detections, id.Count)
		found[id.Name] = [2]int{id.Detections[0].Start, id.Detections[0].End}
	}
	assert.Equal(t, map[string][2]int{"EmailAddress": {5, 25}, "CreditCardNumber": {41, 57}, "EmployeeId": {61, 81}}, found)

	s3Report := readOnlyS3ObjectWithPrefix(t, bucket, "AWSLogs/123456789012/")
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(s3Report)), &report))
	assert.Len(t, report.DataIdentifiers, 3)
	firehoseRecord := readOnlyS3ObjectWithPrefix(t, firehoseBucket, "initial/")
	assert.Contains(t, firehoseRecord, `"resourceArn":"arn:aws:logs:us-east-1:123456789012:log-group:`+group+`:*"`)

	stats, err := cloudwatchClient().GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace: aws.String("AWS/Logs"), MetricName: aws.String("LogEventsWithFindings"),
		StartTime: aws.Time(time.Now().Add(-5 * time.Minute)), EndTime: aws.Time(time.Now().Add(time.Minute)),
		Period: aws.Int32(60), Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
	})
	require.NoError(t, err)
	total := 0.0
	for _, dp := range stats.Datapoints {
		total += aws.ToFloat64(dp.Sum)
	}
	assert.GreaterOrEqual(t, total, 1.0)

	arn := "arn:aws:logs:us-east-1:123456789012:log-group:" + group
	cfg := scopedCredentials(t, "logs-no-unmask-user", "read-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:GetLogEvents","logs:FilterLogEvents"],"Resource":["`+arn+`","`+arn+`:*"]}]}`)
	reader := cloudwatchlogs.NewFromConfig(cfg, func(o *cloudwatchlogs.Options) { o.BaseEndpoint = aws.String(baseURL) })
	read, err := reader.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), StartFromHead: aws.Bool(true)})
	require.NoError(t, err)
	assert.Equal(t, masked, aws.ToString(read.Events[0].Message))
	_, err = reader.GetLogEvents(ctx, &cloudwatchlogs.GetLogEventsInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream), Unmask: true})
	require.Error(t, err)
	assert.Equal(t, "AccessDeniedException", errCodeOf(err))
}

// TestLogs_DataProtectionPolicyValidation refuses the documents the guide
// rules out.
func TestLogs_DataProtectionPolicyValidation(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	group := "/test/data-protection-validation"
	newPolicyLogGroup(t, cw, group, "")
	for name, doc := range map[string]string{
		"unknown managed identifier":          dataProtectionDocument("", "NoSuchIdentifier"),
		"missing findings log group":          dataProtectionDocument(`"CloudWatchLogs":{"LogGroup":"/test/no-such-group"}`, "EmailAddress"),
		"custom name of a managed identifier": dataProtectionDocument("", `EmailAddress=x\d+`),
		"audit statement only": `{"Name":"p","Version":"2021-06-01","Statement":[{"Sid":"audit",` +
			`"DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Audit":{"FindingsDestination":{}}}}]}`,
	} {
		_, err := cw.PutDataProtectionPolicy(ctx, &cloudwatchlogs.PutDataProtectionPolicyInput{LogGroupIdentifier: aws.String(group), PolicyDocument: aws.String(doc)})
		var invalid *cwlogtypes.InvalidParameterException
		assert.True(t, errors.As(err, &invalid), "%s: %v", name, err)
	}
}

// TestLogs_AccountDataProtectionIsCumulative masks what the account-level
// policy and a log group's own policy each name, as the PutAccountPolicy API
// reference describes. The account-level policy reaches every group, so the
// test does not run beside others and names only its own token.
func TestLogs_AccountDataProtectionIsCumulative(t *testing.T) {
	cw := cwLogsClient()
	group, stream := "/test/data-protection-account", "app"
	newPolicyLogGroup(t, cw, group, stream)
	_, err := cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String("sdk-account-data-protection"), PolicyType: cwlogtypes.PolicyTypeDataProtectionPolicy,
		PolicyDocument: aws.String(dataProtectionDocument("", `AccountToken=AcctToken-\d{6}`)),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{
			PolicyName: aws.String("sdk-account-data-protection"), PolicyType: cwlogtypes.PolicyTypeDataProtectionPolicy,
		})
	})
	_, err = cw.PutDataProtectionPolicy(ctx, &cloudwatchlogs.PutDataProtectionPolicyInput{
		LogGroupIdentifier: aws.String(group), PolicyDocument: aws.String(dataProtectionDocument("", "EmailAddress")),
	})
	require.NoError(t, err)
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String("sdk-second-data-protection"), PolicyType: cwlogtypes.PolicyTypeDataProtectionPolicy,
		PolicyDocument: aws.String(dataProtectionDocument("", "IpAddress")),
	})
	var limit *cwlogtypes.LimitExceededException
	require.True(t, errors.As(err, &limit), "an account holds one data protection policy: %v", err)

	putMessages(t, cw, group, stream, "AcctToken-123456 sent by ops@example.com")
	assert.Equal(t, []string{"**************** sent by ***************"}, streamMessages(t, cw, group, stream, false))
	assert.Equal(t, []string{"AcctToken-123456 sent by ops@example.com"}, streamMessages(t, cw, group, stream, true))
}

// TestLogs_FieldIndexPrecedence applies an account-level field index policy
// to the groups its prefix selects unless a group has its own, as the
// PutAccountPolicy and PutIndexPolicy API references describe, and lets
// filterIndex read only the events those indexes hold.
func TestLogs_FieldIndexPrecedence(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	prefix := "/test/field-index-precedence/"
	policyName := "sdk-field-index-prefix"
	_, err := cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policyName), PolicyType: cwlogtypes.PolicyTypeFieldIndexPolicy,
		PolicyDocument:    aws.String(`{"Fields":["requestId"]}`),
		SelectionCriteria: aws.String(fmt.Sprintf(`LogGroupNamePrefix IN [%q]`, prefix)),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{PolicyName: aws.String(policyName), PolicyType: cwlogtypes.PolicyTypeFieldIndexPolicy})
	})
	inherited, own := prefix+"inherited", prefix+"own"
	newPolicyLogGroup(t, cw, inherited, "s")
	newPolicyLogGroup(t, cw, own, "s")
	_, err = cw.PutIndexPolicy(ctx, &cloudwatchlogs.PutIndexPolicyInput{
		LogGroupIdentifier: aws.String(own), PolicyDocument: aws.String(`{"FieldsV2":{"sessionId":{"type":"FACET"}}}`),
	})
	require.NoError(t, err)

	describePolicy := func(group string) cwlogtypes.IndexPolicy {
		out, err := cw.DescribeIndexPolicies(ctx, &cloudwatchlogs.DescribeIndexPoliciesInput{LogGroupIdentifiers: []string{group}})
		require.NoError(t, err)
		require.Len(t, out.IndexPolicies, 1)
		return out.IndexPolicies[0]
	}
	acct := describePolicy(inherited)
	assert.Equal(t, cwlogtypes.IndexSourceAccount, acct.Source)
	assert.Equal(t, policyName, aws.ToString(acct.PolicyName))
	assert.Equal(t, cwlogtypes.IndexSourceLogGroup, describePolicy(own).Source)

	for _, group := range []string{inherited, own} {
		putMessages(t, cw, group, "s",
			`{"requestId":"r-1","sessionId":"s-1","traceId":"t-1"}`,
			`{"requestId":"r-2","sessionId":"s-2"}`,
			`plain text`)
	}
	indexes := func(group string, categories ...cwlogtypes.IndexCategory) map[string]cwlogtypes.FieldIndex {
		out, err := cw.DescribeFieldIndexes(ctx, &cloudwatchlogs.DescribeFieldIndexesInput{LogGroupIdentifiers: []string{group}, IndexCategories: categories})
		require.NoError(t, err)
		byName := map[string]cwlogtypes.FieldIndex{}
		for _, fi := range out.FieldIndexes {
			byName[aws.ToString(fi.FieldIndexName)] = fi
		}
		return byName
	}
	got := indexes(inherited)
	require.Contains(t, got, "requestId")
	assert.Equal(t, cwlogtypes.IndexCategoryCustom, got["requestId"].IndexCategory)
	assert.Equal(t, cwlogtypes.IndexTypeFieldIndex, got["requestId"].Type)
	assert.Equal(t, cwlogtypes.IndexCategoryDefault, got["@logStream"].IndexCategory)
	assert.Equal(t, cwlogtypes.IndexCategoryDefault, got["traceId"].IndexCategory)
	assert.NotContains(t, got, "sessionId")
	got = indexes(own)
	assert.Equal(t, cwlogtypes.IndexTypeFacet, got["sessionId"].Type)
	assert.NotContains(t, got, "requestId", "the group's own policy overrides the account-level one")
	assert.Empty(t, indexes(own, cwlogtypes.IndexCategoryInactive))

	query := func(group, q string) ([]string, float64) {
		start, err := cw.StartQuery(ctx, &cloudwatchlogs.StartQueryInput{
			LogGroupName: aws.String(group), QueryString: aws.String(q),
			StartTime: aws.Int64(time.Now().Add(-time.Hour).Unix()), EndTime: aws.Int64(time.Now().Add(time.Minute).Unix()),
		})
		require.NoError(t, err)
		res, err := cw.GetQueryResults(ctx, &cloudwatchlogs.GetQueryResultsInput{QueryId: start.QueryId})
		require.NoError(t, err)
		var messages []string
		for _, row := range res.Results {
			for _, f := range row {
				if aws.ToString(f.Field) == "@message" {
					messages = append(messages, aws.ToString(f.Value))
				}
			}
		}
		return messages, res.Statistics.EstimatedRecordsSkipped
	}
	messages, skipped := query(inherited, `fields @message | filterIndex requestId = "r-2"`)
	assert.Equal(t, []string{`{"requestId":"r-2","sessionId":"s-2"}`}, messages)
	assert.Equal(t, 2.0, skipped, "the other indexed event and the unindexed one are skipped")
	messages, _ = query(own, `fields @message | filterIndex requestId = "r-2"`)
	assert.Empty(t, messages, "requestId is not indexed in a group with its own policy")

	_, err = cw.DeleteIndexPolicy(ctx, &cloudwatchlogs.DeleteIndexPolicyInput{LogGroupIdentifier: aws.String(own)})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.IndexSourceAccount, describePolicy(own).Source, "the account-level policy applies once the group's own is gone")
	inactive := indexes(own, cwlogtypes.IndexCategoryInactive)
	assert.Contains(t, inactive, "sessionId")

	for name, doc := range map[string]string{
		"no fields":           `{"Fields":[]}`,
		"field in both lists": `{"Fields":["a"],"FieldsV2":{"a":{"type":"FIELD_INDEX"}}}`,
		"unknown type":        `{"FieldsV2":{"a":{"type":"BTREE"}}}`,
		"custom @ field":      `{"Fields":["@userId"]}`,
		"too many fields":     `{"Fields":["f1","f2","f3","f4","f5","f6","f7","f8","f9","f10","f11","f12","f13","f14","f15","f16","f17","f18","f19","f20","f21"]}`,
	} {
		_, err := cw.PutIndexPolicy(ctx, &cloudwatchlogs.PutIndexPolicyInput{LogGroupIdentifier: aws.String(own), PolicyDocument: aws.String(doc)})
		var invalid *cwlogtypes.InvalidParameterException
		assert.True(t, errors.As(err, &invalid), "%s: %v", name, err)
	}
}

// TestLogs_MetricExtractionPolicyDisablesEMF stops embedded metric format
// extraction for the log groups a metric extraction policy selects and counts
// the EMF events it ignored in EMFDisabledErrors.
func TestLogs_MetricExtractionPolicyDisablesEMF(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	disabled, enabled := "/test/emf-extraction-disabled", "/test/emf-extraction-enabled"
	newPolicyLogGroup(t, cw, disabled, "s")
	newPolicyLogGroup(t, cw, enabled, "s")
	policyName := "sdk-disable-emf"
	_, err := cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String(policyName), PolicyType: cwlogtypes.PolicyTypeMetricExtractionPolicy,
		PolicyDocument:    aws.String(`{"EmbeddedMetricFormat":{"Status":"Disabled"}}`),
		SelectionCriteria: aws.String(fmt.Sprintf(`LogGroupName IN [%q]`, disabled)),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteAccountPolicy(ctx, &cloudwatchlogs.DeleteAccountPolicyInput{PolicyName: aws.String(policyName), PolicyType: cwlogtypes.PolicyTypeMetricExtractionPolicy})
	})
	_, err = cw.PutAccountPolicy(ctx, &cloudwatchlogs.PutAccountPolicyInput{
		PolicyName: aws.String("sdk-overlapping-emf"), PolicyType: cwlogtypes.PolicyTypeMetricExtractionPolicy,
		PolicyDocument:    aws.String(`{"EmbeddedMetricFormat":{"Status":"Disabled"}}`),
		SelectionCriteria: aws.String(`LogGroupNamePrefix IN ["/test/emf-extraction"]`),
	})
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "overlapping selection criteria: %v", err)

	ts := time.Now().UTC().Truncate(time.Minute)
	ns := "SDK/EMFExtraction"
	emf := func(svc string) string {
		return fmt.Sprintf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":%q,"Dimensions":[["svc"]],"Metrics":[{"Name":"hits"}]}]},"svc":%q,"hits":1}`, ts.UnixMilli(), ns, svc)
	}
	putMessages(t, cw, disabled, "s", emf("disabled"), emf("disabled"), "not emf")
	putMessages(t, cw, enabled, "s", emf("enabled"))

	metrics := cloudwatchClient()
	sum := func(namespace, name string, dims ...cwtypes.Dimension) []float64 {
		out, err := metrics.GetMetricData(ctx, &cloudwatch.GetMetricDataInput{
			StartTime: aws.Time(ts.Add(-2 * time.Minute)), EndTime: aws.Time(time.Now().Add(2 * time.Minute)),
			MetricDataQueries: []cwtypes.MetricDataQuery{{Id: aws.String("q"), MetricStat: &cwtypes.MetricStat{
				Metric: &cwtypes.Metric{Namespace: aws.String(namespace), MetricName: aws.String(name), Dimensions: dims},
				Period: aws.Int32(300), Stat: aws.String("Sum"),
			}}},
		})
		require.NoError(t, err)
		return out.MetricDataResults[0].Values
	}
	assert.Equal(t, []float64{1}, sum(ns, "hits", cwtypes.Dimension{Name: aws.String("svc"), Value: aws.String("enabled")}))
	assert.Empty(t, sum(ns, "hits", cwtypes.Dimension{Name: aws.String("svc"), Value: aws.String("disabled")}))
	assert.Equal(t, []float64{2}, sum("AWS/Logs", "EMFDisabledErrors", cwtypes.Dimension{Name: aws.String("LogGroupName"), Value: aws.String(disabled)}))
}
