package aws_sdk_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	smithy "github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKMS_KeyPolicyDenyBlocksDecrypt verifies that a CMK key policy with an
// explicit Deny statement for a principal prevents that principal from calling
// Decrypt, while an Allow on Encrypt still succeeds. This locks issue #732:
// the simulator must evaluate key-policy Deny at call time.
func TestKMS_KeyPolicyDenyBlocksDecrypt(t *testing.T) {
	adminIAM := iamClient()
	adminKMS := kmsClient()
	user := "kms-deny-user"

	_, err := adminIAM.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(user)})
	require.NoError(t, err)
	defer adminIAM.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(user)})

	key, err := adminKMS.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("policy-deny-key")})
	require.NoError(t, err)
	keyId := aws.ToString(key.KeyMetadata.KeyId)
	keyArn := aws.ToString(key.KeyMetadata.Arn)

	ak, err := adminIAM.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(user)})
	require.NoError(t, err)
	akid := aws.ToString(ak.AccessKey.AccessKeyId)
	secret := aws.ToString(ak.AccessKey.SecretAccessKey)

	userARN := fmt.Sprintf("arn:aws:iam::123456789012:user/%s", user)
	policy := fmt.Sprintf(`{
  "Version": "2012-10-17",
  "Statement": [
    {"Effect": "Allow", "Principal": {"AWS": %q}, "Action": ["kms:Encrypt","kms:Decrypt"], "Resource": %q},
    {"Effect": "Deny",  "Principal": {"AWS": %q}, "Action": "kms:Decrypt", "Resource": %q}
  ]
}`, userARN, keyArn, userARN, keyArn)
	_, err = adminKMS.PutKeyPolicy(ctx, &kms.PutKeyPolicyInput{
		KeyId:      aws.String(keyId),
		PolicyName: aws.String("default"),
		Policy:     aws.String(policy),
	})
	require.NoError(t, err)

	userCfg := aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider(akid, secret, ""),
	}
	userKMS := kms.NewFromConfig(userCfg, func(o *kms.Options) { o.BaseEndpoint = aws.String(baseURL) })

	plaintext := []byte("secret-data")
	enc, err := userKMS.Encrypt(ctx, &kms.EncryptInput{
		KeyId:     aws.String(keyId),
		Plaintext: plaintext,
	})
	require.NoError(t, err, "kms:Encrypt must be allowed by the key policy")

	_, err = userKMS.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: enc.CiphertextBlob})
	require.Error(t, err, "kms:Decrypt must be denied by the key policy Deny statement")

	var ae smithy.APIError
	require.True(t, errors.As(err, &ae), "error must be a smithy API error")
	assert.Equal(t, "AccessDeniedException", ae.ErrorCode())
}
