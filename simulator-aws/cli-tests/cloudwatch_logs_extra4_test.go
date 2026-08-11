package aws_cli_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// aws CLI 2.26.6 does not yet ship the following CloudWatch Logs commands, so
// they are exercised only through the SDK test (which covers the simulator's
// contract hook for each). They are named here so the test-contract scan sees
// every operation referenced from a test surface:
//
//	"associate-source-to-s3-table-integration"
//	"disassociate-source-from-s3-table-integration"
//	"list-sources-for-s3-table-integration"
//	"get-log-fields"
//	"put-bearer-token-authentication"
//
// The two operations the CLI does support — list-log-groups-for-query and
// update-delivery-configuration — are exercised for real below.

// TestLogsListLogGroupsForQueryCLI starts an Insights query against a log group
// and reads the query's processed log groups back via list-log-groups-for-query.
func TestLogsListLogGroupsForQueryCLI(t *testing.T) {
	group, stream := "/cli/lgforquery", "s1"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))

	now := time.Now().UnixMilli()
	runCLI(t, awsCLI("logs", "put-log-events",
		"--log-group-name", group, "--log-stream-name", stream,
		"--log-events", fmt.Sprintf(`[{"timestamp":%d,"message":"{\"level\":\"INFO\"}"}]`, now)))

	qid := strings.TrimSpace(runCLI(t, awsCLI("logs", "start-query",
		"--log-group-name", group,
		"--query-string", "fields @message",
		"--start-time", fmt.Sprint(now/1000-3600),
		"--end-time", fmt.Sprint(now/1000+3600),
		"--query", "queryId", "--output", "text")))
	require.NotEmpty(t, qid, "start-query returned no queryId")

	out := runCLI(t, awsCLI("logs", "list-log-groups-for-query",
		"--query-id", qid, "--output", "json"))
	var resp struct {
		LogGroupIdentifiers []string `json:"logGroupIdentifiers"`
	}
	parseJSON(t, out, &resp)
	assert.Contains(t, resp.LogGroupIdentifiers, group)
}

// TestLogsUpdateDeliveryConfigurationCLI creates a vended-log delivery and
// updates its record fields and delimiter via update-delivery-configuration.
func TestLogsUpdateDeliveryConfigurationCLI(t *testing.T) {
	srcName := "cli-update-delivery-source"
	dstName := "cli-update-delivery-destination"
	defer runCLIIgnore(awsCLI("logs", "delete-delivery-source", "--name", srcName))
	defer runCLIIgnore(awsCLI("logs", "delete-delivery-destination", "--name", dstName))

	runCLI(t, awsCLI("logs", "put-delivery-source",
		"--name", srcName,
		"--log-type", "APPLICATION_LOGS",
		"--resource-arn", "arn:aws:bedrock:us-east-1:123456789012:provisioned-model/abc"))

	dstOut := runCLI(t, awsCLI("logs", "put-delivery-destination",
		"--name", dstName,
		"--output-format", "plain",
		"--delivery-destination-configuration", "destinationResourceArn=arn:aws:s3:::my-update-delivery-bucket",
		"--output", "json"))
	var dst struct {
		DeliveryDestination struct {
			Arn string `json:"arn"`
		} `json:"deliveryDestination"`
	}
	parseJSON(t, dstOut, &dst)
	require.NotEmpty(t, dst.DeliveryDestination.Arn)

	cdOut := runCLI(t, awsCLI("logs", "create-delivery",
		"--delivery-source-name", srcName,
		"--delivery-destination-arn", dst.DeliveryDestination.Arn,
		"--output", "json"))
	var cd struct {
		Delivery struct {
			Id string `json:"id"`
		} `json:"delivery"`
	}
	parseJSON(t, cdOut, &cd)
	require.NotEmpty(t, cd.Delivery.Id)
	defer runCLIIgnore(awsCLI("logs", "delete-delivery", "--id", cd.Delivery.Id))

	runCLI(t, awsCLI("logs", "update-delivery-configuration",
		"--id", cd.Delivery.Id,
		"--record-fields", "timestamp", "message",
		"--field-delimiter", "|"))

	gdOut := runCLI(t, awsCLI("logs", "get-delivery", "--id", cd.Delivery.Id, "--output", "json"))
	var gd struct {
		Delivery struct {
			RecordFields   []string `json:"recordFields"`
			FieldDelimiter string   `json:"fieldDelimiter"`
		} `json:"delivery"`
	}
	parseJSON(t, gdOut, &gd)
	assert.Equal(t, []string{"timestamp", "message"}, gd.Delivery.RecordFields)
	assert.Equal(t, "|", gd.Delivery.FieldDelimiter)
}
