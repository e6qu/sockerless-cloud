package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestIAM_RoleFidelitySDK proves the role attributes the provider reads back
// round-trip (description / max_session_duration / permissions_boundary were
// dropped, drifting aws_iam_role every plan), plus UpdateRole and TagRole/
// UntagRole (post-create attribute + tag changes were UnknownOperation).
func TestIAM_RoleFidelitySDK(t *testing.T) {
	c := iamClient()
	assumeDoc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}]}`

	boundary, err := c.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String("fidelity-boundary"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`),
	})
	require.NoError(t, err)
	boundaryArn := aws.ToString(boundary.Policy.Arn)

	_, err = c.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String("fidelity-role"),
		AssumeRolePolicyDocument: aws.String(assumeDoc),
		Description:              aws.String("controls the reconciler"),
		MaxSessionDuration:       aws.Int32(7200),
		PermissionsBoundary:      aws.String(boundaryArn),
		Tags:                     []iamtypes.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	require.NoError(t, err)

	got, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("fidelity-role")})
	require.NoError(t, err)
	role := got.Role
	assert.Equal(t, "controls the reconciler", aws.ToString(role.Description), "description must round-trip")
	assert.Equal(t, int32(7200), aws.ToInt32(role.MaxSessionDuration), "max_session_duration must round-trip")
	require.NotNil(t, role.PermissionsBoundary, "permissions_boundary must round-trip")
	assert.Equal(t, boundaryArn, aws.ToString(role.PermissionsBoundary.PermissionsBoundaryArn))
	require.Len(t, role.Tags, 1)
	assert.Equal(t, "platform", aws.ToString(role.Tags[0].Value))

	// UpdateRole changes description + max-session-duration in place.
	_, err = c.UpdateRole(ctx, &iam.UpdateRoleInput{
		RoleName: aws.String("fidelity-role"), Description: aws.String("updated desc"), MaxSessionDuration: aws.Int32(3600),
	})
	require.NoError(t, err)
	got2, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("fidelity-role")})
	require.NoError(t, err)
	assert.Equal(t, "updated desc", aws.ToString(got2.Role.Description), "UpdateRole description must persist")
	assert.Equal(t, int32(3600), aws.ToInt32(got2.Role.MaxSessionDuration), "UpdateRole max_session_duration must persist")

	// TagRole adds a tag (upsert), UntagRole removes one.
	_, err = c.TagRole(ctx, &iam.TagRoleInput{
		RoleName: aws.String("fidelity-role"),
		Tags:     []iamtypes.Tag{{Key: aws.String("env"), Value: aws.String("ci")}, {Key: aws.String("team"), Value: aws.String("infra")}},
	})
	require.NoError(t, err)
	got3, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("fidelity-role")})
	require.NoError(t, err)
	tags := map[string]string{}
	for _, tag := range got3.Role.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	assert.Equal(t, "ci", tags["env"], "TagRole adds new tag")
	assert.Equal(t, "infra", tags["team"], "TagRole overwrites existing tag value")

	_, err = c.UntagRole(ctx, &iam.UntagRoleInput{RoleName: aws.String("fidelity-role"), TagKeys: []string{"env"}})
	require.NoError(t, err)
	got4, err := c.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String("fidelity-role")})
	require.NoError(t, err)
	for _, tag := range got4.Role.Tags {
		assert.NotEqual(t, "env", aws.ToString(tag.Key), "UntagRole removed the key")
	}
}

// TestIAM_OIDCProviderTagsSDK proves the OIDC provider tags round-trip (they
// were stored but GetOpenIDConnectProvider dropped them) plus Tag/Untag.
func TestIAM_OIDCProviderTagsSDK(t *testing.T) {
	c := iamClient()

	created, err := c.CreateOpenIDConnectProvider(ctx, &iam.CreateOpenIDConnectProviderInput{
		Url:            aws.String("https://oidc.fidelity.example.test"),
		ClientIDList:   []string{"sts.amazonaws.com"},
		ThumbprintList: []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		Tags:           []iamtypes.Tag{{Key: aws.String("purpose"), Value: aws.String("github-actions")}},
	})
	require.NoError(t, err)
	arn := aws.ToString(created.OpenIDConnectProviderArn)

	got, err := c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, got.Tags, 1, "OIDC provider tags must round-trip")
	assert.Equal(t, "github-actions", aws.ToString(got.Tags[0].Value))

	_, err = c.TagOpenIDConnectProvider(ctx, &iam.TagOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn),
		Tags:                     []iamtypes.Tag{{Key: aws.String("env"), Value: aws.String("prod")}},
	})
	require.NoError(t, err)
	got2, err := c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, got2.Tags, 2, "TagOpenIDConnectProvider adds a tag")

	_, err = c.UntagOpenIDConnectProvider(ctx, &iam.UntagOpenIDConnectProviderInput{
		OpenIDConnectProviderArn: aws.String(arn), TagKeys: []string{"purpose"},
	})
	require.NoError(t, err)
	got3, err := c.GetOpenIDConnectProvider(ctx, &iam.GetOpenIDConnectProviderInput{OpenIDConnectProviderArn: aws.String(arn)})
	require.NoError(t, err)
	require.Len(t, got3.Tags, 1, "UntagOpenIDConnectProvider removed a tag")
	assert.Equal(t, "env", aws.ToString(got3.Tags[0].Key))
}
