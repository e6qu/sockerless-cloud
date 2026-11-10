package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestLogsDataSourcesCLI classifies a log group's events under the data
// source its cw:datasource tags name, and reads it back with get-log-fields,
// list-aggregate-log-group-summaries and the field index policy that selects
// the data source.
func TestLogsDataSourcesCLI(t *testing.T) {
	t.Parallel()
	name, typ := uniquename.Alnum("cliapp"), "orders"
	group := uniquename.New("/cli/ds/tagged")
	cliLogGroup(t, group, "s")
	runCLI(t, awsCLI("logs", "tag-resource",
		"--resource-arn", "arn:aws:logs:us-east-1:123456789012:log-group:"+group,
		"--tags", "cw:datasource:name="+name+",cw:datasource:type="+typ))
	policyName := uniquename.New("cli-ds-index")
	runCLI(t, awsCLI("logs", "put-account-policy", "--policy-name", policyName, "--policy-type", "FIELD_INDEX_POLICY",
		"--policy-document", `{"Fields":["orderId"]}`,
		"--selection-criteria", fmt.Sprintf(`DataSourceName = %q AND DataSourceType = %q`, name, typ)))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("logs", "delete-account-policy", "--policy-name", policyName, "--policy-type", "FIELD_INDEX_POLICY"))
	})
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", "s",
		"--log-events", cliLogEvents(t, [2]any{time.Now().UnixMilli(), `{"orderId":"o-1","amount":3}`})))

	var fields struct {
		LogFields []struct {
			LogFieldName string `json:"logFieldName"`
			LogFieldType struct {
				Type string `json:"type"`
			} `json:"logFieldType"`
		} `json:"logFields"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "get-log-fields", "--data-source-name", name, "--data-source-type", typ, "--output", "json")), &fields)
	byName := map[string]string{}
	for _, f := range fields.LogFields {
		byName[f.LogFieldName] = f.LogFieldType.Type
	}
	assert.Equal(t, "string", byName["orderId"])
	assert.Equal(t, "double", byName["amount"])
	assert.Contains(t, runCLIExpectError(t, awsCLI("logs", "get-log-fields", "--data-source-name", name, "--data-source-type", "refunds")),
		"ResourceNotFoundException")

	var agg struct {
		AggregateLogGroupSummaries []struct {
			GroupingIdentifiers []struct {
				Key   string `json:"key"`
				Value string `json:"value"`
			} `json:"groupingIdentifiers"`
			LogGroupCount int `json:"logGroupCount"`
		} `json:"aggregateLogGroupSummaries"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "list-aggregate-log-group-summaries", "--group-by", "DATA_SOURCE_NAME_AND_TYPE",
		"--data-sources", "name="+name, "--output", "json")), &agg)
	require.Len(t, agg.AggregateLogGroupSummaries, 1)
	assert.Equal(t, 1, agg.AggregateLogGroupSummaries[0].LogGroupCount)
	assert.Equal(t, name, agg.AggregateLogGroupSummaries[0].GroupingIdentifiers[0].Value)

	var indexes struct {
		FieldIndexes []struct {
			FieldIndexName string `json:"fieldIndexName"`
			IndexCategory  string `json:"indexCategory"`
		} `json:"fieldIndexes"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-field-indexes", "--log-group-identifiers", group, "--output", "json")), &indexes)
	categories := map[string]string{}
	for _, fi := range indexes.FieldIndexes {
		categories[fi.FieldIndexName] = fi.IndexCategory
	}
	assert.Equal(t, "CUSTOM", categories["orderId"])
	assert.Equal(t, "DEFAULT", categories["@data_source_type"])
}

// TestEventBridgeLogGroupTargetResourcePolicyCLI refuses an Amazon
// EventBridge delivery to a CloudWatch Logs log group until a CloudWatch Logs
// resource policy grants events.amazonaws.com, as the EventBridge user guide
// requires of a target added through the CLI.
func TestEventBridgeLogGroupTargetResourcePolicyCLI(t *testing.T) {
	t.Parallel()
	group, rule := uniquename.New("/aws/events/cli-target"), uniquename.New("eb-cli-logs-target")
	cliLogGroup(t, group, "")
	groupARN := "arn:aws:logs:us-east-1:123456789012:log-group:" + group

	var queue struct {
		QueueUrl string `json:"QueueUrl"`
	}
	parseJSON(t, runCLI(t, awsCLI("sqs", "create-queue", "--queue-name", uniquename.New("eb-cli-logs-dlq"), "--output", "json")), &queue)
	t.Cleanup(func() { runCLIIgnore(awsCLI("sqs", "delete-queue", "--queue-url", queue.QueueUrl)) })
	dlqARN := strings.TrimSpace(runCLI(t, awsCLI("sqs", "get-queue-attributes", "--queue-url", queue.QueueUrl,
		"--attribute-names", "QueueArn", "--query", "Attributes.QueueArn", "--output", "text")))
	ruleARN := strings.TrimSpace(runCLI(t, awsCLI("events", "put-rule", "--name", rule,
		"--event-pattern", `{"source":["`+rule+`"]}`, "--query", "RuleArn", "--output", "text")))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("events", "remove-targets", "--rule", rule, "--ids", "logs"))
		runCLIIgnore(awsCLI("events", "delete-rule", "--name", rule))
	})
	setEBQueuePolicyCLI(t, queue.QueueUrl, dlqARN, ruleARN)
	targets, err := json.Marshal([]map[string]any{{
		"Id": "logs", "Arn": groupARN,
		"RetryPolicy":      map[string]any{"MaximumRetryAttempts": 0, "MaximumEventAgeInSeconds": 60},
		"DeadLetterConfig": map[string]any{"Arn": dlqARN},
	}})
	require.NoError(t, err)
	runCLI(t, awsCLI("events", "put-targets", "--rule", rule, "--targets", string(targets)))
	putEvent := func(detail string) {
		entries, err := json.Marshal([]map[string]any{{"Source": rule, "DetailType": "Order Placed", "Detail": detail}})
		require.NoError(t, err)
		runCLI(t, awsCLI("events", "put-events", "--entries", string(entries)))
	}

	putEvent(`{"attempt":"refused"}`)
	var received struct {
		Messages []struct {
			MessageAttributes map[string]struct {
				StringValue string `json:"StringValue"`
			} `json:"MessageAttributes"`
		} `json:"Messages"`
	}
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("sqs", "receive-message", "--queue-url", queue.QueueUrl,
			"--wait-time-seconds", "2", "--message-attribute-names", "All", "--output", "json")), &received)
		return len(received.Messages) == 1
	}, 30*time.Second, 100*time.Millisecond, "the refused delivery must reach the dead-letter queue")
	assert.Equal(t, "AccessDeniedException", received.Messages[0].MessageAttributes["ERROR_CODE"].StringValue)

	policyName := uniquename.New("cli-eventbridge-to-logs")
	runCLI(t, awsCLI("logs", "put-resource-policy", "--policy-name", policyName, "--policy-document",
		fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},`+
			`"Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"%s:*"}]}`, groupARN)))
	t.Cleanup(func() { runCLIIgnore(awsCLI("logs", "delete-resource-policy", "--policy-name", policyName)) })

	putEvent(`{"attempt":"granted"}`)
	var events struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	require.Eventually(t, func() bool {
		parseJSON(t, runCLI(t, awsCLI("logs", "filter-log-events", "--log-group-name", group, "--output", "json")), &events)
		return len(events.Events) > 0
	}, 30*time.Second, 200*time.Millisecond, "the granted delivery must reach the log group")
	require.Len(t, events.Events, 1)
	assert.Contains(t, events.Events[0].Message, `"granted"`)
}
