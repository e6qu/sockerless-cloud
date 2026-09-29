package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/eventbridge"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AWS KMS answers a Marker it never issued with InvalidMarkerException rather
// than listing from the first key.
func TestKMS_ListKeysRejectsForeignMarker(t *testing.T) {
	_, err := kmsClient().ListKeys(ctx, &kms.ListKeysInput{Marker: aws.String("not-a-marker")})
	requireAWSErrorCode(t, err, "InvalidMarkerException")
}

// Amazon EventBridge validates a rule's event pattern when PutRule stores it.
func TestEventBridge_PutRuleRejectsInvalidPattern(t *testing.T) {
	_, err := eventbridgeClient().PutRule(ctx, &eventbridge.PutRuleInput{
		Name:         aws.String("invalid-pattern-rule"),
		EventPattern: aws.String(`{"source":[{"numeric":["!=",1]}]}`),
	})
	requireAWSErrorCode(t, err, "InvalidEventPatternException")
}

// A MessageAttributes-scoped Amazon SNS filter policy cannot nest keys.
func TestSNS_SubscribeRejectsNestedAttributePolicy(t *testing.T) {
	snsC := snsClient()
	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("nested-policy-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String("arn:aws:sqs:us-east-1:000000000000:nested-policy-q"),
		Attributes: map[string]string{
			"FilterPolicyScope": "MessageAttributes",
			"FilterPolicy":      `{"order":{"kind":["refund"]}}`,
		},
	})
	requireAWSErrorCode(t, err, "InvalidParameter")
}

// AWS Lambda rejects filter criteria whose pattern is not a valid filter, and
// deletes the Amazon SQS messages a valid one rejects without invoking the
// function.
func TestLambda_SQSEventSourceMappingFilterCriteria_SDK(t *testing.T) {
	lambdaC := lambdaClient()
	sqsC := sqsClient()
	functionName := "sqs-event-source-filtered"

	_, err := lambdaC.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(functionName),
		Role:         aws.String("arn:aws:iam::123456789012:role/lambda-sqs-filtered"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = lambdaC.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(functionName)})
	})

	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{
		QueueName:  aws.String("lambda-sqs-event-source-filtered"),
		Attributes: map[string]string{string(sqstypes.QueueAttributeNameVisibilityTimeout): "1"},
	})
	require.NoError(t, err)
	queueURL := aws.ToString(queue.QueueUrl)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: aws.String(queueURL)}) })
	queueARN := queueARNOf(t, sqsC, queue.QueueUrl)

	_, err = lambdaC.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		EventSourceArn: aws.String(queueARN),
		FunctionName:   aws.String(functionName),
		FilterCriteria: &lambdatypes.FilterCriteria{Filters: []lambdatypes.Filter{
			{Pattern: aws.String(`{"body":"not-a-list"}`)},
		}},
	})
	requireAWSErrorCode(t, err, "InvalidParameterValueException")

	mapping, err := lambdaC.CreateEventSourceMapping(ctx, &lambda.CreateEventSourceMappingInput{
		EventSourceArn: aws.String(queueARN),
		FunctionName:   aws.String(functionName),
		Enabled:        aws.Bool(true),
		FilterCriteria: &lambdatypes.FilterCriteria{Filters: []lambdatypes.Filter{
			{Pattern: aws.String(`{"body":{"kind":["order"]}}`)},
		}},
	})
	require.NoError(t, err)
	mappingID := aws.ToString(mapping.UUID)
	t.Cleanup(func() {
		_, _ = lambdaC.DeleteEventSourceMapping(ctx, &lambda.DeleteEventSourceMappingInput{UUID: aws.String(mappingID)})
	})

	_, err = sqsC.SendMessage(ctx, &sqs.SendMessageInput{
		QueueUrl:    aws.String(queueURL),
		MessageBody: aws.String(`{"kind":"refund"}`),
	})
	require.NoError(t, err)

	require.Eventually(t, func() bool {
		attrs, getErr := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl: aws.String(queueURL),
			AttributeNames: []sqstypes.QueueAttributeName{
				sqstypes.QueueAttributeNameApproximateNumberOfMessages,
				sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			},
		})
		return getErr == nil &&
			attrs.Attributes[string(sqstypes.QueueAttributeNameApproximateNumberOfMessages)] == "0" &&
			attrs.Attributes[string(sqstypes.QueueAttributeNameApproximateNumberOfMessagesNotVisible)] == "0"
	}, 30*time.Second, 250*time.Millisecond, "the filtered Amazon SQS message was not deleted")

	current, err := lambdaC.GetEventSourceMapping(ctx, &lambda.GetEventSourceMappingInput{UUID: aws.String(mappingID)})
	require.NoError(t, err)
	assert.NotEqual(t, "OK", aws.ToString(current.LastProcessingResult),
		"a message the filter rejects must not reach the function")
}
