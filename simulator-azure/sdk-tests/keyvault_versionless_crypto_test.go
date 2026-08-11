package azure_sdk_test

import (
	"crypto/sha256"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeyVault_VersionlessKeyCrypto exercises the no-version crypto form
// (POST /keys/{name}/{verb}), which azkeys issues when version == "". Before
// the fix the sim returned 405 for the 3-segment path; real Key Vault applies
// the key's current version.
func TestKeyVault_VersionlessKeyCrypto(t *testing.T) {
	rg := "kv-versionless-rg"
	vault := "kv-versionless"
	createKVViaARM(t, rg, vault)

	client, err := azkeys.NewClient(kvVaultURL(vault), &fakeCredential{},
		&azkeys.ClientOptions{
			ClientOptions:                        kvClientOptions(),
			DisableChallengeResourceVerification: true,
		})
	require.NoError(t, err)

	_, err = client.CreateKey(ctx, "vl-key",
		azkeys.CreateKeyParameters{Kty: keyTypePtr(azkeys.KeyTypeRSA)}, nil)
	require.NoError(t, err)

	// Every crypto op with version "" must resolve the current version, not 405.
	encryptResp, err := client.Encrypt(ctx, "vl-key", "",
		azkeys.KeyOperationParameters{Algorithm: encryptionAlgPtr(azkeys.EncryptionAlgorithmRSAOAEP256), Value: []byte("secret")}, nil)
	require.NoError(t, err, "version-less Encrypt must not 405")
	require.NotEmpty(t, encryptResp.Result)

	decryptResp, err := client.Decrypt(ctx, "vl-key", "",
		azkeys.KeyOperationParameters{Algorithm: encryptionAlgPtr(azkeys.EncryptionAlgorithmRSAOAEP256), Value: encryptResp.Result}, nil)
	require.NoError(t, err, "version-less Decrypt must not 405")
	assert.Equal(t, []byte("secret"), decryptResp.Result)

	digest := sha256.Sum256([]byte("payload"))
	signResp, err := client.Sign(ctx, "vl-key", "",
		azkeys.SignParameters{Algorithm: signatureAlgPtr(azkeys.SignatureAlgorithmRS256), Value: digest[:]}, nil)
	require.NoError(t, err, "version-less Sign must not 405")
	require.NotEmpty(t, signResp.Result)

	verifyResp, err := client.Verify(ctx, "vl-key", "",
		azkeys.VerifyParameters{Algorithm: signatureAlgPtr(azkeys.SignatureAlgorithmRS256), Digest: digest[:], Signature: signResp.Result}, nil)
	require.NoError(t, err, "version-less Verify must not 405")
	require.NotNil(t, verifyResp.Value)
	assert.True(t, *verifyResp.Value)

	wrapResp, err := client.WrapKey(ctx, "vl-key", "",
		azkeys.KeyOperationParameters{Algorithm: encryptionAlgPtr(azkeys.EncryptionAlgorithmRSAOAEP256), Value: []byte("wrapme")}, nil)
	require.NoError(t, err, "version-less WrapKey must not 405")
	unwrapResp, err := client.UnwrapKey(ctx, "vl-key", "",
		azkeys.KeyOperationParameters{Algorithm: encryptionAlgPtr(azkeys.EncryptionAlgorithmRSAOAEP256), Value: wrapResp.Result}, nil)
	require.NoError(t, err, "version-less UnwrapKey must not 405")
	assert.Equal(t, []byte("wrapme"), unwrapResp.Result)
}
