package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3CLI_ListBucketsPaginates lists a prefix's buckets two to a page; the
// CLI follows the continuation token and each bucket names its Region.
func TestS3CLI_ListBucketsPaginates(t *testing.T) {
	t.Parallel()
	names := []string{"lb-page-cli-a", "lb-page-cli-b", "lb-page-cli-c"}
	for _, name := range names {
		runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", name))
		t.Cleanup(func() { runCLIIgnore(awsCLI("s3api", "delete-bucket", "--bucket", name)) })
	}

	var listed [][]string
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, awsCLI("s3api", "list-buckets",
		"--prefix", "lb-page-cli-", "--page-size", "2",
		"--query", "Buckets[].[Name,BucketRegion]", "--output", "json"))), &listed))
	require.Len(t, listed, len(names))
	for i, row := range listed {
		assert.Equal(t, []string{names[i], "us-east-1"}, row)
	}

	assert.JSONEq(t, "[]", runCLI(t, awsCLI("s3api", "list-buckets",
		"--prefix", "lb-page-cli-", "--bucket-region", "eu-west-1",
		"--query", "Buckets", "--output", "json")))
}
