package aws_sdk_test

import (
	"crypto"
	"crypto/ed25519"
	"crypto/mlkem"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKMS_Ed25519SignVerify signs with an ECC_NIST_EDWARDS25519 key under
// both algorithms the key spec reference lists, and checks each signature
// offline with Go's crypto/ed25519 against the exported public key.
func TestKMS_Ed25519SignVerify(t *testing.T) {
	t.Parallel()
	c := kmsClient()
	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		KeySpec:  kmstypes.KeySpecEccNistEdwards25519,
		KeyUsage: kmstypes.KeyUsageTypeSignVerify,
	})
	require.NoError(t, err)
	keyID := created.KeyMetadata.KeyId
	assert.Equal(t, []kmstypes.SigningAlgorithmSpec{kmstypes.SigningAlgorithmSpecEd25519Sha512, kmstypes.SigningAlgorithmSpecEd25519PhSha512},
		created.KeyMetadata.SigningAlgorithms)

	pub, err := c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: keyID})
	require.NoError(t, err)
	parsed, err := x509.ParsePKIXPublicKey(pub.PublicKey)
	require.NoError(t, err)
	edPub, ok := parsed.(ed25519.PublicKey)
	require.True(t, ok, "the public key is Ed25519, got %T", parsed)

	message := []byte("sign me with ed25519")
	raw, err := c.Sign(ctx, &kms.SignInput{KeyId: keyID, Message: message,
		MessageType: kmstypes.MessageTypeRaw, SigningAlgorithm: kmstypes.SigningAlgorithmSpecEd25519Sha512})
	require.NoError(t, err)
	assert.True(t, ed25519.Verify(edPub, message, raw.Signature))

	digest := sha512.Sum512(message)
	prehashed, err := c.Sign(ctx, &kms.SignInput{KeyId: keyID, Message: digest[:],
		MessageType: kmstypes.MessageTypeDigest, SigningAlgorithm: kmstypes.SigningAlgorithmSpecEd25519PhSha512})
	require.NoError(t, err)
	again := sha512.Sum512(digest[:])
	require.NoError(t, ed25519.VerifyWithOptions(edPub, again[:], prehashed.Signature, &ed25519.Options{Hash: crypto.SHA512}),
		"KMS prehashes the digest once more before HashEdDSA")

	verified, err := c.Verify(ctx, &kms.VerifyInput{KeyId: keyID, Message: message, Signature: raw.Signature,
		MessageType: kmstypes.MessageTypeRaw, SigningAlgorithm: kmstypes.SigningAlgorithmSpecEd25519Sha512})
	require.NoError(t, err)
	assert.True(t, verified.SignatureValid)

	_, err = c.Sign(ctx, &kms.SignInput{KeyId: keyID, Message: message,
		MessageType: kmstypes.MessageTypeDigest, SigningAlgorithm: kmstypes.SigningAlgorithmSpecEd25519Sha512})
	requireAWSErrorCode(t, err, "ValidationException")
}

// TestKMS_MLDSASignVerify signs with an ML_DSA_65 key and verifies through
// KMS; a changed message fails verification.
func TestKMS_MLDSASignVerify(t *testing.T) {
	t.Parallel()
	c := kmsClient()
	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		KeySpec:  kmstypes.KeySpecMlDsa65,
		KeyUsage: kmstypes.KeyUsageTypeSignVerify,
	})
	require.NoError(t, err)
	keyID := created.KeyMetadata.KeyId

	pub, err := c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: keyID})
	require.NoError(t, err)
	assert.Equal(t, []kmstypes.SigningAlgorithmSpec{kmstypes.SigningAlgorithmSpecMlDsaShake256}, pub.SigningAlgorithms)
	assert.Empty(t, pub.CustomerMasterKeySpec)
	var spki struct {
		Algorithm struct{ Algorithm asn1.ObjectIdentifier }
		PublicKey asn1.BitString
	}
	_, err = asn1.Unmarshal(pub.PublicKey, &spki)
	require.NoError(t, err)
	assert.True(t, spki.Algorithm.Algorithm.Equal(asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 18}), "id-ml-dsa-65")
	assert.Len(t, spki.PublicKey.Bytes, 1952, "an ML-DSA-65 public key is 1952 bytes")

	message := []byte("post-quantum signed")
	signed, err := c.Sign(ctx, &kms.SignInput{KeyId: keyID, Message: message,
		MessageType: kmstypes.MessageTypeRaw, SigningAlgorithm: kmstypes.SigningAlgorithmSpecMlDsaShake256})
	require.NoError(t, err)
	assert.Len(t, signed.Signature, 3309, "an ML-DSA-65 signature is 3309 bytes")
	verified, err := c.Verify(ctx, &kms.VerifyInput{KeyId: keyID, Message: message, Signature: signed.Signature,
		MessageType: kmstypes.MessageTypeRaw, SigningAlgorithm: kmstypes.SigningAlgorithmSpecMlDsaShake256})
	require.NoError(t, err)
	assert.True(t, verified.SignatureValid)
	_, err = c.Verify(ctx, &kms.VerifyInput{KeyId: keyID, Message: []byte("tampered"), Signature: signed.Signature,
		MessageType: kmstypes.MessageTypeRaw, SigningAlgorithm: kmstypes.SigningAlgorithmSpecMlDsaShake256})
	requireAWSErrorCode(t, err, "KMSInvalidSignatureException")
}

// TestKMS_PostQuantumAndEdwardsDataKeyPairs generates Ed25519 and ML-KEM
// data key pairs: the Ed25519 private key is PKCS#8, and the ML-KEM one is
// PKCS#8 in seed format, from which Go's crypto/mlkem derives the returned
// public key.
func TestKMS_PostQuantumAndEdwardsDataKeyPairs(t *testing.T) {
	t.Parallel()
	c := kmsClient()
	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{})
	require.NoError(t, err)
	keyID := created.KeyMetadata.KeyId

	ed, err := c.GenerateDataKeyPair(ctx, &kms.GenerateDataKeyPairInput{KeyId: keyID, KeyPairSpec: kmstypes.DataKeyPairSpecEccNistEdwards25519})
	require.NoError(t, err)
	priv, err := x509.ParsePKCS8PrivateKey(ed.PrivateKeyPlaintext)
	require.NoError(t, err)
	edPriv, ok := priv.(ed25519.PrivateKey)
	require.True(t, ok)
	pub, err := x509.ParsePKIXPublicKey(ed.PublicKey)
	require.NoError(t, err)
	assert.Equal(t, edPriv.Public(), pub)

	kem, err := c.GenerateDataKeyPair(ctx, &kms.GenerateDataKeyPairInput{KeyId: keyID, KeyPairSpec: kmstypes.DataKeyPairSpecMlKem768})
	require.NoError(t, err)
	var info struct {
		Version    int
		Algorithm  struct{ Algorithm asn1.ObjectIdentifier }
		PrivateKey []byte
	}
	_, err = asn1.Unmarshal(kem.PrivateKeyPlaintext, &info)
	require.NoError(t, err)
	assert.True(t, info.Algorithm.Algorithm.Equal(asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 4, 2}))
	var seed asn1.RawValue
	_, err = asn1.Unmarshal(info.PrivateKey, &seed)
	require.NoError(t, err)
	require.Equal(t, 0, seed.Tag)
	require.Len(t, seed.Bytes, mlkem.SeedSize)
	dk, err := mlkem.NewDecapsulationKey768(seed.Bytes)
	require.NoError(t, err)
	var spki struct {
		Algorithm struct{ Algorithm asn1.ObjectIdentifier }
		PublicKey asn1.BitString
	}
	_, err = asn1.Unmarshal(kem.PublicKey, &spki)
	require.NoError(t, err)
	assert.Equal(t, dk.EncapsulationKey().Bytes(), spki.PublicKey.Bytes)
	plain, err := c.Decrypt(ctx, &kms.DecryptInput{CiphertextBlob: kem.PrivateKeyCiphertextBlob})
	require.NoError(t, err)
	assert.Equal(t, kem.PrivateKeyPlaintext, plain.Plaintext)
}
