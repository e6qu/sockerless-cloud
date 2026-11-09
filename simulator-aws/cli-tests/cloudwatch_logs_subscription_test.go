package aws_cli_test

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"io"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type cliSubscriptionMessage struct {
	Owner               string   `json:"owner"`
	LogGroup            string   `json:"logGroup"`
	LogStream           string   `json:"logStream"`
	SubscriptionFilters []string `json:"subscriptionFilters"`
	MessageType         string   `json:"messageType"`
	LogEvents           []struct {
		ID        string `json:"id"`
		Timestamp int64  `json:"timestamp"`
		Message   string `json:"message"`
	} `json:"logEvents"`
}

func createCLIActiveStream(t *testing.T, stream string) string {
	t.Helper()
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", stream, "--shard-count", "1"))
	t.Cleanup(func() { runCLIIgnore(awsCLI("kinesis", "delete-stream", "--stream-name", stream)) })
	runCLI(t, awsCLI("kinesis", "wait", "stream-exists", "--stream-name", stream))
	var summary struct {
		StreamDescriptionSummary struct {
			StreamARN string `json:"StreamARN"`
		} `json:"StreamDescriptionSummary"`
	}
	parseJSON(t, runCLI(t, awsCLI("kinesis", "describe-stream-summary", "--stream-name", stream)), &summary)
	return summary.StreamDescriptionSummary.StreamARN
}

// The subscription guide's role for CloudWatch Logs.
func createCLILogsDeliveryRole(t *testing.T, name, action, resource string) string {
	t.Helper()
	out := runCLI(t, awsCLI("iam", "create-role", "--role-name", name, "--assume-role-policy-document",
		`{"Statement":{"Effect":"Allow","Principal":{"Service":"logs.amazonaws.com"},"Action":"sts:AssumeRole",`+
			`"Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:logs:us-east-1:123456789012:*"}}}}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("iam", "delete-role-policy", "--role-name", name, "--policy-name", "Permissions-Policy-For-CWL"))
		runCLIIgnore(awsCLI("iam", "delete-role", "--role-name", name))
	})
	runCLI(t, awsCLI("iam", "put-role-policy", "--role-name", name, "--policy-name", "Permissions-Policy-For-CWL",
		"--policy-document", `{"Statement":[{"Effect":"Allow","Action":"`+action+`","Resource":"`+resource+`"}]}`))
	var created struct {
		Role struct {
			Arn string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, out, &created)
	return created.Role.Arn
}

// The guide's example: put-subscription-filter, then get-shard-iterator and
// get-records, then base64 -d | zcat.
func TestLogs_SubscriptionFilterKinesisCLI(t *testing.T) {
	t.Parallel()
	const group, logStream, stream = "CloudTrail/cli-logs", "111111111111_CloudTrail_us-east-1", "cli-RootAccess"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", logStream))
	streamARN := createCLIActiveStream(t, stream)
	role := createCLILogsDeliveryRole(t, "cli-CWLtoKinesisRole", "kinesis:PutRecord", streamARN)

	runCLI(t, awsCLI("logs", "put-subscription-filter",
		"--log-group-name", group,
		"--filter-name", "RootAccess",
		"--filter-pattern", "{$.userIdentity.type = Root}",
		"--destination-arn", streamARN,
		"--role-arn", role,
		"--distribution", "Random"))
	var desc struct {
		SubscriptionFilters []struct {
			FilterName             string `json:"filterName"`
			DestinationArn         string `json:"destinationArn"`
			RoleArn                string `json:"roleArn"`
			Distribution           string `json:"distribution"`
			ApplyOnTransformedLogs bool   `json:"applyOnTransformedLogs"`
		} `json:"subscriptionFilters"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-subscription-filters", "--log-group-name", group)), &desc)
	require.Len(t, desc.SubscriptionFilters, 1)
	assert.Equal(t, "RootAccess", desc.SubscriptionFilters[0].FilterName)
	assert.Equal(t, streamARN, desc.SubscriptionFilters[0].DestinationArn)
	assert.Equal(t, role, desc.SubscriptionFilters[0].RoleArn)
	assert.Equal(t, "Random", desc.SubscriptionFilters[0].Distribution)

	root := `{"eventVersion":"1.03","userIdentity":{"type":"Root"}}`
	now := strconv.FormatInt(time.Now().UnixMilli(), 10)
	events, err := json.Marshal([]map[string]any{
		{"timestamp": json.Number(now), "message": root},
		{"timestamp": json.Number(now), "message": `{"eventVersion":"1.03","userIdentity":{"type":"AssumedRole"}}`},
	})
	require.NoError(t, err)
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", logStream,
		"--log-events", string(events)))

	var iterator struct {
		ShardIterator string `json:"ShardIterator"`
	}
	parseJSON(t, runCLI(t, awsCLI("kinesis", "get-shard-iterator", "--stream-name", stream,
		"--shard-id", "shardId-000000000000", "--shard-iterator-type", "TRIM_HORIZON")), &iterator)
	next := iterator.ShardIterator
	var delivered []cliSubscriptionMessage
	require.Eventually(t, func() bool {
		var records struct {
			Records []struct {
				Data string `json:"Data"`
			} `json:"Records"`
			NextShardIterator string `json:"NextShardIterator"`
		}
		parseJSON(t, runCLI(t, awsCLI("kinesis", "get-records", "--limit", "10", "--shard-iterator", next)), &records)
		next = records.NextShardIterator
		for _, record := range records.Records {
			compressed, err := base64.StdEncoding.DecodeString(record.Data)
			require.NoError(t, err)
			zr, err := gzip.NewReader(bytes.NewReader(compressed))
			require.NoError(t, err)
			raw, err := io.ReadAll(zr)
			require.NoError(t, err)
			var message cliSubscriptionMessage
			require.NoError(t, json.Unmarshal(raw, &message), string(raw))
			if message.MessageType == "DATA_MESSAGE" {
				delivered = append(delivered, message)
			}
		}
		return len(delivered) > 0
	}, 60*time.Second, 500*time.Millisecond, "the stream must receive the filter's DATA_MESSAGE")
	m := delivered[0]
	assert.Equal(t, "123456789012", m.Owner)
	assert.Equal(t, group, m.LogGroup)
	assert.Equal(t, logStream, m.LogStream)
	assert.Equal(t, []string{"RootAccess"}, m.SubscriptionFilters)
	require.Len(t, m.LogEvents, 1)
	assert.Equal(t, root, m.LogEvents[0].Message)
	assert.Len(t, m.LogEvents[0].ID, 56)

	runCLI(t, awsCLI("logs", "delete-subscription-filter", "--log-group-name", group, "--filter-name", "RootAccess"))
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-subscription-filters", "--log-group-name", group)), &desc)
	assert.Empty(t, desc.SubscriptionFilters)

	refused := runCLIExpectError(t, awsCLI("logs", "put-subscription-filter",
		"--log-group-name", group, "--filter-name", "RootAccess", "--filter-pattern", "",
		"--destination-arn", "arn:aws:lambda:us-east-1:123456789012:function:cli-no-such-function"))
	assert.Contains(t, refused, "Could not execute the lambda function")
}

// describe-log-groups reports the class, both forms of the ARN and the
// deletion protection setting, which refuses delete-log-group.
func TestLogs_LogGroupClassAndProtectionCLI(t *testing.T) {
	t.Parallel()
	const group = "/cli/infrequent-protected"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group,
		"--log-group-class", "INFREQUENT_ACCESS", "--deletion-protection-enabled"))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))

	var described struct {
		LogGroups []struct {
			Arn                       string `json:"arn"`
			LogGroupArn               string `json:"logGroupArn"`
			LogGroupClass             string `json:"logGroupClass"`
			MetricFilterCount         int    `json:"metricFilterCount"`
			DeletionProtectionEnabled bool   `json:"deletionProtectionEnabled"`
		} `json:"logGroups"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-log-groups", "--log-group-name-prefix", group,
		"--log-group-class", "INFREQUENT_ACCESS")), &described)
	require.Len(t, described.LogGroups, 1)
	g := described.LogGroups[0]
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:"+group+":*", g.Arn)
	assert.Equal(t, "arn:aws:logs:us-east-1:123456789012:log-group:"+group, g.LogGroupArn)
	assert.Equal(t, "INFREQUENT_ACCESS", g.LogGroupClass)
	assert.Zero(t, g.MetricFilterCount)
	assert.True(t, g.DeletionProtectionEnabled)

	refused := runCLIExpectError(t, awsCLI("logs", "put-transformer", "--log-group-identifier", group,
		"--transformer-config", `[{"parseJSON":{}}]`))
	assert.Contains(t, refused, "InvalidOperationException")

	refused = runCLIExpectError(t, awsCLI("logs", "delete-log-group", "--log-group-name", group))
	assert.Contains(t, refused, "Cannot delete log group with deletion protection enabled")
	runCLI(t, awsCLI("logs", "put-log-group-deletion-protection", "--log-group-identifier", g.LogGroupArn,
		"--no-deletion-protection-enabled"))
	runCLI(t, awsCLI("logs", "delete-log-group", "--log-group-name", group))
}
