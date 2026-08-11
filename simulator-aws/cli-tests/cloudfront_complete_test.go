package aws_cli_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// writeTempFile writes content to a temp file and returns its path, for the
// CLI's fileb:// blob arguments (connection-function code, event objects).
func writeTempFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(p, content, 0o600))
	return p
}

// TestCloudFront_ConnectionFunction_CLI exercises the connection-function CRUD +
// lifecycle through the real aws CLI.
func TestCloudFront_ConnectionFunction_CLI(t *testing.T) {
	name := "clifn-" + time.Now().Format("150405.000000")
	codePath := writeTempFile(t, "code.js", []byte("function handler(event) { return event; }"))
	objPath := writeTempFile(t, "obj.json", []byte(`{"request":{}}`))

	out := runCLI(t, awsCLI("cloudfront", "create-connection-function",
		"--name", name,
		"--connection-function-config", `{"Comment":"cli cnx fn","Runtime":"cloudfront-js-2.0"}`,
		"--connection-function-code", "fileb://"+codePath,
		"--output", "json"))
	var created struct {
		ConnectionFunctionSummary struct {
			Id    string `json:"Id"`
			Name  string `json:"Name"`
			Stage string `json:"Stage"`
		} `json:"ConnectionFunctionSummary"`
		ETag string `json:"ETag"`
	}
	parseJSON(t, out, &created)
	id := created.ConnectionFunctionSummary.Id
	require.NotEmpty(t, id)
	require.NotEmpty(t, created.ETag)

	defer func() {
		descOut := runCLIIgnoreErr(awsCLI("cloudfront", "describe-connection-function", "--identifier", id, "--output", "json"))
		var d struct {
			ETag string `json:"ETag"`
		}
		_ = json.Unmarshal([]byte(descOut), &d)
		if d.ETag != "" {
			runCLIIgnoreErr(awsCLI("cloudfront", "delete-connection-function", "--id", id, "--if-match", d.ETag))
		}
	}()

	// Describe.
	descOut := runCLI(t, awsCLI("cloudfront", "describe-connection-function", "--identifier", id, "--output", "json"))
	var desc struct {
		ConnectionFunctionSummary struct {
			Name string `json:"Name"`
		} `json:"ConnectionFunctionSummary"`
		ETag string `json:"ETag"`
	}
	parseJSON(t, descOut, &desc)
	require.Equal(t, name, desc.ConnectionFunctionSummary.Name)
	etag := desc.ETag
	require.NotEmpty(t, etag)

	// Update.
	updOut := runCLI(t, awsCLI("cloudfront", "update-connection-function",
		"--id", id,
		"--if-match", etag,
		"--connection-function-config", `{"Comment":"updated","Runtime":"cloudfront-js-2.0"}`,
		"--connection-function-code", "fileb://"+codePath,
		"--output", "json"))
	var upd struct {
		ETag string `json:"ETag"`
	}
	parseJSON(t, updOut, &upd)
	require.NotEmpty(t, upd.ETag)

	// Test.
	testOut := runCLI(t, awsCLI("cloudfront", "test-connection-function",
		"--id", id,
		"--if-match", upd.ETag,
		"--connection-object", "fileb://"+objPath,
		"--output", "json"))
	require.Contains(t, testOut, "ConnectionFunctionTestResult")

	// Publish → LIVE.
	pubOut := runCLI(t, awsCLI("cloudfront", "publish-connection-function",
		"--id", id, "--if-match", upd.ETag, "--output", "json"))
	require.Contains(t, pubOut, "LIVE")

	// List.
	listOut := runCLI(t, awsCLI("cloudfront", "list-connection-functions", "--output", "json"))
	require.Contains(t, listOut, id)

	// ListDistributionsByConnectionFunction — honest empty.
	byFnOut := runCLI(t, awsCLI("cloudfront", "list-distributions-by-connection-function",
		"--connection-function-identifier", id, "--output", "json"))
	require.Contains(t, byFnOut, "DistributionList")
}

// TestCloudFront_TestFunction_CLI exercises the CloudFront Functions
// test-function op through the real aws CLI.
func TestCloudFront_TestFunction_CLI(t *testing.T) {
	name := "clifn-tf-" + time.Now().Format("150405.000000")
	codePath := writeTempFile(t, "code.js", []byte("function handler(event) { return event.request; }"))
	eventPath := writeTempFile(t, "event.json", []byte(`{"version":"1.0","request":{"uri":"/"}}`))

	out := runCLI(t, awsCLI("cloudfront", "create-function",
		"--name", name,
		"--function-config", `{"Comment":"cli test fixture","Runtime":"cloudfront-js-2.0"}`,
		"--function-code", "fileb://"+codePath,
		"--output", "json"))
	var created struct {
		ETag string `json:"ETag"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.ETag)

	defer func() {
		descOut := runCLIIgnoreErr(awsCLI("cloudfront", "describe-function", "--name", name, "--output", "json"))
		var d struct {
			ETag string `json:"ETag"`
		}
		_ = json.Unmarshal([]byte(descOut), &d)
		if d.ETag != "" {
			runCLIIgnoreErr(awsCLI("cloudfront", "delete-function", "--name", name, "--if-match", d.ETag))
		}
	}()

	testOut := runCLI(t, awsCLI("cloudfront", "test-function",
		"--name", name,
		"--if-match", created.ETag,
		"--event-object", "fileb://"+eventPath,
		"--output", "json"))
	require.Contains(t, testOut, "TestResult")
	require.Contains(t, testOut, name)
}

// TestCloudFront_Tagging_CLI exercises tag-resource / untag-resource /
// list-tags-for-resource on a stored distribution ARN through the aws CLI.
func TestCloudFront_Tagging_CLI(t *testing.T) {
	arn, id, etag := cfCLICreateMinimalDistribution(t)
	defer runCLIIgnoreErr(awsCLI("cloudfront", "delete-distribution", "--id", id, "--if-match", etag))

	runCLI(t, awsCLI("cloudfront", "tag-resource",
		"--resource", arn,
		"--tags", `{"Items":[{"Key":"team","Value":"infra"}]}`))

	listOut := runCLI(t, awsCLI("cloudfront", "list-tags-for-resource", "--resource", arn, "--output", "json"))
	require.Contains(t, listOut, "infra")

	runCLI(t, awsCLI("cloudfront", "untag-resource",
		"--resource", arn,
		"--tag-keys", `{"Items":["team"]}`))

	listOut2 := runCLI(t, awsCLI("cloudfront", "list-tags-for-resource", "--resource", arn, "--output", "json"))
	require.NotContains(t, listOut2, "infra")
}

// TestCloudFront_CreateStreamingDistributionWithTags_CLI exercises the
// create-streaming-distribution-with-tags variant through the aws CLI.
func TestCloudFront_CreateStreamingDistributionWithTags_CLI(t *testing.T) {
	caller := "cli-stream-wt-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"StreamingDistributionConfig": {
			"CallerReference": "%s",
			"S3Origin": {"DomainName": "example.s3.amazonaws.com", "OriginAccessIdentity": ""},
			"Comment": "cli streaming withtags",
			"TrustedSigners": {"Enabled": false, "Quantity": 0},
			"Enabled": false
		},
		"Tags": {"Items": [{"Key": "k", "Value": "v"}]}
	}`, caller)

	out := runCLI(t, awsCLI("cloudfront", "create-streaming-distribution-with-tags",
		"--streaming-distribution-config-with-tags", cfgJSON, "--output", "json"))
	var created struct {
		StreamingDistribution struct {
			Id string `json:"Id"`
		} `json:"StreamingDistribution"`
		ETag string `json:"ETag"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.StreamingDistribution.Id)
	runCLIIgnoreErr(awsCLI("cloudfront", "delete-streaming-distribution",
		"--id", created.StreamingDistribution.Id, "--if-match", created.ETag))
}

// cfCLICreateMinimalDistribution creates a disabled distribution and returns its
// ARN, ID, and ETag for tagging/teardown.
func cfCLICreateMinimalDistribution(t *testing.T) (arn, id, etag string) {
	t.Helper()
	caller := "cli-complete-" + time.Now().Format("150405.000000")
	cfgJSON := fmt.Sprintf(`{
		"CallerReference": "%s",
		"Comment": "cli complete-test distribution",
		"Enabled": false,
		"Origins": {
			"Quantity": 1,
			"Items": [{
				"Id": "o1",
				"DomainName": "example.com",
				"CustomOriginConfig": {
					"HTTPPort": 80,
					"HTTPSPort": 443,
					"OriginProtocolPolicy": "http-only",
					"OriginSslProtocols": {"Quantity": 1, "Items": ["TLSv1.2"]}
				}
			}]
		},
		"DefaultCacheBehavior": {
			"TargetOriginId": "o1",
			"ViewerProtocolPolicy": "allow-all",
			"ForwardedValues": {"QueryString": false, "Cookies": {"Forward": "none"}},
			"MinTTL": 0
		}
	}`, caller)
	out := runCLI(t, awsCLI("cloudfront", "create-distribution",
		"--distribution-config", cfgJSON, "--output", "json"))
	var created struct {
		Distribution struct {
			Id  string `json:"Id"`
			ARN string `json:"ARN"`
		} `json:"Distribution"`
		ETag string `json:"ETag"`
	}
	parseJSON(t, out, &created)
	require.NotEmpty(t, created.Distribution.Id)
	return created.Distribution.ARN, created.Distribution.Id, created.ETag
}
