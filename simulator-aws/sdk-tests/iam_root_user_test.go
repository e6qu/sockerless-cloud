package aws_sdk_test

import (
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	ststypes "github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func rootUserConfig() aws.Config {
	cfg := sdkConfig()
	cfg.Credentials = credentials.NewStaticCredentialsProvider("root", "root", "")
	return cfg
}

// TestIAM_RootUserSignsAsTheAccount signs with the management account's root
// user key: the caller is the account itself, its GetSessionToken sessions
// last at most an hour, AssumeRoot refuses it, and the access key operations
// that name no user act on the root user's own keys.
func TestIAM_RootUserSignsAsTheAccount(t *testing.T) {
	t.Parallel()
	admin, err := stsClient().GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	account := aws.ToString(admin.Account)

	rootSTS := stsWith(rootUserConfig())
	who, err := rootSTS.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	require.NoError(t, err)
	assert.Equal(t, "arn:aws:iam::"+account+":root", aws.ToString(who.Arn))
	assert.Equal(t, account, aws.ToString(who.UserId), "the root user's ID is the account ID")
	assert.Equal(t, account, aws.ToString(who.Account))

	requested := time.Now()
	session, err := rootSTS.GetSessionToken(ctx, &sts.GetSessionTokenInput{DurationSeconds: aws.Int32(7200)})
	require.NoError(t, err)
	assert.False(t, session.Credentials.Expiration.After(requested.Add(time.Hour+time.Minute)),
		"a root user's session asked for two hours lasts one")

	_, err = rootSTS.GetSessionToken(ctx, &sts.GetSessionTokenInput{DurationSeconds: aws.Int32(600)})
	require.Error(t, err)
	assert.Equal(t, "ValidationError", errCodeOf(err), "a root session is at least 900 seconds")

	_, err = rootSTS.AssumeRoot(ctx, &sts.AssumeRootInput{
		TargetPrincipal: aws.String(account),
		TaskPolicyArn:   &ststypes.PolicyDescriptorType{Arn: aws.String("arn:aws:iam::aws:policy/root-task/IAMAuditRootUserCredentials")},
	})
	require.Error(t, err)
	assert.Equal(t, "AccessDenied", errCodeOf(err), "a root user cannot call AssumeRoot")

	keys, err := iamWith(rootUserConfig()).ListAccessKeys(ctx, &iam.ListAccessKeysInput{})
	require.NoError(t, err)
	var ids []string
	for _, k := range keys.AccessKeyMetadata {
		ids = append(ids, aws.ToString(k.AccessKeyId))
		assert.Nil(t, k.UserName, "a root user's key names no user")
	}
	assert.Contains(t, ids, "root")
}
