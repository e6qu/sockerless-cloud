package aws_sdk_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/organizations"
	orgtypes "github.com/aws/aws-sdk-go-v2/service/organizations/types"
	"github.com/aws/aws-sdk-go-v2/service/rds"
	rdstypes "github.com/aws/aws-sdk-go-v2/service/rds/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
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

// TestS3_ExistingObjectTagConditionKeyScopesTheGrant covers
// s3:ExistingObjectTag/<key>: the tags already on the object a request targets,
// which is how a policy grants a read of the objects somebody tagged one way
// and refuses the ones tagged another.
func TestS3_ExistingObjectTagConditionKeyScopesTheGrant(t *testing.T) {
	bucket := "s3-existing-tag-bucket"
	admin := s3Client()
	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)

	for key, classification := range map[string]string{"open": "public", "closed": "secret"} {
		_, err = admin.PutObject(ctx, &s3.PutObjectInput{
			Bucket: aws.String(bucket), Key: aws.String(key), Body: strings.NewReader("v")})
		require.NoError(t, err)
		_, err = admin.PutObjectTagging(ctx, &s3.PutObjectTaggingInput{
			Bucket: aws.String(bucket), Key: aws.String(key),
			Tagging: &s3types.Tagging{TagSet: []s3types.Tag{
				{Key: aws.String("classification"), Value: aws.String(classification)}}}})
		require.NoError(t, err)
	}

	akid, secret := restrictedCredential(t, "s3-public-objects-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject",
		  "Resource":"arn:aws:s3:::`+bucket+`/*",
		  "Condition":{"StringEquals":{"s3:ExistingObjectTag/classification":"public"}}}]}`)
	restricted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })

	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("open")})
	assert.NoError(t, err, "the object carries the tag the grant names")

	_, err = restricted.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String("closed")})
	require.Error(t, err, "an object tagged otherwise is not covered by the grant")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}

// TestSecretsManager_SecretIdConditionKeyScopesTheGrant covers
// secretsmanager:SecretId, which AWS documents for scoping a grant to the one
// secret a request may name.
func TestSecretsManager_SecretIdConditionKeyScopesTheGrant(t *testing.T) {
	admin := secretsmanager.NewFromConfig(sdkConfig(),
		func(o *secretsmanager.Options) { o.BaseEndpoint = aws.String(baseURL) })
	permitted, refused := "cond-permitted-secret", "cond-refused-secret"
	for _, name := range []string{permitted, refused} {
		_, err := admin.CreateSecret(ctx, &secretsmanager.CreateSecretInput{
			Name: aws.String(name), SecretString: aws.String("value")})
		require.NoError(t, err)
		t.Cleanup(func() {
			_, _ = admin.DeleteSecret(ctx, &secretsmanager.DeleteSecretInput{
				SecretId: aws.String(name), ForceDeleteWithoutRecovery: aws.Bool(true)})
		})
	}

	akid, secret := restrictedCredential(t, "sm-one-secret",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"secretsmanager:GetSecretValue",
		  "Resource":"*","Condition":{"StringEquals":{"secretsmanager:SecretId":"`+permitted+`"}}}]}`)
	restricted := secretsmanager.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *secretsmanager.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(permitted)})
	assert.NoError(t, err, "the grant names this secret")

	_, err = restricted.GetSecretValue(ctx, &secretsmanager.GetSecretValueInput{
		SecretId: aws.String(refused)})
	require.Error(t, err, "a secret the condition does not name is refused")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestIAM_PermissionsBoundaryConditionKeyScopesTheGrant covers
// iam:PermissionsBoundary, the key an administrator uses to delegate user
// creation while requiring every created user to carry a boundary.
func TestIAM_PermissionsBoundaryConditionKeyScopesTheGrant(t *testing.T) {
	iamc := iamClient()
	boundary, err := iamc.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName: aws.String("cond-boundary"),
		PolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	boundaryARN := aws.ToString(boundary.Policy.Arn)
	t.Cleanup(func() { _, _ = iamc.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(boundaryARN)}) })

	akid, secretKey := restrictedCredential(t, "iam-delegated-creator",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateUser","Resource":"*",
		  "Condition":{"StringEquals":{"iam:PermissionsBoundary":"`+boundaryARN+`"}}}]}`)
	delegated := iam.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secretKey, "")},
		func(o *iam.Options) { o.BaseEndpoint = aws.String(baseURL) })

	created, err := delegated.CreateUser(ctx, &iam.CreateUserInput{
		UserName: aws.String("cond-bounded-user"), PermissionsBoundary: aws.String(boundaryARN)})
	assert.NoError(t, err, "the request attaches the boundary the grant requires")
	if err == nil {
		t.Cleanup(func() {
			_, _ = iamc.DeleteUser(ctx, &iam.DeleteUserInput{UserName: created.User.UserName})
		})
	}

	_, err = delegated.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String("cond-unbounded-user")})
	require.Error(t, err, "a user created with no boundary is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestRDS_RequestTagConditionKeyScopesTheGrant covers rds:req-tag/${TagKey},
// which is Amazon RDS's own spelling of the tags a request carries — the key a
// policy uses to require that everything created be labelled.
func TestRDS_RequestTagConditionKeyScopesTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "rds-must-tag-env",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"rds:CreateDBParameterGroup",
		  "Resource":"*","Condition":{"StringEquals":{"rds:req-tag/env":"dev"}}}]}`)
	restricted := rds.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *rds.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err := restricted.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String("cond-tagged-group"),
		DBParameterGroupFamily: aws.String("postgres17"),
		Description:            aws.String("carries the tag the grant requires"),
		Tags:                   []rdstypes.Tag{{Key: aws.String("env"), Value: aws.String("dev")}},
	})
	assert.NoError(t, err, "the request carries the tag the grant requires")

	_, err = restricted.CreateDBParameterGroup(ctx, &rds.CreateDBParameterGroupInput{
		DBParameterGroupName:   aws.String("cond-untagged-group"),
		DBParameterGroupFamily: aws.String("postgres17"),
		Description:            aws.String("carries no such tag"),
	})
	require.Error(t, err, "a request carrying no such tag is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestKMS_RequestAliasConditionKeyScopesTheGrant covers kms:RequestAlias — the
// alias a request named the key by, which is how a policy grants use of a key
// through one alias and not another.
func TestKMS_RequestAliasConditionKeyScopesTheGrant(t *testing.T) {
	admin := kmsClient()
	key, err := admin.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("alias condition")})
	require.NoError(t, err)
	keyID := aws.ToString(key.KeyMetadata.KeyId)

	for _, alias := range []string{"alias/cond-permitted", "alias/cond-refused"} {
		_, err = admin.CreateAlias(ctx, &kms.CreateAliasInput{
			AliasName: aws.String(alias), TargetKeyId: aws.String(keyID)})
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = admin.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String(alias)}) })
	}

	akid, secret := restrictedCredential(t, "kms-one-alias",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Encrypt","Resource":"*",
		  "Condition":{"StringEquals":{"kms:RequestAlias":"alias/cond-permitted"}}}]}`)
	restricted := kms.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *kms.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = restricted.Encrypt(ctx, &kms.EncryptInput{
		KeyId: aws.String("alias/cond-permitted"), Plaintext: []byte("secret")})
	assert.NoError(t, err, "the request names the key by the alias the grant allows")

	_, err = restricted.Encrypt(ctx, &kms.EncryptInput{
		KeyId: aws.String("alias/cond-refused"), Plaintext: []byte("secret")})
	require.Error(t, err, "the same key reached through another alias is not covered")
	assert.Contains(t, err.Error(), "not authorized")
}

// TestOrganizations_PolicyTypeConditionKeyScopesTheGrant covers
// organizations:PolicyType, which scopes a grant to one kind of policy — the
// request states the type where it creates one, and names the policy whose type
// it is everywhere else.
func TestOrganizations_PolicyTypeConditionKeyScopesTheGrant(t *testing.T) {
	akid, secret := restrictedCredential(t, "org-scp-only",
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"organizations:CreatePolicy",
		  "Resource":"*",
		  "Condition":{"StringEquals":{"organizations:PolicyType":"SERVICE_CONTROL_POLICY"}}}]}`)
	restricted := organizations.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, "")},
		func(o *organizations.Options) { o.BaseEndpoint = aws.String(baseURL) })

	const document = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	_, err := restricted.CreatePolicy(ctx, &organizations.CreatePolicyInput{
		Name: aws.String("cond-scp"), Description: aws.String("the type the grant names"),
		Content: aws.String(document), Type: orgtypes.PolicyTypeServiceControlPolicy})
	assert.NoError(t, err, "the request creates the policy type the grant names")

	_, err = restricted.CreatePolicy(ctx, &organizations.CreatePolicyInput{
		Name: aws.String("cond-tag-policy"), Description: aws.String("another type"),
		Content: aws.String(`{"tags":{}}`), Type: orgtypes.PolicyTypeTagPolicy})
	require.Error(t, err, "another policy type is not covered by the grant")
	assert.Contains(t, err.Error(), "not authorized")
}
