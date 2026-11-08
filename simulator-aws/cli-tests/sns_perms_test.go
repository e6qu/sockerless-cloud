package aws_cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSNSCLI_SubscriptionAttributes(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-subattr-t"))
	var topic struct{ TopicArn string }
	parseJSON(t, out, &topic)
	require.NotEmpty(t, topic.TopicArn)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	out = runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", "cli-subattr-q"))
	var queue struct{ QueueUrl string }
	parseJSON(t, out, &queue)
	t.Cleanup(func() {
		_ = awsCLI("sqs", "delete-queue", "--queue-url", queue.QueueUrl).Run()
	})
	out = runCLI(t, awsCLI("sqs", "get-queue-attributes",
		"--queue-url", queue.QueueUrl, "--attribute-names", "QueueArn"))
	var attrs struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &attrs)
	queueARN := attrs.Attributes["QueueArn"]
	require.NotEmpty(t, queueARN)

	out = runCLI(t, awsCLI("sns", "subscribe",
		"--topic-arn", topic.TopicArn,
		"--protocol", "sqs",
		"--notification-endpoint", queueARN))
	var sub struct{ SubscriptionArn string }
	parseJSON(t, out, &sub)
	require.NotEmpty(t, sub.SubscriptionArn)

	out = runCLI(t, awsCLI("sns", "get-subscription-attributes",
		"--subscription-arn", sub.SubscriptionArn))
	var got struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, sub.SubscriptionArn, got.Attributes["SubscriptionArn"])
	assert.Equal(t, topic.TopicArn, got.Attributes["TopicArn"])
	assert.Equal(t, "false", got.Attributes["RawMessageDelivery"])

	runCLI(t, awsCLI("sns", "set-subscription-attributes",
		"--subscription-arn", sub.SubscriptionArn,
		"--attribute-name", "RawMessageDelivery",
		"--attribute-value", "true"))

	out = runCLI(t, awsCLI("sns", "get-subscription-attributes",
		"--subscription-arn", sub.SubscriptionArn))
	parseJSON(t, out, &got)
	assert.Equal(t, "true", got.Attributes["RawMessageDelivery"])
}

// TestSNSCLI_ConfirmSubscription confirms an HTTP subscription with the token
// the confirmation message delivered to the endpoint.
func TestSNSCLI_ConfirmSubscription(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-confirm-t"))
	var topic struct{ TopicArn string }
	parseJSON(t, out, &topic)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	confirmations := make(chan []byte, 2)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		confirmations <- body
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(endpoint.Close)
	out = runCLI(t, awsCLI("sns", "subscribe",
		"--topic-arn", topic.TopicArn,
		"--protocol", "http",
		"--notification-endpoint", endpoint.URL,
		"--return-subscription-arn"))
	var sub struct{ SubscriptionArn string }
	parseJSON(t, out, &sub)
	require.NotEmpty(t, sub.SubscriptionArn)
	require.Contains(t, sub.SubscriptionArn, ":cli-confirm-t:")

	var confirmation struct{ Type, Token string }
	select {
	case body := <-confirmations:
		require.NoError(t, json.Unmarshal(body, &confirmation))
	case <-time.After(30 * time.Second):
		t.Fatal("the endpoint received no confirmation message")
	}
	require.Equal(t, "SubscriptionConfirmation", confirmation.Type)
	token := confirmation.Token
	out = runCLI(t, awsCLI("sns", "confirm-subscription",
		"--topic-arn", topic.TopicArn,
		"--token", token))
	var conf struct{ SubscriptionArn string }
	parseJSON(t, out, &conf)
	assert.Equal(t, sub.SubscriptionArn, conf.SubscriptionArn)

	out = runCLI(t, awsCLI("sns", "get-subscription-attributes",
		"--subscription-arn", sub.SubscriptionArn))
	var got struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &got)
	assert.Equal(t, "false", got.Attributes["PendingConfirmation"])
}

// TestSNSCLI_AddRemovePermission expects add-permission to write a statement
// into the topic Policy and remove-permission to drop it.
func TestSNSCLI_AddRemovePermission(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("sns", "create-topic", "--name", "cli-perm-t"))
	var topic struct{ TopicArn string }
	parseJSON(t, out, &topic)
	t.Cleanup(func() {
		_ = awsCLI("sns", "delete-topic", "--topic-arn", topic.TopicArn).Run()
	})

	runCLI(t, awsCLI("sns", "add-permission",
		"--topic-arn", topic.TopicArn,
		"--label", "cli-cross-acct",
		"--aws-account-id", "123456789012",
		"--action-name", "Publish"))

	out = runCLI(t, awsCLI("sns", "get-topic-attributes", "--topic-arn", topic.TopicArn))
	var ta struct {
		Attributes map[string]string `json:"Attributes"`
	}
	parseJSON(t, out, &ta)
	policy := ta.Attributes["Policy"]
	require.NotEmpty(t, policy)
	var doc struct {
		Statement []struct {
			Sid      string   `json:"Sid"`
			Action   []string `json:"Action"`
			Resource string   `json:"Resource"`
		} `json:"Statement"`
	}
	require.NoError(t, json.Unmarshal([]byte(policy), &doc))
	require.Len(t, doc.Statement, 2, "the label's statement joins the default one")
	assert.Equal(t, "__default_statement_ID", doc.Statement[0].Sid)
	assert.Equal(t, "cli-cross-acct", doc.Statement[1].Sid)
	assert.Equal(t, []string{"SNS:Publish"}, doc.Statement[1].Action)
	assert.Equal(t, topic.TopicArn, doc.Statement[1].Resource)

	runCLI(t, awsCLI("sns", "remove-permission",
		"--topic-arn", topic.TopicArn,
		"--label", "cli-cross-acct"))

	out = runCLI(t, awsCLI("sns", "get-topic-attributes", "--topic-arn", topic.TopicArn))
	parseJSON(t, out, &ta)
	doc.Statement = nil
	require.NoError(t, json.Unmarshal([]byte(ta.Attributes["Policy"]), &doc))
	require.Len(t, doc.Statement, 1, "remove-permission drops the label's statement and keeps the default")
	assert.Equal(t, "__default_statement_ID", doc.Statement[0].Sid)

	// Removing an absent label is NotFound.
	errOut := runCLIExpectError(t, awsCLI("sns", "remove-permission",
		"--topic-arn", topic.TopicArn,
		"--label", "no-such-label"))
	assert.True(t,
		strings.Contains(errOut, "NotFound") || strings.Contains(errOut, "not found") ||
			strings.Contains(errOut, "No statement"),
		"expected NotFound, got %s", errOut)
}
