package azure_sdk_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azcertificates"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAzureResources_MoveKeyVault drives the Microsoft.KeyVault move hook end
// to end. A key vault is addressed on two planes and only one of them carries
// the resource group, so the move has to hold both halves true at once:
//
//   - the ARM plane re-keys — the vault record and its
//     privateEndpointConnections child answer under the destination group and
//     stop answering under the source group, and the tags written at the
//     vault's scope come with it;
//   - the data plane does not move at all — secrets, keys and certificates are
//     addressed through the vault's globally unique name, so the vault URI is
//     unchanged, the secret reads back byte for byte, the certificate keeps its
//     DER, and the private key created before the move still decrypts a
//     ciphertext produced before the move.
//
// The last assertion is the one that would catch a hook that re-derived key
// material from the resource ID: the ciphertext only decrypts if the stored
// RSA key is literally the same key.
func TestAzureResources_MoveKeyVault(t *testing.T) {
	srcRG, dstRG := "kv-move-src-rg", "kv-move-dst-rg"
	ensureRG(t, srcRG)
	ensureRG(t, dstRG)

	const vault = "kv-move-vault"
	createKVVault(t, srcRG, vault)

	vaults, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	before, err := vaults.Get(ctx, srcRG, vault, nil)
	require.NoError(t, err)
	require.NotNil(t, before.Properties)
	require.NotNil(t, before.Properties.VaultURI)
	uriBefore := *before.Properties.VaultURI

	// An ARM-plane child of the vault, created before the move.
	pecClient, err := armkeyvault.NewPrivateEndpointConnectionsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = pecClient.Put(ctx, srcRG, vault, "kv-move-pec", armkeyvault.PrivateEndpointConnection{
		Properties: &armkeyvault.PrivateEndpointConnectionProperties{
			PrivateLinkServiceConnectionState: &armkeyvault.PrivateLinkServiceConnectionState{
				Status:      ptr(armkeyvault.PrivateEndpointServiceConnectionStatusApproved),
				Description: ptrStr("approved before the move"),
			},
		},
	}, nil)
	require.NoError(t, err)

	// Real material written through the vault's own data plane, before the
	// move: a secret, an RSA key (plus a ciphertext only that key can open),
	// and a self-signed certificate.
	secrets, err := azsecrets.NewClient(kvVaultURL(vault), &fakeCredential{},
		&azsecrets.ClientOptions{
			ClientOptions:                        kvClientOptions(),
			DisableChallengeResourceVerification: true,
		})
	require.NoError(t, err)
	const secretValue = "material written before the move"
	_, err = secrets.SetSecret(ctx, "move-secret",
		azsecrets.SetSecretParameters{Value: stringPtr(secretValue)}, nil)
	require.NoError(t, err)

	keys, err := azkeys.NewClient(kvVaultURL(vault), &fakeCredential{},
		&azkeys.ClientOptions{
			ClientOptions:                        kvClientOptions(),
			DisableChallengeResourceVerification: true,
		})
	require.NoError(t, err)
	createdKey, err := keys.CreateKey(ctx, "move-key",
		azkeys.CreateKeyParameters{Kty: keyTypePtr(azkeys.KeyTypeRSA)}, nil)
	require.NoError(t, err)
	require.NotNil(t, createdKey.Key)
	require.NotNil(t, createdKey.Key.KID)
	kidBefore := string(*createdKey.Key.KID)
	plaintext := []byte("sealed before the move")
	encrypted, err := keys.Encrypt(ctx, "move-key", "",
		azkeys.KeyOperationParameters{
			Algorithm: encryptionAlgPtr(azkeys.EncryptionAlgorithmRSAOAEP256),
			Value:     plaintext,
		}, nil)
	require.NoError(t, err)
	require.NotEmpty(t, encrypted.Result)

	certs, err := azcertificates.NewClient(kvVaultURL(vault), &fakeCredential{},
		&azcertificates.ClientOptions{
			ClientOptions:                        kvClientOptions(),
			DisableChallengeResourceVerification: true,
		})
	require.NoError(t, err)
	_, err = certs.CreateCertificate(ctx, "move-cert",
		azcertificates.CreateCertificateParameters{CertificatePolicy: selfSignedCertPolicy()}, nil)
	require.NoError(t, err)
	certBefore, err := certs.GetCertificate(ctx, "move-cert", "", nil)
	require.NoError(t, err)
	require.NotEmpty(t, certBefore.CER)

	// Tags written at the vault's scope before the move — Azure Resource
	// Manager moves a resource's tags with the resource.
	vaultID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG +
		"/providers/Microsoft.KeyVault/vaults/" + vault
	tagsClient, err := armresources.NewTagsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = tagsClient.CreateOrUpdateAtScope(ctx, vaultID, armresources.TagsResource{
		Properties: &armresources.Tags{Tags: map[string]*string{"team": to.Ptr("keyvault")}},
	}, nil)
	require.NoError(t, err)

	client, err := armresources.NewClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	move := armresources.MoveInfo{
		Resources:           []*string{to.Ptr(vaultID)},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}
	valPoller, err := client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = valPoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	movePoller, err := client.BeginMoveResources(ctx, srcRG, move, nil)
	require.NoError(t, err)
	_, err = movePoller.PollUntilDone(ctx, nil)
	require.NoError(t, err)

	// The ARM record relocated.
	moved, err := vaults.Get(ctx, dstRG, vault, nil)
	require.NoError(t, err)
	require.NotNil(t, moved.ID)
	assert.Contains(t, *moved.ID, "/resourceGroups/"+dstRG+"/")
	_, err = vaults.Get(ctx, srcRG, vault, nil)
	require.Error(t, err, "the moved vault must no longer resolve under the source group")

	// The vault URI is built from the vault's name, which the move does not
	// change: every client holding the URI keeps reaching the same vault.
	require.NotNil(t, moved.Properties)
	require.NotNil(t, moved.Properties.VaultURI)
	assert.Equal(t, uriBefore, *moved.Properties.VaultURI, "a cross-group move must not change the vault URI")

	// The private-endpoint-connection child moved with the vault.
	movedPEC, err := pecClient.Get(ctx, dstRG, vault, "kv-move-pec", nil)
	require.NoError(t, err)
	require.NotNil(t, movedPEC.ID)
	assert.Contains(t, *movedPEC.ID, "/resourceGroups/"+dstRG+"/")
	_, err = pecClient.Get(ctx, srcRG, vault, "kv-move-pec", nil)
	require.Error(t, err, "the moved connection child must no longer resolve under the source group")

	// The tags written at the vault's old scope answer at the new one.
	movedTags, err := tagsClient.GetAtScope(ctx, *moved.ID, nil)
	require.NoError(t, err)
	require.NotNil(t, movedTags.Properties)
	require.NotNil(t, movedTags.Properties.Tags["team"])
	assert.Equal(t, "keyvault", *movedTags.Properties.Tags["team"], "tags must move with the resource")

	// The data plane — addressed by the vault name, untouched by the move —
	// serves exactly the material written before it.
	gotSecret, err := secrets.GetSecret(ctx, "move-secret", "", nil)
	require.NoError(t, err)
	require.NotNil(t, gotSecret.Value)
	assert.Equal(t, secretValue, *gotSecret.Value, "secret material must survive the vault move")

	gotKey, err := keys.GetKey(ctx, "move-key", "", nil)
	require.NoError(t, err)
	require.NotNil(t, gotKey.Key)
	require.NotNil(t, gotKey.Key.KID)
	assert.Equal(t, kidBefore, string(*gotKey.Key.KID), "the key identifier is built from the vault name, not its resource ID")
	decrypted, err := keys.Decrypt(ctx, "move-key", "",
		azkeys.KeyOperationParameters{
			Algorithm: encryptionAlgPtr(azkeys.EncryptionAlgorithmRSAOAEP256),
			Value:     encrypted.Result,
		}, nil)
	require.NoError(t, err, "the key created before the move must still open its own ciphertext")
	assert.Equal(t, plaintext, decrypted.Result, "a cross-group move must not rotate key material")

	certAfter, err := certs.GetCertificate(ctx, "move-cert", "", nil)
	require.NoError(t, err)
	assert.Equal(t, certBefore.CER, certAfter.CER, "certificate material must survive the vault move")
	assert.Equal(t, certBefore.X509Thumbprint, certAfter.X509Thumbprint)

	// A vault the move already re-homed cannot be moved again from the old
	// group: the pre-flight reports it absent, exactly as real Azure Resource
	// Manager does for an id that names no live resource.
	_, err = client.BeginValidateMoveResources(ctx, srcRG, move, nil)
	require.Error(t, err, "re-validating the stale source id must fail")
	var staleErr *azcore.ResponseError
	require.True(t, errors.As(err, &staleErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, "ResourceNotFound", staleErr.ErrorCode)
	assert.Equal(t, http.StatusNotFound, staleErr.StatusCode)

	// A type Azure Resource Manager does not move still answers
	// ResourceMoveNotSupported with the 409 a real validation failure reports.
	_, err = client.BeginValidateMoveResources(ctx, srcRG, armresources.MoveInfo{
		Resources: []*string{to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + srcRG +
			"/providers/Microsoft.ContainerInstance/containerGroups/kv-move-never-movable")},
		TargetResourceGroup: to.Ptr("/subscriptions/" + subscriptionID + "/resourceGroups/" + dstRG),
	}, nil)
	require.Error(t, err, "validating a move of an unmovable type must fail")
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, "ResourceMoveNotSupported", respErr.ErrorCode)
	assert.Equal(t, http.StatusConflict, respErr.StatusCode)
}
