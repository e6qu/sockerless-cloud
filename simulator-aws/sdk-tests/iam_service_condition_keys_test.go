package aws_sdk_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// restrictedCredential registers a user carrying one inline policy and returns
// its access key, so a test can call as a principal the policy governs.
func restrictedCredential(t *testing.T, user, policy string) (akid, secret string) {
	t.Helper()
	iamc := iamClient()
	_, err := iamc.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })
	_, err = iamc.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("scoped"),
		PolicyDocument: aws.String(policy),
	})
	require.NoError(t, err)
	key, err := iamc.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	return aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey)
}

// TestEC2_RegionConditionKeyScopesTheGrant covers ec2:Region, the condition key
// Amazon EC2 declares against nearly every one of its actions. A grant carrying
// it must hold in the region it names and nowhere else; before the request's
// region reached the condition context, the key was absent and the grant
// matched nothing at all.
func TestEC2_RegionConditionKeyScopesTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "ec2-one-region",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*",
		  "Condition":{"StringEquals":{"ec2:Region":"us-east-1"}}}]}`)

	clientIn := func(region string) *ec2.Client {
		return ec2.NewFromConfig(aws.Config{Region: region,
			Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
			func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })
	}

	_, err := clientIn("us-east-1").DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "the grant names this region, so ec2:Region must be in the context for it to match")

	_, err = clientIn("us-west-2").DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	require.Error(t, err, "a region the condition does not name is refused")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestS3_AuthTypeConditionKeyScopesTheGrant covers s3:authType, which Amazon S3
// declares so a policy can require that a request be signed in its headers
// rather than presigned in its query string. Both forms are real requests the
// SDK makes, and they differ only in how the signature travels, so the key has
// to describe the request that actually arrived.
func TestS3_AuthTypeConditionKeyScopesTheGrant(t *testing.T) {
	bucket := "s3-authtype-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	akid, secret := restrictedCredential(t, "s3-header-signed-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject",
		  "Resource":"arn:aws:s3:::`+bucket+`/*",
		  "Condition":{"StringEquals":{"s3:authType":"REST-HEADER"}}}]}`)

	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	assert.NoError(t, err, "the SDK signs in the Authorization header, which is what the grant requires")

	// The same read, presigned: the signature travels in the query string, so
	// s3:authType is REST-QUERY-STRING and the grant does not cover it.
	presigned, err := s3.NewPresignClient(restricted).PresignGetObject(ctx,
		&s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
	require.NoError(t, err)
	resp, err := http.Get(presigned.URL) //nolint:noctx // the presigned URL carries its own auth
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	assert.Equal(t, 403, resp.StatusCode, "a presigned read is not REST-HEADER, so the grant does not cover it")
}

// TestS3_SignatureAgeConditionKeyScopesTheGrant covers s3:signatureAge, the
// number of milliseconds since the request was signed. It is how a policy
// refuses a presigned URL that has been passed around: the grant holds while
// the signature is fresh and lapses once it is not. A context without the key
// cannot express either half.
func TestS3_SignatureAgeConditionKeyScopesTheGrant(t *testing.T) {
	bucket := "s3-sigage-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("k"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	read := func(user, bound string) error {
		akid, secret := restrictedCredential(t, user,
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject",
			  "Resource":"arn:aws:s3:::`+bucket+`/*",
			  "Condition":{"NumericLessThan":{"s3:signatureAge":"`+bound+`"}}}]}`)
		client := s3.NewFromConfig(aws.Config{Region: "us-east-1",
			Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
			func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })
		_, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("k")})
		return err
	}

	// A freshly signed request is younger than five minutes.
	assert.NoError(t, read("s3-fresh-signature", "300000"),
		"the request was just signed, so its age must be in the context and under the bound")

	// No request is younger than zero milliseconds.
	err = read("s3-no-signature-age", "0")
	require.Error(t, err, "a signature age no request can be under refuses every request")
	assert.Contains(t, err.Error(), "not authorized")
}
