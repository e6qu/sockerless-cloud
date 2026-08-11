package aws_sdk_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func snsClient() *sns.Client {
	return sns.NewFromConfig(sdkConfig(), func(o *sns.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestSNS_TopicLifecycle exercises the Create / Get / List / Delete
// flow that every runner integration tutorial runs against SNS.
func TestSNS_TopicLifecycle(t *testing.T) {
	client := snsClient()
	create, err := client.CreateTopic(ctx, &sns.CreateTopicInput{
		Name: aws.String("life-topic"),
	})
	require.NoError(t, err)
	require.NotNil(t, create.TopicArn)
	topicARN := *create.TopicArn
	t.Cleanup(func() {
		_, _ = client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	list, err := client.ListTopics(ctx, &sns.ListTopicsInput{})
	require.NoError(t, err)
	found := false
	for _, tpc := range list.Topics {
		if aws.ToString(tpc.TopicArn) == topicARN {
			found = true
			break
		}
	}
	assert.True(t, found, "topic should appear in ListTopics")

	attrs, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
		TopicArn: aws.String(topicARN),
	})
	require.NoError(t, err)
	assert.Equal(t, topicARN, attrs.Attributes["TopicArn"])
	assert.Equal(t, "0", attrs.Attributes["SubscriptionsConfirmed"])

	_, err = client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	require.NoError(t, err)
}

// TestSNS_SubscribeAndUnsubscribe asserts subscription CRUD against
// an SQS endpoint protocol.
func TestSNS_SubscribeAndUnsubscribe(t *testing.T) {
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("sub-t")})
	require.NoError(t, err)
	topicARN := *tpc.TopicArn
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("sub-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})

	// SQS subscription endpoint is the queue ARN.
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	sub, err := snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topicARN),
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)
	require.NotNil(t, sub.SubscriptionArn)

	listSubs, err := snsC.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{
		TopicArn: aws.String(topicARN),
	})
	require.NoError(t, err)
	require.Len(t, listSubs.Subscriptions, 1)
	assert.Equal(t, "sqs", aws.ToString(listSubs.Subscriptions[0].Protocol))
	assert.Equal(t, queueARN, aws.ToString(listSubs.Subscriptions[0].Endpoint))

	_, err = snsC.Unsubscribe(ctx, &sns.UnsubscribeInput{SubscriptionArn: sub.SubscriptionArn})
	require.NoError(t, err)

	listAfter, err := snsC.ListSubscriptionsByTopic(ctx, &sns.ListSubscriptionsByTopicInput{
		TopicArn: aws.String(topicARN),
	})
	require.NoError(t, err)
	assert.Empty(t, listAfter.Subscriptions)
}

func TestSNS_SubscribeConfirmationRequiredProtocols(t *testing.T) {
	snsC := snsClient()
	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("confirm-t")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	emailSub, err := snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(aws.ToString(tpc.TopicArn)),
		Protocol: aws.String("email"),
		Endpoint: aws.String("ops@example.com"),
	})
	require.NoError(t, err)
	assert.Equal(t, "pending confirmation", aws.ToString(emailSub.SubscriptionArn))

	returnedSub, err := snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn:              tpc.TopicArn,
		Protocol:              aws.String("https"),
		Endpoint:              aws.String("https://example.com/hook"),
		ReturnSubscriptionArn: true,
	})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(returnedSub.SubscriptionArn), ":confirm-t:")

	attrs, err := snsC.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: tpc.TopicArn})
	require.NoError(t, err)
	assert.Equal(t, "0", attrs.Attributes["SubscriptionsConfirmed"])
	assert.Equal(t, "2", attrs.Attributes["SubscriptionsPending"])
}

// TestSNS_PublishFanoutToSQS asserts the load-bearing real-world
// pattern: SNS topic with SQS subscriber, Publish message lands in
// the subscriber queue's Messages.
func TestSNS_PublishFanoutToSQS(t *testing.T) {
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("fanout-t")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: tpc.TopicArn})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("fanout-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	queueAttrs, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := queueAttrs.Attributes["QueueArn"]

	// Real SNS→SQS delivery is gated by the queue's resource policy: the
	// queue must grant sns.amazonaws.com sqs:SendMessage from this topic.
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(q.QueueUrl), queueARN, aws.ToString(tpc.TopicArn))

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: tpc.TopicArn,
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)

	_, err = snsC.Publish(ctx, &sns.PublishInput{
		TopicArn: tpc.TopicArn,
		Subject:  aws.String("hello"),
		Message:  aws.String("payload-1"),
	})
	require.NoError(t, err)

	recv, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: q.QueueUrl})
	require.NoError(t, err)
	require.Len(t, recv.Messages, 1)
	envelopeBody := aws.ToString(recv.Messages[0].Body)
	assert.Contains(t, envelopeBody, `"Type":"Notification"`,
		"SNS-fanned-out message body should carry the canonical Notification envelope")
	assert.Contains(t, envelopeBody, `"Message":"payload-1"`)
	assert.Contains(t, envelopeBody, aws.ToString(tpc.TopicArn))

	// Sanity: envelope is valid JSON.
	var env map[string]any
	require.NoError(t, json.Unmarshal([]byte(envelopeBody), &env))
	assert.Equal(t, "payload-1", env["Message"])
}

// TestSNS_NonExistentTopic confirms canonical error shape.
func TestSNS_NonExistentTopic(t *testing.T) {
	client := snsClient()
	_, err := client.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
		TopicArn: aws.String("arn:aws:sns:us-east-1:000000000000:nope"),
	})
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "NotFound") ||
			strings.Contains(err.Error(), "does not exist"),
		"expected NotFound error, got %v", err)
}

func TestSNS_ListTopics_Pagination(t *testing.T) {
	client := snsClient()
	var arns []string
	for _, n := range []string{"pag-topic-a", "pag-topic-b", "pag-topic-c"} {
		out, err := client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(n)})
		require.NoError(t, err)
		arns = append(arns, aws.ToString(out.TopicArn))
		arn := aws.ToString(out.TopicArn)
		t.Cleanup(func() { client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(arn)}) })
	}

	// SNS SDK v2 has no built-in paginator for ListTopics; iterate manually via NextToken.
	seen := map[string]bool{}
	var token *string
	for {
		out, err := client.ListTopics(ctx, &sns.ListTopicsInput{NextToken: token})
		require.NoError(t, err)
		for _, t := range out.Topics {
			seen[aws.ToString(t.TopicArn)] = true
		}
		if out.NextToken == nil || *out.NextToken == "" {
			break
		}
		token = out.NextToken
	}
	for _, arn := range arns {
		assert.True(t, seen[arn], "topic %s should appear via pagination", arn)
	}
}
