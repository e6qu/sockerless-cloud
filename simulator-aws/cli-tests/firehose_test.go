package aws_cli_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirehoseCLI_DeliveryStreamWritesS3(t *testing.T) {
	const (
		name   = "cli-firehose-stream"
		bucket = "cli-firehose-bucket"
		role   = "cli-firehose-delivery"
	)
	createdRole := runCLI(t, awsCLI("iam", "create-role",
		"--role-name", role,
		"--assume-role-policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"firehose.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		"--output", "json"))
	var roleOutput struct {
		Role struct {
			ARN string `json:"Arn"`
		} `json:"Role"`
	}
	parseJSON(t, createdRole, &roleOutput)
	require.NotEmpty(t, roleOutput.Role.ARN)
	runCLI(t, awsCLI("iam", "put-role-policy",
		"--role-name", role,
		"--policy-name", "delivery",
		"--policy-document",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetBucketLocation","s3:ListBucket","s3:PutObject"],"Resource":"*"}]}`))
	t.Cleanup(func() {
		runCLI(t, awsCLI("iam", "delete-role-policy", "--role-name", role, "--policy-name", "delivery"))
		runCLI(t, awsCLI("iam", "delete-role", "--role-name", role))
	})
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	t.Cleanup(func() {
		objects := runCLI(t, awsCLI("s3api", "list-objects-v2", "--bucket", bucket, "--output", "json"))
		var listed struct {
			Contents []struct {
				Key string `json:"Key"`
			} `json:"Contents"`
		}
		parseJSON(t, objects, &listed)
		for _, object := range listed.Contents {
			runCLI(t, awsCLI("s3api", "delete-object", "--bucket", bucket, "--key", object.Key))
		}
		runCLI(t, awsCLI("s3api", "delete-bucket", "--bucket", bucket))
	})

	destination := `{"RoleARN":"` + roleOutput.Role.ARN + `","BucketARN":"arn:aws:s3:::` +
		bucket + `","Prefix":"cli/","BufferingHints":{"SizeInMBs":1,"IntervalInSeconds":0}}`
	created := runCLI(t, awsCLI("firehose", "create-delivery-stream",
		"--delivery-stream-name", name,
		"--extended-s3-destination-configuration", destination,
		"--tags", "Key=environment,Value=cli",
		"--output", "json"))
	var stream struct {
		DeliveryStreamARN string `json:"DeliveryStreamARN"`
	}
	parseJSON(t, created, &stream)
	require.NotEmpty(t, stream.DeliveryStreamARN)
	t.Cleanup(func() {
		runCLI(t, awsCLI("firehose", "delete-delivery-stream",
			"--delivery-stream-name", name, "--allow-force-delete"))
	})

	described := runCLI(t, awsCLI("firehose", "describe-delivery-stream",
		"--delivery-stream-name", name, "--output", "json"))
	assert.Contains(t, described, `"DeliveryStreamStatus": "ACTIVE"`)
	assert.Contains(t, runCLI(t, awsCLI("firehose", "list-delivery-streams", "--output", "json")), name)
	assert.Contains(t, runCLI(t, awsCLI("firehose", "list-tags-for-delivery-stream",
		"--delivery-stream-name", name, "--output", "json")), "environment")

	runCLI(t, awsCLI("firehose", "tag-delivery-stream", "--delivery-stream-name", name,
		"--tags", "Key=owner,Value=platform"))
	runCLI(t, awsCLI("firehose", "untag-delivery-stream", "--delivery-stream-name", name,
		"--tag-keys", "environment"))
	runCLI(t, awsCLI("firehose", "put-record", "--delivery-stream-name", name,
		"--record", `Data=ZnJvbS1jbGkK`, "--output", "json"))

	objects := runCLI(t, awsCLI("s3api", "list-objects-v2",
		"--bucket", bucket, "--prefix", "cli/", "--output", "json"))
	var listed struct {
		Contents []struct {
			Key string `json:"Key"`
		} `json:"Contents"`
	}
	parseJSON(t, objects, &listed)
	require.Len(t, listed.Contents, 1)
	output := filepath.Join(t.TempDir(), "record")
	runCLI(t, awsCLI("s3api", "get-object", "--bucket", bucket, "--key", listed.Contents[0].Key, output))
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	assert.Equal(t, "from-cli\n", string(data))
}
