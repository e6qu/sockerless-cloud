package aws_cli_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A new topic carries the owner-only default policy, a misspelled attribute is
// refused, and a message no subscription's filter admits counts as filtered
// out.
func TestSNS_CLI_TopicDefaultsAttributeNamesAndFilterMetrics(t *testing.T) {
	t.Parallel()
	name := "cli-sns-fidelity-" + time.Now().Format("150405000")
	var topic struct{ TopicArn string }
	parseJSON(t, runCLI(t, awsCLI("sns", "create-topic", "--name", name)), &topic)
	t.Cleanup(func() { runCLI(t, awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn)) })

	var attrs struct{ Attributes map[string]string }
	parseJSON(t, runCLI(t, awsCLI("sns", "get-topic-attributes", "--topic-arn", topic.TopicArn)), &attrs)
	var policy struct {
		Id        string
		Statement []struct{ Sid string }
	}
	require.NoError(t, json.Unmarshal([]byte(attrs.Attributes["Policy"]), &policy))
	assert.Equal(t, "__default_policy_ID", policy.Id)
	require.Len(t, policy.Statement, 1)
	assert.Equal(t, "__default_statement_ID", policy.Statement[0].Sid)
	assert.Contains(t, attrs.Attributes["EffectiveDeliveryPolicy"], `"defaultHealthyRetryPolicy"`)

	refused := runCLIExpectError(t, awsCLI("sns", "set-topic-attributes", "--topic-arn", topic.TopicArn,
		"--attribute-name", "LambdaFailureFeedbackRoleARN", "--attribute-value", "arn:aws:iam::123456789012:role/feedback"))
	assert.Contains(t, refused, "InvalidParameter")

	var queue struct{ QueueUrl string }
	parseJSON(t, runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", name+"-q")), &queue)
	t.Cleanup(func() { runCLI(t, awsCLI("sqs", "delete-queue", "--queue-url", queue.QueueUrl)) })
	queueARN := strings.TrimSpace(runCLI(t, awsCLI("sqs", "get-queue-attributes", "--queue-url", queue.QueueUrl,
		"--attribute-names", "QueueArn", "--query", "Attributes.QueueArn", "--output", "text")))
	runCLI(t, awsCLI("sns", "subscribe", "--topic-arn", topic.TopicArn, "--protocol", "sqs", "--notification-endpoint", queueARN,
		"--attributes", `{"FilterPolicy":"{\"event\":[\"order_placed\"]}"}`))
	start := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	runCLI(t, awsCLI("sns", "publish", "--topic-arn", topic.TopicArn, "--message", "no attributes"))

	end := time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	sum := strings.TrimSpace(runCLI(t, awsCLI("cloudwatch", "get-metric-statistics",
		"--namespace", "AWS/SNS", "--metric-name", "NumberOfNotificationsFilteredOut-NoMessageAttributes",
		"--dimensions", "Name=TopicName,Value="+name,
		"--start-time", start, "--end-time", end, "--period", "300",
		"--statistics", "Sum", "--query", "Datapoints[0].Sum", "--output", "text")))
	assert.Equal(t, "1.0", sum)
}
