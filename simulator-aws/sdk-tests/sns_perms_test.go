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

// TestSNS_SubscriptionAttributes round-trips RawMessageDelivery and
// FilterPolicy, and reads the fixed SubscriptionArn, TopicArn, Protocol,
// Endpoint, Owner and PendingConfirmation.
func TestSNS_SubscriptionAttributes(t *testing.T) {
	t.Parallel()
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

	got, err := snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String(subARN),
	})
	require.NoError(t, err)
	assert.Equal(t, subARN, got.Attributes["SubscriptionArn"])
	assert.Equal(t, topicARN, got.Attributes["TopicArn"])
	assert.Equal(t, "sqs", got.Attributes["Protocol"])
	assert.Equal(t, queueARN, got.Attributes["Endpoint"])
	assert.Equal(t, "false", got.Attributes["RawMessageDelivery"])
	// An Amazon SQS subscription confirms itself.
	assert.Equal(t, "false", got.Attributes["PendingConfirmation"])

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
	// Clearing restores the default.
	assert.Equal(t, "false", got.Attributes["RawMessageDelivery"])
}

func TestSNS_GetSubscriptionAttributes_NotFound(t *testing.T) {
	t.Parallel()
	snsC := snsClient()
	_, err := snsC.GetSubscriptionAttributes(ctx, &sns.GetSubscriptionAttributesInput{
		SubscriptionArn: aws.String("arn:aws:sns:us-east-1:000000000000:nope:00000000-0000-0000-0000-000000000000"),
	})
	requireAWSErrorCode(t, err, "NotFound")
	assert.True(t,
		strings.Contains(err.Error(), "NotFound") || strings.Contains(err.Error(), "does not exist"),
		"expected NotFound, got %v", err)
}

// TestSNS_ConfirmSubscription confirms an https subscription with the token its
// confirmation message carries, the subscription ARN without colons;
// ConfirmSubscription is idempotent.
func TestSNS_ConfirmSubscription(t *testing.T) {
	t.Parallel()
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

	conf2, err := snsC.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN),
		Token:    aws.String(token),
	})
	require.NoError(t, err)
	assert.Equal(t, subARN, aws.ToString(conf2.SubscriptionArn))

	_, err = snsC.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: aws.String(topicARN),
		Token:    aws.String("not-a-real-token"),
	})
	require.Error(t, err)
}

// TestSNS_AddRemovePermission expects AddPermission to add a statement to the
// topic's Policy attribute and RemovePermission to drop it.
func TestSNS_AddRemovePermission(t *testing.T) {
	t.Parallel()
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
	require.Len(t, doc.Statement, 2, "the label's statement joins the default one")
	assert.Equal(t, "__default_statement_ID", doc.Statement[0].Sid)
	assert.Equal(t, "cross-acct-publish", doc.Statement[1].Sid)
	assert.Equal(t, "Allow", doc.Statement[1].Effect)
	assert.Equal(t, []string{"SNS:Publish"}, doc.Statement[1].Action)
	assert.Equal(t, topicARN, doc.Statement[1].Resource)
	awsP, _ := json.Marshal(doc.Statement[1].Principal["AWS"])
	assert.Contains(t, string(awsP), "arn:aws:iam::123456789012:root")

	// A duplicate label is refused.
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
	doc.Statement = nil
	require.NoError(t, json.Unmarshal([]byte(attrs.Attributes["Policy"]), &doc))
	require.Len(t, doc.Statement, 1, "RemovePermission drops the label's statement and keeps the default")
	assert.Equal(t, "__default_statement_ID", doc.Statement[0].Sid)

	// Removing an absent label is NotFound.
	_, err = snsC.RemovePermission(ctx, &sns.RemovePermissionInput{
		TopicArn: aws.String(topicARN),
		Label:    aws.String("no-such-label"),
	})
	require.Error(t, err)
}
