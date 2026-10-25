package aws_sdk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	cwtypes "github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambda_AsyncFailureDestinationRuntime_SDK proves event-invoke
// configuration controls the real asynchronous runtime: a failing function
// with zero retries emits the canonical invocation record to its Amazon SQS
// OnFailure destination, sent as the execution role, whose policy allows
// sqs:SendMessage.
func TestLambda_AsyncFailureDestinationRuntime_SDK(t *testing.T) {
	t.Parallel()
	lambdaClient := lambdaClient()
	sqsClient := sqsClient()
	functionName := "async-failure-destination"
	queueName := "lambda-async-failure-destination"

	_, err := lambdaClient.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String(lambdaRole(t, "sqs:SendMessage")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t,
			`exports.handler = async () => { throw new Error("destination failure"); };`)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaClient.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})

	queue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(queueName)})
	require.NoError(t, err)
	queueURL := aws.ToString(queue.QueueUrl)
	t.Cleanup(func() {
		_, _ = sqsClient.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)})
	})
	attributes, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)
	queueARN := attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)]

	_, err = lambdaClient.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:         aws.String(functionName),
		MaximumRetryAttempts: aws.Int32(0),
		DestinationConfig: &lambdatypes.DestinationConfig{
			OnFailure: &lambdatypes.OnFailure{Destination: aws.String(queueARN)},
		},
	})
	require.NoError(t, err)

	invocationPayload := []byte(`{"source":"official-sdk","value":42}`)
	invoked, err := lambdaClient.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        invocationPayload,
	})
	require.NoError(t, err)
	assert.Equal(t, int32(202), invoked.StatusCode)

	destinationMessage := aws.ToString(receiveSQSMessages(t, sqsClient, aws.String(queueURL), 1, 20*time.Second)[0].Body)

	var record struct {
		Version         string         `json:"version"`
		RequestContext  map[string]any `json:"requestContext"`
		RequestPayload  map[string]any `json:"requestPayload"`
		ResponseContext map[string]any `json:"responseContext"`
		ResponsePayload map[string]any `json:"responsePayload"`
	}
	require.NoError(t, json.Unmarshal([]byte(destinationMessage), &record))
	assert.Equal(t, "1.0", record.Version)
	assert.Equal(t, "official-sdk", record.RequestPayload["source"])
	assert.Equal(t, "RetriesExhausted", record.RequestContext["condition"])
	assert.Equal(t, float64(1), record.RequestContext["approximateInvokeCount"])
	assert.Equal(t, "Unhandled", record.ResponseContext["functionError"])
	assert.Contains(t, record.ResponsePayload["errorMessage"], "destination failure")
}

// TestLambda_AsyncDestinationNeedsTheRolesPermission_SDK proves AWS Lambda
// sends an invocation record as the function's execution role: a role whose
// policies do not allow sqs:SendMessage delivers nothing to the Amazon SQS
// destination, and the function's DestinationDeliveryFailures metric counts
// the refused delivery.
func TestLambda_AsyncDestinationNeedsTheRolesPermission_SDK(t *testing.T) {
	t.Parallel()
	lambdaAPI := lambdaClient()
	sqsAPI := sqsClient()
	functionName := uniquename.New("async-destination-denied")

	_, err := lambdaAPI.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code: &lambdatypes.FunctionCode{ZipFile: lambdaNodeSourceZip(t,
			`exports.handler = async () => ({ delivered: true });`)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaAPI.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})

	queue, err := sqsAPI.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("lambda-destination-denied"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsAPI.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
	attributes, err := sqsAPI.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queue.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{sqstypes.QueueAttributeNameQueueArn},
	})
	require.NoError(t, err)

	// Both outcomes name the queue, so the invocation's own result decides
	// nothing: whichever record Lambda sends, the role may not send it.
	queueARN := aws.String(attributes.Attributes[string(sqstypes.QueueAttributeNameQueueArn)])
	_, err = lambdaAPI.PutFunctionEventInvokeConfig(ctx, &lambda.PutFunctionEventInvokeConfigInput{
		FunctionName:         aws.String(functionName),
		MaximumRetryAttempts: aws.Int32(0),
		DestinationConfig: &lambdatypes.DestinationConfig{
			OnSuccess: &lambdatypes.OnSuccess{Destination: queueARN},
			OnFailure: &lambdatypes.OnFailure{Destination: queueARN},
		},
	})
	require.NoError(t, err)

	invokedAt := time.Now().UTC().Add(-time.Minute)
	invoked, err := lambdaAPI.Invoke(ctx, &lambda.InvokeInput{
		FunctionName:   aws.String(functionName),
		InvocationType: lambdatypes.InvocationTypeEvent,
		Payload:        []byte(`{"source":"denied-destination"}`),
	})
	require.NoError(t, err)
	assert.Equal(t, int32(202), invoked.StatusCode)

	// CloudWatch offers nothing to wait on for a metric, so the metric is
	// polled until the delivery attempt has been counted.
	require.Eventually(t, func() bool {
		return lambdaDestinationDeliveryFailures(t, functionName, invokedAt) >= 1
	}, 2*time.Minute, time.Second, "the refused delivery must count in DestinationDeliveryFailures")

	received, err := sqsAPI.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl: queue.QueueUrl, MaxNumberOfMessages: 10, WaitTimeSeconds: 2,
	})
	require.NoError(t, err)
	assert.Empty(t, received.Messages, "a role that may not send to the queue must deliver no record")
}

// lambdaDestinationDeliveryFailures sums the function's
// DestinationDeliveryFailures metric since start.
func lambdaDestinationDeliveryFailures(t *testing.T, functionName string, start time.Time) float64 {
	t.Helper()
	stats, err := cloudwatchClient().GetMetricStatistics(ctx, &cloudwatch.GetMetricStatisticsInput{
		Namespace:  aws.String("AWS/Lambda"),
		MetricName: aws.String("DestinationDeliveryFailures"),
		Dimensions: []cwtypes.Dimension{{Name: aws.String("FunctionName"), Value: aws.String(functionName)}},
		StartTime:  aws.Time(start),
		EndTime:    aws.Time(time.Now().UTC().Add(time.Minute)),
		Period:     aws.Int32(300),
		Statistics: []cwtypes.Statistic{cwtypes.StatisticSum},
	})
	require.NoError(t, err)
	var sum float64
	for _, datapoint := range stats.Datapoints {
		sum += aws.ToFloat64(datapoint.Sum)
	}
	return sum
}
