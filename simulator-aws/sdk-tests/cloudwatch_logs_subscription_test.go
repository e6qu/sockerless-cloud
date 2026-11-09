package aws_sdk_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	cwlogtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kinesis"
	ktypes "github.com/aws/aws-sdk-go-v2/service/kinesis/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The decompressed record the subscription guide documents.
type logsSubscriptionMessage struct {
	Owner               string   `json:"owner"`
	LogGroup            string   `json:"logGroup"`
	LogStream           string   `json:"logStream"`
	SubscriptionFilters []string `json:"subscriptionFilters"`
	MessageType         string   `json:"messageType"`
	LogEvents           []struct {
		ID        string `json:"id"`
		Timestamp int64  `json:"timestamp"`
		Message   string `json:"message"`
	} `json:"logEvents"`
}

// A Firehose object can hold several gzip members back to back.
func decodeLogsSubscriptionData(t *testing.T, data []byte) []logsSubscriptionMessage {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(data))
	require.NoError(t, err)
	raw, err := io.ReadAll(zr)
	require.NoError(t, err)
	var messages []logsSubscriptionMessage
	decoder := json.NewDecoder(bytes.NewReader(raw))
	for decoder.More() {
		var m logsSubscriptionMessage
		require.NoError(t, decoder.Decode(&m))
		messages = append(messages, m)
	}
	return messages
}

func dataMessages(messages []logsSubscriptionMessage) []logsSubscriptionMessage {
	var out []logsSubscriptionMessage
	for _, m := range messages {
		if m.MessageType == "DATA_MESSAGE" {
			out = append(out, m)
		}
	}
	return out
}

// The subscription guide's role: logs.amazonaws.com may assume it for any
// log group of the account, and it may call action on resource.
func logsDeliveryRole(t *testing.T, action, resource string) string {
	t.Helper()
	iamC := iamClient()
	name := uniquename.New("cwl-delivery")
	trust := `{"Statement":{"Effect":"Allow","Principal":{"Service":"logs.amazonaws.com"},"Action":"sts:AssumeRole",` +
		`"Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:logs:us-east-1:123456789012:*"}}}}`
	created, err := iamC.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(name), AssumeRolePolicyDocument: aws.String(trust)})
	require.NoError(t, err)
	_, err = iamC.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(name), PolicyName: aws.String("Permissions-Policy-For-CWL"),
		PolicyDocument: aws.String(`{"Statement":[{"Effect":"Allow","Action":"` + action + `","Resource":"` + resource + `"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = iamC.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(name), PolicyName: aws.String("Permissions-Policy-For-CWL")})
		_, _ = iamC.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(name)})
	})
	return aws.ToString(created.Role.Arn)
}

func createActiveStream(t *testing.T, name string) string {
	t.Helper()
	k := kinesisClient()
	_, err := k.CreateStream(ctx, &kinesis.CreateStreamInput{StreamName: aws.String(name), ShardCount: aws.Int32(1)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = k.DeleteStream(ctx, &kinesis.DeleteStreamInput{StreamName: aws.String(name)}) })
	require.NoError(t, kinesis.NewStreamExistsWaiter(k, func(o *kinesis.StreamExistsWaiterOptions) {
		o.MinDelay, o.MaxDelay = waiterMinDelay, waiterMaxDelay
	}).Wait(ctx, &kinesis.DescribeStreamInput{StreamName: aws.String(name)}, 2*time.Minute))
	summary, err := k.DescribeStreamSummary(ctx, &kinesis.DescribeStreamSummaryInput{StreamName: aws.String(name)})
	require.NoError(t, err)
	return aws.ToString(summary.StreamDescriptionSummary.StreamARN)
}

// Read the stream from its start until want DATA_MESSAGE records arrived.
func awaitStreamDataMessages(t *testing.T, stream string, want int) ([]logsSubscriptionMessage, []ktypes.Record) {
	t.Helper()
	k := kinesisClient()
	iterator, err := k.GetShardIterator(ctx, &kinesis.GetShardIteratorInput{
		StreamName: aws.String(stream), ShardId: aws.String("shardId-000000000000"), ShardIteratorType: ktypes.ShardIteratorTypeTrimHorizon,
	})
	require.NoError(t, err)
	next := iterator.ShardIterator
	var messages []logsSubscriptionMessage
	var records []ktypes.Record
	require.Eventually(t, func() bool {
		out, err := k.GetRecords(ctx, &kinesis.GetRecordsInput{ShardIterator: next})
		if err != nil {
			return false
		}
		next = out.NextShardIterator
		for _, record := range out.Records {
			if data := dataMessages(decodeLogsSubscriptionData(t, record.Data)); len(data) > 0 {
				messages = append(messages, data...)
				records = append(records, record)
			}
		}
		return len(messages) >= want
	}, 30*time.Second, 200*time.Millisecond, "stream %s did not receive %d DATA_MESSAGE record(s)", stream, want)
	return messages, records
}

func createLogGroupWithStream(t *testing.T, group, stream string) {
	t.Helper()
	cw := cwLogsClient()
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(group)})
	})
	_, err = cw.CreateLogStream(ctx, &cloudwatchlogs.CreateLogStreamInput{LogGroupName: aws.String(group), LogStreamName: aws.String(stream)})
	require.NoError(t, err)
}

func putLogLines(t *testing.T, group, stream string, lines ...string) int64 {
	t.Helper()
	now := time.Now().UnixMilli()
	events := make([]cwlogtypes.InputLogEvent, 0, len(lines))
	for _, line := range lines {
		events = append(events, cwlogtypes.InputLogEvent{Timestamp: aws.Int64(now), Message: aws.String(line)})
	}
	_, err := cwLogsClient().PutLogEvents(ctx, &cloudwatchlogs.PutLogEventsInput{
		LogGroupName: aws.String(group), LogStreamName: aws.String(stream), LogEvents: events,
	})
	require.NoError(t, err)
	return now
}

var subscriptionEventID = regexp.MustCompile(`^[1-9][0-9]{55}$`)

// A filter on an Amazon Kinesis Data Streams stream puts each ingested batch's
// matching events on the stream as the filter's role, gzipped in the
// DATA_MESSAGE envelope. A log group holds at most five filters.
func TestLogs_SubscriptionFilterDeliversToKinesis(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	const group, logStream, stream = "/test/subscription-kinesis", "app-1", "cwl-subscription-root-access"
	createLogGroupWithStream(t, group, logStream)
	streamARN := createActiveStream(t, stream)
	role := logsDeliveryRole(t, "kinesis:PutRecord", streamARN)

	_, err := cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName:   aws.String(group),
		FilterName:     aws.String("RootAccess"),
		FilterPattern:  aws.String(`{$.userIdentity.type = Root}`),
		DestinationArn: aws.String(streamARN),
		RoleArn:        aws.String(role),
	})
	require.NoError(t, err)
	described, err := cw.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	require.Len(t, described.SubscriptionFilters, 1)
	filter := described.SubscriptionFilters[0]
	assert.Equal(t, streamARN, aws.ToString(filter.DestinationArn))
	assert.Equal(t, role, aws.ToString(filter.RoleArn))
	assert.Equal(t, cwlogtypes.DistributionByLogStream, filter.Distribution)
	assert.False(t, filter.ApplyOnTransformedLogs)

	root := `{"eventVersion":"1.03","userIdentity":{"type":"Root"}}`
	at := putLogLines(t, group, logStream, root, `{"eventVersion":"1.03","userIdentity":{"type":"IAMUser"}}`, root)

	messages, _ := awaitStreamDataMessages(t, stream, 1)
	m := messages[0]
	assert.Equal(t, "123456789012", m.Owner)
	assert.Equal(t, group, m.LogGroup)
	assert.Equal(t, logStream, m.LogStream)
	assert.Equal(t, []string{"RootAccess"}, m.SubscriptionFilters)
	require.Len(t, m.LogEvents, 2, "only the events the pattern matches are forwarded")
	for _, e := range m.LogEvents {
		assert.Equal(t, root, e.Message)
		assert.Equal(t, at, e.Timestamp)
		assert.Regexp(t, subscriptionEventID, e.ID)
	}
	assert.NotEqual(t, m.LogEvents[0].ID, m.LogEvents[1].ID)

	putLogLines(t, group, logStream, root)
	_, records := awaitStreamDataMessages(t, stream, 2)
	assert.Equal(t, aws.ToString(records[0].PartitionKey), aws.ToString(records[1].PartitionKey),
		"ByLogStream groups one log stream's data under one partition key")

	for _, name := range []string{"second", "third", "fourth", "fifth"} {
		_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
			LogGroupName: aws.String(group), FilterName: aws.String(name), FilterPattern: aws.String("never-logged"),
			DestinationArn: aws.String(streamARN), RoleArn: aws.String(role),
		})
		require.NoError(t, err, name)
	}
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("sixth"), FilterPattern: aws.String(""),
		DestinationArn: aws.String(streamARN), RoleArn: aws.String(role),
	})
	var limit *cwlogtypes.LimitExceededException
	require.True(t, errors.As(err, &limit), "a sixth filter on one log group: %v", err)

	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("RootAccess"), FilterPattern: aws.String(""),
		DestinationArn: aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/cwl-no-such-stream"), RoleArn: aws.String(role),
	})
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "a filter on a stream that does not exist: %v", err)
	assert.Contains(t, invalid.ErrorMessage(), "Could not deliver test message to specified Kinesis stream")
}

// A filter on a function invokes it asynchronously as logs.amazonaws.com with
// {"awslogs":{"data":...}}, which the function's resource policy must admit.
func TestLogs_SubscriptionFilterInvokesLambda(t *testing.T) {
	t.Parallel()
	cw, lc, sqsC := cwLogsClient(), lambdaClient(), sqsClient()
	const group, logStream = "/test/subscription-lambda", "stream1"
	createLogGroupWithStream(t, group, logStream)
	functionName := uniquename.New("cwl-subscriber")
	created, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName:  aws.String(functionName),
		Role:          aws.String(lambdaRole(t, "sqs:SendMessage")),
		PackageType:   lambdatypes.PackageTypeImage,
		Code:          &lambdatypes.FunctionCode{ImageUri: aws.String(lambdaHandlerImageName)},
		Architectures: nativeLambdaArchitectures(),
		Timeout:       aws.Int32(60),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})
	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("cwl-subscriber-success"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
	_, err = lc.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:         aws.String(functionName),
		MaximumRetryAttempts: aws.Int32(0),
		DestinationConfig: &lambdatypes.DestinationConfig{
			OnSuccess: &lambdatypes.OnSuccess{Destination: aws.String(queueARNOf(t, sqsC, queue.QueueUrl))},
		},
	})
	require.NoError(t, err)

	subscribe := func() error {
		_, err := cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
			LogGroupName: aws.String(group), FilterName: aws.String("demo"), FilterPattern: aws.String(""),
			DestinationArn: created.FunctionArn,
		})
		return err
	}
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(subscribe(), &invalid), "a function that does not grant CloudWatch Logs")
	assert.Contains(t, invalid.ErrorMessage(), "Could not execute the lambda function")

	_, err = lc.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName:  aws.String(functionName),
		StatementId:   aws.String("cloudwatch-logs"),
		Principal:     aws.String("logs.amazonaws.com"),
		Action:        aws.String("lambda:InvokeFunction"),
		SourceArn:     aws.String("arn:aws:logs:us-east-1:123456789012:log-group:" + group + ":*"),
		SourceAccount: aws.String("123456789012"),
	})
	require.NoError(t, err)
	require.NoError(t, subscribe())

	putLogLines(t, group, logStream, "Simple Lambda Test")
	body := aws.ToString(receiveSQSMessages(t, sqsC, queue.QueueUrl, 1, 90*time.Second)[0].Body)
	var record struct {
		RequestPayload struct {
			Awslogs struct {
				Data string `json:"data"`
			} `json:"awslogs"`
		} `json:"requestPayload"`
	}
	require.NoError(t, json.Unmarshal([]byte(body), &record), body)
	data, err := base64.StdEncoding.DecodeString(record.RequestPayload.Awslogs.Data)
	require.NoError(t, err)
	messages := decodeLogsSubscriptionData(t, data)
	require.Len(t, messages, 1)
	assert.Equal(t, "DATA_MESSAGE", messages[0].MessageType)
	assert.Equal(t, []string{"demo"}, messages[0].SubscriptionFilters)
	require.Len(t, messages[0].LogEvents, 1)
	assert.Equal(t, "Simple Lambda Test", messages[0].LogEvents[0].Message)

	_, err = cw.DeleteSubscriptionFilter(ctx, &cloudwatchlogs.DeleteSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("demo"),
	})
	require.NoError(t, err)
	described, err := cw.DescribeSubscriptionFilters(ctx, &cloudwatchlogs.DescribeSubscriptionFiltersInput{LogGroupName: aws.String(group)})
	require.NoError(t, err)
	assert.Empty(t, described.SubscriptionFilters)
}

// A filter that applies to transformed logs matches and forwards the events
// as the log group's transformer left them, here to an Amazon Data Firehose
// stream that writes them to Amazon S3.
func TestLogs_SubscriptionFilterForwardsTransformedEventsToFirehose(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	const group, logStream = "/test/subscription-firehose", "web"
	createLogGroupWithStream(t, group, logStream)
	streamARN, bucket := createImmediateFirehose(t, "cwl-subscription-firehose")
	role := logsDeliveryRole(t, "firehose:PutRecord", streamARN)
	_, err := cw.PutTransformer(ctx, &cloudwatchlogs.PutTransformerInput{
		LogGroupIdentifier: aws.String(group),
		TransformerConfig: []cwlogtypes.Processor{
			{ParseJSON: &cwlogtypes.ParseJSON{}},
			{AddKeys: &cwlogtypes.AddKeys{Entries: []cwlogtypes.AddKeyEntry{{Key: aws.String("env"), Value: aws.String("prod")}}}},
		},
	})
	require.NoError(t, err)
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("Destination"),
		FilterPattern:          aws.String(`{ $.env = "prod" && $.level = "error" }`),
		DestinationArn:         aws.String(streamARN),
		RoleArn:                aws.String(role),
		ApplyOnTransformedLogs: true,
	})
	require.NoError(t, err)

	putLogLines(t, group, logStream, `{"level":"info","msg":"ok"}`, `{"level":"error","msg":"disk full"}`)

	s3c := s3Client()
	var delivered []logsSubscriptionMessage
	require.Eventually(t, func() bool {
		objects, err := s3c.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(bucket), Prefix: aws.String("initial/")})
		if err != nil {
			return false
		}
		delivered = nil
		for _, object := range objects.Contents {
			got, err := s3c.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: object.Key})
			require.NoError(t, err)
			body, err := io.ReadAll(got.Body)
			_ = got.Body.Close()
			require.NoError(t, err)
			delivered = append(delivered, dataMessages(decodeLogsSubscriptionData(t, body))...)
		}
		return len(delivered) == 1
	}, 60*time.Second, 500*time.Millisecond, "the stream must write the filter's one batch to the bucket")
	require.Len(t, delivered[0].LogEvents, 1)
	var event map[string]any
	require.NoError(t, json.Unmarshal([]byte(delivered[0].LogEvents[0].Message), &event))
	assert.Equal(t, map[string]any{"level": "error", "msg": "disk full", "env": "prod"}, event)
}

// A destination wraps a stream and the role CloudWatch Logs writes to it
// with; a filter that names the destination needs no role of its own.
func TestLogs_SubscriptionFilterDeliversThroughADestination(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	const group, logStream, stream = "/test/subscription-destination", "sender", "cwl-recipient-stream"
	createLogGroupWithStream(t, group, logStream)
	streamARN := createActiveStream(t, stream)
	role := logsDeliveryRole(t, "kinesis:PutRecord", streamARN)

	_, err := cw.PutDestination(ctx, &cloudwatchlogs.PutDestinationInput{
		DestinationName: aws.String("unreachable"),
		TargetArn:       aws.String("arn:aws:kinesis:us-east-1:123456789012:stream/cwl-no-such-stream"),
		RoleArn:         aws.String(role),
	})
	var invalid *cwlogtypes.InvalidParameterException
	require.True(t, errors.As(err, &invalid), "a destination whose stream does not exist: %v", err)

	put, err := cw.PutDestination(ctx, &cloudwatchlogs.PutDestinationInput{
		DestinationName: aws.String("testDestination"),
		TargetArn:       aws.String(streamARN),
		RoleArn:         aws.String(role),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteDestination(ctx, &cloudwatchlogs.DeleteDestinationInput{DestinationName: aws.String("testDestination")})
	})
	destinationARN := aws.ToString(put.Destination.Arn)
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:destination:testDestination", destinationARN)
	_, err = cw.PutDestinationPolicy(ctx, &cloudwatchlogs.PutDestinationPolicyInput{
		DestinationName: aws.String("testDestination"),
		AccessPolicy: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"123456789012"},` +
			`"Action":"logs:PutSubscriptionFilter","Resource":"` + destinationARN + `"}]}`),
	})
	require.NoError(t, err)

	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(group), FilterName: aws.String("RecipientStream"), FilterPattern: aws.String(""),
		DestinationArn: aws.String(destinationARN),
	})
	require.NoError(t, err)
	putLogLines(t, group, logStream, "shared with the recipient")

	messages, _ := awaitStreamDataMessages(t, stream, 1)
	assert.Equal(t, group, messages[0].LogGroup)
	assert.Equal(t, []string{"RecipientStream"}, messages[0].SubscriptionFilters)
	require.Len(t, messages[0].LogEvents, 1)
	assert.Equal(t, "shared with the recipient", messages[0].LogEvents[0].Message)
}

// DescribeLogGroups answers arn with the trailing ":*" and logGroupArn without
// it, with the class, the number of metric filters and the deletion and data
// protection settings. Subscription filters and transformers need the
// Standard class, and deletion protection refuses DeleteLogGroup.
func TestLogs_LogGroupShapeAndClass(t *testing.T) {
	t.Parallel()
	cw := cwLogsClient()
	const standard, essentials = "/test/shape-standard", "/test/shape-infrequent"
	_, err := cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(standard), DeletionProtectionEnabled: aws.Bool(true),
	})
	require.NoError(t, err)
	_, err = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{
		LogGroupName: aws.String(essentials), LogGroupClass: cwlogtypes.LogGroupClassInfrequentAccess,
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(essentials)})
	})
	_, err = cw.PutMetricFilter(ctx, &cloudwatchlogs.PutMetricFilterInput{
		LogGroupName: aws.String(standard), FilterName: aws.String("errors"), FilterPattern: aws.String("ERROR"),
		MetricTransformations: []cwlogtypes.MetricTransformation{{
			MetricName: aws.String("Errors"), MetricNamespace: aws.String("Shape"), MetricValue: aws.String("1"),
		}},
	})
	require.NoError(t, err)
	_, err = cw.PutDataProtectionPolicy(ctx, &cloudwatchlogs.PutDataProtectionPolicyInput{
		LogGroupIdentifier: aws.String(standard),
		PolicyDocument: aws.String(`{"Name":"data-protection","Version":"2021-06-01","Statement":[` +
			`{"Sid":"audit","DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Audit":{"FindingsDestination":{}}}},` +
			`{"Sid":"redact","DataIdentifier":["arn:aws:dataprotection::aws:data-identifier/EmailAddress"],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`),
	})
	require.NoError(t, err)

	describe := func(name string) cwlogtypes.LogGroup {
		out, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{LogGroupNamePrefix: aws.String(name)})
		require.NoError(t, err)
		require.Len(t, out.LogGroups, 1)
		return out.LogGroups[0]
	}
	g := describe(standard)
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:"+standard+":*", aws.ToString(g.Arn))
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:"+standard, aws.ToString(g.LogGroupArn))
	assert.Equal(t, cwlogtypes.LogGroupClassStandard, g.LogGroupClass)
	assert.Equal(t, int32(1), aws.ToInt32(g.MetricFilterCount))
	assert.True(t, aws.ToBool(g.DeletionProtectionEnabled))
	assert.Equal(t, cwlogtypes.DataProtectionStatusActivated, g.DataProtectionStatus)
	assert.Equal(t, cwlogtypes.LogGroupClassInfrequentAccess, describe(essentials).LogGroupClass)
	classOnly, err := cw.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String("/test/shape-"), LogGroupClass: cwlogtypes.LogGroupClassInfrequentAccess,
	})
	require.NoError(t, err)
	require.Len(t, classOnly.LogGroups, 1)
	assert.Equal(t, essentials, aws.ToString(classOnly.LogGroups[0].LogGroupName))

	_, err = cw.DeleteDataProtectionPolicy(ctx, &cloudwatchlogs.DeleteDataProtectionPolicyInput{LogGroupIdentifier: aws.String(standard)})
	require.NoError(t, err)
	assert.Equal(t, cwlogtypes.DataProtectionStatusDeleted, describe(standard).DataProtectionStatus)

	var invalidOp *cwlogtypes.InvalidOperationException
	_, err = cw.PutSubscriptionFilter(ctx, &cloudwatchlogs.PutSubscriptionFilterInput{
		LogGroupName: aws.String(essentials), FilterName: aws.String("f"), FilterPattern: aws.String(""),
		DestinationArn: aws.String("arn:aws:lambda:us-east-1:123456789012:function:any"),
	})
	require.True(t, errors.As(err, &invalidOp), "a subscription filter on an Infrequent Access group: %v", err)
	_, err = cw.PutTransformer(ctx, &cloudwatchlogs.PutTransformerInput{
		LogGroupIdentifier: aws.String(essentials),
		TransformerConfig:  []cwlogtypes.Processor{{ParseJSON: &cwlogtypes.ParseJSON{}}},
	})
	require.True(t, errors.As(err, &invalidOp), "a transformer on an Infrequent Access group: %v", err)

	_, err = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(standard)})
	var validation smithy.APIError
	require.True(t, errors.As(err, &validation), "deleting a protected log group: %v", err)
	assert.Equal(t, "ValidationException", validation.ErrorCode())
	assert.True(t, aws.ToBool(describe(standard).DeletionProtectionEnabled), "a refused delete keeps the group")
	_, err = cw.PutLogGroupDeletionProtection(ctx, &cloudwatchlogs.PutLogGroupDeletionProtectionInput{
		LogGroupIdentifier: g.LogGroupArn, DeletionProtectionEnabled: aws.Bool(false),
	})
	require.NoError(t, err)
	_, err = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(standard)})
	require.NoError(t, err)
	_, err = cw.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(standard)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = cw.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(standard)})
	})
	recreated := describe(standard)
	assert.Equal(t, int32(0), aws.ToInt32(recreated.MetricFilterCount), "deleting a log group deletes its metric filters")
	assert.Empty(t, recreated.DataProtectionStatus)
	assert.False(t, aws.ToBool(recreated.DeletionProtectionEnabled))
}
