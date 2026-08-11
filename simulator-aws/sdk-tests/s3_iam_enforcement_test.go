package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests exercise the S3 REST routes now wrapped with IAM enforcement:
//   GET /{$}, GET /{bucket}, PUT /{bucket}, DELETE /{bucket},
//   GET /{bucket}/{key...}, PUT /{bucket}/{key...}, DELETE /{bucket}/{key...},
//   POST /{bucket}, POST /{bucket}/{key...}

// TestS3_RESTEnforcement proves call-time IAM enforcement on the S3 REST data
// plane: a registered restricted credential is allowed the object action its
// policy grants (resource-scoped) and denied the one it doesn't — while
// unregistered test credentials remain permissive.
func TestS3_RESTEnforcement(t *testing.T) {
	admin := s3Client()
	iamc := iamClient()
	bucket := "s3-enf-bucket"

	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	user := "s3-reader"
	_, err = iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	// Allow only GetObject on this bucket — not DeleteObject.
	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("ro"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`),
	})
	require.NoError(t, err)
	key, err := iamc.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)

	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	// Granted: GetObject succeeds.
	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	assert.NoError(t, err, "s3:GetObject is granted on this bucket")

	// Not granted: DeleteObject is denied (AccessDenied).
	_, err = restricted.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	require.Error(t, err, "s3:DeleteObject is not granted and must be denied")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestS3_BucketPolicyDenyEnforced proves a resource-based bucket policy that
// explicitly denies a principal is enforced even when the identity policy
// allows the action.
func TestS3_BucketPolicyDenyEnforced(t *testing.T) {
	admin := s3Client()
	iamc := iamClient()
	bucket := "s3-bp-deny"

	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	user := "s3-bp-user"
	_, err = iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	created, err := iamc.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	userArn := aws.ToString(created.User.Arn)

	// Identity allows all of S3.
	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("s3-all"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	// Bucket policy explicitly denies this user GetObject.
	_, err = admin.PutBucketPolicy(ctx, &s3.PutBucketPolicyInput{
		Bucket: aws.String(bucket),
		Policy: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Principal":{"AWS":"` + userArn + `"},"Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*"}]}`),
	})
	require.NoError(t, err)

	key, err := iamc.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	require.Error(t, err, "the bucket policy explicitly denies GetObject")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}
