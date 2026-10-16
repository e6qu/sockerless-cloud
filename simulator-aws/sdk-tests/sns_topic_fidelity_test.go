package aws_sdk_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatchlogs"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	snstypes "github.com/aws/aws-sdk-go-v2/service/sns/types"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A topic no one gave a policy answers the owner-only default policy and the
// service's default HTTP delivery policy.
func TestSNS_NewTopicDefaults(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(uniqueName("sns-defaults"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })
	attrs, err := snsC.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{TopicArn: topic.TopicArn})
	require.NoError(t, err)

	var policy struct {
		Id        string
		Statement []struct {
			Sid       string
			Resource  string
			Condition map[string]map[string]string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(attrs.Attributes["Policy"]), &policy))
	assert.Equal(t, "__default_policy_ID", policy.Id)
	require.Len(t, policy.Statement, 1)
	assert.Equal(t, "__default_statement_ID", policy.Statement[0].Sid)
	assert.Equal(t, aws.ToString(topic.TopicArn), policy.Statement[0].Resource)
	assert.Equal(t, attrs.Attributes["Owner"], policy.Statement[0].Condition["StringEquals"]["AWS:SourceOwner"])

	var effective struct {
		HTTP struct {
			DefaultHealthyRetryPolicy struct {
				NumRetries      int
				MinDelayTarget  int
				BackoffFunction string
			}
			DisableSubscriptionOverrides bool
		}
	}
	require.NoError(t, json.Unmarshal([]byte(attrs.Attributes["EffectiveDeliveryPolicy"]), &effective))
	assert.Equal(t, 3, effective.HTTP.DefaultHealthyRetryPolicy.NumRetries)
	assert.Equal(t, 20, effective.HTTP.DefaultHealthyRetryPolicy.MinDelayTarget)
	assert.Equal(t, "linear", effective.HTTP.DefaultHealthyRetryPolicy.BackoffFunction)
}

// An attribute name the operation does not document is refused rather than
// stored to do nothing.
func TestSNS_UnknownTopicAttributeIsRefused(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	_, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(uniqueName("sns-bad-attr")),
		Attributes: map[string]string{"DisplayNmae": "typo"},
	})
	var apiErr smithy.APIError
	require.True(t, errors.As(err, &apiErr), "CreateTopic with an unknown attribute: %v", err)
	assert.Equal(t, "InvalidParameter", apiErr.ErrorCode())

	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(uniqueName("sns-typo"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })
	_, err = snsC.SetTopicAttributes(ctx, &sns.SetTopicAttributesInput{
		TopicArn:       topic.TopicArn,
		AttributeName:  aws.String("LambdaFailureFeedbackRoleARN"),
		AttributeValue: aws.String("arn:aws:iam::123456789012:role/feedback"),
	})
	require.True(t, errors.As(err, &apiErr), "SetTopicAttributes with a misspelled name: %v", err)
	assert.Equal(t, "InvalidParameter", apiErr.ErrorCode())
}

// A subscription's filter policy publishes the NumberOfNotificationsFilteredOut
// variant that says why it dropped a message.
func TestSNS_FilteredOutMetrics(t *testing.T) {
	t.Parallel()
	snsC, sqsC := snsClient(), sqsClient()
	since := time.Now().UTC().Add(-time.Minute)
	topicName := uniqueName("sns-filtered")
	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String(topicName)})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })

	subscribe := func(scope, policy string) {
		t.Helper()
		queue, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniqueName("sns-filter-q"))})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: queue.QueueUrl}) })
		queueARN := queueARNOf(t, sqsC, queue.QueueUrl)
		setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(queue.QueueUrl), queueARN, topicARN)
		_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
			TopicArn: topic.TopicArn, Protocol: aws.String("sqs"), Endpoint: aws.String(queueARN),
			Attributes: map[string]string{"FilterPolicyScope": scope, "FilterPolicy": policy},
		})
		require.NoError(t, err)
	}
	subscribe("MessageAttributes", `{"event":["order_placed"]}`)
	subscribe("MessageBody", `{"kind":["invoice"]}`)

	publish := func(message string, attrs map[string]snstypes.MessageAttributeValue) {
		t.Helper()
		_, err := snsC.Publish(ctx, &sns.PublishInput{TopicArn: topic.TopicArn, Message: aws.String(message), MessageAttributes: attrs})
		require.NoError(t, err)
	}
	publish("not json", nil)
	publish(`{"kind":"receipt"}`, map[string]snstypes.MessageAttributeValue{
		"event": {DataType: aws.String("String"), StringValue: aws.String("order_cancelled")},
	})

	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfNotificationsFilteredOut-NoMessageAttributes", since))
	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfNotificationsFilteredOut-InvalidMessageBody", since))
	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfNotificationsFilteredOut-MessageBody", since))
	assert.Equal(t, float64(4), snsTopicMetricSum(t, topicName, "NumberOfNotificationsFilteredOut", since),
		"every rejection counts toward the total")
}

// An HTTP endpoint that refuses a notification has its answer recorded as the
// delivery's providerResponse, and the message moves to the subscription's
// dead-letter queue, which the redrive metric counts.
func TestSNS_HTTPRefusalIsLoggedAndRedriven(t *testing.T) {
	t.Parallel()
	snsC, sqsC, logs := snsClient(), sqsClient(), cwLogsClient()
	confirmations := make(chan string, 1)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if r.Header.Get("x-amz-sns-message-type") == "SubscriptionConfirmation" {
			var envelope map[string]any
			if json.Unmarshal(body, &envelope) == nil {
				token, _ := envelope["Token"].(string)
				confirmations <- token
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte("rejected by the endpoint"))
	}))
	t.Cleanup(endpoint.Close)

	since := time.Now().UTC().Add(-time.Minute)
	role := snsFeedbackRole(t)
	topicName := uniqueName("sns-http-refused")
	topic, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{
		Name:       aws.String(topicName),
		Attributes: map[string]string{"HTTPFailureFeedbackRoleArn": role},
	})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() { _, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: topic.TopicArn}) })
	failureGroup := snsDeliveryStatusLogGroup(topicARN) + "/Failure"
	t.Cleanup(func() {
		_, _ = logs.DeleteLogGroup(ctx, &cloudwatchlogs.DeleteLogGroupInput{LogGroupName: aws.String(failureGroup)})
	})

	dlq, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String(uniqueName("sns-http-dlq"))})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: dlq.QueueUrl}) })
	dlqARN := queueARNOf(t, sqsC, dlq.QueueUrl)
	setQueuePolicyAllowingSNS(t, sqsC, aws.ToString(dlq.QueueUrl), dlqARN, topicARN)

	_, err = snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: topic.TopicArn, Protocol: aws.String("http"), Endpoint: aws.String(endpoint.URL),
		Attributes: map[string]string{"RedrivePolicy": `{"deadLetterTargetArn":"` + dlqARN + `"}`},
	})
	require.NoError(t, err)
	select {
	case token := <-confirmations:
		_, err = snsC.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{TopicArn: topic.TopicArn, Token: aws.String(token)})
		require.NoError(t, err)
	case <-time.After(30 * time.Second):
		t.Fatal("the endpoint never received its subscription confirmation")
	}

	published, err := snsC.Publish(ctx, &sns.PublishInput{TopicArn: topic.TopicArn, Message: aws.String("refused payload")})
	require.NoError(t, err)

	// Amazon SNS logs the failed delivery before it moves the message to the
	// dead-letter queue, so the queue's message says the log entry exists.
	received, err := sqsC.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{QueueUrl: dlq.QueueUrl, WaitTimeSeconds: 20})
	require.NoError(t, err)
	require.Len(t, received.Messages, 1, "the refused notification reaches the dead-letter queue")
	assert.Equal(t, float64(1), snsTopicMetricSum(t, topicName, "NumberOfNotificationsRedrivenToDlq", since))

	entry := snsAwaitDeliveryStatus(t, logs, failureGroup, aws.ToString(published.MessageId))
	assert.Equal(t, "FAILURE", entry.Status)
	assert.Equal(t, 400, entry.Delivery.StatusCode)
}
