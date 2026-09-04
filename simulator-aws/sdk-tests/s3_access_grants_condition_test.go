package aws_sdk_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3control"
	s3ctypes "github.com/aws/aws-sdk-go-v2/service/s3control/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestS3_AccessGrantsInstanceArnConditionKeyScopesTheGrant covers
// s3:AccessGrantsInstanceArn, which names the S3 Access Grants instance that
// issued the credentials a request is signed with. A policy uses it to admit
// data access that came through Access Grants and refuse the same principal
// reaching the object any other way.
//
// The negative control is the point: the refused call is made by the same role,
// under the same policy, holding credentials from a plain AssumeRole. Nothing
// differs but how the credentials were obtained, which is exactly what the key
// describes.
func TestS3_AccessGrantsInstanceArnConditionKeyScopesTheGrant(t *testing.T) {
	control := s3ControlClient()
	admin := s3Client()
	iamc := iamClient()
	bucket := "grants-condition-bucket"

	_, err := admin.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(bucket)})
	require.NoError(t, err)
	_, err = admin.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("data/report.csv"), Body: strings.NewReader("v")})
	require.NoError(t, err)

	// The location's role may read the object only when the request carries
	// credentials this Access Grants instance issued.
	const roleName = "s3-grants-condition-role"
	role, err := iamc.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName: aws.String(roleName),
		AssumeRolePolicyDocument: aws.String(
			`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",` +
				`"Principal":{"AWS":"*"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = iamc.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)}) })

	instanceARN := fmt.Sprintf("arn:aws:s3:us-east-1:%s:access-grants/default", s3ObjectLambdaAccount)
	_, err = iamc.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName: aws.String(roleName), PolicyName: aws.String("through-access-grants-only"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
			"Action":"s3:GetObject","Resource":"arn:aws:s3:::` + bucket + `/*",
			"Condition":{"StringEquals":{"s3:AccessGrantsInstanceArn":"` + instanceARN + `"}}}]}`),
	})
	require.NoError(t, err)

	_, err = control.CreateAccessGrantsInstance(ctx, &s3control.CreateAccessGrantsInstanceInput{
		AccountId: aws.String(s3ObjectLambdaAccount)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = control.DeleteAccessGrantsInstance(ctx, &s3control.DeleteAccessGrantsInstanceInput{
			AccountId: aws.String(s3ObjectLambdaAccount)})
	})

	scope := fmt.Sprintf("s3://%s/data/*", bucket)
	location, err := control.CreateAccessGrantsLocation(ctx, &s3control.CreateAccessGrantsLocationInput{
		AccountId:     aws.String(s3ObjectLambdaAccount),
		LocationScope: aws.String(scope), IAMRoleArn: role.Role.Arn})
	require.NoError(t, err)
	locationID := aws.ToString(location.AccessGrantsLocationId)
	t.Cleanup(func() {
		_, _ = control.DeleteAccessGrantsLocation(ctx, &s3control.DeleteAccessGrantsLocationInput{
			AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantsLocationId: aws.String(locationID)})
	})

	grantee := s3ControlCallerARN(t)
	grant, err := control.CreateAccessGrant(ctx, &s3control.CreateAccessGrantInput{
		AccountId:              aws.String(s3ObjectLambdaAccount),
		AccessGrantsLocationId: aws.String(locationID),
		Permission:             s3ctypes.PermissionRead,
		Grantee: &s3ctypes.Grantee{
			GranteeType: s3ctypes.GranteeTypeIam, GranteeIdentifier: aws.String(grantee)},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = control.DeleteAccessGrant(ctx, &s3control.DeleteAccessGrantInput{
			AccountId: aws.String(s3ObjectLambdaAccount), AccessGrantId: grant.AccessGrantId})
	})

	// Credentials from Access Grants carry the instance the grant belongs to.
	access, err := control.GetDataAccess(ctx, &s3control.GetDataAccessInput{
		AccountId:  aws.String(s3ObjectLambdaAccount),
		Target:     aws.String(fmt.Sprintf("s3://%s/data/report.csv", bucket)),
		Permission: s3ctypes.PermissionRead,
	})
	require.NoError(t, err)
	require.NotNil(t, access.Credentials)

	granted := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(access.Credentials.AccessKeyId),
			aws.ToString(access.Credentials.SecretAccessKey),
			aws.ToString(access.Credentials.SessionToken))},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })
	read, err := granted.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("data/report.csv")})
	require.NoError(t, err, "the credentials came from the Access Grants instance the grant names")
	_ = read.Body.Close()

	// The same role, the same policy, credentials obtained any other way: the
	// key is not there, so the grant does not match.
	stsClient := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	assumed, err := stsClient.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn: role.Role.Arn, RoleSessionName: aws.String("not-through-access-grants")})
	require.NoError(t, err)
	direct := s3.NewFromConfig(aws.Config{Region: "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(
			aws.ToString(assumed.Credentials.AccessKeyId),
			aws.ToString(assumed.Credentials.SecretAccessKey),
			aws.ToString(assumed.Credentials.SessionToken))},
		func(o *s3.Options) { o.BaseEndpoint = aws.String(baseURL); o.UsePathStyle = true })
	_, err = direct.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket), Key: aws.String("data/report.csv")})
	require.Error(t, err, "the same role reaching the object without Access Grants is not covered")
	assert.Equal(t, "AccessDenied", errCodeOf(err))
}
