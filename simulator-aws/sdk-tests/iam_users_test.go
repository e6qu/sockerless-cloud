package aws_sdk_test

import (
	"net/url"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_UserLifecycle exercises the IAM user surface added for call-time
// enforcement: CreateUser/GetUser/DeleteUser, CreateAccessKey/ListAccessKeys/
// DeleteAccessKey, PutUserPolicy/GetUserPolicy/ListUserPolicies/DeleteUserPolicy,
// and AttachUserPolicy/ListAttachedUserPolicies/DetachUserPolicy.
func TestIAM_UserLifecycle(t *testing.T) {
	c := iamClient()
	user := "lifecycle-user"

	created, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(created.User.Arn), ":user/")
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	got, err := c.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.Equal(t, user, aws.ToString(got.User.UserName))

	key, err := c.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	akid := aws.ToString(key.AccessKey.AccessKeyId)
	assert.Contains(t, akid, "AKIA")

	keys, err := c.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, keys.AccessKeyMetadata, 1)
	assert.Equal(t, akid, aws.ToString(keys.AccessKeyMetadata[0].AccessKeyId))

	_, err = c.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("inline1"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
	})
	require.NoError(t, err)

	gp, err := c.GetUserPolicy(ctx, &iam.GetUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("inline1")})
	require.NoError(t, err)
	// IAM returns PolicyDocument URL-encoded (RFC 3986), like GetRolePolicy.
	doc, _ := url.QueryUnescape(aws.ToString(gp.PolicyDocument))
	assert.Contains(t, doc, "s3:GetObject")

	lp, err := c.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.Contains(t, lp.PolicyNames, "inline1")

	managed, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("managed-lifecycle"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	arn := aws.ToString(managed.Policy.Arn)

	_, err = c.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: aws.String(user), PolicyArn: aws.String(arn)})
	require.NoError(t, err)

	la, err := c.ListAttachedUserPolicies(ctx, &iam.ListAttachedUserPoliciesInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, la.AttachedPolicies, 1)
	assert.Equal(t, arn, aws.ToString(la.AttachedPolicies[0].PolicyArn))

	_, err = c.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{UserName: aws.String(user), PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	_, err = c.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("inline1")})
	require.NoError(t, err)
	_, err = c.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(user), AccessKeyId: aws.String(akid)})
	require.NoError(t, err)
}
