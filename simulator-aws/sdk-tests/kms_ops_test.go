package aws_sdk_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKMS_EnableDisableKey pins the key-state lifecycle terraform drives for
// `aws_kms_key { is_enabled = ... }`: DisableKey moves the key to Disabled,
// EnableKey returns it to Enabled — both reflected in DescribeKey.
func TestKMS_EnableDisableKey(t *testing.T) {
	c := kmsClient()

	out, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("enable-disable")})
	require.NoError(t, err)
	keyId := *out.KeyMetadata.KeyId

	_, err = c.DisableKey(ctx, &kms.DisableKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	desc, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "Disabled", string(desc.KeyMetadata.KeyState))
	assert.False(t, desc.KeyMetadata.Enabled)

	_, err = c.EnableKey(ctx, &kms.EnableKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	desc, err = c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "Enabled", string(desc.KeyMetadata.KeyState))
	assert.True(t, desc.KeyMetadata.Enabled)
}

// TestKMS_CancelKeyDeletion covers ScheduleKeyDeletion → CancelKeyDeletion:
// cancellation returns the key to Disabled and reports the KeyId.
func TestKMS_CancelKeyDeletion(t *testing.T) {
	c := kmsClient()

	out, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("cancel-deletion")})
	require.NoError(t, err)
	keyId := *out.KeyMetadata.KeyId

	_, err = c.ScheduleKeyDeletion(ctx, &kms.ScheduleKeyDeletionInput{
		KeyId:               aws.String(keyId),
		PendingWindowInDays: aws.Int32(7),
	})
	require.NoError(t, err)

	cancel, err := c.CancelKeyDeletion(ctx, &kms.CancelKeyDeletionInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, keyId, aws.ToString(cancel.KeyId))

	desc, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "Disabled", string(desc.KeyMetadata.KeyState),
		"CancelKeyDeletion must return the key to Disabled like real KMS")

	// Cancelling a key that isn't pending deletion is rejected.
	_, err = c.CancelKeyDeletion(ctx, &kms.CancelKeyDeletionInput{KeyId: aws.String(keyId)})
	require.Error(t, err, "CancelKeyDeletion on a non-pending key must fail")
}

// TestKMS_UpdateKeyDescription verifies the description round-trips through
// DescribeKey after UpdateKeyDescription.
func TestKMS_UpdateKeyDescription(t *testing.T) {
	c := kmsClient()

	out, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("original")})
	require.NoError(t, err)
	keyId := *out.KeyMetadata.KeyId

	_, err = c.UpdateKeyDescription(ctx, &kms.UpdateKeyDescriptionInput{
		KeyId:       aws.String(keyId),
		Description: aws.String("updated-description"),
	})
	require.NoError(t, err)

	desc, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "updated-description", aws.ToString(desc.KeyMetadata.Description))
}

// TestKMS_UpdateAlias re-points an existing alias at a new target key.
func TestKMS_UpdateAlias(t *testing.T) {
	c := kmsClient()

	keyA, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("alias-target-a")})
	require.NoError(t, err)
	keyB, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("alias-target-b")})
	require.NoError(t, err)
	idA, idB := *keyA.KeyMetadata.KeyId, *keyB.KeyMetadata.KeyId

	aliasName := "alias/update-alias-test"
	_, err = c.CreateAlias(ctx, &kms.CreateAliasInput{AliasName: aws.String(aliasName), TargetKeyId: aws.String(idA)})
	require.NoError(t, err)
	t.Cleanup(func() { c.DeleteAlias(ctx, &kms.DeleteAliasInput{AliasName: aws.String(aliasName)}) })

	_, err = c.UpdateAlias(ctx, &kms.UpdateAliasInput{AliasName: aws.String(aliasName), TargetKeyId: aws.String(idB)})
	require.NoError(t, err)

	enc, err := c.Encrypt(ctx, &kms.EncryptInput{KeyId: aws.String(aliasName), Plaintext: []byte("x")})
	require.NoError(t, err)
	assert.Contains(t, aws.ToString(enc.KeyId), idB, "UpdateAlias must re-point the alias to the new key")

	// Updating a non-existent alias must fail.
	_, err = c.UpdateAlias(ctx, &kms.UpdateAliasInput{AliasName: aws.String("alias/does-not-exist"), TargetKeyId: aws.String(idA)})
	require.Error(t, err)
}

// TestKMS_GenerateRandom asserts GenerateRandom returns exactly the requested
// number of cryptographically random bytes and rejects out-of-range sizes.
func TestKMS_GenerateRandom(t *testing.T) {
	c := kmsClient()

	out, err := c.GenerateRandom(ctx, &kms.GenerateRandomInput{NumberOfBytes: aws.Int32(64)})
	require.NoError(t, err)
	assert.Len(t, out.Plaintext, 64)

	out2, err := c.GenerateRandom(ctx, &kms.GenerateRandomInput{NumberOfBytes: aws.Int32(64)})
	require.NoError(t, err)
	assert.NotEqual(t, out.Plaintext, out2.Plaintext, "GenerateRandom must return fresh randomness")

	_, err = c.GenerateRandom(ctx, &kms.GenerateRandomInput{NumberOfBytes: aws.Int32(2048)})
	require.Error(t, err, "NumberOfBytes above 1024 must be rejected")
}

// TestKMS_ListKeyPolicies pins the single "default" policy name real KMS
// exposes per key.
func TestKMS_ListKeyPolicies(t *testing.T) {
	c := kmsClient()

	out, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("list-policies")})
	require.NoError(t, err)
	keyId := *out.KeyMetadata.KeyId

	pol, err := c.ListKeyPolicies(ctx, &kms.ListKeyPoliciesInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, []string{"default"}, pol.PolicyNames)
	assert.False(t, pol.Truncated)
}

// TestKMS_RotateKeyOnDemandAndList covers RotateKeyOnDemand recording an
// ON_DEMAND rotation that ListKeyRotations then reports.
func TestKMS_RotateKeyOnDemandAndList(t *testing.T) {
	c := kmsClient()

	out, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("rotate-on-demand")})
	require.NoError(t, err)
	keyId := *out.KeyMetadata.KeyId

	// Fresh key: no rotation history.
	list, err := c.ListKeyRotations(ctx, &kms.ListKeyRotationsInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Empty(t, list.Rotations)

	rot, err := c.RotateKeyOnDemand(ctx, &kms.RotateKeyOnDemandInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, keyId, aws.ToString(rot.KeyId))

	list, err = c.ListKeyRotations(ctx, &kms.ListKeyRotationsInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.Len(t, list.Rotations, 1)
	assert.Equal(t, kmstypes.RotationTypeOnDemand, list.Rotations[0].RotationType)
	assert.Equal(t, keyId, aws.ToString(list.Rotations[0].KeyId))
}

// TestKMS_ImportKeyMaterialCycle exercises the EXTERNAL-origin import flow:
// CreateKey(Origin=EXTERNAL) starts PendingImport, GetParametersForImport
// hands back a real RSA wrapping key + token, ImportKeyMaterial enables the
// key, DeleteImportedKeyMaterial returns it to PendingImport.
func TestKMS_ImportKeyMaterialCycle(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("external-key"),
		Origin:      kmstypes.OriginTypeExternal,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId
	assert.Equal(t, "PendingImport", string(created.KeyMetadata.KeyState),
		"EXTERNAL key must start PendingImport")

	params, err := c.GetParametersForImport(ctx, &kms.GetParametersForImportInput{
		KeyId:             aws.String(keyId),
		WrappingAlgorithm: kmstypes.AlgorithmSpecRsaesOaepSha256,
		WrappingKeySpec:   kmstypes.WrappingKeySpecRsa2048,
	})
	require.NoError(t, err)
	require.NotEmpty(t, params.ImportToken, "ImportToken must be present")
	require.NotEmpty(t, params.PublicKey, "a real wrapping public key must be returned")

	pubAny, err := x509.ParsePKIXPublicKey(params.PublicKey)
	require.NoError(t, err, "PublicKey must be a valid DER-encoded SubjectPublicKeyInfo")
	pubRSA, ok := pubAny.(*rsa.PublicKey)
	require.True(t, ok, "PublicKey must be an RSA public key")

	material := make([]byte, 32)
	_, err = rand.Read(material)
	require.NoError(t, err, "must generate key material to import")
	encryptedMaterial, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, pubRSA, material, nil)
	require.NoError(t, err, "must encrypt key material with RSA-OAEP-SHA256")

	_, err = c.ImportKeyMaterial(ctx, &kms.ImportKeyMaterialInput{
		KeyId:                aws.String(keyId),
		ImportToken:          params.ImportToken,
		EncryptedKeyMaterial: encryptedMaterial,
		ExpirationModel:      kmstypes.ExpirationModelTypeKeyMaterialDoesNotExpire,
	})
	require.NoError(t, err)

	desc, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "Enabled", string(desc.KeyMetadata.KeyState),
		"importing material must enable the key")

	_, err = c.DeleteImportedKeyMaterial(ctx, &kms.DeleteImportedKeyMaterialInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	desc, err = c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Equal(t, "PendingImport", string(desc.KeyMetadata.KeyState),
		"deleting material must return the key to PendingImport")

	// A mismatched/absent import token is rejected.
	_, err = c.ImportKeyMaterial(ctx, &kms.ImportKeyMaterialInput{
		KeyId:                aws.String(keyId),
		ImportToken:          []byte("bogus-token"),
		EncryptedKeyMaterial: []byte("x"),
	})
	require.Error(t, err, "a non-matching import token must be rejected")
}
