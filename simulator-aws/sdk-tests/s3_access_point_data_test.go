package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3controltypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// An access point is an addressable front door onto one bucket, reached at
// <name>-<account>.s3-accesspoint.<region>.amazonaws.com. The SDK resolves that
// host from the access point ARN a caller passes as the bucket, so these tests
// pass the ARN and let it, with resolution pointed at the simulator.

// TestS3_AccessPointFrontsItsBucketAndNarrowsWhatGoesThroughIt covers the data
// plane: an object read through the access point is the bucket's object, and
// the access point's scope refuses what it does not name — whatever the
// caller's own policies allow on the bucket behind it.
func TestS3_AccessPointFrontsItsBucketAndNarrowsWhatGoesThroughIt(t *testing.T) {
	bucket := "ap-data-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	for _, key := range []string{"reports/q1", "private/keys"} {
		_, err = admin.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("v")})
		require.NoError(t, err)
	}

	control := s3control.NewFromConfig(sdkConfig(),
		func(o *s3control.Options) { o.BaseEndpoint = aws.String(simEndpoint("s3-control")) })
	const point = "ap-data-point"
	_, err = control.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point), Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = control.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point)})
	})
	arn := "arn:aws:s3:us-east-1:" + s3ObjectLambdaAccount + ":accesspoint/" + point

	// Addressed through the access point, the object is the bucket's object.
	through := s3ExpressClient()
	read, err := through.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(arn), Key: aws.String("reports/q1")})
	require.NoError(t, err, "the access point fronts the bucket it was created for")
	_ = read.Body.Close()

	// A scope narrows it: only the reports prefix, and only reads.
	_, err = control.PutAccessPointScope(ctx, &s3control.PutAccessPointScopeInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point),
		Scope: &s3controltypes.Scope{
			Prefixes:    []string{"reports/"},
			Permissions: []s3controltypes.ScopePermission{s3controltypes.ScopePermissionGetObject},
		}})
	require.NoError(t, err)

	_, err = through.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(arn), Key: aws.String("reports/q1")})
	assert.NoError(t, err, "the scope names this prefix and this operation")

	_, err = through.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(arn), Key: aws.String("private/keys")})
	require.Error(t, err, "a key outside the scope's prefixes is refused")
	assert.Equal(t, "AccessDenied", errCodeOf(err))

	_, err = through.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(arn), Key: aws.String("reports/q2"), Body: strings.NewReader("v")})
	require.Error(t, err, "an operation the scope does not permit is refused")
	assert.Equal(t, "AccessDenied", errCodeOf(err))

	// The bucket itself is unaffected: the access point narrows the door, not
	// the bucket behind it.
	_, err = admin.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("private/keys")})
	assert.NoError(t, err, "the bucket still serves what its own policies allow")
}

// TestS3_DataAccessPointConditionKeysScopeTheGrant covers
// s3:DataAccessPointArn: a policy that grants a read only when it arrives
// through one access point, which is how an administrator requires that data be
// reached through a front door rather than at the bucket.
func TestS3_DataAccessPointConditionKeysScopeTheGrant(t *testing.T) {
	bucket := "ap-condition-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	control := s3control.NewFromConfig(sdkConfig(),
		func(o *s3control.Options) { o.BaseEndpoint = aws.String(simEndpoint("s3-control")) })
	const point = "ap-condition-point"
	_, err = control.CreateAccessPoint(ctx, &s3control.CreateAccessPointInput{
		AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point), Bucket: aws.String(bucket)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = control.DeleteAccessPoint(ctx, &s3control.DeleteAccessPointInput{
			AccountId: aws.String(s3ObjectLambdaAccount), Name: aws.String(point)})
	})
	arn := "arn:aws:s3:us-east-1:" + s3ObjectLambdaAccount + ":accesspoint/" + point

	akid, secret := restrictedCredential(t, "s3-through-the-front-door",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject",
		  "Resource":"arn:aws:s3:::`+bucket+`/*",
		  "Condition":{"StringEquals":{"s3:DataAccessPointArn":"`+arn+`"}}}]}`)

	throughPoint := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, ""),
		HTTPClient:  simHTTPClient},
		func(o *s3.Options) { o.EndpointOptions.DisableHTTPS = true })
	_, err = throughPoint.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(arn), Key: aws.String("k")})
	assert.NoError(t, err, "the read arrives through the access point the grant names")

	atBucket := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })
	_, err = atBucket.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k")})
	require.Error(t, err, "the same read at the bucket carries no access point, so the grant does not match")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}
