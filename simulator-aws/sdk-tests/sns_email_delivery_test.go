package aws_sdk_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/stretchr/testify/require"
)

func smtpMessageBody(message string) string {
	if _, body, ok := strings.Cut(message, "\n\n"); ok {
		return body
	}
	return message
}

func TestSNSEmailAndEmailJSONUseRealSMTP(t *testing.T) {
	messages := runSMTPReceiver(t)
	client := snsClient()
	topic, err := client.CreateTopic(ctx, &sns.CreateTopicInput{Name: aws.String("smtp-delivery-topic")})
	require.NoError(t, err)
	topicARN := aws.ToString(topic.TopicArn)
	t.Cleanup(func() {
		_, _ = client.DeleteTopic(ctx, &sns.DeleteTopicInput{TopicArn: &topicARN})
	})

	plain, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn:              &topicARN,
		Protocol:              aws.String("email"),
		Endpoint:              aws.String("operator@localhost"),
		ReturnSubscriptionArn: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(plain.SubscriptionArn))
	plainConfirmation := awaitValidationEmail(t, messages)
	tokenMatch := regexp.MustCompile(`Token: ([^[:space:]]+)`).FindStringSubmatch(plainConfirmation)
	require.Len(t, tokenMatch, 2, "subscription confirmation email: %s", plainConfirmation)
	_, err = client.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: &topicARN,
		Token:    aws.String(tokenMatch[1]),
	})
	require.NoError(t, err)

	_, err = client.Publish(ctx, &sns.PublishInput{
		TopicArn: &topicARN,
		Subject:  aws.String("real SMTP subject"),
		Message:  aws.String("real SMTP body"),
	})
	require.NoError(t, err)
	plainNotification := awaitValidationEmail(t, messages)
	require.Contains(t, plainNotification, "Subject: real SMTP subject")
	require.Contains(t, smtpMessageBody(plainNotification), "real SMTP body")

	jsonSubscription, err := client.Subscribe(ctx, &sns.SubscribeInput{
		TopicArn:              &topicARN,
		Protocol:              aws.String("email-json"),
		Endpoint:              aws.String("automation@localhost"),
		ReturnSubscriptionArn: true,
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(jsonSubscription.SubscriptionArn))
	var confirmationEnvelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(smtpMessageBody(awaitValidationEmail(t, messages)))),
		&confirmationEnvelope))
	require.Equal(t, "SubscriptionConfirmation", snsHTTPString(confirmationEnvelope, "Type"))
	verifySNSHTTPEnvelope(t, confirmationEnvelope)
	_, err = client.ConfirmSubscription(ctx, &sns.ConfirmSubscriptionInput{
		TopicArn: &topicARN,
		Token:    aws.String(snsHTTPString(confirmationEnvelope, "Token")),
	})
	require.NoError(t, err)

	_, err = client.Publish(ctx, &sns.PublishInput{
		TopicArn: &topicARN,
		Subject:  aws.String("JSON subject"),
		Message:  aws.String("JSON body"),
	})
	require.NoError(t, err)
	firstDelivery := awaitValidationEmail(t, messages)
	secondDelivery := awaitValidationEmail(t, messages)
	jsonDelivery := firstDelivery
	if !strings.HasPrefix(strings.TrimSpace(smtpMessageBody(jsonDelivery)), "{") {
		jsonDelivery = secondDelivery
	}
	var notificationEnvelope map[string]any
	require.NoError(t, json.Unmarshal([]byte(strings.TrimSpace(smtpMessageBody(jsonDelivery))),
		&notificationEnvelope))
	require.Equal(t, "Notification", snsHTTPString(notificationEnvelope, "Type"))
	require.Equal(t, "JSON body", snsHTTPString(notificationEnvelope, "Message"))
	require.Equal(t, "JSON subject", snsHTTPString(notificationEnvelope, "Subject"))
	verifySNSHTTPEnvelope(t, notificationEnvelope)
}
