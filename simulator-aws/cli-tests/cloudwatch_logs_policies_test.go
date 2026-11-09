package aws_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func cliLogEvents(t *testing.T, events ...[2]any) string {
	t.Helper()
	var out []map[string]any
	for _, e := range events {
		out = append(out, map[string]any{"timestamp": e[0], "message": e[1]})
	}
	body, err := json.Marshal(out)
	require.NoError(t, err)
	return "file://" + writeTempFile(t, "events.json", body)
}

func cliLogGroup(t *testing.T, group, stream string) {
	t.Helper()
	runCLI(t, awsCLI("logs", "create-log-group", "--log-group-name", group))
	t.Cleanup(func() { runCLIIgnore(awsCLI("logs", "delete-log-group", "--log-group-name", group)) })
	if stream != "" {
		runCLI(t, awsCLI("logs", "create-log-stream", "--log-group-name", group, "--log-stream-name", stream))
	}
}

func cliStreamMessages(t *testing.T, group, stream string, extra ...string) []string {
	t.Helper()
	args := append([]string{"logs", "get-log-events", "--log-group-name", group, "--log-stream-name", stream, "--start-from-head", "--output", "json"}, extra...)
	var out struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, runCLI(t, awsCLI(args...)), &out)
	var messages []string
	for _, e := range out.Events {
		messages = append(messages, e.Message)
	}
	return messages
}

func TestLogsPutLogEventsLimitsCLI(t *testing.T) {
	t.Parallel()
	group, stream := "/cli/put-log-events-limits", "s"
	cliLogGroup(t, group, stream)
	now := time.Now().UnixMilli()

	out := runCLIExpectError(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream,
		"--log-events", cliLogEvents(t, [2]any{now, strings.Repeat("x", 1048551)})))
	assert.Contains(t, out, "InvalidParameterException")
	assert.Contains(t, out, "Log event too large")
	out = runCLIExpectError(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream,
		"--log-events", cliLogEvents(t, [2]any{now, "later"}, [2]any{now - 1000, "earlier"})))
	assert.Contains(t, out, "chronological order")

	var put struct {
		RejectedLogEventsInfo struct {
			TooOldLogEventEndIndex int `json:"tooOldLogEventEndIndex"`
		} `json:"rejectedLogEventsInfo"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream, "--output", "json",
		"--log-events", cliLogEvents(t, [2]any{now - 15*24*3600*1000, "too old"}, [2]any{now, "kept"}))), &put)
	assert.Equal(t, 1, put.RejectedLogEventsInfo.TooOldLogEventEndIndex)
	assert.Equal(t, []string{"kept"}, cliStreamMessages(t, group, stream))
}

func cliDataProtectionDocument(identifiers ...string) string {
	quoted := make([]string, len(identifiers))
	for i, id := range identifiers {
		quoted[i] = fmt.Sprintf("%q", "arn:aws:dataprotection::aws:data-identifier/"+id)
	}
	list := strings.Join(quoted, ",")
	return `{"Name":"cli-data-protection","Version":"2021-06-01","Statement":[` +
		`{"Sid":"audit","DataIdentifier":[` + list + `],"Operation":{"Audit":{"FindingsDestination":{}}}},` +
		`{"Sid":"redact","DataIdentifier":[` + list + `],"Operation":{"Deidentify":{"MaskConfig":{}}}}]}`
}

func TestLogsDataProtectionMaskingCLI(t *testing.T) {
	t.Parallel()
	group, stream := "/cli/data-protection-masking", "s"
	cliLogGroup(t, group, stream)
	runCLI(t, awsCLI("logs", "put-data-protection-policy", "--log-group-identifier", group,
		"--policy-document", cliDataProtectionDocument("EmailAddress", "IpAddress")))
	out := runCLIExpectError(t, awsCLI("logs", "put-data-protection-policy", "--log-group-identifier", group,
		"--policy-document", cliDataProtectionDocument("NoSuchIdentifier")))
	assert.Contains(t, out, "InvalidParameterException")

	original := "login by ana@example.org from 10.20.30.40"
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", stream,
		"--log-events", cliLogEvents(t, [2]any{time.Now().UnixMilli(), original})))
	assert.Equal(t, []string{"login by *************** from ***********"}, cliStreamMessages(t, group, stream))
	assert.Equal(t, []string{original}, cliStreamMessages(t, group, stream, "--unmask"))

	var filtered struct {
		Events []struct {
			Message string `json:"message"`
		} `json:"events"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "filter-log-events", "--log-group-name", group, "--unmask", "--output", "json")), &filtered)
	require.Len(t, filtered.Events, 1)
	assert.Equal(t, original, filtered.Events[0].Message)
}

func TestLogsFieldIndexAccountPolicyCLI(t *testing.T) {
	t.Parallel()
	group := "/cli/field-index-account/app"
	policy := "cli-field-index"
	runCLI(t, awsCLI("logs", "put-account-policy", "--policy-name", policy, "--policy-type", "FIELD_INDEX_POLICY",
		"--policy-document", `{"Fields":["orderId"]}`, "--selection-criteria", `LogGroupNamePrefix IN ["/cli/field-index-account/"]`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("logs", "delete-account-policy", "--policy-name", policy, "--policy-type", "FIELD_INDEX_POLICY"))
	})
	cliLogGroup(t, group, "s")
	var policies struct {
		IndexPolicies []struct {
			Source     string `json:"source"`
			PolicyName string `json:"policyName"`
		} `json:"indexPolicies"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-index-policies", "--log-group-identifiers", group, "--output", "json")), &policies)
	require.Len(t, policies.IndexPolicies, 1)
	assert.Equal(t, "ACCOUNT", policies.IndexPolicies[0].Source)
	assert.Equal(t, policy, policies.IndexPolicies[0].PolicyName)

	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", "s",
		"--log-events", cliLogEvents(t, [2]any{time.Now().UnixMilli(), `{"orderId":"o-1"}`})))
	var indexes struct {
		FieldIndexes []struct {
			FieldIndexName string `json:"fieldIndexName"`
			IndexCategory  string `json:"indexCategory"`
		} `json:"fieldIndexes"`
	}
	parseJSON(t, runCLI(t, awsCLI("logs", "describe-field-indexes", "--log-group-identifiers", group,
		"--index-categories", "CUSTOM", "--output", "json")), &indexes)
	require.Len(t, indexes.FieldIndexes, 1)
	assert.Equal(t, "orderId", indexes.FieldIndexes[0].FieldIndexName)
	assert.Equal(t, "CUSTOM", indexes.FieldIndexes[0].IndexCategory)
}

func TestLogsMetricExtractionPolicyCLI(t *testing.T) {
	t.Parallel()
	group := "/cli/emf-extraction-disabled"
	policy := "cli-disable-emf"
	cliLogGroup(t, group, "s")
	runCLI(t, awsCLI("logs", "put-account-policy", "--policy-name", policy, "--policy-type", "METRIC_EXTRACTION_POLICY",
		"--policy-document", `{"EmbeddedMetricFormat":{"Status":"Disabled"}}`, "--selection-criteria", `LogGroupName IN ["`+group+`"]`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("logs", "delete-account-policy", "--policy-name", policy, "--policy-type", "METRIC_EXTRACTION_POLICY"))
	})
	out := runCLIExpectError(t, awsCLI("logs", "put-account-policy", "--policy-name", "cli-emf-all", "--policy-type", "METRIC_EXTRACTION_POLICY",
		"--policy-document", `{"EmbeddedMetricFormat":{"Status":"Disabled"}}`))
	assert.Contains(t, out, "LimitExceededException")

	now := time.Now()
	emf := fmt.Sprintf(`{"_aws":{"Timestamp":%d,"CloudWatchMetrics":[{"Namespace":"CLI/EMFDisabled","Dimensions":[["svc"]],"Metrics":[{"Name":"hits"}]}]},"svc":"a","hits":1}`, now.UnixMilli())
	runCLI(t, awsCLI("logs", "put-log-events", "--log-group-name", group, "--log-stream-name", "s",
		"--log-events", cliLogEvents(t, [2]any{now.UnixMilli(), emf})))
	stats := func(namespace, name string, dims ...string) []any {
		args := []string{"cloudwatch", "get-metric-statistics", "--namespace", namespace, "--metric-name", name,
			"--start-time", now.Add(-5 * time.Minute).UTC().Format(time.RFC3339), "--end-time", now.Add(5 * time.Minute).UTC().Format(time.RFC3339),
			"--period", "600", "--statistics", "Sum", "--output", "json"}
		if len(dims) > 0 {
			args = append(args, append([]string{"--dimensions"}, dims...)...)
		}
		var out struct {
			Datapoints []any `json:"Datapoints"`
		}
		parseJSON(t, runCLI(t, awsCLI(args...)), &out)
		return out.Datapoints
	}
	assert.Empty(t, stats("CLI/EMFDisabled", "hits", "Name=svc,Value=a"))
	assert.Len(t, stats("AWS/Logs", "EMFDisabledErrors", "Name=LogGroupName,Value="+group), 1)
}

func TestKinesisCLI_RecordSizeLimit(t *testing.T) {
	t.Parallel()
	name := "cli-kinesis-record-size"
	runCLI(t, awsCLI("kinesis", "create-stream", "--stream-name", name, "--shard-count", "1"))
	t.Cleanup(func() { runCLIIgnore(awsCLI("kinesis", "delete-stream", "--stream-name", name)) })
	runCLI(t, awsCLI("kinesis", "wait", "stream-exists", "--stream-name", name))
	oversized := writeTempFile(t, "record.bin", make([]byte, 1024*1024))
	out := runCLIExpectError(t, awsCLI("kinesis", "put-record", "--stream-name", name, "--partition-key", "k", "--data", "fileb://"+oversized))
	assert.Contains(t, out, "InvalidArgumentException")
	fits := writeTempFile(t, "fits.bin", make([]byte, 1024*1024-1))
	runCLI(t, awsCLI("kinesis", "put-record", "--stream-name", name, "--partition-key", "k", "--data", "fileb://"+fits))
}

func TestFirehoseCLI_RecordSizeLimit(t *testing.T) {
	t.Parallel()
	const name = "cli-firehose-record-size"
	bucket := "cli-firehose-record-size-bucket"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() { runCLIIgnore(awsCLI("s3", "rb", "s3://"+bucket, "--force")) })
	role := cliRoleAllowing(t, "firehose.amazonaws.com", "s3:GetBucketLocation", "s3:ListBucket", "s3:PutObject")
	runCLI(t, awsCLI("firehose", "create-delivery-stream", "--delivery-stream-name", name,
		"--extended-s3-destination-configuration", `{"RoleARN":"`+role+`","BucketARN":"arn:aws:s3:::`+bucket+`","BufferingHints":{"SizeInMBs":5,"IntervalInSeconds":300}}`))
	t.Cleanup(func() {
		runCLIIgnore(awsCLI("firehose", "delete-delivery-stream", "--delivery-stream-name", name, "--allow-force-delete"))
	})
	input := func(size int) string {
		body, err := json.Marshal(map[string]any{"DeliveryStreamName": name, "Record": map[string]any{
			"Data": base64.StdEncoding.EncodeToString(make([]byte, size)),
		}})
		require.NoError(t, err)
		return "file://" + writeTempFile(t, "input.json", body)
	}
	out := runCLIExpectError(t, awsCLI("firehose", "put-record", "--cli-input-json", input(1000*1024+1)))
	assert.Contains(t, out, "InvalidArgumentException")
	runCLI(t, awsCLI("firehose", "put-record", "--cli-input-json", input(1000*1024)))
}

func TestLambda_PayloadSizeLimitCLI(t *testing.T) {
	t.Parallel()
	fn := "cli-payload-limit"
	runCLI(t, awsCLI("lambda", "create-function", "--function-name", fn, "--runtime", "nodejs20.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"), "--handler", "index.handler",
		"--zip-file", "fileb://"+createLambdaSourceZip(t, fn, `exports.handler = async () => ({ ok: true });`)))
	t.Cleanup(func() { runCLIIgnore(awsCLI("lambda", "delete-function", "--function-name", fn)) })
	payload := writeTempFile(t, "payload.json", []byte(`"`+strings.Repeat("p", 1024*1024-1)+`"`))
	out := runCLIExpectError(t, awsCLI("lambda", "invoke", "--function-name", fn, "--invocation-type", "Event",
		"--payload", "fileb://"+payload, filepath.Join(t.TempDir(), "out.json")))
	assert.Contains(t, out, "RequestTooLargeException")
}
