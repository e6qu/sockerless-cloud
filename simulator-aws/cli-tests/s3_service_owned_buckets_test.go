package aws_cli_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_CLI_ServiceOwnedBucketsStayOutOfTheCallersAccount follows the
// presigned URL `aws lambda get-function` reports for a function's deployment
// package. The bucket it names belongs to AWS: `aws s3api list-buckets` does
// not list it and the caller's own requests against it are refused, while the
// URL itself downloads the package.
func TestS3_CLI_ServiceOwnedBucketsStayOutOfTheCallersAccount(t *testing.T) {
	t.Parallel()
	fn := "cli-svc-owned-bucket-fn"
	zipPath := createLambdaSourceZip(t, fn, `exports.handler = async () => ({statusCode: 200});`)
	runCLI(t, awsCLI("lambda", "create-function",
		"--function-name", fn,
		"--runtime", "nodejs20.x",
		"--role", cliServiceRole(t, "lambda.amazonaws.com"),
		"--handler", "index.handler",
		"--zip-file", "fileb://"+zipPath,
	))
	t.Cleanup(func() { runCLI(t, awsCLI("lambda", "delete-function", "--function-name", fn)) })

	var got struct {
		Code struct {
			Location string `json:"Location"`
		} `json:"Code"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("lambda", "get-function", "--function-name", fn, "--output", "json"))), &got))
	location, err := url.Parse(got.Code.Location)
	require.NoError(t, err)
	bucket, key, ok := strings.Cut(strings.TrimPrefix(location.Path, "/"), "/")
	require.True(t, ok, "the location names a bucket and a key: %s", got.Code.Location)

	download, err := http.Get(got.Code.Location)
	require.NoError(t, err)
	defer download.Body.Close()
	body, err := io.ReadAll(download.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, download.StatusCode, string(body))
	assert.Equal(t, "PK", string(body[:2]), "the location serves the deployment package")

	var listed struct {
		Buckets []struct {
			Name string `json:"Name"`
		} `json:"Buckets"`
	}
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("s3api", "list-buckets", "--output", "json"))), &listed))
	for _, b := range listed.Buckets {
		assert.NotEqual(t, bucket, b.Name, "aws s3api list-buckets lists a bucket AWS Lambda owns")
	}

	assert.Contains(t, runCLIExpectError(t, awsCLI("s3api", "head-bucket", "--bucket", bucket)), "403")
	assert.Contains(t, runCLIExpectError(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", key)), "AccessDenied")
	assert.Contains(t, runCLIExpectError(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket)), "AccessDenied")
	assert.Contains(t, runCLIExpectError(t, awsCLI("s3api", "create-bucket", "--bucket", bucket)), "BucketAlreadyExists")
}
