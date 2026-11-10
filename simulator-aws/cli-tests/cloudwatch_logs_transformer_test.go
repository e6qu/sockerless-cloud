package aws_cli_test

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLogsTestTransformerProcessorsCLI(t *testing.T) {
	t.Parallel()
	out := runCLI(t, awsCLI("logs", "test-transformer",
		"--transformer-config", `[{"parseKeyValue":{"destination":"new_key","fieldDelimiter":"!","keyValueDelimiter":":","nonMatchValue":"defaultValue","keyPrefix":"parsed_"}},{"renameKeys":{"entries":[{"key":"new_key.parsed_key1","renameTo":"first"}]}}]`,
		"--log-event-messages", "key1:value1!key2:value2!key3:value3!key4",
		"--output", "json"))
	var res struct {
		TransformedLogs []struct {
			EventNumber             int64  `json:"eventNumber"`
			EventMessage            string `json:"eventMessage"`
			TransformedEventMessage string `json:"transformedEventMessage"`
		} `json:"transformedLogs"`
	}
	parseJSON(t, out, &res)
	require.Len(t, res.TransformedLogs, 1)
	assert.Equal(t, "key1:value1!key2:value2!key3:value3!key4", res.TransformedLogs[0].EventMessage)
	assert.JSONEq(t, `{"new_key":{"parsed_key2":"value2","parsed_key3":"value3","parsed_key4":"defaultValue"},"first":"value1"}`,
		res.TransformedLogs[0].TransformedEventMessage)

	errOut := runCLIExpectError(t, awsCLI("logs", "test-transformer",
		"--transformer-config", `[{"grok":{"match":"%{NGINX_ACCESS_LOG} %{NUMBER:num}"}}]`,
		"--log-event-messages", "x"))
	assert.Contains(t, errOut, "InvalidParameterException")
}

func TestLogsTransformerIngestionCLI(t *testing.T) {
	t.Parallel()
	group, stream := "/cli/transformer-ingest", "s1"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))
	runCLI(t, awsCLI("logs", "put-transformer", "--log-group-identifier", group,
		"--transformer-config", `[{"parseVPC":{}},{"typeConverter":{"entries":[{"key":"accountId","type":"string"}]}}]`))

	got := runCLI(t, awsCLI("logs", "get-transformer", "--log-group-identifier", group, "--output", "json"))
	var gt struct {
		TransformerConfig []map[string]any `json:"transformerConfig"`
	}
	parseJSON(t, got, &gt)
	require.Len(t, gt.TransformerConfig, 2)
	assert.Contains(t, gt.TransformerConfig[0], "parseVPC")

	now := time.Now().UnixMilli()
	flow := "2 123456789010 eni-abc123de 192.0.2.0 192.0.2.24 20641 22 6 20 4249 1418530010 1418530070 ACCEPT OK"
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream,
		"--log-events", `[{"timestamp":`+strconv.FormatInt(now, 10)+`,"message":"`+flow+`"}]`))

	events := runCLI(t, awsCLI("logs", "get-log-events", "--log-group-name", group, "--log-stream-name", stream,
		"--start-from-head", "--output", "json"))
	var ge struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, events, &ge)
	require.Len(t, ge.Events, 1)
	assert.Equal(t, flow, ge.Events[0].Message, "get-log-events returns the original event")

	started := runCLI(t, awsCLI("logs", "start-query", "--log-group-name", group,
		"--start-time", strconv.FormatInt(now/1000-60, 10), "--end-time", strconv.FormatInt(now/1000+60, 10),
		"--query-string", `fields interfaceId, dstPort | filter action = "ACCEPT"`, "--output", "json"))
	var sq struct {
		QueryID string `json:"queryId"`
	}
	parseJSON(t, started, &sq)
	results := runCLI(t, awsCLI("logs", "get-query-results", "--query-id", sq.QueryID, "--output", "json"))
	var qr struct {
		Status  string `json:"status"`
		Results [][]struct {
			Field string `json:"field"`
			Value string `json:"value"`
		} `json:"results"`
	}
	parseJSON(t, results, &qr)
	assert.Equal(t, "Complete", qr.Status)
	require.Len(t, qr.Results, 1)
	values := map[string]string{}
	for _, f := range qr.Results[0] {
		values[f.Field] = f.Value
	}
	assert.Equal(t, "eni-abc123de", values["interfaceId"])
	assert.Equal(t, "22", values["dstPort"])

	runCLI(t, awsCLI("logs", "delete-transformer", "--log-group-identifier", group))
	errOut := runCLIExpectError(t, awsCLI("logs", "get-transformer", "--log-group-identifier", group))
	assert.Contains(t, errOut, "ResourceNotFoundException")
}

func TestLogsParseToOCSFIngestionCLI(t *testing.T) {
	t.Parallel()
	group, stream := "/cli/transformer-ocsf", "s1"
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	defer runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group))
	runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))
	runCLI(t, awsCLI("logs", "put-transformer", "--log-group-identifier", group,
		"--transformer-config", `[{"parseToOCSF":{"eventSource":"Route53Resolver","ocsfVersion":"V1.5","mappingVersion":"1.5.0"}},`+
			`{"dateTimeConverter":{"source":"time_dt","target":"local_time","matchPatterns":["uuuu-MM-dd'T'HH:mm:ss.SSSX"],"targetTimezone":"Europe/Berlin","targetFormat":"EEEE HH:mm zzzz","locale":"de"}}]`))

	now := time.Now().UnixMilli()
	query := `{\"version\":\"1.100000\",\"account_id\":\"123456789012\",\"region\":\"us-east-1\",\"vpc_id\":\"vpc-0123\",` +
		`\"query_timestamp\":\"2022-10-13T21:02:36Z\",\"query_name\":\"example.com.\",\"query_type\":\"A\",\"query_class\":\"IN\",` +
		`\"rcode\":\"NXDOMAIN\",\"answers\":[],\"srcaddr\":\"10.200.21.100\",\"srcport\":\"15083\",\"transport\":\"UDP\",` +
		`\"srcids\":{\"instance\":\"i-0123\"},\"firewall_rule_action\":\"BLOCK\"}`
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream,
		"--log-events", `[{"timestamp":`+strconv.FormatInt(now, 10)+`,"message":"`+query+`"}]`))

	started := runCLI(t, awsCLI("logs", "start-query", "--log-group-name", group,
		"--start-time", strconv.FormatInt(now/1000-60, 10), "--end-time", strconv.FormatInt(now/1000+60, 10),
		"--query-string", "fields class_uid, rcode, rcode_id, disposition, src_endpoint.instance_uid, local_time", "--output", "json"))
	var sq struct {
		QueryID string `json:"queryId"`
	}
	parseJSON(t, started, &sq)
	results := runCLI(t, awsCLI("logs", "get-query-results", "--query-id", sq.QueryID, "--output", "json"))
	var qr struct {
		Status  string `json:"status"`
		Results [][]struct {
			Field string `json:"field"`
			Value string `json:"value"`
		} `json:"results"`
	}
	parseJSON(t, results, &qr)
	assert.Equal(t, "Complete", qr.Status)
	require.Len(t, qr.Results, 1)
	values := map[string]string{}
	for _, f := range qr.Results[0] {
		values[f.Field] = f.Value
	}
	assert.Equal(t, "4003", values["class_uid"], "a Route 53 Resolver query is a DNS Activity event")
	assert.Equal(t, "NXDomain", values["rcode"])
	assert.Equal(t, "3", values["rcode_id"])
	assert.Equal(t, "Blocked", values["disposition"])
	assert.Equal(t, "i-0123", values["src_endpoint.instance_uid"])
	assert.Equal(t, "Donnerstag 23:02 Mitteleuropäische Sommerzeit", values["local_time"])
}
