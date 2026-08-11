package aws_sdk_test

import (
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKMS_GrantsLifecycle covers CreateGrant → ListGrants →
// RevokeGrant — the `aws_kms_grant` surface AWS services use to delegate CMK
// use for encryption at rest.
func TestKMS_GrantsLifecycle(t *testing.T) {
	c := kmsClient()
	key, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("grant-test")})
	require.NoError(t, err)
	keyID := aws.ToString(key.KeyMetadata.KeyId)

	grant, err := c.CreateGrant(ctx, &kms.CreateGrantInput{
		KeyId:            aws.String(keyID),
		GranteePrincipal: aws.String("arn:aws:iam::000000000000:role/test"),
		Operations:       []kmstypes.GrantOperation{kmstypes.GrantOperationDecrypt, kmstypes.GrantOperationGenerateDataKey},
		Name:             aws.String("my-grant"),
	})
	require.NoError(t, err)
	require.NotEmpty(t, aws.ToString(grant.GrantId))
	require.NotEmpty(t, aws.ToString(grant.GrantToken))

	list, err := c.ListGrants(ctx, &kms.ListGrantsInput{KeyId: aws.String(keyID)})
	require.NoError(t, err)
	require.Len(t, list.Grants, 1)
	g := list.Grants[0]
	assert.Equal(t, aws.ToString(grant.GrantId), aws.ToString(g.GrantId))
	assert.Equal(t, "arn:aws:iam::000000000000:role/test", aws.ToString(g.GranteePrincipal))
	assert.Equal(t, "my-grant", aws.ToString(g.Name))
	assert.ElementsMatch(t,
		[]kmstypes.GrantOperation{kmstypes.GrantOperationDecrypt, kmstypes.GrantOperationGenerateDataKey},
		g.Operations)

	_, err = c.RevokeGrant(ctx, &kms.RevokeGrantInput{KeyId: aws.String(keyID), GrantId: grant.GrantId})
	require.NoError(t, err)

	list2, err := c.ListGrants(ctx, &kms.ListGrantsInput{KeyId: aws.String(keyID)})
	require.NoError(t, err)
	require.Empty(t, list2.Grants, "grant must be gone after revoke")

	// Revoking a non-existent grant is an error, not a silent success.
	_, err = c.RevokeGrant(ctx, &kms.RevokeGrantInput{KeyId: aws.String(keyID), GrantId: aws.String("does-not-exist")})
	require.Error(t, err)
}

// TestKMS_GenerateDataKeyWithoutPlaintextAndReEncrypt covers the two secondary
// crypto ops: the encrypted-only data key, and re-wrapping ciphertext
// under a new key (rotation).
func TestKMS_GenerateDataKeyWithoutPlaintextAndReEncrypt(t *testing.T) {
	c := kmsClient()
	k1, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("src")})
	require.NoError(t, err)
	k1ID := aws.ToString(k1.KeyMetadata.KeyId)
	k2, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("dst")})
	require.NoError(t, err)
	k2ID := aws.ToString(k2.KeyMetadata.KeyId)

	dk, err := c.GenerateDataKeyWithoutPlaintext(ctx, &kms.GenerateDataKeyWithoutPlaintextInput{
		KeyId:   aws.String(k1ID),
		KeySpec: kmstypes.DataKeySpecAes256,
	})
	require.NoError(t, err)
	require.NotEmpty(t, dk.CiphertextBlob, "must return an encrypted data key")

	// Encrypt → ReEncrypt under k2 → Decrypt round-trips the same plaintext.
	enc, err := c.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(k1ID), Plaintext: []byte("rotate-me")})
	require.NoError(t, err)
	re, err := c.ReEncrypt(ctx, &kms.ReEncryptInput{
		CiphertextBlob:   enc.CiphertextBlob,
		DestinationKeyId: aws.String(k2ID),
	})
	require.NoError(t, err)
	require.NotEmpty(t, re.CiphertextBlob)
	assert.Contains(t, aws.ToString(re.KeyId), k2ID, "re-encrypted under destination key")

	dec, err := c.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: re.CiphertextBlob})
	require.NoError(t, err)
	assert.Equal(t, "rotate-me", string(dec.Plaintext))
}
