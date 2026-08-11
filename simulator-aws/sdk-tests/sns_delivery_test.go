package aws_sdk_test

import (
	"fmt"
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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setQueuePolicyAllowingSNS attaches a resource policy to the queue that
// grants sns.amazonaws.com sqs:SendMessage, scoped to the given topic via an
// aws:SourceArn condition — exactly the policy the AWS console / terraform
// emits when you wire an SNS topic to an SQS queue. Real SNS→SQS delivery is
// gated by this policy.
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

// queueARNOf returns the queue's ARN from its attributes.
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

// TestSNS_DeliverToSQS_AuthorizedByQueuePolicy proves end-to-end SNS→SQS
// delivery: when the subscriber queue's resource policy admits
// sns.amazonaws.com sqs:SendMessage from the topic, a Publish actually lands a
// message in the queue (a subsequent ReceiveMessage returns it).
func TestSNS_DeliverToSQS_AuthorizedByQueuePolicy(t *testing.T) {
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

// TestSNS_DeliverToSQS_DeniedNoQueuePolicy proves the service-initiated IAM
// authorization gates real delivery: a queue with NO resource policy never
// receives the message (real AWS drops the unauthorized delivery).
func TestSNS_DeliverToSQS_DeniedNoQueuePolicy(t *testing.T) {
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("deny-nopol-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("deny-nopol-q")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl}) })

	queueARN := queueARNOf(t, sqsC, q.QueueUrl)
	// Deliberately set NO queue policy.

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

// TestSNS_DeliverToSQS_DeniedWrongSourceArn proves the SourceArn condition is
// load-bearing: a queue whose policy admits SNS only from a DIFFERENT topic
// does not receive messages published to this topic.
func TestSNS_DeliverToSQS_DeniedWrongSourceArn(t *testing.T) {
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
	// Policy names a DIFFERENT topic in the SourceArn condition.
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

// TestSNS_DeliverToLambda_AuthorizedByFunctionPolicy proves end-to-end
// SNS→Lambda delivery: when the function's resource policy (via AddPermission)
// admits sns.amazonaws.com lambda:InvokeFunction, a Publish performs a real
// in-process invoke — observable through the per-invocation CloudWatch log
// group the runtime creates (`/aws/lambda/<name>`), which exists only when the
// function actually executed.
func TestSNS_DeliverToLambda_AuthorizedByFunctionPolicy(t *testing.T) {
	snsC := snsClient()
	lc := lambdaClient()
	logs := cwLogsClient()

	fnName := "sns-deliver-fn"
	logGroup := "/aws/lambda/" + fnName

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	getFn, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	functionARN := aws.ToString(getFn.Configuration.FunctionArn)

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("sns-deliver-lambda-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	// Grant SNS permission to invoke the function (the AddPermission an SNS
	// subscription requires on real AWS).
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

	// The invoke runs in the background; poll for the log group it creates.
	require.Eventually(t, func() bool {
		out, err := logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
			LogGroupNamePrefix: aws.String(logGroup),
		})
		if err != nil {
			return false
		}
		for _, g := range out.LogGroups {
			if aws.ToString(g.LogGroupName) == logGroup {
				return true
			}
		}
		return false
	}, 5*time.Second, 50*time.Millisecond,
		"authorized SNS→Lambda delivery must invoke the function (creating its log group)")
}

// TestSNS_DeliverToLambda_DeniedNoPermission proves the IAM gate denies
// SNS→Lambda delivery when the function has no resource policy admitting SNS:
// no invoke happens, so no per-invocation log group is created.
func TestSNS_DeliverToLambda_DeniedNoPermission(t *testing.T) {
	snsC := snsClient()
	lc := lambdaClient()
	logs := cwLogsClient()

	fnName := "sns-deny-fn"
	logGroup := "/aws/lambda/" + fnName

	_, err := lc.CreateFunction(ctx, &lambda.CreateFunctionInput{
		FunctionName: aws.String(fnName),
		Role:         aws.String("arn:aws:iam::123456789012:role/test-role"),
		Runtime:      lambdatypes.RuntimeNodejs20x,
		Handler:      aws.String("index.handler"),
		Code:         &lambdatypes.FunctionCode{ZipFile: lambdaDeploymentZip(t)},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = lc.DeleteFunction(ctx, &lambda.DeleteFunctionInput{FunctionName: aws.String(fnName)}) })
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(logGroup)})
	})

	getFn, err := lc.GetFunction(ctx, &lambda.GetFunctionInput{FunctionName: aws.String(fnName)})
	require.NoError(t, err)
	functionARN := aws.ToString(getFn.Configuration.FunctionArn)

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("sns-deny-lambda-t")})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn}) })

	// No AddPermission — the function has no policy admitting SNS.
	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("lambda"),
		Endpoint: aws.String(functionARN),
	})
	require.NoError(t, err)

	_, err = snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Message:  aws.String("should-not-invoke"),
	})
	require.NoError(t, err)

	// Give any (erroneous) background invoke a chance to run, then assert the
	// log group was never created.
	time.Sleep(500 * time.Millisecond)
	out, err := logs.DescribeLogGroups(ctx, &cloudwatchlogs.DescribeLogGroupsInput{
		LogGroupNamePrefix: aws.String(logGroup),
	})
	require.NoError(t, err)
	for _, g := range out.LogGroups {
		assert.NotEqual(t, logGroup, aws.ToString(g.LogGroupName),
			"a function with no SNS permission must NOT be invoked by an SNS publish")
	}
}
