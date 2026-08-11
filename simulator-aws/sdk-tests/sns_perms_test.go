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

// TestSNS_SubscriptionAttributes round-trips a subscription's mutable
// attributes: Set RawMessageDelivery / FilterPolicy, then Get them back along
// with the fixed read-only fields (SubscriptionArn / TopicArn / Protocol /
// Endpoint / Owner / PendingConfirmation).
func TestSNS_SubscriptionAttributes(t *testing.T) {
	snsC := snsClient()
	sqsC := sqsClient()

	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("subattr-t")})
	require.NoError(t, err)
	topicARN := *tpc.TopicArn
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	q, err := sqsC.CreateQueue(ctx, &sqs.CreateQueueInput{QueueName: aws.String("subattr-q")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = sqsC.DeleteQueue(ctx, &sqs.DeleteQueueInput{QueueUrl: q.QueueUrl})
	})
	qa, err := sqsC.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       q.QueueUrl,
		AttributeNames: []sqstypes.QueueAttributeName{"QueueArn"},
	})
	require.NoError(t, err)
	queueARN := qa.Attributes["QueueArn"]

	sub, err := snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn: aws.String(topicARN),
		Protocol: aws.String("sqs"),
		Endpoint: aws.String(queueARN),
	})
	require.NoError(t, err)
	subARN := aws.ToString(sub.SubscriptionArn)
	require.NotEmpty(t, subARN)

	// Defaults before any Set.
	got, err := snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
	})
	require.NoError(t, err)
	assert.Equal(t, subARN, got.Attributes["SubscriptionArn"])
	assert.Equal(t, topicARN, got.Attributes["TopicArn"])
	assert.Equal(t, "sqs", got.Attributes["Protocol"])
	assert.Equal(t, queueARN, got.Attributes["Endpoint"])
	assert.Equal(t, "false", got.Attributes["RawMessageDelivery"])
	// sqs is auto-confirmed.
	assert.Equal(t, "false", got.Attributes["PendingConfirmation"])

	// Set RawMessageDelivery=true and a FilterPolicy, round-trip both.
	filterPolicy := `{"event":["order_placed"]}`
	for name, val := range map[string]string{
		"RawMessageDelivery": "true",
		"FilterPolicy":       filterPolicy,
	} {
		_, err = snsC.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
			SubscriptionArn: aws.String(subARN),
			AttributeName:   aws.String(name),
			AttributeValue:  aws.String(val),
		})
		require.NoError(t, err)
	}

	got, err = snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "true", got.Attributes["RawMessageDelivery"])
	assert.Equal(t, filterPolicy, got.Attributes["FilterPolicy"])

	// Clearing an attribute removes it.
	_, err = snsC.SetSubscriptionAttributes(ctx, &sns.SetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
		AttributeName:   aws.String("RawMessageDelivery"),
		AttributeValue:  aws.String(""),
	})
	require.NoError(t, err)
	got, err = snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
	})
	require.NoError(t, err)
	// Default reasserted after clearing.
	assert.Equal(t, "false", got.Attributes["RawMessageDelivery"])
}

// TestSNS_GetSubscriptionAttributes_NotFound asserts the canonical NotFound
// shape for an unknown subscription ARN.
func TestSNS_GetSubscriptionAttributes_NotFound(t *testing.T) {
	snsC := snsClient()
	_, err := snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String("arn:aws:sns:us-east-1:000000000000:nope:00000000-0000-0000-0000-000000000000"),
	})
	require.Error(t, err)
	assert.True(t,
		strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "does not exist"),
		"expected NotFound, got %v", err)
}

// TestSNS_ConfirmSubscription confirms a confirmation-required (https)
// subscription. The token is the subscription ARN with its colons stripped —
// the deterministic token the confirmation message carries; ConfirmSubscription
// flips the subscription to confirmed and is idempotent.
func TestSNS_ConfirmSubscription(t *testing.T) {
	snsC := snsClient()
	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("confirm-sub-t")})
	require.NoError(t, err)
	topicARN := *tpc.TopicArn
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	sub, err := snsC.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn:              aws.String(topicARN),
		Protocol:              aws.String("https"),
		Endpoint:              aws.String("https://example.com/hook"),
		ReturnSubscriptionArn: true,
	})
	require.NoError(t, err)
	subARN := aws.ToString(sub.SubscriptionArn)
	require.NotEmpty(t, subARN)

	// Pending before confirmation.
	got, err := snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "true", got.Attributes["PendingConfirmation"])

	token := strings.ReplaceAll(subARN, ":", "")
	conf, err := snsC.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN),
		Token:    aws.String(token),
	})
	require.NoError(t, err)
	assert.Equal(t, subARN, aws.ToString(conf.SubscriptionArn))

	got, err = snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
	})
	require.NoError(t, err)
	assert.Equal(t, "false", got.Attributes["PendingConfirmation"])

	// Idempotent re-confirm.
	conf2, err := snsC.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN),
		Token:    aws.String(token),
	})
	require.NoError(t, err)
	assert.Equal(t, subARN, aws.ToString(conf2.SubscriptionArn))

	// A bogus token is rejected.
	_, err = snsC.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN),
		Token:    aws.String("not-a-real-token"),
	})
	require.Error(t, err)
}

// TestSNS_AddRemovePermission asserts that AddPermission writes a statement
// into the topic's Policy attribute (readable via GetTopicAttributes) and that
// RemovePermission drops it.
func TestSNS_AddRemovePermission(t *testing.T) {
	snsC := snsClient()
	tpc, err := snsC.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("perm-t")})
	require.NoError(t, err)
	topicARN := *tpc.TopicArn
	t.Cleanup(func() {
		_, _ = snsC.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: aws.String(topicARN)})
	})

	_, err = snsC.AddPermission(ctx, &sns.AddPermissionInput{
		TopicArn:     aws.String(topicARN),
		Label:        aws.String("cross-acct-publish"),
		AWSAccountId: []string{"123456789012"},
		ActionName:   []string{"Publish"},
	})
	require.NoError(t, err)

	attrs, err := snsC.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
		TopicArn: aws.String(topicARN),
	})
	require.NoError(t, err)
	policy := attrs.Attributes["Policy"]
	require.NotEmpty(t, policy, "Policy attribute should carry the added statement")

	var doc struct {
		Statement []struct {
			Sid       string         `json:"Sid"`
			Effect    string         `json:"Effect"`
			Action    []string       `json:"Action"`
			Resource  string         `json:"Resource"`
			Principal map[string]any `json:"Principal"`
		} `json:"Statement"`
	}
	require.NoError(t, json.Unmarshal([]byte(policy), &doc))
	require.Len(t, doc.Statement, 1)
	assert.Equal(t, "cross-acct-publish", doc.Statement[0].Sid)
	assert.Equal(t, "Allow", doc.Statement[0].Effect)
	assert.Equal(t, []string{"SNS:Publish"}, doc.Statement[0].Action)
	assert.Equal(t, topicARN, doc.Statement[0].Resource)
	awsP, _ := json.Marshal(doc.Statement[0].Principal["AWS"])
	assert.Contains(t, string(awsP), "arn:aws:iam::123456789012:root")

	// Duplicate label is rejected.
	_, err = snsC.AddPermission(ctx, &sns.AddPermissionInput{
		TopicArn:     aws.String(topicARN),
		Label:        aws.String("cross-acct-publish"),
		AWSAccountId: []string{"123456789012"},
		ActionName:   []string{"Subscribe"},
	})
	require.Error(t, err)

	_, err = snsC.RemovePermission(ctx, &sns.RemovePermissionInput{
		TopicArn: aws.String(topicARN),
		Label:    aws.String("cross-acct-publish"),
	})
	require.NoError(t, err)

	attrs, err = snsC.GetTopicAttributes(ctx, &sns.GetTopicAttributesInput{
		TopicArn: aws.String(topicARN),
	})
	require.NoError(t, err)
	policy = attrs.Attributes["Policy"]
	if policy != "" {
		require.NoError(t, json.Unmarshal([]byte(policy), &doc))
		assert.Empty(t, doc.Statement, "statement should be gone after RemovePermission")
	}

	// Removing an absent label is a NotFound.
	_, err = snsC.RemovePermission(ctx, &sns.RemovePermissionInput{
		TopicArn: aws.String(topicARN),
		Label:    aws.String("no-such-label"),
	})
	require.Error(t, err)
}
