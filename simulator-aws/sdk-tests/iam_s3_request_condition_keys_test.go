package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_RequestHeaderConditionKeysScopeTheGrant covers the keys that constrain
// how an object is written rather than which object it is — the canonical S3
// policy: refuse a public ACL, require server-side encryption, hold a caller to
// one storage class. Each reads a header of the request, verbatim.
func TestS3_RequestHeaderConditionKeysScopeTheGrant(t *testing.T) {
	bucket := "s3-request-header-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	// Writes must be encrypted and must not be public — the shape an
	// administrator actually writes.
	akid, secret := restrictedCredential(t, "s3-encrypted-private-only",
		`{"Version":"2012-10-17","Statement":[
		  {"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::`+bucket+`/*",
		   "Condition":{"StringEquals":{"s3:x-amz-server-side-encryption":"AES256"}}},
		  {"Effect":"Deny","Action":"s3:PutObject","Resource":"arn:aws:s3:::`+bucket+`/*",
		   "Condition":{"StringEquals":{"s3:x-amz-acl":"public-read"}}}]}`)
	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	_, err = restricted.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("encrypted"), Body: strings.NewReader("v"),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256})
	assert.NoError(t, err, "the write asks for the encryption the grant requires")

	_, err = restricted.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("plain"), Body: strings.NewReader("v")})
	require.Error(t, err, "an unencrypted write is not covered by the grant")
	assert.Equal(t, "AccessDenied", errCodeOf(err))

	_, err = restricted.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("public"), Body: strings.NewReader("v"),
		ServerSideEncryption: s3types.ServerSideEncryptionAes256,
		ACL:                  s3types.ObjectCannedACLPublicRead})
	require.Error(t, err, "a public ACL is denied outright")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestS3_RequestObjectTagConditionKeyScopesTheGrant covers
// s3:RequestObjectTag/<key>, the tags a write puts on the object it creates.
func TestS3_RequestObjectTagConditionKeyScopesTheGrant(t *testing.T) {
	bucket := "s3-request-tag-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	akid, secret := restrictedCredential(t, "s3-must-label-writes",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject",
		  "Resource":"arn:aws:s3:::`+bucket+`/*",
		  "Condition":{"StringEquals":{"s3:RequestObjectTag/classification":"public"}}}]}`)
	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	_, err = restricted.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("labelled"), Body: strings.NewReader("v"),
		Tagging: aws.String("classification=public")})
	assert.NoError(t, err, "the write carries the tag the grant requires")

	_, err = restricted.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("unlabelled"), Body: strings.NewReader("v")})
	require.Error(t, err, "a write carrying no such tag is not covered")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestLambda_FunctionUrlAuthTypeConditionKeyScopesTheGrant covers
// lambda:FunctionUrlAuthType, which a policy pins so no function URL is left
// reachable without authentication.
func TestLambda_FunctionUrlAuthTypeConditionKeyScopesTheGrant(t *testing.T) {
	admin := lambdaClient()
	lambdaConditionFunction(t, admin, "cond-authtype-fn")

	akid, secret := restrictedCredential(t, "lambda-signed-urls-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateFunctionUrlConfig",
		  "Resource":"*","Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"AWS_IAM"}}}]}`)
	restricted := lambda.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *lambda.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String("cond-authtype-fn"), AuthType: lambdatypes.FunctionUrlAuthTypeAwsIam})
	assert.NoError(t, err, "the URL requires the authentication the grant names")

	// The URL exists now, so the second attempt needs a clear field: removing
	// it is the administrator's call, not the restricted principal's.
	_, err = admin.DeleteFunctionUrlConfig(ctx, &lambda.DeleteFunctionUrlConfigInput{
		FunctionName: aws.String("cond-authtype-fn")})
	require.NoError(t, err)

	_, err = restricted.CreateFunctionUrlConfig(ctx, &lambda.CreateFunctionUrlConfigInput{
		FunctionName: aws.String("cond-authtype-fn"), AuthType: lambdatypes.FunctionUrlAuthTypeNone})
	require.Error(t, err, "an unauthenticated URL is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}
