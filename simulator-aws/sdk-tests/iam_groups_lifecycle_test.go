package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_GroupLifecycle exercises the full IAM group surface plus ListUsers and
// the permission-boundary delete: CreateGroup/GetGroup/ListGroups,
// AddUserToGroup/RemoveUserFromGroup/ListGroupsForUser, the group inline +
// attached policy ops, and DeleteGroup.
func TestIAM_GroupLifecycle(t *testing.T) {
	c := iamClient()
	user, group := "gl-user", "gl-group"

	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	_, err = c.CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(group)}) })

	gg, err := c.GetGroup(ctx, &iam.GetGroupInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	assert.Equal(t, group, aws.ToString(gg.Group.GroupName))

	lg, err := c.ListGroups(ctx, &iam.ListGroupsInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, lg.Groups)

	_, err = c.AddUserToGroup(ctx, &iam.AddUserToGroupInput{GroupName: aws.String(group), UserName: aws.String(user)})
	require.NoError(t, err)

	lgu, err := c.ListGroupsForUser(ctx, &iam.ListGroupsForUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, lgu.Groups, 1)
	assert.Equal(t, group, aws.ToString(lgu.Groups[0].GroupName))

	lu, err := c.ListUsers(ctx, &iam.ListUsersInput{})
	require.NoError(t, err)
	assert.NotEmpty(t, lu.Users)

	_, err = c.PutGroupPolicy(ctx, &iam.PutGroupPolicyInput{
		GroupName:      aws.String(group),
		PolicyName:     aws.String("g-inline"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:ListBucket","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	_, err = c.GetGroupPolicy(ctx, &iam.GetGroupPolicyInput{GroupName: aws.String(group), PolicyName: aws.String("g-inline")})
	require.NoError(t, err)
	lgp, err := c.ListGroupPolicies(ctx, &iam.ListGroupPoliciesInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	assert.Contains(t, lgp.PolicyNames, "g-inline")

	managed, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("g-managed"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	arn := aws.ToString(managed.Policy.Arn)
	_, err = c.AttachGroupPolicy(ctx, &iam.AttachGroupPolicyInput{GroupName: aws.String(group), PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	lagp, err := c.ListAttachedGroupPolicies(ctx, &iam.ListAttachedGroupPoliciesInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	require.Len(t, lagp.AttachedPolicies, 1)
	_, err = c.DetachGroupPolicy(ctx, &iam.DetachGroupPolicyInput{GroupName: aws.String(group), PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	_, err = c.DeleteGroupPolicy(ctx, &iam.DeleteGroupPolicyInput{GroupName: aws.String(group), PolicyName: aws.String("g-inline")})
	require.NoError(t, err)
	_, err = c.RemoveUserFromGroup(ctx, &iam.RemoveUserFromGroupInput{GroupName: aws.String(group), UserName: aws.String(user)})
	require.NoError(t, err)
}

// TestIAM_PermissionBoundaryPutDelete covers Put/DeleteUserPermissionsBoundary.
func TestIAM_PermissionBoundaryPutDelete(t *testing.T) {
	c := iamClient()
	user := "pb-user"
	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	p, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("pb-policy"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:Describe*","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	_, err = c.PutUserPermissionsBoundary(ctx, &iam.PutUserPermissionsBoundaryInput{
		UserName: aws.String(user), PermissionsBoundary: p.Policy.Arn,
	})
	require.NoError(t, err)
	_, err = c.DeleteUserPermissionsBoundary(ctx, &iam.DeleteUserPermissionsBoundaryInput{UserName: aws.String(user)})
	require.NoError(t, err)
}

// TestSTS_GetSessionToken covers the permission-less GetSessionToken call.
// AssumeRoleWithWebIdentity, which now verifies the web identity token against
// a registered OpenID Connect provider, is covered in sts_webidentity_test.go.
func TestSTS_GetSessionToken(t *testing.T) {
	stsc := sts.NewFromConfig(sdkConfig(), func(o *sts.Options) { o.BaseEndpoint = aws.String(baseURL) })

	st, err := stsc.GetSessionToken(ctx, &sts.GetSessionTokenInput{})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(st.Credentials.AccessKeyId), "ASIA")
}
