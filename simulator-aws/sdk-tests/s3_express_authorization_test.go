package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// s3ExpressRoleClient is s3ExpressClient signed as a role session carrying
// policyDocument, so the gate evaluates the role's policies.
func s3ExpressRoleClient(t *testing.T, name, policyDocument string) *s3.Client {
	t.Helper()
	cfg := sdkConfig()
	cfg.Credentials = roleSessionConfig(t, name, policyDocument).Credentials
	return s3.NewFromConfig(cfg, func(o *s3.Options) { o.EndpointOptions.DisableHTTPS = true })
}

func s3ExpressBucketInput(bucket, zone string) *s3.CreateBucketInput {
	return &s3.CreateBucketInput{
		Bucket: aws.String(bucket),
		CreateBucketConfiguration: &s3types.CreateBucketConfiguration{
			Location: &s3types.LocationInfo{Type: s3types.LocationTypeAvailabilityZone, Name: aws.String(zone)},
			Bucket:   &s3types.BucketInfo{Type: s3types.BucketTypeDirectory, DataRedundancy: s3types.DataRedundancySingleAvailabilityZone},
		},
	}
}

// A directory bucket's operations are authorized in the s3express namespace:
// CreateBucket as s3express:CreateBucket with s3express:LocationName, and
// CreateSession as s3express:CreateSession on the bucket's s3express ARN with
// s3express:SessionMode.
func TestS3_DirectoryBucketOperationsAreAuthorizedAsS3Express(t *testing.T) {
	t.Parallel()
	admin := s3ExpressClient()
	base := strings.ToLower(uniquename.New("sim-auth"))
	bucket := base + "--use1-az4--x-s3"

	creator := s3ExpressRoleClient(t, "s3express-create",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3express:CreateBucket","Resource":"*",`+
			`"Condition":{"StringEquals":{"s3express:LocationName":"use1-az4"}}}]}`)
	_, err := creator.CreateBucket(ctx, s3ExpressBucketInput(base+"--use1-az5--x-s3", "use1-az5"))
	assert.Equal(t, "AccessDenied", errCodeOf(err), "a bucket outside the granted zone")
	_, err = creator.CreateBucket(ctx, s3ExpressBucketInput(bucket, "use1-az4"))
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteBucket(ctx, &s3.DeleteBucketInput{Bucket: aws.String(bucket)}) })

	reader := s3ExpressRoleClient(t, "s3express-session",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3express:CreateSession",`+
			`"Resource":"arn:aws:s3express:us-east-1:*:bucket/`+bucket+`",`+
			`"Condition":{"StringEquals":{"s3express:SessionMode":"ReadOnly"}}}]}`)
	_, err = reader.CreateSession(ctx, &s3.CreateSessionInput{Bucket: aws.String(bucket), SessionMode: s3types.SessionModeReadWrite})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "a ReadWrite session the grant does not cover")
	session, err := reader.CreateSession(ctx, &s3.CreateSessionInput{Bucket: aws.String(bucket), SessionMode: s3types.SessionModeReadOnly})
	require.NoError(t, err)
	assert.NotEmpty(t, aws.ToString(session.Credentials.SessionToken))

	ungranted := s3ExpressRoleClient(t, "s3express-s3-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`)
	_, err = ungranted.CreateSession(ctx, &s3.CreateSessionInput{Bucket: aws.String(bucket), SessionMode: s3types.SessionModeReadOnly})
	assert.Equal(t, "AccessDenied", errCodeOf(err), "s3 actions grant nothing on a directory bucket")
}
