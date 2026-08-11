package aws_sdk_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	kmstypes "github.com/aws/aws-sdk-go-v2/service/kms/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKMS_AsymmetricSignVerify exercises the ASYMMETRIC_SIGN flow end-to-end
// with REAL cryptography: CreateKey(RSA_2048, SIGN_VERIFY) → GetPublicKey →
// Sign → Verify succeeds, and a tampered message fails Verify. The sim
// generates a real RSA key, so the signature genuinely verifies.
func TestKMS_AsymmetricSignVerify(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("rsa-sign-key"),
		KeySpec:     kmstypes.KeySpecRsa2048,
		KeyUsage:    kmstypes.KeyUsageTypeSignVerify,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	pub, err := c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.NotEmpty(t, pub.PublicKey, "GetPublicKey must return a DER-encoded SPKI public key")
	require.Contains(t, pub.SigningAlgorithms, kmstypes.SigningAlgorithmSpecRsassaPssSha256)
	// The returned public key must be a parseable RSA SPKI.
	_, err = x509.ParsePKIXPublicKey(pub.PublicKey)
	require.NoError(t, err, "GetPublicKey output must be a valid DER SubjectPublicKeyInfo")

	message := []byte("the message to sign")
	signOut, err := c.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(keyId),
		Message:          message,
		MessageType:      kmstypes.MessageTypeRaw,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha256,
	})
	require.NoError(t, err)
	require.NotEmpty(t, signOut.Signature, "Sign must return a real signature")

	verifyOut, err := c.Verify(ctx, &kms.VerifyInput{
		KeyId:            aws.String(keyId),
		Message:          message,
		MessageType:      kmstypes.MessageTypeRaw,
		Signature:        signOut.Signature,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha256,
	})
	require.NoError(t, err)
	assert.True(t, verifyOut.SignatureValid, "Verify of a real signature must succeed")

	// A tampered message must fail verification.
	_, err = c.Verify(ctx, &kms.VerifyInput{
		KeyId:            aws.String(keyId),
		Message:          []byte("the TAMPERED message"),
		MessageType:      kmstypes.MessageTypeRaw,
		Signature:        signOut.Signature,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha256,
	})
	require.Error(t, err, "Verify of a tampered message must fail")
	var invalidSig *kmstypes.KMSInvalidSignatureException
	require.ErrorAs(t, err, &invalidSig, "tampered Verify must return KMSInvalidSignatureException")
}

// TestKMS_ECDSASignVerify covers the EC signing path (ECC_NIST_P256 →
// ECDSA_SHA_256) with real crypto.
func TestKMS_ECDSASignVerify(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("ecc-sign-key"),
		KeySpec:     kmstypes.KeySpecEccNistP256,
		KeyUsage:    kmstypes.KeyUsageTypeSignVerify,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	message := []byte("ecdsa message")
	signOut, err := c.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(keyId),
		Message:          message,
		MessageType:      kmstypes.MessageTypeRaw,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecEcdsaSha256,
	})
	require.NoError(t, err)

	verifyOut, err := c.Verify(ctx, &kms.VerifyInput{
		KeyId:            aws.String(keyId),
		Message:          message,
		MessageType:      kmstypes.MessageTypeRaw,
		Signature:        signOut.Signature,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecEcdsaSha256,
	})
	require.NoError(t, err)
	assert.True(t, verifyOut.SignatureValid)
}

// TestKMS_GenerateVerifyMac exercises the HMAC flow: CreateKey(HMAC_256) →
// GenerateMac → VerifyMac round-trips, and a tampered message fails VerifyMac.
func TestKMS_GenerateVerifyMac(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("hmac-key"),
		KeySpec:     kmstypes.KeySpecHmac256,
		KeyUsage:    kmstypes.KeyUsageTypeGenerateVerifyMac,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	message := []byte("mac me")
	macOut, err := c.GenerateMac(ctx, &kms.GenerateMacInput{
		KeyId:        aws.String(keyId),
		Message:      message,
		MacAlgorithm: kmstypes.MacAlgorithmSpecHmacSha256,
	})
	require.NoError(t, err)
	require.NotEmpty(t, macOut.Mac, "GenerateMac must return a real HMAC")

	verifyOut, err := c.VerifyMac(ctx, &kms.VerifyMacInput{
		KeyId:        aws.String(keyId),
		Message:      message,
		Mac:          macOut.Mac,
		MacAlgorithm: kmstypes.MacAlgorithmSpecHmacSha256,
	})
	require.NoError(t, err)
	assert.True(t, verifyOut.MacValid, "VerifyMac of a real MAC must succeed")

	// A tampered message must fail MAC verification.
	_, err = c.VerifyMac(ctx, &kms.VerifyMacInput{
		KeyId:        aws.String(keyId),
		Message:      []byte("mac ME (tampered)"),
		Mac:          macOut.Mac,
		MacAlgorithm: kmstypes.MacAlgorithmSpecHmacSha256,
	})
	require.Error(t, err, "VerifyMac of a tampered message must fail")
}

// TestKMS_GenerateDataKeyPair covers GenerateDataKeyPair (real keypair +
// wrapped private key + decryptable wrap) and GenerateDataKeyPairWithoutPlaintext
// (wrapped private key only, no plaintext on the wire).
func TestKMS_GenerateDataKeyPair(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("dkp-cmk")})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	pair, err := c.GenerateDataKeyPair(ctx, &kms.GenerateDataKeyPairInput{
		KeyId:       aws.String(keyId),
		KeyPairSpec: kmstypes.DataKeyPairSpecRsa2048,
	})
	require.NoError(t, err)
	require.NotEmpty(t, pair.PublicKey, "public key must be returned")
	require.NotEmpty(t, pair.PrivateKeyPlaintext, "plaintext private key must be returned")
	require.NotEmpty(t, pair.PrivateKeyCiphertextBlob, "wrapped private key must be returned")
	// The plaintext private key is a parseable PKCS#8 key.
	_, err = x509.ParsePKCS8PrivateKey(pair.PrivateKeyPlaintext)
	require.NoError(t, err, "PrivateKeyPlaintext must be a valid PKCS#8 DER key")
	// The public key is a parseable SPKI.
	_, err = x509.ParsePKIXPublicKey(pair.PublicKey)
	require.NoError(t, err)

	// The wrapped private key decrypts back to the plaintext via the CMK.
	dec, err := c.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob: pair.PrivateKeyCiphertextBlob,
	})
	require.NoError(t, err)
	assert.True(t, bytes.Equal(dec.Plaintext, pair.PrivateKeyPlaintext),
		"unwrapping the private key must round-trip the plaintext")

	noPlain, err := c.GenerateDataKeyPairWithoutPlaintext(ctx, &kms.GenerateDataKeyPairWithoutPlaintextInput{
		KeyId:       aws.String(keyId),
		KeyPairSpec: kmstypes.DataKeyPairSpecEccNistP256,
	})
	require.NoError(t, err)
	require.NotEmpty(t, noPlain.PublicKey)
	require.NotEmpty(t, noPlain.PrivateKeyCiphertextBlob)
}

// TestKMS_DeriveSharedSecret runs a real ECDH between the CMK's EC key and a
// peer EC public key, and asserts both sides derive the same secret.
func TestKMS_DeriveSharedSecret(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("ecdh-cmk"),
		KeySpec:     kmstypes.KeySpecEccNistP256,
		KeyUsage:    kmstypes.KeyUsageTypeKeyAgreement,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	// The CMK's public key (one side of the exchange).
	cmkPub, err := c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.Contains(t, cmkPub.KeyAgreementAlgorithms, kmstypes.KeyAgreementAlgorithmSpecEcdh)
	cmkPubAny, err := x509.ParsePKIXPublicKey(cmkPub.PublicKey)
	require.NoError(t, err)
	cmkEcdsaPub := cmkPubAny.(*ecdsa.PublicKey)

	// Our peer key (the other side).
	peerPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	peerPubDER, err := x509.MarshalPKIXPublicKey(&peerPriv.PublicKey)
	require.NoError(t, err)

	derived, err := c.DeriveSharedSecret(ctx, &kms.DeriveSharedSecretInput{
		KeyId:                 aws.String(keyId),
		KeyAgreementAlgorithm: kmstypes.KeyAgreementAlgorithmSpecEcdh,
		PublicKey:             peerPubDER,
	})
	require.NoError(t, err)
	require.NotEmpty(t, derived.SharedSecret, "DeriveSharedSecret must return the raw shared secret")

	// Compute the same secret on the peer side with stdlib ECDH and compare.
	cmkEcdh, err := cmkEcdsaPub.ECDH()
	require.NoError(t, err)
	peerEcdh, err := peerPriv.ECDH()
	require.NoError(t, err)
	expected, err := peerEcdh.ECDH(cmkEcdh)
	require.NoError(t, err)
	assert.True(t, bytes.Equal(expected, derived.SharedSecret),
		"the sim's ECDH must match the peer-side ECDH for the same key pair")
}

// TestKMS_CustomKeyStores covers the custom-key-store CRUD + connection-state
// machine: Create → Describe → Connect → Disconnect → Update → Delete.
func TestKMS_CustomKeyStores(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateCustomKeyStore(ctx, &kms.CreateCustomKeyStoreInput{
		CustomKeyStoreName: aws.String("sdk-cks"),
		CustomKeyStoreType: kmstypes.CustomKeyStoreTypeExternalKeyStore,
	})
	require.NoError(t, err)
	cksId := *created.CustomKeyStoreId
	t.Cleanup(func() {
		_, _ = c.DisconnectCustomKeyStore(ctx, &kms.DisconnectCustomKeyStoreInput{CustomKeyStoreId: aws.String(cksId)})
		_, _ = c.DeleteCustomKeyStore(ctx, &kms.DeleteCustomKeyStoreInput{CustomKeyStoreId: aws.String(cksId)})
	})

	desc, err := c.DescribeCustomKeyStores(ctx, &kms.DescribeCustomKeyStoresInput{
		CustomKeyStoreId: aws.String(cksId),
	})
	require.NoError(t, err)
	require.Len(t, desc.CustomKeyStores, 1)
	assert.Equal(t, kmstypes.ConnectionStateTypeDisconnected, desc.CustomKeyStores[0].ConnectionState)

	_, err = c.ConnectCustomKeyStore(ctx, &kms.ConnectCustomKeyStoreInput{CustomKeyStoreId: aws.String(cksId)})
	require.NoError(t, err)
	desc, err = c.DescribeCustomKeyStores(ctx, &kms.DescribeCustomKeyStoresInput{CustomKeyStoreId: aws.String(cksId)})
	require.NoError(t, err)
	assert.Equal(t, kmstypes.ConnectionStateTypeConnected, desc.CustomKeyStores[0].ConnectionState)

	_, err = c.DisconnectCustomKeyStore(ctx, &kms.DisconnectCustomKeyStoreInput{CustomKeyStoreId: aws.String(cksId)})
	require.NoError(t, err)

	_, err = c.UpdateCustomKeyStore(ctx, &kms.UpdateCustomKeyStoreInput{
		CustomKeyStoreId:      aws.String(cksId),
		NewCustomKeyStoreName: aws.String("sdk-cks-renamed"),
	})
	require.NoError(t, err)
	desc, err = c.DescribeCustomKeyStores(ctx, &kms.DescribeCustomKeyStoresInput{CustomKeyStoreId: aws.String(cksId)})
	require.NoError(t, err)
	assert.Equal(t, "sdk-cks-renamed", aws.ToString(desc.CustomKeyStores[0].CustomKeyStoreName))

	_, err = c.DeleteCustomKeyStore(ctx, &kms.DeleteCustomKeyStoreInput{CustomKeyStoreId: aws.String(cksId)})
	require.NoError(t, err)
}

// TestKMS_RetireAndListRetirableGrants covers RetireGrant (by grant id) and
// ListRetirableGrants (filtering by RetiringPrincipal).
func TestKMS_RetireAndListRetirableGrants(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{Description: aws.String("grant-retire-cmk")})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	retiringPrincipal := "arn:aws:iam::000000000000:role/retirer-" + keyId[:8]
	grant, err := c.CreateGrant(ctx, &kms.CreateGrantInput{
		KeyId:             aws.String(keyId),
		GranteePrincipal:  aws.String("arn:aws:iam::000000000000:role/grantee"),
		RetiringPrincipal: aws.String(retiringPrincipal),
		Operations:        []kmstypes.GrantOperation{kmstypes.GrantOperationEncrypt},
	})
	require.NoError(t, err)
	grantId := *grant.GrantId

	retirable, err := c.ListRetirableGrants(ctx, &kms.ListRetirableGrantsInput{
		RetiringPrincipal: aws.String(retiringPrincipal),
	})
	require.NoError(t, err)
	require.Len(t, retirable.Grants, 1, "the grant must be listed as retirable for its RetiringPrincipal")
	assert.Equal(t, grantId, aws.ToString(retirable.Grants[0].GrantId))

	_, err = c.RetireGrant(ctx, &kms.RetireGrantInput{
		KeyId:   aws.String(keyId),
		GrantId: aws.String(grantId),
	})
	require.NoError(t, err)

	retirable, err = c.ListRetirableGrants(ctx, &kms.ListRetirableGrantsInput{
		RetiringPrincipal: aws.String(retiringPrincipal),
	})
	require.NoError(t, err)
	assert.Empty(t, retirable.Grants, "the retired grant must no longer be listed")
}

// TestKMS_MultiRegionReplicateAndPrimary covers the multi-region key flow:
// CreateKey(MultiRegion=true) → ReplicateKey → UpdatePrimaryRegion, and
// asserts DescribeKey surfaces the MultiRegionConfiguration.
func TestKMS_MultiRegionReplicateAndPrimary(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("mrk"),
		MultiRegion: aws.Bool(true),
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId
	require.True(t, aws.ToBool(created.KeyMetadata.MultiRegion), "key must be multi-region")
	require.NotNil(t, created.KeyMetadata.MultiRegionConfiguration)
	assert.Equal(t, kmstypes.MultiRegionKeyTypePrimary,
		created.KeyMetadata.MultiRegionConfiguration.MultiRegionKeyType)

	rep, err := c.ReplicateKey(ctx, &kms.ReplicateKeyInput{
		KeyId:         aws.String(keyId),
		ReplicaRegion: aws.String("us-west-2"),
	})
	require.NoError(t, err)
	require.NotNil(t, rep.ReplicaKeyMetadata)
	assert.Equal(t, keyId, aws.ToString(rep.ReplicaKeyMetadata.KeyId),
		"the replica shares the primary key id")
	require.NotNil(t, rep.ReplicaKeyMetadata.MultiRegionConfiguration)
	assert.Equal(t, kmstypes.MultiRegionKeyTypeReplica,
		rep.ReplicaKeyMetadata.MultiRegionConfiguration.MultiRegionKeyType)

	// The primary now lists the replica region.
	desc, err := c.DescribeKey(ctx, &kms.DescribeKeyInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.NotNil(t, desc.KeyMetadata.MultiRegionConfiguration)
	require.Len(t, desc.KeyMetadata.MultiRegionConfiguration.ReplicaKeys, 1)
	assert.Equal(t, "us-west-2",
		aws.ToString(desc.KeyMetadata.MultiRegionConfiguration.ReplicaKeys[0].Region))

	_, err = c.UpdatePrimaryRegion(ctx, &kms.UpdatePrimaryRegionInput{
		KeyId:         aws.String(keyId),
		PrimaryRegion: aws.String("us-west-2"),
	})
	require.NoError(t, err)
}

// TestKMS_GetKeyLastUsage asserts GetKeyLastUsage omits KeyLastUsage for a
// never-used key, then reports it after a Sign operation.
func TestKMS_GetKeyLastUsage(t *testing.T) {
	c := kmsClient()

	created, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("last-usage-key"),
		KeySpec:     kmstypes.KeySpecRsa2048,
		KeyUsage:    kmstypes.KeyUsageTypeSignVerify,
	})
	require.NoError(t, err)
	keyId := *created.KeyMetadata.KeyId

	usage, err := c.GetKeyLastUsage(ctx, &kms.GetKeyLastUsageInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	assert.Nil(t, usage.KeyLastUsage, "an unused key must not report KeyLastUsage")
	require.NotNil(t, usage.KeyCreationDate)

	_, err = c.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(keyId),
		Message:          []byte("use the key"),
		MessageType:      kmstypes.MessageTypeRaw,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha256,
	})
	require.NoError(t, err)

	usage, err = c.GetKeyLastUsage(ctx, &kms.GetKeyLastUsageInput{KeyId: aws.String(keyId)})
	require.NoError(t, err)
	require.NotNil(t, usage.KeyLastUsage, "after a Sign, KeyLastUsage must be reported")
	assert.Equal(t, "Sign", string(usage.KeyLastUsage.Operation))
}

// TestKMS_AsymmetricOperationsRestored exercises every asymmetric/MAC
// operation handler restored in the kms_crypto.go rewrite: GetPublicKey,
// Sign, Verify, GenerateMac, VerifyMac, GenerateDataKeyPair,
// GenerateDataKeyPairWithoutPlaintext, and DeriveSharedSecret. Each call
// must succeed, confirming the handlers are registered and functional.
func TestKMS_AsymmetricOperationsRestored(t *testing.T) {
	c := kmsClient()

	signKey, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("rsa-sign-all-ops"),
		KeySpec:     kmstypes.KeySpecRsa2048,
		KeyUsage:    kmstypes.KeyUsageTypeSignVerify,
	})
	require.NoError(t, err)
	signKeyId := *signKey.KeyMetadata.KeyId

	_, err = c.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: aws.String(signKeyId)})
	require.NoError(t, err)
	signOut, err := c.Sign(ctx, &kms.SignInput{
		KeyId:            aws.String(signKeyId),
		Message:          []byte("msg"),
		MessageType:      kmstypes.MessageTypeRaw,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha256,
	})
	require.NoError(t, err)
	_, err = c.Verify(ctx, &kms.VerifyInput{
		KeyId:            aws.String(signKeyId),
		Message:          []byte("msg"),
		Signature:        signOut.Signature,
		SigningAlgorithm: kmstypes.SigningAlgorithmSpecRsassaPssSha256,
		MessageType:      kmstypes.MessageTypeRaw,
	})
	require.NoError(t, err)

	hmacKey, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("hmac-all-ops"),
		KeySpec:     kmstypes.KeySpecHmac256,
		KeyUsage:    kmstypes.KeyUsageTypeGenerateVerifyMac,
	})
	require.NoError(t, err)
	hmacKeyId := *hmacKey.KeyMetadata.KeyId

	macOut, err := c.GenerateMac(ctx, &kms.GenerateMacInput{
		KeyId:        aws.String(hmacKeyId),
		Message:      []byte("mac-msg"),
		MacAlgorithm: kmstypes.MacAlgorithmSpecHmacSha256,
	})
	require.NoError(t, err)
	_, err = c.VerifyMac(ctx, &kms.VerifyMacInput{
		KeyId:        aws.String(hmacKeyId),
		Message:      []byte("mac-msg"),
		Mac:          macOut.Mac,
		MacAlgorithm: kmstypes.MacAlgorithmSpecHmacSha256,
	})
	require.NoError(t, err)

	symmetricKey, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("data-key-pair-all-ops"),
	})
	require.NoError(t, err)
	symmetricKeyId := *symmetricKey.KeyMetadata.KeyId

	_, err = c.GenerateDataKeyPair(ctx, &kms.GenerateDataKeyPairInput{
		KeyId:       aws.String(symmetricKeyId),
		KeyPairSpec: kmstypes.DataKeyPairSpecRsa2048,
	})
	require.NoError(t, err)
	_, err = c.GenerateDataKeyPairWithoutPlaintext(ctx, &kms.GenerateDataKeyPairWithoutPlaintextInput{
		KeyId:       aws.String(symmetricKeyId),
		KeyPairSpec: kmstypes.DataKeyPairSpecRsa2048,
	})
	require.NoError(t, err)

	eccKey, err := c.CreateKey(ctx, &kms.CreateKeyInput{
		Description: aws.String("ecdh-all-ops"),
		KeySpec:     kmstypes.KeySpecEccNistP256,
		KeyUsage:    kmstypes.KeyUsageTypeKeyAgreement,
	})
	require.NoError(t, err)
	eccKeyId := *eccKey.KeyMetadata.KeyId

	peerPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	peerPub, err := x509.MarshalPKIXPublicKey(&peerPriv.PublicKey)
	require.NoError(t, err)
	_, err = c.DeriveSharedSecret(ctx, &kms.DeriveSharedSecretInput{
		KeyId:                 aws.String(eccKeyId),
		KeyAgreementAlgorithm: kmstypes.KeyAgreementAlgorithmSpecEcdh,
		PublicKey:             peerPub,
	})
	require.NoError(t, err)
}
