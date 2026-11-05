package aws_sdk_test

import (
	"encoding/json"
	"regexp"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var encodedAuthorizationMessage = regexp.MustCompile(`Encoded authorization failure message: (\S+)`)

// TestSTS_DecodeAuthorizationMessage refuses a user an Amazon EC2 call,
// decodes the message the refusal carried as the account administrator, and
// expects the user itself, lacking sts:DecodeAuthorizationMessage, to be
// refused the decode, and a message AWS never issued to be invalid.
func TestSTS_DecodeAuthorizationMessage(t *testing.T) {
	t.Parallel()
	admin := iamClient()
	user := uniquename.New("decode-user")
	_, err := admin.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = admin.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)}) })
	key, err := admin.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = admin.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(user), AccessKeyId: key.AccessKey.AccessKeyId})
	})
	cfg := sdkConfig()
	cfg.Credentials = credentials.NewStaticCredentialsProvider(aws.ToString(key.AccessKey.AccessKeyId), aws.ToString(key.AccessKey.SecretAccessKey), "")

	_, err = ec2.NewFromConfig(cfg, func(o *ec2.Options) { o.BaseEndpoint = aws.String(baseURL) }).
		CreateVolume(ctx, &ec2.CreateVolumeInput{AvailabilityZone: aws.String("us-east-1a"), Size: aws.Int32(1)})
	require.Error(t, err)
	assert.Equal(t, "UnauthorizedOperation", errCodeOf(err))
	match := encodedAuthorizationMessage.FindStringSubmatch(err.Error())
	require.Len(t, match, 2, "the refusal carries an encoded authorization message: %v", err)

	out, err := stsClient().DecodeAuthorizationMessage(ctx, &sts.DecodeAuthorizationMessageInput{EncodedMessage: aws.String(match[1])})
	require.NoError(t, err)
	var decoded struct {
		Allowed      string `json:"allowed"`
		ExplicitDeny string `json:"explicitDeny"`
		Context      struct {
			Principal struct {
				ID, Name, Arn string
			} `json:"principal"`
			Action     string `json:"action"`
			Conditions []struct {
				Item struct {
					Key    string   `json:"key"`
					Values []string `json:"values"`
				} `json:"item"`
			} `json:"conditions"`
		} `json:"context"`
	}
	require.NoError(t, json.Unmarshal([]byte(aws.ToString(out.DecodedMessage)), &decoded))
	assert.Equal(t, "false", decoded.Allowed)
	assert.Equal(t, "false", decoded.ExplicitDeny, "no policy denied the call explicitly")
	assert.Equal(t, "ec2:CreateVolume", decoded.Context.Action)
	assert.Equal(t, user, decoded.Context.Principal.Name)
	assert.Contains(t, decoded.Context.Principal.Arn, ":user/"+user)
	assert.NotEmpty(t, decoded.Context.Principal.ID)
	var keys []string
	for _, c := range decoded.Context.Conditions {
		keys = append(keys, c.Item.Key)
	}
	assert.Contains(t, keys, "aws:username", "the message carries the request's condition values")

	_, err = stsWith(cfg).DecodeAuthorizationMessage(ctx, &sts.DecodeAuthorizationMessageInput{EncodedMessage: aws.String(match[1])})
	require.Error(t, err)
	assert.Equal(t, "AccessDenied", errCodeOf(err), "decoding takes sts:DecodeAuthorizationMessage")

	_, err = stsClient().DecodeAuthorizationMessage(ctx, &sts.DecodeAuthorizationMessageInput{EncodedMessage: aws.String("bm90LWEtbWVzc2FnZQ")})
	require.Error(t, err)
	assert.Equal(t, "InvalidAuthorizationMessageException", errCodeOf(err))
}
