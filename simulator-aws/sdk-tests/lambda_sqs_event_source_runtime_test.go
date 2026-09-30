package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLambda_SQSEventSourceMappingRuntime_SDK proves an event source mapping is
// an active AWS Lambda poller rather than control-plane metadata: an official
// Amazon SQS client sends a message, the Lambda runtime receives the canonical
// Records event, CloudWatch Logs contains the application output, and the
// successfully processed message is deleted.
func TestLambda_SQSEventSourceMappingRuntime_SDK(t *testing.T) {
	lambdaClient := lambdaClient()
	sqsClient := sqsClient()
	logsClient := cwLogsClient()
	functionName := "sqs-event-source-runtime"
	queueName := "lambda-sqs-event-source-runtime"

	_, err := lambdaClient.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-sqs-runtime"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaClient.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})

	queue, err := sqsClient.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName: aws.String(queueName),
		Attributes: map[string]string{
			string(sqstypes.QueueAttributeNameVisibilityTimeout): "1",
		},
	})
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
	require.NotEmpty(t, queueARN)

	mapping, err := lambdaClient.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		EventSourceArn: aws.String(queueARN),
		FunctionName:   aws.String(functionName),
		BatchSize:      aws.Int32(10),
		Enabled:        aws.Bool(true),
	})
	require.NoError(t, err)
	mappingID := aws.ToString(mapping.UUID)
	t.Cleanup(func() {
		_, _ = lambdaClient.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: aws.String(mappingID)})
	})

	_, err = sqsClient.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String("esm-message-from-official-sdk"),
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		current, getErr := lambdaClient.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{
			UUID: aws.String(mappingID),
		})
		return getErr == nil && aws.ToString(current.LastProcessingResult) == "OK"
	}, 30*time.Second, 250*time.Millisecond, "AWS Lambda event source mapping did not process the Amazon SQS message")

	logGroup := "/aws/lambda/" + functionName
	awaitLogLine(t, logsClient, logGroup, "esm-message-from-official-sdk", 10*time.Second)

	// The mapping reported the batch OK, and AWS Lambda deletes a batch it
	// processed before it reports the result: the message is gone, neither
	// visible nor in flight.
	remaining, err := sqsClient.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl: aws.String(queueURL),
		AttributeNames: []sqstypes.QueueAttributeName{
			sqstypes.QueueAttributeNameApproximateNumberOfMessages,
			sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	require.NoError(t, err)
	assert.Equal(t, "0", remaining.Attributes["ApproximateNumberOfMessages"], "successfully processed Amazon SQS message must be deleted")
	assert.Equal(t, "0", remaining.Attributes["ApproximateNumberOfMessagesNotVisible"], "successfully processed Amazon SQS message must be deleted")
	_, _ = logsClient.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
}
