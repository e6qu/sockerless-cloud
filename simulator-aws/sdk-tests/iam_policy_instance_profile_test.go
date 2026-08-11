package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_CreatePolicy_RoundTrip exercises the canonical managed-
// policy lifecycle: CreatePolicy → GetPolicy → GetPolicyVersion →
// DeletePolicy. terraform-provider-aws's aws_iam_policy resource
// follows this path on every plan/apply; pre-fix the sim returned
// InvalidAction.
func TestIAM_CreatePolicy_RoundTrip(t *testing.T) {
	client := iamClient()
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`

	created, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("test-policy"),
		PolicyDocument: aws.String(doc),
		Description:    aws.String("iam policy round-trip test"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.Policy)
	require.NotNil(t, created.Policy.Arn)
	policyArn := *created.Policy.Arn
	assert.Contains(t, policyArn, "test-policy")
	assert.Equal(t, "v1", aws.ToString(created.Policy.DefaultVersionId))

	got, err := client.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(policyArn)})
	require.NoError(t, err)
	assert.Equal(t, "test-policy", aws.ToString(got.Policy.PolicyName))

	ver, err := client.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
		PolicyArn: aws.String(policyArn),
		VersionId: aws.String("v1"),
	})
	require.NoError(t, err)
	require.NotNil(t, ver.PolicyVersion)
	assert.Equal(t, "v1", aws.ToString(ver.PolicyVersion.VersionId))

	_, err = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(policyArn)})
	require.NoError(t, err)
}

// TestIAM_InstanceProfile_RoundTrip exercises the canonical instance-
// profile lifecycle: CreateInstanceProfile → AddRoleToInstanceProfile
// → GetInstanceProfile → RemoveRoleFromInstanceProfile →
// DeleteInstanceProfile. Required by aws_iam_instance_profile +
// referenced by EC2 / ECS launch templates.
func TestIAM_InstanceProfile_RoundTrip(t *testing.T) {
	client := iamClient()

	rolePolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	_, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("ip-role"),
		AssumeRolePolicyDocument: aws.String(rolePolicy),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("ip-role")}) })

	created, err := client.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String("ip-test"),
	})
	require.NoError(t, err)
	require.NotNil(t, created.InstanceProfile)
	assert.Contains(t, aws.ToString(created.InstanceProfile.Arn), "ip-test")

	_, err = client.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
		InstanceProfileName: aws.String("ip-test"),
		RoleName:            aws.String("ip-role"),
	})
	require.NoError(t, err)

	got, err := client.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String("ip-test"),
	})
	require.NoError(t, err)
	require.Len(t, got.InstanceProfile.Roles, 1, "instance profile should hold the just-added role")
	assert.Equal(t, "ip-role", aws.ToString(got.InstanceProfile.Roles[0].RoleName))

	listed, err := client.ListInstanceProfilesForRole(ctx, &iam.ListInstanceProfilesForRoleInput{
		RoleName: aws.String("ip-role"),
	})
	require.NoError(t, err)
	require.Len(t, listed.InstanceProfiles, 1, "role lookup should find the instance profile holding it")

	_, err = client.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
		InstanceProfileName: aws.String("ip-test"),
		RoleName:            aws.String("ip-role"),
	})
	require.NoError(t, err)

	_, err = client.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{
		InstanceProfileName: aws.String("ip-test"),
	})
	require.NoError(t, err)
}

// TestIAM_ListPolicies asserts the sim's ListPolicies returns the
// managed policies created via CreatePolicy.
func TestIAM_ListPolicies(t *testing.T) {
	client := iamClient()
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`

	_, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("list-test-policy"),
		PolicyDocument: aws.String(doc),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		// Sim emits arn:aws:iam::123456789012:policy/list-test-policy.
		_, _ = client.DeletePolicy(ctx, &iam.DeletePolicyInput{
			PolicyArn: aws.String("arn:aws:iam::123456789012:policy/list-test-policy"),
		})
	})

	list, err := client.ListPolicies(ctx, &iam.ListPoliciesInput{})
	require.NoError(t, err)
	found := false
	for _, p := range list.Policies {
		if aws.ToString(p.PolicyName) == "list-test-policy" {
			found = true
			break
		}
	}
	assert.True(t, found, "ListPolicies must include the just-created policy")
}
