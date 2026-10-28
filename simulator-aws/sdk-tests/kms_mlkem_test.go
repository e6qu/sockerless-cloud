package aws_sdk_test

import (
	"crypto/mlkem"
	"encoding/asn1"
	"errors"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/require"
)

// TestKMS_MLKEMEncapsulateDecapsulate establishes a shared secret with an
// ML_KEM_768 key both ways: a peer encapsulates to the key's exported public
// key and KMS decapsulates, and KMS encapsulates and decapsulates its own
// ciphertext. The peer side runs Go's crypto/mlkem.
func TestKMS_MLKEMEncapsulateDecapsulate(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("ml-kem-768"),
		KeySpec:     kmstypes.KeySpecMlKem768,
		KeyUsage:    kmstypes.KeyUsageTypeEncapsulateDecapsulate,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId
	require.Equal(t, []kmstypes.EncapsulationAlgorithmSpec{kmstypes.EncapsulationAlgorithmSpecMlKem},
		created.KeyMetadata.EncapsulationAlgorithms)

	pub, err := c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.Equal(t, []kmstypes.EncapsulationAlgorithmSpec{kmstypes.EncapsulationAlgorithmSpecMlKem},
		pub.EncapsulationAlgorithms)
	var spki struct {
		Algorithm struct{ Algorithm asn1.ObjectIdentifier }
		PublicKey asn1.BitString
	}
	rest, err := asn1.Unmarshal(pub.PublicKey, &spki)
	require.NoError(t, err)
	require.Empty(t, rest)
	require.True(t, spki.Algorithm.Algorithm.Equal(asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 4, 2}),
		"the SubjectPublicKeyInfo names id-alg-ml-kem-768")
	ek, err := mlkem.NewEncapsulationKey768(spki.PublicKey.Bytes)
	require.NoError(t, err)

	peerSecret, ciphertext := ek.Encapsulate()
	decapsulated, err := c.Decapsulate(ctx, &kms.DecapsulateInput{
		KeyId:                      aws.String(keyId),
		SharedSecretCiphertextBlob: ciphertext,
		EncapsulationAlgorithm:     kmstypes.EncapsulationAlgorithmSpecMlKem,
	})
	require.NoError(t, err)
	require.Equal(t, peerSecret, decapsulated.SharedSecret)

	encapsulated, err := c.Encapsulate(ctx, &kms.EncapsulateInput{
		KeyId:                  aws.String(keyId),
		EncapsulationAlgorithm: kmstypes.EncapsulationAlgorithmSpecMlKem,
	})
	require.NoError(t, err)
	require.Len(t, encapsulated.SharedSecret, mlkem.SharedKeySize)
	require.Len(t, encapsulated.SharedSecretCiphertextBlob, mlkem.CiphertextSize768)
	again, err := c.Decapsulate(ctx, &kms.DecapsulateInput{
		KeyId:                      aws.String(keyId),
		SharedSecretCiphertextBlob: encapsulated.SharedSecretCiphertextBlob,
		EncapsulationAlgorithm:     kmstypes.EncapsulationAlgorithmSpecMlKem,
	})
	require.NoError(t, err)
	require.Equal(t, encapsulated.SharedSecret, again.SharedSecret)

	_, err = c.Encapsulate(ctx, &kms.EncapsulateInput{
		KeyId:                  aws.String(keyId),
		EncapsulationAlgorithm: kmstypes.EncapsulationAlgorithmSpecMlKem,
		DryRun:                 aws.Bool(true),
	})
	var dryRun *kmstypes.DryRunOperationException
	require.True(t, errors.As(err, &dryRun), "DryRun answers DryRunOperationException, got %v", err)

	_, err = c.Decapsulate(ctx, &kms.DecapsulateInput{
		KeyId:                      aws.String(keyId),
		SharedSecretCiphertextBlob: ciphertext[:len(ciphertext)-1],
		EncapsulationAlgorithm:     kmstypes.EncapsulationAlgorithmSpecMlKem,
	})
	var badCiphertext *kmstypes.InvalidCiphertextException
	require.True(t, errors.As(err, &badCiphertext), "a short ciphertext answers InvalidCiphertextException, got %v", err)
}

// TestKMS_MLKEMKeyUsage holds ML-KEM key specs and ENCAPSULATE_DECAPSULATE
// usage to each other, and Encapsulate to ML-KEM keys.
func TestKMS_MLKEMKeyUsage(t *testing.T) {
	t.Parallel()
	c := kmsClient()

	_, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		KeySpec:  kmstypes.KeySpecMlKem1024,
		KeyUsage: kmstypes.KeyUsageTypeEncryptDecrypt,
	})
	requireAWSErrorCode(t, err, "ValidationException")

	symmetric, err := c.CreateKey(ctx, &kms.CreateKeyInput{})
	require.NoError(t, err)
	_, err = c.Encapsulate(ctx, &kms.EncapsulateInput{
		KeyId:                  symmetric.KeyMetadata.KeyId,
		EncapsulationAlgorithm: kmstypes.EncapsulationAlgorithmSpecMlKem,
	})
	var usage *kmstypes.InvalidKeyUsageException
	require.True(t, errors.As(err, &usage), "Encapsulate on a symmetric key answers InvalidKeyUsageException, got %v", err)

	for _, spec := range []kmstypes.KeySpec{kmstypes.KeySpecMlKem512, kmstypes.KeySpecMlKem1024} {
		created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
			KeySpec:  spec,
			KeyUsage: kmstypes.KeyUsageTypeEncapsulateDecapsulate,
		})
		require.NoError(t, err)
		encapsulated, err := c.Encapsulate(ctx, &kms.EncapsulateInput{
			KeyId:                  created.KeyMetadata.KeyId,
			EncapsulationAlgorithm: kmstypes.EncapsulationAlgorithmSpecMlKem,
		})
		require.NoError(t, err, "%s", spec)
		decapsulated, err := c.Decapsulate(ctx, &kms.DecapsulateInput{
			KeyId:                      created.KeyMetadata.KeyId,
			SharedSecretCiphertextBlob: encapsulated.SharedSecretCiphertextBlob,
			EncapsulationAlgorithm:     kmstypes.EncapsulationAlgorithmSpecMlKem,
		})
		require.NoError(t, err, "%s", spec)
		require.Equal(t, encapsulated.SharedSecret, decapsulated.SharedSecret, "%s", spec)
	}
}
