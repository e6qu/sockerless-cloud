package aws_sdk_test

import (
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/apigatewayv2"
	"github.com/aws/aws-sdk-go-v2/service/cloudtrail"
	cttypes "github.com/aws/aws-sdk-go-v2/service/cloudtrail/types"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedtypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ctLookup reads every page of a LookupEvents query: a page holds at most 50
// events, and the parallel suite records more than that between a test's call
// and its lookup.
func ctLookup(t *testing.T, ct *cloudtrail.Client, key cttypes.LookupAttributeKey, value string) []cttypes.Event {
	t.Helper()
	pages := cloudtrail.NewLookupEventsPaginator(ct, &cloudtrail.LookupEventsInput{
		LookupAttributes: []cttypes.LookupAttribute{{AttributeKey: key, AttributeValue: aws.String(value)}},
	})
	var events []cttypes.Event
	for pages.HasMorePages() {
		page, err := pages.NextPage(ctx)
		require.NoError(t, err)
		events = append(events, page.Events...)
	}
	return events
}

func ctHasEventNamed(events []cttypes.Event, name string) bool {
	for _, e := range events {
		if aws.ToString(e.EventName) == name {
			return true
		}
	}
	return false
}

// TestCloudTrailLookupFilterKeysSDK covers the EventId, ResourceName,
// ResourceType and ReadOnly lookup keys on an Amazon ECS CreateCluster, a
// resource-bearing management event.
func TestCloudTrailLookupFilterKeysSDK(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	ecsc := ecsClient()
	const cluster = "ct-filter-cluster"

	_, err := ecsc.CreateCluster(ctx, &ecs.CreateClusterInput{ClusterName: aws.String(cluster)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = ecsc.DeleteCluster(ctx, &ecs.DeleteClusterInput{Cluster: aws.String(cluster)})
	})

	byName := ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "CreateCluster")
	require.True(t, ctHasEventNamed(byName, "CreateCluster"),
		"LookupEvents by EventName must return the CreateCluster event")
	var eventID, src string
	for _, e := range byName {
		if aws.ToString(e.EventName) == "CreateCluster" {
			eventID = aws.ToString(e.EventId)
			src = aws.ToString(e.EventSource)
		}
	}
	assert.Equal(t, "ecs.amazonaws.com", src)
	require.NotEmpty(t, eventID)

	byID := ctLookup(t, ct, cttypes.LookupAttributeKeyEventId, eventID)
	require.Len(t, byID, 1, "EventId filter must return exactly the one matching event")
	assert.Equal(t, eventID, aws.ToString(byID[0].EventId))

	byResName := ctLookup(t, ct, cttypes.LookupAttributeKeyResourceName, cluster)
	require.True(t, ctHasEventNamed(byResName, "CreateCluster"),
		"ResourceName filter must return events touching the named resource")

	byResType := ctLookup(t, ct, cttypes.LookupAttributeKeyResourceType, "AWS::ECS::Cluster")
	require.True(t, ctHasEventNamed(byResType, "CreateCluster"),
		"ResourceType filter must return events of that resource type")

	// ReadOnly=true excludes a mutating call.
	rw := ctLookup(t, ct, cttypes.LookupAttributeKeyReadOnly, "false")
	assert.True(t, ctHasEventNamed(rw, "CreateCluster"), "CreateCluster is a write event (ReadOnly=false)")
	ro := ctLookup(t, ct, cttypes.LookupAttributeKeyReadOnly, "true")
	assert.False(t, ctHasEventNamed(ro, "CreateCluster"), "CreateCluster must not appear under ReadOnly=true")

	// A non-matching ResourceName returns nothing, so the filter applies.
	none := ctLookup(t, ct, cttypes.LookupAttributeKeyResourceName, "no-such-resource-xyz")
	assert.False(t, ctHasEventNamed(none, "CreateCluster"),
		"a non-matching ResourceName filter must not return the CreateCluster event")
}

// TestCloudTrailRecordsSchedulerAPICallsSDK expects Amazon EventBridge Scheduler
// calls, which bypass the central POST / router, recorded against
// scheduler.amazonaws.com.
func TestCloudTrailRecordsSchedulerAPICallsSDK(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	sched := schedulerClient()
	const name = "ct-api-schedule"

	_, err := sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String(name),
		ScheduleExpression: aws.String("rate(1 hour)"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:sqs:us-east-1:123456789012:ct-api-queue"),
			RoleArn: aws.String(serviceRole(t, "scheduler.amazonaws.com")),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String(name)})
	})

	byName := ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "CreateSchedule")
	require.True(t, ctHasEventNamed(byName, "CreateSchedule"),
		"Scheduler CreateSchedule must be recorded in CloudTrail")
	for _, e := range byName {
		if aws.ToString(e.EventName) == "CreateSchedule" {
			assert.Equal(t, "scheduler.amazonaws.com", aws.ToString(e.EventSource))
		}
	}

	byRes := ctLookup(t, ct, cttypes.LookupAttributeKeyResourceName, name)
	assert.True(t, ctHasEventNamed(byRes, "CreateSchedule"),
		"the CreateSchedule event must carry the schedule as a ResourceName")
}

// TestCloudTrailRecordsSchedulerFiredTargetSDK expects a scheduler-fired Amazon
// SQS target to deliver, while the SendMessage it makes, a data event, stays
// out of LookupEvents. The RunTask tests cover service-initiated management
// targets, recorded with invokedBy=scheduler.amazonaws.com.
func TestCloudTrailRecordsSchedulerFiredTargetSDK(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	sched := schedulerClient()
	sqsc := sqsClient()
	const queue = "ct-fire-queue"

	cq, err := sqsc.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queue)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsc.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: cq.QueueUrl}) })

	fireAt := time.Now().UTC().Add(3 * time.Second).Format("2006-01-02T15:04:05")
	_, err = sched.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name:               aws.String("ct-fire-sqs"),
		ScheduleExpression: aws.String("at(" + fireAt + ")"),
		FlexibleTimeWindow: &schedtypes.FlexibleTimeWindow{Mode: schedtypes.FlexibleTimeWindowModeOff},
		Target: &schedtypes.Target{
			Arn:     aws.String("arn:aws:sqs:us-east-1:123456789012:" + queue),
			RoleArn: aws.String(createServiceRole(t, "scheduler-ct-fire-sqs", "scheduler.amazonaws.com", "sqs:SendMessage")),
			Input:   aws.String("scheduled-payload"),
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sched.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{Name: aws.String("ct-fire-sqs")})
	})

	delivered, err := sqsc.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: cq.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 20,
	})
	require.NoError(t, err)
	require.Len(t, delivered.Messages, 1, "scheduler must deliver the SendMessage to the queue")
	assert.Equal(t, "scheduled-payload", aws.ToString(delivered.Messages[0].Body))

	// SendMessage is a data event and stays out of LookupEvents whoever initiates
	// it.
	for _, e := range ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "SendMessage") {
		assert.NotEqual(t, "sqs.amazonaws.com", aws.ToString(e.EventSource),
			"SQS SendMessage is a data event and must not appear in LookupEvents")
	}
}

func TestCloudTrailRecordsRESTServiceAPICallsSDK(t *testing.T) {
	t.Parallel()
	ct := cloudTrailClient()
	s3c := s3Client()
	lambdac := lambdaClient()
	apigw := apigwv2Client()
	cw := cloudwatchClient()

	bucket := "sdk-ct-rest-bucket"
	_, err := s3c.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = s3c.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)})
	})

	_, err = lambdac.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String("missing-ct-function")})
	requireAWSErrorCode(t, err, "ResourceNotFoundException")

	api, err := apigw.CreateApi(ctx, &apigatewayv2.CreateApiInput{
		Name:         aws.String("ct-rest-api"),
		ProtocolType: "HTTP",
	})
	require.NoError(t, err)
	apiID := aws.ToString(api.ApiId)
	t.Cleanup(func() {
		_, _ = apigw.DeleteApi(ctx, &apigatewayv2.DeleteApiInput{ApiId: aws.String(apiID)})
	})

	_, err = cw.PutMetricData(ctx, &cloudwatch.PutMetricDataInput{
		Namespace: aws.String("CT/REST"),
		MetricData: []cwtypes.MetricDatum{{
			MetricName: aws.String("Recorded"),
			Value:      aws.Float64(1),
		}},
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		return ctHasEventNamed(ctLookup(t, ct, cttypes.LookupAttributeKeyEventSource, "s3.amazonaws.com"), "CreateBucket") &&
			ctHasEventNamed(ctLookup(t, ct, cttypes.LookupAttributeKeyEventSource, "lambda.amazonaws.com"), "GetFunction") &&
			ctHasEventNamed(ctLookup(t, ct, cttypes.LookupAttributeKeyEventSource, "apigateway.amazonaws.com"), "CreateApi") &&
			ctHasEventNamed(ctLookup(t, ct, cttypes.LookupAttributeKeyEventSource, "monitoring.amazonaws.com"), "PutMetricData")
	}, 10*time.Second, 500*time.Millisecond)

	lambdaEvents := ctLookup(t, ct, cttypes.LookupAttributeKeyEventName, "GetFunction")
	require.NotEmpty(t, lambdaEvents)
	foundError := false
	for _, ev := range lambdaEvents {
		record := aws.ToString(ev.CloudTrailEvent)
		if strings.Contains(record, "ResourceNotFoundException") && strings.Contains(record, "missing-ct-function") {
			foundError = true
			break
		}
	}
	assert.True(t, foundError, "failed Lambda REST call must carry CloudTrail errorCode/errorMessage")
}
