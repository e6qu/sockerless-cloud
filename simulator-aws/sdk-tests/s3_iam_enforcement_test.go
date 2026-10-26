package aws_sdk_test

import (
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_RESTEnforcement expects a user's credential to be allowed the object
// action its policy grants on the bucket and refused the one it does not.
func TestS3_RESTEnforcement(t *testing.T) {
	t.Parallel()
	admin := s3Client()
	iamc := iamClient()
	bucket := uniquename.New("s3-enf-bucket")

	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	user := uniquename.New("s3-reader")
	_, err = iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
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

	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	assert.NoError(t, err, "s3:GetObject is granted on this bucket")

	_, err = restricted.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	require.Error(t, err, "s3:DeleteObject is not granted and must be denied")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestS3_BucketPolicyDenyEnforced expects a bucket policy's explicit Deny to
// win over an identity policy's Allow.
func TestS3_BucketPolicyDenyEnforced(t *testing.T) {
	t.Parallel()
	admin := s3Client()
	iamc := iamClient()
	bucket := uniquename.New("s3-bp-deny")

	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	user := uniquename.New("s3-bp-user")
	_, err = iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	created, err := iamc.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	userArn := aws.ToString(created.User.Arn)

	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("s3-all"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`),
	})
	require.NoError(t, err)
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
