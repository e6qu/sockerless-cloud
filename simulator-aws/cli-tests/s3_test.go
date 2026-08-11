package aws_cli_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestS3_MakeBucketAndList(t *testing.T) {
	runCLI(t, awsCLI("s3", "mb", "s3://cli-test-bucket"))

	out := runCLI(t, awsCLI("s3", "ls"))
	assert.Contains(t, out, "cli-test-bucket")

	// Cleanup
	runCLI(t, awsCLI("s3", "rb", "s3://cli-test-bucket"))
}

func TestS3_CopyUpload(t *testing.T) {
	runCLI(t, awsCLI("s3", "mb", "s3://upload-test-bucket"))

	// Create a local file
	localFile := filepath.Join(tmpDir, "upload.txt")
	require.NoError(t, os.WriteFile(localFile, []byte("hello from cli test"), 0644))

	runCLI(t, awsCLI("s3", "cp", localFile, "s3://upload-test-bucket/upload.txt"))

	// Verify via listing objects
	out := runCLI(t, awsCLI("s3", "ls", "s3://upload-test-bucket/"))
	assert.Contains(t, out, "upload.txt")

	// Cleanup
	runCLI(t, awsCLI("s3", "rm", "s3://upload-test-bucket/upload.txt"))
	runCLI(t, awsCLI("s3", "rb", "s3://upload-test-bucket"))
}

func TestS3API_CopyObjectListed(t *testing.T) {
	bucket := "cli-copy-object-listed"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))

	srcFile := filepath.Join(tmpDir, "copy-source.txt")
	require.NoError(t, os.WriteFile(srcFile, []byte("copy me"), 0644))
	runCLI(t, awsCLI("s3api", "put-object", "--bucket", bucket, "--key", "source.txt", "--body", srcFile))
	runCLI(t, awsCLI("s3api", "copy-object", "--bucket", bucket, "--key", "copied.txt", "--copy-source", bucket+"/source.txt"))

	out := runCLI(t, awsCLI("s3api", "list-objects-v2", "--bucket", bucket, "--prefix", "copied.txt"))
	assert.Contains(t, out, `"Key": "copied.txt"`)

	runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "source.txt"))
	runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", "copied.txt"))
	runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket))
}

func TestS3_CopyDownload(t *testing.T) {
	runCLI(t, awsCLI("s3", "mb", "s3://download-test-bucket"))

	content := "download test content"
	localFile := filepath.Join(tmpDir, "to-upload.txt")
	require.NoError(t, os.WriteFile(localFile, []byte(content), 0644))

	runCLI(t, awsCLI("s3", "cp", localFile, "s3://download-test-bucket/file.txt"))

	// Download
	downloadFile := filepath.Join(tmpDir, "downloaded.txt")
	runCLI(t, awsCLI("s3", "cp", "s3://download-test-bucket/file.txt", downloadFile))

	data, err := os.ReadFile(downloadFile)
	require.NoError(t, err)
	assert.Equal(t, content, strings.TrimSpace(string(data)))

	// Cleanup
	runCLI(t, awsCLI("s3", "rm", "s3://download-test-bucket/file.txt"))
	runCLI(t, awsCLI("s3", "rb", "s3://download-test-bucket"))
}

func TestS3_RemoveBucket(t *testing.T) {
	runCLI(t, awsCLI("s3", "mb", "s3://remove-test-bucket"))
	runCLI(t, awsCLI("s3", "rb", "s3://remove-test-bucket"))

	// Verify it's gone
	out := runCLI(t, awsCLI("s3", "ls"))
	assert.NotContains(t, out, "remove-test-bucket")
}

func TestS3APIMultipartLists(t *testing.T) {
	bucket := "cli-multipart-list-bucket"
	key := "object.txt"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))

	createOut := runCLI(t, awsCLI("s3api", "create-multipart-upload", "--bucket", bucket, "--key", key))
	var created struct {
		UploadID string `json:"UploadId"`
	}
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	require.NotEmpty(t, created.UploadID)

	partFile := filepath.Join(tmpDir, "multipart-part.txt")
	require.NoError(t, os.WriteFile(partFile, []byte("part-one"), 0644))
	runCLI(t, awsCLI("s3api", "upload-part",
		"--bucket", bucket,
		"--key", key,
		"--upload-id", created.UploadID,
		"--part-number", "1",
		"--body", partFile,
	))

	partsOut := runCLI(t, awsCLI("s3api", "list-parts",
		"--bucket", bucket,
		"--key", key,
		"--upload-id", created.UploadID,
	))
	assert.Contains(t, partsOut, `"PartNumber": 1`)

	uploadsOut := runCLI(t, awsCLI("s3api", "list-multipart-uploads", "--bucket", bucket))
	assert.Contains(t, uploadsOut, created.UploadID)
	assert.Contains(t, uploadsOut, key)

	runCLI(t, awsCLI("s3api", "abort-multipart-upload",
		"--bucket", bucket,
		"--key", key,
		"--upload-id", created.UploadID,
	))
	runCLI(t, awsCLI("s3", "rb", "s3://"+bucket))
}

func TestS3API_BucketSubresourceCoverage(t *testing.T) {
	bucket := "cli-bucket-subresources"
	destBucket := "cli-bucket-subresources-dest"
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", destBucket))
	t.Cleanup(func() {
		runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", destBucket))
		runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket))
	})

	replicationFile := writeJSONFile(t, "replication.json", `{
		"Role": "arn:aws:iam::000000000001:role/s3-replication",
		"Rules": [{
			"ID": "replicate-logs",
			"Status": "Enabled",
			"Filter": {"Prefix": "logs/"},
			"Destination": {"Bucket": "arn:aws:s3:::`+destBucket+`"}
		}]
	}`)
	runCLI(t, awsCLI("s3api", "put-bucket-replication", "--bucket", bucket, "--replication-configuration", "file://"+replicationFile))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-replication", "--bucket", bucket)), "replicate-logs")
	runCLI(t, awsCLI("s3api", "delete-bucket-replication", "--bucket", bucket))

	loggingFile := writeJSONFile(t, "logging.json", `{"LoggingEnabled":{"TargetBucket":"`+bucket+`","TargetPrefix":"logs/"}}`)
	runCLI(t, awsCLI("s3api", "put-bucket-logging", "--bucket", bucket, "--bucket-logging-status", "file://"+loggingFile))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-logging", "--bucket", bucket)), "logs/")

	aclFile := writeJSONFile(t, "acl.json", `{
		"Owner": {"ID": "000000000001", "DisplayName": "simulator"},
		"Grants": [{
			"Grantee": {"Type": "CanonicalUser", "ID": "000000000001", "DisplayName": "simulator"},
			"Permission": "FULL_CONTROL"
		}]
	}`)
	runCLI(t, awsCLI("s3api", "put-bucket-acl", "--bucket", bucket, "--access-control-policy", "file://"+aclFile))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-acl", "--bucket", bucket)), "FULL_CONTROL")

	runCLI(t, awsCLI("s3api", "put-bucket-request-payment", "--bucket", bucket, "--request-payment-configuration", `{"Payer":"Requester"}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-request-payment", "--bucket", bucket)), "Requester")

	runCLI(t, awsCLI("s3api", "put-bucket-accelerate-configuration", "--bucket", bucket, "--accelerate-configuration", `{"Status":"Enabled"}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-accelerate-configuration", "--bucket", bucket)), "Enabled")

	runCLI(t, awsCLI("s3api", "put-bucket-ownership-controls", "--bucket", bucket, "--ownership-controls", `{"Rules":[{"ObjectOwnership":"BucketOwnerPreferred"}]}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-ownership-controls", "--bucket", bucket)), "BucketOwnerPreferred")
	runCLI(t, awsCLI("s3api", "delete-bucket-ownership-controls", "--bucket", bucket))

	notificationFile := writeJSONFile(t, "notification.json", `{
		"QueueConfigurations": [{
			"Id": "queue-created",
			"QueueArn": "arn:aws:sqs:us-east-1:000000000001:queue",
			"Events": ["s3:ObjectCreated:Put"]
		}]
	}`)
	runCLI(t, awsCLI("s3api", "put-bucket-notification-configuration", "--bucket", bucket, "--notification-configuration", "file://"+notificationFile))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-notification-configuration", "--bucket", bucket)), "queue-created")

	runCLI(t, awsCLI("s3api", "put-public-access-block", "--bucket", bucket, "--public-access-block-configuration", `{"BlockPublicAcls":true,"BlockPublicPolicy":true,"IgnorePublicAcls":true,"RestrictPublicBuckets":true}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-public-access-block", "--bucket", bucket)), "BlockPublicAcls")
	runCLI(t, awsCLI("s3api", "delete-public-access-block", "--bucket", bucket))

	runCLI(t, awsCLI("s3api", "put-object-lock-configuration", "--bucket", bucket, "--object-lock-configuration", `{"ObjectLockEnabled":"Enabled","Rule":{"DefaultRetention":{"Mode":"GOVERNANCE","Days":1}}}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-object-lock-configuration", "--bucket", bucket)), "GOVERNANCE")

	runCLI(t, awsCLI("s3api", "put-bucket-intelligent-tiering-configuration", "--bucket", bucket, "--id", "archive-tier", "--intelligent-tiering-configuration", `{"Id":"archive-tier","Status":"Enabled","Tierings":[{"Days":90,"AccessTier":"ARCHIVE_ACCESS"}]}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-intelligent-tiering-configuration", "--bucket", bucket, "--id", "archive-tier")), "archive-tier")
	assert.Contains(t, runCLI(t, awsCLI("s3api", "list-bucket-intelligent-tiering-configurations", "--bucket", bucket)), "archive-tier")
	runCLI(t, awsCLI("s3api", "delete-bucket-intelligent-tiering-configuration", "--bucket", bucket, "--id", "archive-tier"))

	inventoryFile := writeJSONFile(t, "inventory.json", `{
		"Id":"inventory-current",
		"IsEnabled":true,
		"IncludedObjectVersions":"Current",
		"Destination":{"S3BucketDestination":{"Bucket":"arn:aws:s3:::`+bucket+`","Format":"CSV"}},
		"Schedule":{"Frequency":"Daily"}
	}`)
	runCLI(t, awsCLI("s3api", "put-bucket-inventory-configuration", "--bucket", bucket, "--id", "inventory-current", "--inventory-configuration", "file://"+inventoryFile))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-inventory-configuration", "--bucket", bucket, "--id", "inventory-current")), "inventory-current")
	assert.Contains(t, runCLI(t, awsCLI("s3api", "list-bucket-inventory-configurations", "--bucket", bucket)), "inventory-current")
	runCLI(t, awsCLI("s3api", "delete-bucket-inventory-configuration", "--bucket", bucket, "--id", "inventory-current"))

	analyticsFile := writeJSONFile(t, "analytics.json", `{
		"Id":"analytics-all",
		"StorageClassAnalysis":{
			"DataExport":{
				"OutputSchemaVersion":"V_1",
				"Destination":{"S3BucketDestination":{"Bucket":"arn:aws:s3:::`+bucket+`","Format":"CSV"}}
			}
		}
	}`)
	runCLI(t, awsCLI("s3api", "put-bucket-analytics-configuration", "--bucket", bucket, "--id", "analytics-all", "--analytics-configuration", "file://"+analyticsFile))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-analytics-configuration", "--bucket", bucket, "--id", "analytics-all")), "analytics-all")
	assert.Contains(t, runCLI(t, awsCLI("s3api", "list-bucket-analytics-configurations", "--bucket", bucket)), "analytics-all")
	runCLI(t, awsCLI("s3api", "delete-bucket-analytics-configuration", "--bucket", bucket, "--id", "analytics-all"))

	runCLI(t, awsCLI("s3api", "put-bucket-metrics-configuration", "--bucket", bucket, "--id", "metrics-prefix", "--metrics-configuration", `{"Id":"metrics-prefix","Filter":{"Prefix":"logs/"}}`))
	assert.Contains(t, runCLI(t, awsCLI("s3api", "get-bucket-metrics-configuration", "--bucket", bucket, "--id", "metrics-prefix")), "metrics-prefix")
	assert.Contains(t, runCLI(t, awsCLI("s3api", "list-bucket-metrics-configurations", "--bucket", bucket)), "metrics-prefix")
	runCLI(t, awsCLI("s3api", "delete-bucket-metrics-configuration", "--bucket", bucket, "--id", "metrics-prefix"))

	locationOut := runCLI(t, awsCLI("s3api", "get-bucket-location", "--bucket", bucket))
	assert.Contains(t, locationOut, "us-east-1")
}

func writeJSONFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(tmpDir, name)
	require.NoError(t, os.WriteFile(path, []byte(body), 0644))
	return path
}
