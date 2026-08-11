package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_RolePermissionsBoundary exercises PutRolePermissionsBoundary →
// GetRole (reads the boundary back) → UpdateRoleDescription →
// DeleteRolePermissionsBoundary. aws_iam_role.permissions_boundary drives the
// Put/Delete pair; the description update returns the modified Role.
func TestIAM_RolePermissionsBoundary(t *testing.T) {
	client := iamClient()
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	_, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("rpb-role"),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("rpb-role")}) })

	boundary := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`
	bound, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("rpb-boundary"),
		PolicyDocument: aws.String(boundary),
	})
	require.NoError(t, err)
	boundaryArn := aws.ToString(bound.Policy.Arn)
	t.Cleanup(func() { _, _ = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(boundaryArn)}) })

	_, err = client.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{
		RoleName:            aws.String("rpb-role"),
		PermissionsBoundary: aws.String(boundaryArn),
	})
	require.NoError(t, err)

	got, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("rpb-role")})
	require.NoError(t, err)
	require.NotNil(t, got.Role.PermissionsBoundary)
	assert.Equal(t, boundaryArn, aws.ToString(got.Role.PermissionsBoundary.PermissionsBoundaryArn))

	updated, err := client.UpdateRoleDescription(ctx, &iam.UpdateRoleDescriptionInput{
		RoleName:    aws.String("rpb-role"),
		Description: aws.String("boundary role"),
	})
	require.NoError(t, err)
	require.NotNil(t, updated.Role)
	assert.Equal(t, "boundary role", aws.ToString(updated.Role.Description))

	_, err = client.DeleteRolePermissionsBoundary(ctx, &iam.DeleteRolePermissionsBoundaryInput{
		RoleName: aws.String("rpb-role"),
	})
	require.NoError(t, err)

	cleared, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("rpb-role")})
	require.NoError(t, err)
	assert.Nil(t, cleared.Role.PermissionsBoundary, "boundary should be cleared after delete")
}

// TestIAM_PolicyVersions exercises the managed-policy versioning flow:
// CreatePolicy (v1) → CreatePolicyVersion (v2, SetAsDefault) →
// ListPolicyVersions → SetDefaultPolicyVersion (back to v1) →
// DeletePolicyVersion (v2). aws_iam_policy uses this when the document changes.
func TestIAM_PolicyVersions(t *testing.T) {
	client := iamClient()
	v1 := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`

	created, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("pv-policy"),
		PolicyDocument: aws.String(v1),
	})
	require.NoError(t, err)
	arn := aws.ToString(created.Policy.Arn)
	assert.Equal(t, "v1", aws.ToString(created.Policy.DefaultVersionId))
	t.Cleanup(func() { _, _ = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)}) })

	v2 := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"*"}]}`
	cv, err := client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{
		PolicyArn:      aws.String(arn),
		PolicyDocument: aws.String(v2),
		SetAsDefault:   true,
	})
	require.NoError(t, err)
	require.NotNil(t, cv.PolicyVersion)
	assert.Equal(t, "v2", aws.ToString(cv.PolicyVersion.VersionId))
	assert.True(t, cv.PolicyVersion.IsDefaultVersion)

	// GetPolicy now reports v2 as the default version.
	gp, err := client.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Equal(t, "v2", aws.ToString(gp.Policy.DefaultVersionId))

	// GetPolicyVersion on the v2 (now default) version returns the document.
	def, err := client.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{
		PolicyArn: aws.String(arn),
		VersionId: aws.String("v2"),
	})
	require.NoError(t, err)
	require.NotNil(t, def.PolicyVersion)
	assert.Equal(t, "v2", aws.ToString(def.PolicyVersion.VersionId))

	// Switch the default back to v1, then v2 is deletable.
	_, err = client.SetDefaultPolicyVersion(ctx, &iam.SetDefaultPolicyVersionInput{
		PolicyArn: aws.String(arn),
		VersionId: aws.String("v1"),
	})
	require.NoError(t, err)

	// Deleting the current default must fail.
	_, err = client.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{
		PolicyArn: aws.String(arn),
		VersionId: aws.String("v1"),
	})
	require.Error(t, err, "cannot delete the default version")

	_, err = client.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{
		PolicyArn: aws.String(arn),
		VersionId: aws.String("v2"),
	})
	require.NoError(t, err)
}

// TestIAM_InstanceProfileTags exercises TagInstanceProfile →
// ListInstanceProfileTags → UntagInstanceProfile. aws_iam_instance_profile.tags
// drives the tag/untag pair.
func TestIAM_InstanceProfileTags(t *testing.T) {
	client := iamClient()

	_, err := client.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String("ipt-profile"),
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String("ipt-profile")})
	})

	_, err = client.TagInstanceProfile(ctx, &iam.TagInstanceProfileInput{
		InstanceProfileName: aws.String("ipt-profile"),
		Tags: []types.Tag{
			{Key: aws.String("env"), Value: aws.String("prod")},
			{Key: aws.String("team"), Value: aws.String("infra")},
		},
	})
	require.NoError(t, err)

	listed, err := client.ListInstanceProfileTags(ctx, &iam.ListInstanceProfileTagsInput{
		InstanceProfileName: aws.String("ipt-profile"),
	})
	require.NoError(t, err)
	tags := map[string]string{}
	for _, tg := range listed.Tags {
		tags[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, "prod", tags["env"])
	assert.Equal(t, "infra", tags["team"])

	_, err = client.UntagInstanceProfile(ctx, &iam.UntagInstanceProfileInput{
		InstanceProfileName: aws.String("ipt-profile"),
		TagKeys:             []string{"env"},
	})
	require.NoError(t, err)

	after, err := client.ListInstanceProfileTags(ctx, &iam.ListInstanceProfileTagsInput{
		InstanceProfileName: aws.String("ipt-profile"),
	})
	require.NoError(t, err)
	remaining := map[string]string{}
	for _, tg := range after.Tags {
		remaining[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	_, hasEnv := remaining["env"]
	assert.False(t, hasEnv, "env tag should be removed")
	assert.Equal(t, "infra", remaining["team"], "team tag should remain")
}

// TestIAM_ContextKeys exercises GetContextKeysForCustomPolicy (parses the
// supplied JSON) and GetContextKeysForPrincipalPolicy (over a role's inline +
// attached policies). Both return the distinct Condition context keys.
func TestIAM_ContextKeys(t *testing.T) {
	client := iamClient()

	custom := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"StringEquals":{"aws:PrincipalTag/team":"infra","s3:prefix":"docs/"}}}]}`
	ck, err := client.GetContextKeysForCustomPolicy(ctx, &iam.GetContextKeysForCustomPolicyInput{
		PolicyInputList: []string{custom},
	})
	require.NoError(t, err)
	keys := map[string]bool{}
	for _, k := range ck.ContextKeyNames {
		keys[k] = true
	}
	assert.True(t, keys["aws:PrincipalTag/team"], "custom policy context key missing")
	assert.True(t, keys["s3:prefix"], "custom policy context key missing")

	// Principal path: build a role with an inline policy carrying a condition.
	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	role, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("ck-role"),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	require.NoError(t, err)
	roleArn := aws.ToString(role.Role.Arn)
	t.Cleanup(func() {
		_, _ = client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String("ck-role"), PolicyName: aws.String("ck-inline")})
		_, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("ck-role")})
	})

	inline := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"ec2:RunInstances","Resource":"*","Condition":{"StringEquals":{"aws:RequestTag/cost-center":"42"}}}]}`
	_, err = client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{
		RoleName:       aws.String("ck-role"),
		PolicyName:     aws.String("ck-inline"),
		PolicyDocument: aws.String(inline),
	})
	require.NoError(t, err)

	pck, err := client.GetContextKeysForPrincipalPolicy(ctx, &iam.GetContextKeysForPrincipalPolicyInput{
		PolicySourceArn: aws.String(roleArn),
	})
	require.NoError(t, err)
	pkeys := map[string]bool{}
	for _, k := range pck.ContextKeyNames {
		pkeys[k] = true
	}
	assert.True(t, pkeys["aws:RequestTag/cost-center"], "principal policy context key missing")
}

// TestIAM_EntitiesForPolicy attaches a managed policy to a user and a role and
// asserts ListEntitiesForPolicy reflects the real attachments, and that
// ListPoliciesGrantingServiceAccess derives the granted namespace from the
// attached policy's actions.
func TestIAM_EntitiesForPolicy(t *testing.T) {
	client := iamClient()

	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	pol, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("efp-policy"),
		PolicyDocument: aws.String(doc),
	})
	require.NoError(t, err)
	arn := aws.ToString(pol.Policy.Arn)
	t.Cleanup(func() { _, _ = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)}) })

	_, err = client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String("efp-user")})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = client.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{UserName: aws.String("efp-user"), PolicyArn: aws.String(arn)})
		_, _ = client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String("efp-user")})
	})

	trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
	role, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("efp-role"),
		AssumeRolePolicyDocument: aws.String(trust),
	})
	require.NoError(t, err)
	roleArn := aws.ToString(role.Role.Arn)
	t.Cleanup(func() {
		_, _ = client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String("efp-role"), PolicyArn: aws.String(arn)})
		_, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String("efp-role")})
	})

	_, err = client.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: aws.String("efp-user"), PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	_, err = client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String("efp-role"), PolicyArn: aws.String(arn)})
	require.NoError(t, err)

	ent, err := client.ListEntitiesForPolicy(ctx, &iam.ListEntitiesForPolicyInput{PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, ent.PolicyUsers, 1)
	assert.Equal(t, "efp-user", aws.ToString(ent.PolicyUsers[0].UserName))
	require.Len(t, ent.PolicyRoles, 1)
	assert.Equal(t, "efp-role", aws.ToString(ent.PolicyRoles[0].RoleName))

	// EntityFilter=Role narrows the result to roles only.
	roleOnly, err := client.ListEntitiesForPolicy(ctx, &iam.ListEntitiesForPolicyInput{
		PolicyArn:    aws.String(arn),
		EntityFilter: types.EntityTypeRole,
	})
	require.NoError(t, err)
	assert.Empty(t, roleOnly.PolicyUsers, "role filter should exclude users")
	assert.Len(t, roleOnly.PolicyRoles, 1)

	granting, err := client.ListPoliciesGrantingServiceAccess(ctx, &iam.ListPoliciesGrantingServiceAccessInput{
		Arn:               aws.String(roleArn),
		ServiceNamespaces: []string{"s3", "ec2"},
	})
	require.NoError(t, err)
	require.Len(t, granting.PoliciesGrantingServiceAccess, 2)
	byNs := map[string]int{}
	for _, e := range granting.PoliciesGrantingServiceAccess {
		byNs[aws.ToString(e.ServiceNamespace)] = len(e.Policies)
	}
	assert.Equal(t, 1, byNs["s3"], "the attached s3:GetObject policy grants the s3 namespace")
	assert.Equal(t, 0, byNs["ec2"], "no attached policy grants the ec2 namespace")
}

// TestIAM_PolicyTags exercises TagPolicy → ListPolicyTags → UntagPolicy on a
// managed policy. aws_iam_policy.tags drives the tag/untag pair.
func TestIAM_PolicyTags(t *testing.T) {
	client := iamClient()
	doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`
	pol, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("ptag-policy"),
		PolicyDocument: aws.String(doc),
	})
	require.NoError(t, err)
	arn := aws.ToString(pol.Policy.Arn)
	t.Cleanup(func() { _, _ = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)}) })

	_, err = client.TagPolicy(ctx, &iam.TagPolicyInput{
		PolicyArn: aws.String(arn),
		Tags:      []types.Tag{{Key: aws.String("owner"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)

	listed, err := client.ListPolicyTags(ctx, &iam.ListPolicyTagsInput{PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, listed.Tags, 1)
	assert.Equal(t, "owner", aws.ToString(listed.Tags[0].Key))
	assert.Equal(t, "platform", aws.ToString(listed.Tags[0].Value))

	_, err = client.UntagPolicy(ctx, &iam.UntagPolicyInput{
		PolicyArn: aws.String(arn),
		TagKeys:   []string{"owner"},
	})
	require.NoError(t, err)

	after, err := client.ListPolicyTags(ctx, &iam.ListPolicyTagsInput{PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	assert.Empty(t, after.Tags, "policy tags should be cleared")
}
