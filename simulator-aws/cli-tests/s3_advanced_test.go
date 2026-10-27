package aws_cli_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3CLI_BucketMetadataTableConfiguration round-trips the V1 S3 Metadata
// table configuration (?metadataTable). aws CLI 2.26.6 lacks the V2
// ?metadataConfiguration operations, ?abac, ?renameObject, object ?encryption
// and the inventory and journal table-config updates;
// sdk-tests/s3_advanced_test.go covers them.
func TestS3CLI_BucketMetadataTableConfiguration(t *testing.T) {
	t.Parallel()
	bucket := "cli-metadata-table-bucket"
	_ = awsCLI("s3api", "create-bucket", "--bucket", bucket).Run()
	t.Cleanup(func() {
		_ = awsCLI("s3api", "delete-bucket-metadata-table-configuration", "--bucket", bucket).Run()
		_ = awsCLI("s3api", "delete-bucket", "--bucket", bucket).Run()
	})

	cfg := `{"S3TablesDestination":{"TableBucketArn":"arn:aws:s3tables:us-east-1:000000000000:bucket/cli-table-bucket","TableName":"cli-metadata-table"}}`
	runCLI(t, awsCLI("s3api", "create-bucket-metadata-table-configuration",
		"--bucket", bucket, "--metadata-table-configuration", cfg))

	out := runCLI(t, awsCLI("s3api", "get-bucket-metadata-table-configuration", "--bucket", bucket))
	var resp struct {
		GetBucketMetadataTableConfigurationResult struct {
			MetadataTableConfigurationResult struct {
				S3TablesDestinationResult struct {
					TableBucketArn string `json:"TableBucketArn"`
					TableName      string `json:"TableName"`
					TableNamespace string `json:"TableNamespace"`
				} `json:"S3TablesDestinationResult"`
			} `json:"MetadataTableConfigurationResult"`
			Status string `json:"Status"`
		} `json:"GetBucketMetadataTableConfigurationResult"`
	}
	require.NoError(t, json.Unmarshal([]byte(out), &resp))
	dest := resp.GetBucketMetadataTableConfigurationResult.MetadataTableConfigurationResult.S3TablesDestinationResult
	assert.Equal(t, "cli-metadata-table", dest.TableName)
	assert.Equal(t, "aws_s3_metadata", dest.TableNamespace)

	runCLI(t, awsCLI("s3api", "delete-bucket-metadata-table-configuration", "--bucket", bucket))
}
