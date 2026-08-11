package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iamClientWithCreds builds an IAM client signed with the given access key, so
// the sim resolves the request's caller (e.g. for ChangePassword) to the user
// that owns that key.
func iamClientWithCreds(akid, secret string) *iam.Client {
	cfg := sdkConfig()
	cfg.Credentials = credentials.NewStaticCredentialsProvider(akid, secret, "")
	return iam.NewFromConfig(cfg, func(o *iam.Options) {
		o.BaseEndpoint = aws.String(baseURL)
	})
}

// TestIAM_UpdateUser renames a user and changes its path, asserting the ARN
// re-keys to the new name/path.
func TestIAM_UpdateUser(t *testing.T) {
	c := iamClient()
	user := "uc-rename-user"
	newUser := "uc-renamed-user"

	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() {
		c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
		c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(newUser)})
	})

	_, err = c.UpdateUser(ctx, &iam.UpdateUserInput{
		UserName:    aws.String(user),
		NewUserName: aws.String(newUser),
		NewPath:     aws.String("/team/"),
	})
	require.NoError(t, err)

	got, err := c.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(newUser)})
	require.NoError(t, err)
	assert.Equal(t, newUser, aws.ToString(got.User.UserName))
	assert.Equal(t, "/team/", aws.ToString(got.User.Path))
	assert.Contains(t, aws.ToString(got.User.Arn), ":user/team/"+newUser)

	// The old name no longer resolves.
	_, err = c.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(user)})
	require.Error(t, err)
}

// TestIAM_UpdateGroup renames a group and changes its path.
func TestIAM_UpdateGroup(t *testing.T) {
	c := iamClient()
	group := "uc-rename-group"
	newGroup := "uc-renamed-group"

	_, err := c.CreateGroup(ctx, &iam.CreateGroupInput{GroupName: aws.String(group)})
	require.NoError(t, err)
	t.Cleanup(func() {
		c.DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(group)})
		c.DeleteGroup(ctx, &iam.DeleteGroupInput{GroupName: aws.String(newGroup)})
	})

	_, err = c.UpdateGroup(ctx, &iam.UpdateGroupInput{
		GroupName:    aws.String(group),
		NewGroupName: aws.String(newGroup),
		NewPath:      aws.String("/div/"),
	})
	require.NoError(t, err)

	got, err := c.GetGroup(ctx, &iam.GetGroupInput{GroupName: aws.String(newGroup)})
	require.NoError(t, err)
	assert.Equal(t, newGroup, aws.ToString(got.Group.GroupName))
	assert.Equal(t, "/div/", aws.ToString(got.Group.Path))
	assert.Contains(t, aws.ToString(got.Group.Arn), ":group/div/"+newGroup)
}

// TestIAM_LoginProfile exercises the console-password profile lifecycle plus
// ChangePassword for the calling user.
func TestIAM_LoginProfile(t *testing.T) {
	c := iamClient()
	user := "uc-login-user"

	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() {
		c.DeleteLoginProfile(ctx, &iam.DeleteLoginProfileInput{UserName: aws.String(user)})
		c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	})

	created, err := c.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{
		UserName:              aws.String(user),
		Password:              aws.String("InitPass-123!"),
		PasswordResetRequired: true,
	})
	require.NoError(t, err)
	assert.Equal(t, user, aws.ToString(created.LoginProfile.UserName))
	assert.True(t, created.LoginProfile.PasswordResetRequired)
	require.NotNil(t, created.LoginProfile.CreateDate)

	got, err := c.GetLoginProfile(ctx, &iam.GetLoginProfileInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.Equal(t, user, aws.ToString(got.LoginProfile.UserName))

	_, err = c.UpdateLoginProfile(ctx, &iam.UpdateLoginProfileInput{
		UserName:              aws.String(user),
		Password:              aws.String("NextPass-456!"),
		PasswordResetRequired: aws.Bool(false),
	})
	require.NoError(t, err)

	got, err = c.GetLoginProfile(ctx, &iam.GetLoginProfileInput{UserName: aws.String(user)})
	require.NoError(t, err)
	assert.False(t, got.LoginProfile.PasswordResetRequired)

	_, err = c.DeleteLoginProfile(ctx, &iam.DeleteLoginProfileInput{UserName: aws.String(user)})
	require.NoError(t, err)
	_, err = c.GetLoginProfile(ctx, &iam.GetLoginProfileInput{UserName: aws.String(user)})
	require.Error(t, err)
}

// TestIAM_AccessKeyLastUsed updates an access key's status and reads back its
// last-used info (the never-used "N/A" sentinel shape).
func TestIAM_AccessKeyLastUsed(t *testing.T) {
	c := iamClient()
	user := "uc-key-user"

	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	key, err := c.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	akid := aws.ToString(key.AccessKey.AccessKeyId)
	t.Cleanup(func() {
		c.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(user), AccessKeyId: aws.String(akid)})
	})

	_, err = c.UpdateAccessKey(ctx, &iam.UpdateAccessKeyInput{
		UserName:    aws.String(user),
		AccessKeyId: aws.String(akid),
		Status:      types.StatusTypeInactive,
	})
	require.NoError(t, err)

	keys, err := c.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(user)})
	require.NoError(t, err)
	require.Len(t, keys.AccessKeyMetadata, 1)
	assert.Equal(t, types.StatusTypeInactive, keys.AccessKeyMetadata[0].Status)

	lastUsed, err := c.GetAccessKeyLastUsed(ctx, &iam.GetAccessKeyLastUsedInput{AccessKeyId: aws.String(akid)})
	require.NoError(t, err)
	assert.Equal(t, user, aws.ToString(lastUsed.UserName))
	require.NotNil(t, lastUsed.AccessKeyLastUsed)
	// Never used: ServiceName and Region are the "N/A" sentinel, no LastUsedDate.
	assert.Equal(t, "N/A", aws.ToString(lastUsed.AccessKeyLastUsed.ServiceName))
	assert.Equal(t, "N/A", aws.ToString(lastUsed.AccessKeyLastUsed.Region))
}

// TestIAM_UserTags exercises TagUser / ListUserTags / UntagUser.
func TestIAM_UserTags(t *testing.T) {
	c := iamClient()
	user := "uc-tag-user"

	_, err := c.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })

	_, err = c.TagUser(ctx, &iam.TagUserInput{
		UserName: aws.String(user),
		Tags: []types.Tag{
			{Key: aws.String("team"), Value: aws.String("platform")},
			{Key: aws.String("env"), Value: aws.String("prod")},
		},
	})
	require.NoError(t, err)

	tags, err := c.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(user)})
	require.NoError(t, err)
	got := map[string]string{}
	for _, tg := range tags.Tags {
		got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Equal(t, "platform", got["team"])
	assert.Equal(t, "prod", got["env"])

	_, err = c.UntagUser(ctx, &iam.UntagUserInput{UserName: aws.String(user), TagKeys: []string{"env"}})
	require.NoError(t, err)

	tags, err = c.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(user)})
	require.NoError(t, err)
	got = map[string]string{}
	for _, tg := range tags.Tags {
		got[aws.ToString(tg.Key)] = aws.ToString(tg.Value)
	}
	assert.Contains(t, got, "team")
	assert.NotContains(t, got, "env")
}

// TestIAM_AccountPasswordPolicy exercises the account password-policy singleton
// plus its NoSuchEntity-when-unset semantics.
func TestIAM_AccountPasswordPolicy(t *testing.T) {
	c := iamClient()
	t.Cleanup(func() { c.DeleteAccountPasswordPolicy(ctx, &iam.DeleteAccountPasswordPolicyInput{}) })

	_, err := c.UpdateAccountPasswordPolicy(ctx, &iam.UpdateAccountPasswordPolicyInput{
		MinimumPasswordLength:      aws.Int32(12),
		RequireSymbols:             true,
		RequireNumbers:             true,
		RequireUppercaseCharacters: true,
		RequireLowercaseCharacters: true,
		MaxPasswordAge:             aws.Int32(90),
		PasswordReusePrevention:    aws.Int32(3),
	})
	require.NoError(t, err)

	got, err := c.GetAccountPasswordPolicy(ctx, &iam.GetAccountPasswordPolicyInput{})
	require.NoError(t, err)
	require.NotNil(t, got.PasswordPolicy)
	assert.Equal(t, int32(12), aws.ToInt32(got.PasswordPolicy.MinimumPasswordLength))
	assert.True(t, got.PasswordPolicy.RequireSymbols)
	assert.True(t, got.PasswordPolicy.RequireNumbers)
	assert.Equal(t, int32(90), aws.ToInt32(got.PasswordPolicy.MaxPasswordAge))
	assert.True(t, got.PasswordPolicy.ExpirePasswords)

	_, err = c.DeleteAccountPasswordPolicy(ctx, &iam.DeleteAccountPasswordPolicyInput{})
	require.NoError(t, err)

	_, err = c.GetAccountPasswordPolicy(ctx, &iam.GetAccountPasswordPolicyInput{})
	require.Error(t, err)
}

// TestIAM_ChangePassword validates a password change against the account policy,
// signing the request with the user's own access key so the caller resolves to
// that user.
func TestIAM_ChangePassword(t *testing.T) {
	admin := iamClient()
	user := "uc-changepw-user"

	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() {
		admin.DeleteLoginProfile(ctx, &iam.DeleteLoginProfileInput{UserName: aws.String(user)})
		admin.DeleteAccountPasswordPolicy(ctx, &iam.DeleteAccountPasswordPolicyInput{})
		admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})
	})

	_, err = admin.UpdateAccountPasswordPolicy(ctx, &iam.UpdateAccountPasswordPolicyInput{
		MinimumPasswordLength: aws.Int32(10),
	})
	require.NoError(t, err)

	_, err = admin.CreateLoginProfile(ctx, &iam.CreateLoginProfileInput{
		UserName: aws.String(user),
		Password: aws.String("OldPass-123!"),
	})
	require.NoError(t, err)

	// Grant the user iam:ChangePassword, the identity-based permission AWS
	// requires for a self-service password change (the sim's call-time IAM
	// enforcement evaluates it).
	_, err = admin.PutUserPolicy(ctx, &iam.PutUserPolicyInput{
		UserName:       aws.String(user),
		PolicyName:     aws.String("allow-changepassword"),
		PolicyDocument: aws.String(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:ChangePassword","Resource":"*"}]}`),
	})
	require.NoError(t, err)

	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	akid := aws.ToString(key.AccessKey.AccessKeyId)
	secret := aws.ToString(key.AccessKey.SecretAccessKey)
	t.Cleanup(func() {
		admin.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(user), AccessKeyId: aws.String(akid)})
		admin.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(user), PolicyName: aws.String("allow-changepassword")})
	})

	// A client signed as the user; ChangePassword acts on the caller.
	userClient := iamClientWithCreds(akid, secret)

	// Too short → policy violation.
	_, err = userClient.ChangePassword(ctx, &iam.ChangePasswordInput{
		OldPassword: aws.String("OldPass-123!"),
		NewPassword: aws.String("short"),
	})
	require.Error(t, err)

	// Wrong old password → error.
	_, err = userClient.ChangePassword(ctx, &iam.ChangePasswordInput{
		OldPassword: aws.String("WrongOld-1!"),
		NewPassword: aws.String("ValidNewPass-123!"),
	})
	require.Error(t, err)

	// Valid change.
	_, err = userClient.ChangePassword(ctx, &iam.ChangePasswordInput{
		OldPassword: aws.String("OldPass-123!"),
		NewPassword: aws.String("ValidNewPass-123!"),
	})
	require.NoError(t, err)
}
