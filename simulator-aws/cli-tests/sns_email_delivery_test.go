package aws_cli_test

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSNSCLI_EmailDeliveryUsesRealSMTP(t *testing.T) {
	messages := acmCLIStartSMTPReceiver(t)
	topicJSON := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-smtp-delivery", "--output", "json"))
	var topic struct {
		TopicArn string `json:"TopicArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(topicJSON), &topic))
	require.NotEmpty(t, topic.TopicArn)

	subscriptionJSON := runCLI(t, awsCLI("sns", "subscribe",
		"--topic-arn", topic.TopicArn,
		"--protocol", "email",
		"--notification-endpoint", "operator@localhost",
		"--return-subscription-arn",
		"--output", "json"))
	var subscription struct {
		SubscriptionArn string `json:"SubscriptionArn"`
	}
	require.NoError(t, json.Unmarshal([]byte(subscriptionJSON), &subscription))
	require.NotEmpty(t, subscription.SubscriptionArn)

	confirmation := acmCLIAwaitEmail(t, messages)
	tokenMatch := regexp.MustCompile(`Token: ([^[:space:]]+)`).FindStringSubmatch(confirmation)
	require.Len(t, tokenMatch, 2, "subscription confirmation email: %s", confirmation)
	runCLI(t, awsCLI("sns", "confirm-subscription",
		"--topic-arn", topic.TopicArn,
		"--token", tokenMatch[1],
		"--output", "json"))

	runCLI(t, awsCLI("sns", "publish",
		"--topic-arn", topic.TopicArn,
		"--subject", "AWS CLI SMTP subject",
		"--message", "AWS CLI SMTP body",
		"--output", "json"))
	notification := acmCLIAwaitEmail(t, messages)
	require.Contains(t, notification, "Subject: AWS CLI SMTP subject")
	require.Contains(t, strings.TrimSpace(notification), "AWS CLI SMTP body")
}
