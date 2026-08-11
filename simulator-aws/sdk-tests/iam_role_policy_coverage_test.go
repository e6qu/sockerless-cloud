package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Coverage backfill for IAM role-policy operations that had no SDK/CLI test —
// the managed-policy attach/detach, inline-policy put/delete/list, and
// assume-role-policy update paths the fck-nat role wiring exercises.

const iamTrustDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
const iamPermDoc = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:DescribeInstances","Resource":"*"}]}`

func TestIAM_RoleManagedPolicyAttachDetach(t *testing.T) {
	c := iamClient()
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String("cov-attach-role"), AssumeRolePolicyDocument: aws.String(iamTrustDoc)})
	require.NoError(t, err)
	pol, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{PolicyName: aws.String("cov-attach-pol"), PolicyDocument: aws.String(iamPermDoc)})
	require.NoError(t, err)
	arn := aws.ToString(pol.Policy.Arn)

	_, err = c.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String("cov-attach-role"), PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	list, err := c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String("cov-attach-role")})
	require.NoError(t, err)
	var attached bool
	for _, p := range list.AttachedPolicies {
		if aws.ToString(p.PolicyArn) == arn {
			attached = true
		}
	}
	assert.True(t, attached, "attached managed policy is listed")

	_, err = c.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String("cov-attach-role"), PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	list, err = c.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String("cov-attach-role")})
	require.NoError(t, err)
	for _, p := range list.AttachedPolicies {
		assert.NotEqual(t, arn, aws.ToString(p.PolicyArn), "policy detached")
	}
}

func TestIAM_RoleInlinePolicyLifecycle(t *testing.T) {
	c := iamClient()
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String("cov-inline-role"), AssumeRolePolicyDocument: aws.String(iamTrustDoc)})
	require.NoError(t, err)

	_, err = c.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String("cov-inline-role"), PolicyName: aws.String("inline-1"), PolicyDocument: aws.String(iamPermDoc)})
	require.NoError(t, err)
	list, err := c.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String("cov-inline-role")})
	require.NoError(t, err)
	assert.Contains(t, list.PolicyNames, "inline-1", "inline policy listed")

	_, err = c.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String("cov-inline-role"), PolicyName: aws.String("inline-1")})
	require.NoError(t, err)
	list, err = c.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String("cov-inline-role")})
	require.NoError(t, err)
	assert.NotContains(t, list.PolicyNames, "inline-1", "inline policy deleted")
}

func TestIAM_UpdateAssumeRolePolicy(t *testing.T) {
	c := iamClient()
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String("cov-trust-role"), AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`)})
	require.NoError(t, err)
	_, err = c.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{RoleName: aws.String("cov-trust-role"), PolicyDocument: aws.String(iamTrustDoc)})
	require.NoError(t, err)
	got, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("cov-trust-role")})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(got.Role.AssumeRolePolicyDocument), "ec2.amazonaws.com", "updated trust policy round-trips")
}

func TestIAM_ListInstanceProfiles(t *testing.T) {
	c := iamClient()
	_, err := c.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String("cov-profile")})
	require.NoError(t, err)
	list, err := c.ListInstanceProfiles(ctx, &iam.ListInstanceProfilesInput{})
	require.NoError(t, err)
	var found bool
	for _, p := range list.InstanceProfiles {
		if aws.ToString(p.InstanceProfileName) == "cov-profile" {
			found = true
		}
	}
	assert.True(t, found, "created instance profile is listed")
}
