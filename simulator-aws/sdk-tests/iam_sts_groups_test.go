package aws_sdk_test

import (
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func errCodeOf(err error) string {
	var ae smithy.APIError
	if errors.As(err, &ae) {
		return ae.ErrorCode()
	}
	return ""
}

// TestSTS_AssumeRoleEnforcement proves AssumeRole mints temporary (ASIA…)
// credentials that resolve, under call-time enforcement, to the assumed role's
// policies — and that GetCallerIdentity reports the assumed-role ARN.
func TestSTS_AssumeRoleEnforcement(t *testing.T) {
	admin := iamClient()
	role := "sts-enf-role"

	_, err := admin.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(role),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`),
	})
	require.NoError(t, err)
	defer admin.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(role)})

	_, err = admin.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String(role),
		PolicyName:     aws.String("only-describe"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*"}]}`),
	})
	require.NoError(t, err)

	stsc := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	out, err := stsc.AssumeRole(ctx, &sts.AssumeRoleInput{
		RoleArn:         aws.String("arn:aws:iam::000000000000:role/" + role),
		RoleSessionName: aws.String("sess1"),
	})
	require.NoError(t, err)
	akid := aws.ToString(out.Credentials.AccessKeyId)
	assert.Contains(t, akid, "ASIA")

	assumed := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		akid, aws.ToString(out.Credentials.SecretAccessKey), aws.ToString(out.Credentials.SessionToken))}
	ec2r := ec2.NewFromConfig(assumed, func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = ec2r.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "the role grants ec2:DescribeVolumes")
	_, err = ec2r.CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1)})
	require.Error(t, err)
	assert.Equal(t, "UnauthorizedOperation", errCodeOf(err))

	stsAssumed := sts.NewFromConfig(assumed, func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })
	id, err := stsAssumed.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(id.Arn), "assumed-role/"+role+"/sess1")
}

// TestIAM_GroupInheritanceEnforcement proves a user inherits its group's
// policies under enforcement.
func TestIAM_GroupInheritanceEnforcement(t *testing.T) {
	admin := iamClient()
	user, group := "grp-user", "describe-group"

	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	_, err = admin.CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	defer admin.DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(group)})

	_, err = admin.PutGroupPolicy(ctx, &iam.PutGroupPolicyInput{
		GroupName:      aws.String(group),
		PolicyName:     aws.String("grp-describe"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeVolumes","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	_, err = admin.AddUserToGroup(ctx, &iam.AddUserToGroupInput{GroupName: aws.String(group), UserName: aws.String(user)})
	require.NoError(t, err)

	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	ec2r := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = ec2r.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "the group grants ec2:DescribeVolumes to its member")
	_, err = ec2r.CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1)})
	require.Error(t, err)
	assert.Equal(t, "UnauthorizedOperation", errCodeOf(err))
}

// TestIAM_PermissionBoundaryEnforcement proves a permission boundary caps a
// user whose identity policy is broader than the boundary.
func TestIAM_PermissionBoundaryEnforcement(t *testing.T) {
	admin := iamClient()
	user := "boundary-user"

	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

	// Identity policy allows all of EC2.
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("ec2-all"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:*","Resource":"*"}]}`),
	})
	require.NoError(t, err)

	// Boundary allows only ec2:Describe*.
	boundary, err := admin.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("describe-boundary"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:Describe*","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	_, err = admin.PutUserPermissionsBoundary(ctx, &iam.PutUserPermissionsBoundaryInput{
		UserName:            aws.String(user),
		PermissionsBoundary: boundary.Policy.Arn,
	})
	require.NoError(t, err)

	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	cfg := aws.Config{Region: "us-east-1", Credentials: credentials.NewStaticCredentialsProvider(
		aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")}
	ec2r := ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) })

	_, err = ec2r.DescribeVolumes(ctx, &ec2.DescribeVolumesInput{})
	assert.NoError(t, err, "Describe* is within both the policy and the boundary")
	_, err = ec2r.CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1)})
	require.Error(t, err, "CreateVolume is allowed by the policy but blocked by the boundary")
	assert.Equal(t, "UnauthorizedOperation", errCodeOf(err))
}
