package aws_cli_test

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
)

// CreateBucket answers by the Region the request is sent to: the owner
// re-creating its bucket gets 200 in us-east-1 and BucketAlreadyOwnedByYou
// elsewhere. A name the general purpose naming rules refuse is
// InvalidBucketName.
func TestS3CLI_CreateBucketByRegionAndName(t *testing.T) {
	t.Parallel()
	bucket := uniquename.New("cli-create-rules")
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	runCLI(t, awsCLI("s3api", "create-bucket", "--bucket", bucket))
	out := runCLIExpectError(t, awsCLI("s3api", "create-bucket", "--bucket", bucket, "--region", "us-west-2",
		"--create-bucket-configuration", "LocationConstraint=us-west-2"))
	assert.Contains(t, out, "BucketAlreadyOwnedByYou")
	for _, name := range []string{"Upper-case", "two..dots", "192.168.5.4", "xn--prefix", "name-s3alias"} {
		out = runCLIExpectError(t, awsCLI("s3api", "create-bucket", "--bucket", name))
		assert.Contains(t, out, "InvalidBucketName", name)
	}
}
