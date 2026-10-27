package aws_sdk_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setQueuePolicyAllowingSNS attaches the queue policy the console and Terraform
// write when a topic feeds a queue: sns.amazonaws.com may sqs:SendMessage under
// an aws:SourceArn condition naming the topic. Amazon SNS delivers to the queue
// only under that policy.
func setQueuePolicyAllowingSNS(t *testing.T, sqsC *sqs.Client, queueURL, queueARN, topicARN string) {
	t.Helper()
	policy := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Service": "sns.amazonaws.com"},
    "Action": "sqs:SendMessage",
    "Resource": %q,
    "Condition": {"ArnEquals": {"aws:SourceArn": %q}}
  }]
}`, queueARN, topicARN)
	_, err := sqsC.SetQueueAttributes(ctx, &sqs.SetQueueAttributesInput{
		QueueUrl:   aws.String(queueURL),
		Attributes: map[string]string{"Policy": policy},
	})
	require.NoError(t, err)
}

func queueARNOf(t *testing.T, sqsC *sqs.Client, queueURL *string) string {
	t.Helper()
	attrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       queueURL,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	arn := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, arn)
	return arn
}

// TestSNS_DeliverToSQS_AuthorizedByQueuePolicy expects a Publish to land in a
// queue whose policy admits sns.amazonaws.com sqs:SendMessage from the topic.
func TestSNS_DeliverToSQS_AuthorizedByQueuePolicy(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("deliver-ok-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("deliver-ok-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })

	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, topicARN)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	_, err = snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Subject:  aws.String("subj"),
		Message:  aws.String("authorized-payload"),
	})
	require.NoError(t, err)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1, "authorized SNS delivery must enqueue the message")
	body := aws.ToString(recv.Messages[0].Body)
	assert.Contains(t, body, `"Type":"Notification"`)
	assert.Contains(t, body, `"Message":"authorized-payload"`)
	assert.Contains(t, body, topicARN)
}

func TestSNS_FilterPolicyAndRawSQSMessageAttributes(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	sqsC := sqsClient()

	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("filtered-delivery-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("filtered-delivery-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })

	queueARN := queueARNOf(t, sqsC, queue.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(queue.QueueUrl), queueARN, aws.ToString(topic.TopicArn))
	subscription, err := snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)
	for name, value := range map[string]string{
		"FilterPolicy":       `{"event":["order_placed"],"amount":[{"numeric":[">=",100]}]}`,
		"RawMessageDelivery": "true",
	} {
		_, err = snsC.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
			SubscriptionArn: subscription.SubscriptionArn,
			AttributeName:   aws.String(name),
			AttributeValue:  aws.String(value),
		})
		require.NoError(t, err)
	}

	publish := func(body, event, amount string) {
		t.Helper()
		_, publishErr := snsC.Publish(ctx, &sns.PublishInput{
			TopicArn: topic.TopicArn,
			Message:  aws.String(body),
			MessageAttributes: map[string]snstypes.MessageAttributeValue{
				"event":  {DataType: aws.String("String"), StringValue: aws.String(event)},
				"amount": {DataType: aws.String("Number"), StringValue: aws.String(amount)},
			},
		})
		require.NoError(t, publishErr)
	}
	publish("filtered-out", "order_cancelled", "150")
	publish("matching-order", "order_placed", "125")

	received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              queue.QueueUrl,
		MaxNumberOfMessages:   10,
		MessageAttributeNames: []string{"All"},
	})
	require.NoError(t, err)
	require.Len(t, received.Messages, 1)
	assert.Equal(t, "matching-order", aws.ToString(received.Messages[0].Body))
	require.Contains(t, received.Messages[0].MessageAttributes, "event")
	assert.Equal(t, "order_placed", aws.ToString(received.Messages[0].MessageAttributes["event"].StringValue))
	require.Contains(t, received.Messages[0].MessageAttributes, "amount")
	assert.Equal(t, "125", aws.ToString(received.Messages[0].MessageAttributes["amount"].StringValue))
}

// TestSNS_DeliverToSQS_DeniedNoQueuePolicy expects a queue without a policy
// never to receive the message: Amazon SNS drops the unauthorized delivery.
func TestSNS_DeliverToSQS_DeniedNoQueuePolicy(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("deny-nopol-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("deny-nopol-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })

	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	// The queue has no policy.

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	_, err = snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Message:  aws.String("should-not-arrive"),
	})
	require.NoError(t, err)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, recv.Messages,
		"a queue with no resource policy must NOT receive SNS-published messages")
}

// TestSNS_DeliverToSQS_DeniedWrongSourceArn expects a queue whose policy admits
// Amazon SNS only from another topic not to receive this topic's messages.
func TestSNS_DeliverToSQS_DeniedWrongSourceArn(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("deny-wrongarn-t")})
	require.NoError(t, err)
	topicARN := aws.ToString(tpc.TopicArn)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("deny-wrongarn-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })

	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	otherTopicARN := topicARN + "-other"
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, otherTopicARN)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	_, err = snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Message:  aws.String("should-not-arrive"),
	})
	require.NoError(t, err)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:            q.QueueUrl,
		MaxNumberOfMessages: 1,
	})
	require.NoError(t, err)
	assert.Empty(t, recv.Messages,
		"a queue whose SourceArn condition names a different topic must NOT receive the message")
}

// TestSNS_DeliverToLambda_AuthorizedByFunctionPolicy expects a Publish to invoke
// a function whose policy admits sns.amazonaws.com lambda:InvokeFunction,
// observed as the invocation's START line in /aws/lambda/<name>.
func TestSNS_DeliverToLambda_AuthorizedByFunctionPolicy(t *testing.T) {
	t.Parallel()
	topicName := uniquename.New("sns-deliver-lambda-t")
	snsC := snsClient()
	lc := lambdaClient()
	logs := cwLogsClient()

	fnName := uniquename.New("sns-deliver-fn")
	logGroup := "/aws/lambda/" + fnName

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	// AWS Lambda writes to the function's log group when it exists, so a Live
	// Tail session can watch it before the function runs.
	_, err = logs.CreateLogGroup(ctx, &cloudwatchlogs.CreateLogGroupInput{LogGroupName: aws.String(logGroup)})
	require.NoError(t, err)

	getFn, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	functionARN := aws.ToString(getFn.Configuration.FunctionArn)

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	// Amazon SNS needs AddPermission on the function.
	_, err = lc.AddPermission(ctx, &lambda.AddPermissionInput{
		FunctionName: aws.String(fnName),
		StatementId:  aws.String("AllowSNSInvoke"),
		Action:       aws.String("lambda:InvokeFunction"),
		Principal:    aws.String("sns.amazonaws.com"),
	})
	require.NoError(t, err)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("lambda"),
		Endpoint: aws.String(functionARN),
	})
	require.NoError(t, err)

	_, err = snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Subject:  aws.String("subj"),
		Message:  aws.String("invoke-me"),
	})
	require.NoError(t, err)

	// The invocation is asynchronous; its START line marks it.
	awaitLogLine(t, logs, logGroup, "START RequestId:", 5*time.Second)
}

// TestSNS_DeliverToLambda_DeniedNoPermission expects a function with no policy
// admitting Amazon SNS never to run, so its log group never appears, and the
// topic's Lambda delivery status log to record the failure.
func TestSNS_DeliverToLambda_DeniedNoPermission(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	lc := lambdaClient()
	logs := cwLogsClient()

	fnName := uniquename.New("sns-deny-fn")
	logGroup := "/aws/lambda/" + fnName

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })

	getFn, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	functionARN := aws.ToString(getFn.Configuration.FunctionArn)

	topicName := uniquename.New("sns-deny-lambda-t")
	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })
	failureGroup := snsDeliveryStatusLogGroup(aws.ToString(tpc.TopicArn)) + "/Failure"
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(failureGroup)})
	})

	feedbackRole := createServiceRole(t, uniquename.New("sns-deny-feedback"), "sns.amazonaws.com",
		"logs:CreateLogGroup", "logs:CreateLogStream", "logs:PutLogEvents", "logs:PutMetricFilter", "logs:PutRetentionPolicy")
	_, err = snsC.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
		TopicArn:       tpc.TopicArn,
		AttributeName:  aws.String("LambdaFailureFeedbackRoleArn"),
		AttributeValue: aws.String(feedbackRole),
	})
	require.NoError(t, err)

	// No AddPermission, so no policy admits Amazon SNS.
	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("lambda"),
		Endpoint: aws.String(functionARN),
	})
	require.NoError(t, err)

	published, err := snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Message:  aws.String("should-not-invoke"),
	})
	require.NoError(t, err)

	entry := snsAwaitDeliveryStatus(t, logs, failureGroup, aws.ToString(published.MessageId))
	assert.Equal(t, "FAILURE", entry.Status)
	assert.Equal(t, aws.ToString(tpc.TopicArn), entry.Notification.TopicArn)
	assert.Equal(t, functionARN, entry.Delivery.Destination)
	assert.Equal(t, 403, entry.Delivery.StatusCode)
	assert.Contains(t, entry.Delivery.ProviderResponse, "AccessDeniedException")

	out, err := logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroup),
	})
	require.NoError(t, err)
	for _, g := range out.LogGroups {
		assert.NotEqual(t, logGroup, aws.ToString(g.LogGroupName),
			"a function with no SNS permission must NOT be invoked by an SNS publish")
	}
}

// snsDeliveryStatusLogGroup is the log group Amazon SNS writes a topic's
// successful deliveries to: sns/<region>/<account-id>/<topic-name>.
func snsDeliveryStatusLogGroup(topicARN string) string {
	parts := strings.Split(topicARN, ":")
	return "sns/" + parts[3] + "/" + parts[4] + "/" + parts[5]
}

type snsDeliveryStatusEntry struct {
	Notification struct {
		MessageMD5Sum string `json:"messageMD5Sum"`
		MessageID     string `json:"messageId"`
		TopicArn      string `json:"topicArn"`
		Timestamp     string `json:"timestamp"`
	} `json:"notification"`
	Delivery struct {
		DeliveryID       string `json:"deliveryId"`
		Destination      string `json:"destination"`
		ProviderResponse string `json:"providerResponse"`
		DwellTimeMs      int64  `json:"dwellTimeMs"`
		Attempts         int    `json:"attempts"`
		StatusCode       int    `json:"statusCode"`
	} `json:"delivery"`
	Status string `json:"status"`
}

// snsAwaitDeliveryStatus waits for the delivery status entry of one message in
// the group and decodes it.
func snsAwaitDeliveryStatus(t *testing.T, logs *cloudwatchlogs.Client, group, messageID string) snsDeliveryStatusEntry {
	t.Helper()
	line := awaitLogLine(t, logs, group, messageID, 30*time.Second)
	var entry snsDeliveryStatusEntry
	require.NoError(t, json.Unmarshal([]byte(line), &entry), line)
	require.Equal(t, messageID, entry.Notification.MessageID)
	return entry
}

// TestSNS_ManagedEndpointRefusalsAreRedriven expects Amazon SNS to hand a
// notification an Amazon SQS or AWS Lambda subscription refused to the
// subscription's dead-letter queue with the RequestID, ErrorCode and
// ErrorMessage attributes, and to count each redrive.
func TestSNS_ManagedEndpointRefusalsAreRedriven(t *testing.T) {
	t.Parallel()
	snsC, sqsC, lc := snsClient(), sqsClient(), lambdaClient()
	since := time.Now().UTC().Add(-time.Minute)

	topicName := uniquename.New("sns-managed-refused")
	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName)})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	dlq, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("sns-managed-dlq"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := queueARNOf(t, sqsC, dlq.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(dlq.QueueUrl), dlqARN, topicARN)
	redrive := map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlqARN + `"}`}

	closed, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniquename.New("sns-managed-closed"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: closed.QueueUrl}) })
	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(queueARNOf(t, sqsC, closed.QueueUrl)),
		Attributes: redrive,
	})
	require.NoError(t, err)

	fnName := uniquename.New("sns-managed-fn")
	fn, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String(serviceRole(t, "lambda.amazonaws.com")),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })
	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("lambda"), Endpoint: fn.FunctionArn, Attributes: redrive,
	})
	require.NoError(t, err)

	published, err := snsC.Publish(ctx, &sns.PublishInput{TopicArn: topic.TopicArn, Message: aws.String("refused twice")})
	require.NoError(t, err)

	codes := map[string]bool{}
	for len(codes) < 2 {
		received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 20, MaxNumberOfMessages: 10, MessageAttributeNames: []string{"All"},
		})
		require.NoError(t, err)
		require.NotEmpty(t, received.Messages, "both refused notifications reach the dead-letter queue")
		for _, m := range received.Messages {
			var envelope map[string]any
			require.NoError(t, json.Unmarshal([]byte(aws.ToString(m.Body)), &envelope))
			assert.Equal(t, "Notification", envelope["Type"])
			assert.Equal(t, aws.ToString(published.MessageId), envelope["MessageId"])
			assert.Equal(t, "refused twice", envelope["Message"])
			assert.NotEmpty(t, aws.ToString(m.MessageAttributes["RequestID"].StringValue))
			assert.NotEmpty(t, aws.ToString(m.MessageAttributes["ErrorMessage"].StringValue))
			codes[aws.ToString(m.MessageAttributes["ErrorCode"].StringValue)] = true
		}
	}
	assert.Equal(t, map[string]bool{"AccessDenied": true, "AccessDeniedException": true}, codes)
	assert.Equal(t, float64(2), snsTopicMetricSum(t, topicName, "NumberOfNotificationsRedrivenToDlq", since))
}
