package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_ListRolesAndTags covers ListRoles + ListRoleTags: a role
// created with tags is enumerable and its tags read back.
func TestIAM_ListRolesAndTags(t *testing.T) {
	c := iamClient()
	_, err := c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("eddsim-list-role"),
		AssumeRolePolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[]}`),
		Tags:                     []iamtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)

	roles, err := c.ListRoles(ctx, &iam.ListRolesInput{})
	require.NoError(t, err)
	var found *iamtypes.Role
	for i := range roles.Roles {
		if aws.ToString(roles.Roles[i].RoleName) == "eddsim-list-role" {
			found = &roles.Roles[i]
		}
	}
	require.NotNil(t, found, "created role must appear in ListRoles")

	tags, err := c.ListRoleTags(ctx, &iam.ListRoleTagsInput{RoleName: aws.String("eddsim-list-role")})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "team", aws.ToString(tags.Tags[0].Key))
	assert.Equal(t, "platform", aws.ToString(tags.Tags[0].Value))
}

// TestIAM_ListPolicyVersionsAndTags covers ListPolicyVersions and
// ListPolicyTags.
func TestIAM_ListPolicyVersionsAndTags(t *testing.T) {
	c := iamClient()
	created, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("list-versions-policy"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`),
		Tags:           []iamtypes.Tag{{Key: aws.String("env"), Value: aws.String("ci")}},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.Policy.Arn)

	versions, err := c.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, versions.Versions, 1)
	assert.Equal(t, "v1", aws.ToString(versions.Versions[0].VersionId))
	assert.True(t, versions.Versions[0].IsDefaultVersion)

	tags, err := c.ListPolicyTags(ctx, &iam.ListPolicyTagsInput{PolicyArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, tags.Tags, 1)
	assert.Equal(t, "env", aws.ToString(tags.Tags[0].Key))

	// Unknown policy ARN is a NoSuchEntity, not a 500.
	_, err = c.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String("arn:aws:iam::000000000000:policy/does-not-exist")})
	require.Error(t, err)
}
