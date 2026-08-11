package aws_sdk_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambda_AsyncFailureDestinationRuntime_SDK proves event-invoke
// configuration controls the real asynchronous runtime: a failing function
// with zero retries emits the canonical invocation record to its Amazon SQS
// OnFailure destination.
func TestLambda_AsyncFailureDestinationRuntime_SDK(t *testing.T) {
	lambdaClient := lambdaClient()
	sqsClient := sqsClient()
	functionName := "async-failure-destination"
	queueName := "lambda-async-failure-destination"

	_, err := lambdaClient.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-async-runtime"),
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

	var destinationMessage string
	require.Eventually(t, func() bool {
		received, receiveErr := sqsClient.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:            aws.String(queueURL),
			MaxNumberOfMessages: 1,
			WaitTimeSeconds:     1,
		})
		if receiveErr != nil || len(received.Messages) == 0 {
			return false
		}
		destinationMessage = aws.ToString(received.Messages[0].Body)
		return true
	}, 20*time.Second, 500*time.Millisecond, "AWS Lambda did not deliver its failed asynchronous invocation")

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
