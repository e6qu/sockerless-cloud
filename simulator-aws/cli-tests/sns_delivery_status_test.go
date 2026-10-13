package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSNSCLI_LambdaFailureFeedbackLogsTheFailedDelivery sets a topic's Lambda
// failure feedback role, publishes to a subscription whose function does not
// exist, and reads the delivery status entry back from the topic's Failure log
// group and the failure from its AWS/SNS metric.
func TestSNSCLI_LambdaFailureFeedbackLogsTheFailedDelivery(t *testing.T) {
	t.Parallel()
	const (
		topicName = "cli-sns-feedback-t"
		roleName  = "cli-sns-feedback-role"
	)
	since := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	created := runCLI(t, awsCLI("iam", "create-role",
		"--role-name", roleName,
		"--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		"--output", "json"))
	var role struct {
		Role struct {
			ARN string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, created, &role)
	require.NotEmpty(t, role.Role.ARN)
	runCLI(t, awsCLI("iam", "put-role-policy",
		"--role-name", roleName,
		"--policy-name", "delivery-status",
		"--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents","logs:PutMetricFilter","logs:PutRetentionPolicy"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		_ = awsCLI("iam", "delete-role-policy", "--role-name", roleName, "--policy-name", "delivery-status").Run()
		_ = awsCLI("iam", "delete-role", "--role-name", roleName).Run()
	})

	out := runCLI(t, awsCLI("sns", "create-topic", "--name", topicName))
	var topic struct{ TopicArn string }
	parseJSON(t, out, &topic)
	require.NotEmpty(t, topic.TopicArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})
	arn := strings.Split(topic.TopicArn, ":")
	failureGroup := "sns/" + arn[3] + "/" + arn[4] + "/" + topicName + "/Failure"
	t.Cleanup(func() {
		_ = awsCLI("logs", "delete-log-group", "--log-group-name", failureGroup).Run()
	})

	runCLIExpectError(t, awsCLI("sns", "set-topic-attributes",
		"--topic-arn", topic.TopicArn,
		"--attribute-name", "LambdaSuccessFeedbackSampleRate",
		"--attribute-value", "150"))
	runCLI(t, awsCLI("sns", "set-topic-attributes",
		"--topic-arn", topic.TopicArn,
		"--attribute-name", "LambdaFailureFeedbackRoleArn",
		"--attribute-value", role.Role.ARN))
	out = runCLI(t, awsCLI("sns", "get-topic-attributes", "--topic-arn", topic.TopicArn))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	assert.Equal(t, role.Role.ARN, attrs.Attributes["LambdaFailureFeedbackRoleArn"])
	assert.NotContains(t, attrs.Attributes, "LambdaSuccessFeedbackSampleRate")

	functionARN := "arn:aws:lambda:" + arn[3] + ":" + arn[4] + ":function:cli-sns-feedback-missing"
	runCLI(t, awsCLI("sns", "subscribe",
		"--topic-arn", topic.TopicArn,
		"--protocol", "lambda",
		"--notification-endpoint", functionARN))
	out = runCLI(t, awsCLI("sns", "publish", "--topic-arn", topic.TopicArn, "--message", "never-delivered"))
	var published struct{ MessageId string }
	parseJSON(t, out, &published)
	require.NotEmpty(t, published.MessageId)

	out = runCLI(t, awsCLI("logs", "filter-log-events", "--log-group-name", failureGroup, "--output", "json"))
	var events struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, out, &events)
	require.Len(t, events.Events, 1)
	var entry struct {
		Notification struct {
			MessageID string `json:"messageId"`
			TopicArn  string `json:"topicArn"`
		} `json:"notification"`
		Delivery struct {
			Destination      string `json:"destination"`
			ProviderResponse string `json:"providerResponse"`
			StatusCode       int    `json:"statusCode"`
		} `json:"delivery"`
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal([]byte(events.Events[0].Message), &entry))
	assert.Equal(t, "FAILURE", entry.Status)
	assert.Equal(t, published.MessageId, entry.Notification.MessageID)
	assert.Equal(t, topic.TopicArn, entry.Notification.TopicArn)
	assert.Equal(t, functionARN, entry.Delivery.Destination)
	assert.NotZero(t, entry.Delivery.StatusCode)
	assert.NotEmpty(t, entry.Delivery.ProviderResponse)

	out = runCLI(t, awsCLI("cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/SNS",
		"--metric-name", "NumberOfNotificationsFailed",
		"--dimensions", "Name=TopicName,Value="+topicName,
		"--start-time", since,
		"--end-time", time.Now().UTC().Add(time.Minute).Format(time.RFC3339),
		"--period", "300",
		"--statistics", "Sum",
		"--output", "json"))
	var stats struct {
		Datapoints []struct {
			Sum float64 `json:"Sum"`
		} `json:"Datapoints"`
	}
	parseJSON(t, out, &stats)
	var sum float64
	for _, point := range stats.Datapoints {
		sum += point.Sum
	}
	assert.Equal(t, float64(1), sum)
}
