package azure_sdk_test

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for Key Vault references reaching an App Service app's
// workload: the app resolves each reference through the identity its
// keyVaultReferenceIdentity names, with that identity's access to the vault —
// an Azure RBAC role assignment or an access policy — and its configreferences
// report why a reference that reaches the workload as text did not resolve.
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}
//	PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/configreferences/appsettings/{appSettingKey}
//	GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/configreferences/connectionstrings/{connectionStringKey}

const kvSecretsUserRole = "4633458b-17de-408a-b874-0445c86b69e6"

func TestSDK_WebApps_KeyVaultReferencesReachTheWorkload(t *testing.T) {
	rg, name := "sdk-kvref-env-rg", "sdk-kvref-env-app"
	rbacVault, policyVault := "sdkkvrefrbac", "sdkkvrefpolicy"
	ensureRG(t, rg)
	vaults, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	createVault := func(vault string, rbac bool) *armkeyvault.Vault {
		poller, err := vaults.BeginCreateOrUpdate(ctx, rg, vault, armkeyvault.VaultCreateOrUpdateParameters{
			Location: to.Ptr("eastus"),
			Properties: &armkeyvault.VaultProperties{
				TenantID:                to.Ptr(simTenantID),
				SKU:                     &armkeyvault.SKU{Family: to.Ptr(armkeyvault.SKUFamilyA), Name: to.Ptr(armkeyvault.SKUNameStandard)},
				EnableRbacAuthorization: to.Ptr(rbac),
				AccessPolicies:          []*armkeyvault.AccessPolicyEntry{},
			},
		}, nil)
		require.NoError(t, err)
		v, err := poller.PollUntilDone(ctx, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = vaults.Delete(ctx, rg, vault, nil) })
		return &v.Vault
	}
	setSecret := func(vault, secret, value string) string {
		client, err := azsecrets.NewClient(kvVaultURL(vault), &fakeCredential{}, &azsecrets.ClientOptions{
			ClientOptions:                        kvClientOptions(),
			DisableChallengeResourceVerification: true,
		})
		require.NoError(t, err)
		resp, err := client.SetSecret(ctx, secret, azsecrets.SetSecretParameters{Value: to.Ptr(value)}, nil)
		require.NoError(t, err)
		return resp.ID.Version()
	}
	rbac := createVault(rbacVault, true)
	createVault(policyVault, false)
	version := setSecret(rbacVault, "db-password", "hunter2")
	setSecret(policyVault, "api-key", "from-the-policy-vault")

	dbRef := "@Microsoft.KeyVault(SecretUri=https://" + rbacVault + ".vault.azure.net/secrets/db-password)"
	missingRef := "@Microsoft.KeyVault(VaultName=" + rbacVault + ";SecretName=no-such-secret)"
	apiRef := "@Microsoft.KeyVault(VaultName=" + policyVault + ";SecretName=api-key)"
	client := createFilesWebApp(t, rg, name, map[string]string{"DB": dbRef, "MISSING": missingRef, "API": apiRef})
	_, err = client.UpdateConnectionStrings(ctx, rg, name, armappservice.ConnectionStringDictionary{
		Properties: map[string]*armappservice.ConnStringValueTypePair{
			"Main": {Value: to.Ptr(dbRef), Type: to.Ptr(armappservice.ConnectionStringTypeCustom)},
		},
	}, nil)
	require.NoError(t, err)

	ref := func(key string) *armappservice.APIKVReferenceProperties {
		t.Helper()
		got, err := client.GetAppSettingKeyVaultReference(ctx, rg, name, key, nil)
		require.NoError(t, err)
		require.NotNil(t, got.Properties)
		return got.Properties
	}

	// An app without a managed identity cannot reach the vault.
	site, err := client.Get(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "SystemAssigned", *site.Properties.KeyVaultReferenceIdentity)
	db := ref("DB")
	assert.Equal(t, armappservice.ResolveStatusMSINotEnabled, *db.Status)
	assert.NotEmpty(t, *db.Details)
	assert.Equal(t, dbRef, siteEnv(t, name, "DB"), "an unresolved reference reaches the workload as written")

	// With a system-assigned identity the vault still denies it until a role
	// assignment grants it the secret.
	updated, err := client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Identity: &armappservice.ManagedServiceIdentity{Type: to.Ptr(armappservice.ManagedServiceIdentityTypeSystemAssigned)},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Identity)
	principal := *updated.Identity.PrincipalID
	require.NotEmpty(t, principal)
	assert.Equal(t, simTenantID, *updated.Identity.TenantID)
	assert.Equal(t, armappservice.ResolveStatusAccessToKeyVaultDenied, *ref("DB").Status)

	roles, err := armauthorization.NewRoleAssignmentsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = roles.Create(ctx, *rbac.ID, uuid.NewString(), armauthorization.RoleAssignmentCreateParameters{
		Properties: &armauthorization.RoleAssignmentProperties{
			RoleDefinitionID: to.Ptr("/subscriptions/" + subscriptionID + "/providers/Microsoft.Authorization/roleDefinitions/" + kvSecretsUserRole),
			PrincipalID:      to.Ptr(principal),
			PrincipalType:    to.Ptr(armauthorization.PrincipalTypeServicePrincipal),
		},
	}, nil)
	require.NoError(t, err)
	db = ref("DB")
	assert.Equal(t, armappservice.ResolveStatusResolved, *db.Status)
	assert.Equal(t, version, *db.ActiveVersion)
	require.NotNil(t, db.IdentityType)
	assert.Equal(t, armappservice.ManagedServiceIdentityTypeSystemAssigned, *db.IdentityType.Type)
	missing := ref("MISSING")
	assert.Equal(t, armappservice.ResolveStatusSecretNotFound, *missing.Status)
	assert.NotEmpty(t, *missing.Details)
	assert.Equal(t, armappservice.ResolveStatusAccessToKeyVaultDenied, *ref("API").Status,
		"an access-policy vault grants nothing to an identity it has no policy for")
	conn, err := client.GetSiteConnectionStringKeyVaultReference(ctx, rg, name, "Main", nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ResolveStatusResolved, *conn.Properties.Status)

	_, err = client.Restart(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "hunter2", siteEnv(t, name, "DB"), "the workload sees the secret's value")
	assert.Equal(t, "hunter2", siteEnv(t, name, "CUSTOMCONNSTR_Main"))
	assert.Equal(t, missingRef, siteEnv(t, name, "MISSING"))
	assert.Equal(t, apiRef, siteEnv(t, name, "API"))

	// A user-assigned identity named by keyVaultReferenceIdentity resolves
	// with its own access: the access policy vault grants it, the RBAC vault
	// does not.
	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	uai, err := identities.CreateOrUpdate(ctx, rg, "sdk-kvref-identity", armmsi.Identity{Location: to.Ptr("eastus")}, nil)
	require.NoError(t, err)
	_, err = vaults.UpdateAccessPolicy(ctx, rg, policyVault, armkeyvault.AccessPolicyUpdateKindAdd, armkeyvault.VaultAccessPolicyParameters{
		Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    uai.Properties.PrincipalID,
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsGet)}},
		}}},
	}, nil)
	require.NoError(t, err)
	updated, err = client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Identity: &armappservice.ManagedServiceIdentity{
			Type:                   to.Ptr(armappservice.ManagedServiceIdentityTypeSystemAssignedUserAssigned),
			UserAssignedIdentities: map[string]*armappservice.UserAssignedIdentity{*uai.ID: {}},
		},
		Properties: &armappservice.SitePatchResourceProperties{KeyVaultReferenceIdentity: uai.ID},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, principal, *updated.Identity.PrincipalID, "the system-assigned identity keeps its principal")
	require.Contains(t, updated.Identity.UserAssignedIdentities, *uai.ID)
	assert.Equal(t, *uai.Properties.PrincipalID, *updated.Identity.UserAssignedIdentities[*uai.ID].PrincipalID)
	assert.Equal(t, *uai.Properties.ClientID, *updated.Identity.UserAssignedIdentities[*uai.ID].ClientID)
	api := ref("API")
	assert.Equal(t, armappservice.ResolveStatusResolved, *api.Status)
	assert.Equal(t, armappservice.ManagedServiceIdentityTypeUserAssigned, *api.IdentityType.Type)
	assert.Contains(t, api.IdentityType.UserAssignedIdentities, *uai.ID)
	assert.Equal(t, armappservice.ResolveStatusAccessToKeyVaultDenied, *ref("DB").Status)
	_, err = client.Restart(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.Equal(t, "from-the-policy-vault", siteEnv(t, name, "API"))
	assert.Equal(t, dbRef, siteEnv(t, name, "DB"))

	// Removing the identity leaves every reference unresolvable.
	_, err = client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Identity:   &armappservice.ManagedServiceIdentity{Type: to.Ptr(armappservice.ManagedServiceIdentityTypeNone)},
		Properties: &armappservice.SitePatchResourceProperties{KeyVaultReferenceIdentity: to.Ptr("SystemAssigned")},
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, armappservice.ResolveStatusMSINotEnabled, *ref("DB").Status)
	assert.Equal(t, armappservice.ResolveStatusMSINotEnabled, *ref("API").Status)
}
