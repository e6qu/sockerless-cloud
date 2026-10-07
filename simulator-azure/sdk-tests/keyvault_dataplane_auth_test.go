package azure_sdk_test

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azkeys"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for how the Key Vault data plane authenticates and authorizes
// a request: the Bearer challenge and the token checks behind 401, the
// access-policy and Azure RBAC permission models and the network rules
// behind 403.

func createAuthTestVault(t *testing.T, rg, vault string, props armkeyvault.VaultProperties) *armkeyvault.VaultsClient {
	t.Helper()
	ensureRG(t, rg)
	vaults, err := armkeyvault.NewVaultsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	props.TenantID = to.Ptr(simTenantID)
	props.SKU = &armkeyvault.SKU{Family: to.Ptr(armkeyvault.SKUFamilyA), Name: to.Ptr(armkeyvault.SKUNameStandard)}
	if props.AccessPolicies == nil {
		props.AccessPolicies = []*armkeyvault.AccessPolicyEntry{}
	}
	poller, err := vaults.BeginCreateOrUpdate(ctx, rg, vault, armkeyvault.VaultCreateOrUpdateParameters{
		Location:   to.Ptr("eastus"),
		Properties: &props,
	}, nil)
	require.NoError(t, err)
	_, err = poller.PollUntilDone(ctx, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = vaults.Delete(ctx, rg, vault, nil) })
	return vaults
}

func kvSecretsClient(t *testing.T, vault string) *azsecrets.Client {
	t.Helper()
	client, err := azsecrets.NewClient(kvVaultURL(vault), &fakeCredential{}, &azsecrets.ClientOptions{
		ClientOptions:                        kvClientOptions(),
		DisableChallengeResourceVerification: true,
	})
	require.NoError(t, err)
	return client
}

// requireKVForbidden asserts err is Key Vault's 403 Forbidden carrying the
// inner error code and a message containing want.
func requireKVForbidden(t *testing.T, err error, inner, want string) {
	t.Helper()
	require.Error(t, err)
	var respErr *azcore.ResponseError
	require.True(t, errors.As(err, &respErr), "want *azcore.ResponseError, got %T: %v", err, err)
	assert.Equal(t, http.StatusForbidden, respErr.StatusCode)
	assert.Equal(t, "Forbidden", respErr.ErrorCode)
	body, readErr := io.ReadAll(respErr.RawResponse.Body)
	require.NoError(t, readErr)
	var envelope struct {
		Error struct {
			Message    string `json:"message"`
			InnerError struct {
				Code string `json:"code"`
			} `json:"innererror"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope), "%s", body)
	assert.Equal(t, inner, envelope.Error.InnerError.Code, "%s", body)
	assert.Contains(t, envelope.Error.Message, want)
	assert.Contains(t, envelope.Error.Message, "oid="+simCallerObjectID, "the refusal names the caller")
}

func kvDataPlaneRequest(t *testing.T, vault, bearer string) (*http.Response, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/secrets?api-version=7.6", nil)
	require.NoError(t, err)
	req.Host = kvDataPlaneHost(vault)
	if bearer != "" {
		req.Header.Set("Authorization", bearer)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	var body map[string]any
	raw, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &body), "%s", raw)
	return resp, body
}

func kvErrorMessage(body map[string]any) (string, string) {
	e, _ := body["error"].(map[string]any)
	code, _ := e["code"].(string)
	message, _ := e["message"].(string)
	return code, message
}

// TestKeyVault_AuthChallenge covers the 401s: a request without a token gets
// the Bearer challenge naming the vault's tenant and the Key Vault resource,
// and a token for another resource or from another tenant is refused with the
// same challenge.
func TestKeyVault_AuthChallenge(t *testing.T) {
	vault := uniqueAlnumName("challengevault")
	createAuthTestVault(t, "kv-challenge-rg", vault, armkeyvault.VaultProperties{
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(simCallerObjectID),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsList)}},
		}},
	})

	resp, body := kvDataPlaneRequest(t, vault, "")
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	challenge := resp.Header.Get("WWW-Authenticate")
	assert.True(t, strings.HasPrefix(challenge, "Bearer "), challenge)
	assert.Contains(t, challenge, "/"+simTenantID+`"`, "the challenge names the vault's tenant")
	assert.Contains(t, challenge, `resource="https://vault.azure.net"`)
	code, message := kvErrorMessage(body)
	assert.Equal(t, "Unauthorized", code)
	assert.Contains(t, message, "AKV10000")

	resp, body = kvDataPlaneRequest(t, vault, simARMBearer)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a token for Azure Resource Manager is not a Key Vault token")
	assert.NotEmpty(t, resp.Header.Get("WWW-Authenticate"))
	_, message = kvErrorMessage(body)
	assert.Contains(t, message, "AKV10022")

	otherTenant := "22222222-3333-4444-5555-666666666666"
	foreign, _, err := fetchSimAccessTokenInTenant(otherTenant, "https://vault.azure.net/.default")
	require.NoError(t, err)
	resp, body = kvDataPlaneRequest(t, vault, "Bearer "+foreign)
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "a token another tenant issued is not accepted")
	_, message = kvErrorMessage(body)
	assert.Contains(t, message, "AKV10032")
	assert.Contains(t, message, "https://sts.windows.net/"+simTenantID+"/")

	resp, _ = kvDataPlaneRequest(t, vault, "Bearer not-a-token")
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)

	resp, body = kvDataPlaneRequest(t, vault, simKVBearer)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "%v", body)

	// A client of the cloud /metadata/endpoints describes acquires its Key
	// Vault tokens for https://<keyVaultDns>, which the vault accepts too.
	metaResp, err := http.Get(baseURL + "/metadata/endpoints?api-version=2022-09-01")
	require.NoError(t, err)
	defer metaResp.Body.Close()
	var meta struct {
		Suffixes struct {
			KeyVaultDNS string `json:"keyVaultDns"`
		} `json:"suffixes"`
	}
	require.NoError(t, json.NewDecoder(metaResp.Body).Decode(&meta))
	assert.True(t, strings.HasPrefix(meta.Suffixes.KeyVaultDNS, "vault."), "keyVaultDns names the vault label, as vault.azure.net does: %q", meta.Suffixes.KeyVaultDNS)
	cloudToken, _, err := fetchSimAccessToken("https://" + meta.Suffixes.KeyVaultDNS + "/.default")
	require.NoError(t, err)
	resp, body = kvDataPlaneRequest(t, vault, "Bearer "+cloudToken)
	assert.Equal(t, http.StatusOK, resp.StatusCode, "%v", body)
}

// TestKeyVault_AccessPoliciesAuthorizeEachOperation covers a vault that uses
// access policies: each operation needs its own permission in the policy for
// the caller's object, and a compound identity policy applies only to tokens
// its application obtained.
func TestKeyVault_AccessPoliciesAuthorizeEachOperation(t *testing.T) {
	rg := "kv-policy-auth-rg"
	vault := uniqueAlnumName("kvpolicyauth")
	policy := func(secrets ...armkeyvault.SecretPermissions) *armkeyvault.AccessPolicyEntry {
		perms := make([]*armkeyvault.SecretPermissions, 0, len(secrets))
		for i := range secrets {
			perms = append(perms, &secrets[i])
		}
		return &armkeyvault.AccessPolicyEntry{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(simCallerObjectID),
			Permissions: &armkeyvault.Permissions{Secrets: perms},
		}
	}
	vaults := createAuthTestVault(t, rg, vault, armkeyvault.VaultProperties{
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{policy(armkeyvault.SecretPermissionsList)},
	})
	secrets := kvSecretsClient(t, vault)

	_, err := secrets.SetSecret(ctx, "policy-secret", azsecrets.SetSecretParameters{Value: to.Ptr("v1")}, nil)
	requireKVForbidden(t, err, "AccessDenied", "does not have secrets set permission on key vault '"+vault+";location=eastus'")
	pager := secrets.NewListSecretPropertiesPager(nil)
	_, err = pager.NextPage(ctx)
	require.NoError(t, err, "the list permission lists")

	_, err = vaults.UpdateAccessPolicy(ctx, rg, vault, armkeyvault.AccessPolicyUpdateKindReplace, armkeyvault.VaultAccessPolicyParameters{
		Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{
			policy(armkeyvault.SecretPermissionsSet, armkeyvault.SecretPermissionsGet),
		}},
	}, nil)
	require.NoError(t, err)
	_, err = secrets.SetSecret(ctx, "policy-secret", azsecrets.SetSecretParameters{Value: to.Ptr("v1")}, nil)
	require.NoError(t, err)
	got, err := secrets.GetSecret(ctx, "policy-secret", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "v1", *got.Value)
	_, err = secrets.DeleteSecret(ctx, "policy-secret", nil)
	requireKVForbidden(t, err, "AccessDenied", "does not have secrets delete permission")
	pager = secrets.NewListSecretPropertiesPager(nil)
	_, err = pager.NextPage(ctx)
	requireKVForbidden(t, err, "AccessDenied", "does not have secrets list permission")

	keys, err := azkeys.NewClient(kvVaultURL(vault), &fakeCredential{}, &azkeys.ClientOptions{
		ClientOptions:                        kvClientOptions(),
		DisableChallengeResourceVerification: true,
	})
	require.NoError(t, err)
	_, err = keys.CreateKey(ctx, "policy-key", azkeys.CreateKeyParameters{Kty: to.Ptr(azkeys.KeyTypeRSA)}, nil)
	requireKVForbidden(t, err, "AccessDenied", "does not have keys create permission")

	compound := policy(armkeyvault.SecretPermissionsGet)
	compound.ApplicationID = to.Ptr(uuid.NewString())
	_, err = vaults.UpdateAccessPolicy(ctx, rg, vault, armkeyvault.AccessPolicyUpdateKindReplace, armkeyvault.VaultAccessPolicyParameters{
		Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{compound}},
	}, nil)
	require.NoError(t, err)
	_, err = secrets.GetSecret(ctx, "policy-secret", "", nil)
	requireKVForbidden(t, err, "AccessDenied", "does not have secrets get permission")

	// A policy naming a group grants its members, through nested groups too.
	inner := createGraphGroup(t, "kv-policy-inner-"+vault)
	outer := createGraphGroup(t, "kv-policy-outer-"+vault)
	addGroupMember(t, inner.ID, simCallerObjectID)
	addGroupMember(t, outer.ID, inner.ID)
	groupPolicy := policy(armkeyvault.SecretPermissionsGet)
	groupPolicy.ObjectID = to.Ptr(outer.ID)
	_, err = vaults.UpdateAccessPolicy(ctx, rg, vault, armkeyvault.AccessPolicyUpdateKindReplace, armkeyvault.VaultAccessPolicyParameters{
		Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{groupPolicy}},
	}, nil)
	require.NoError(t, err)
	got, err = secrets.GetSecret(ctx, "policy-secret", "", nil)
	require.NoError(t, err, "a member of a nested group the policy names reads the secret")
	assert.Equal(t, "v1", *got.Value)
}

// TestKeyVault_RBACAuthorizesDataActions covers a vault that uses Azure RBAC:
// its access policies grant nothing, a role grants the data actions its
// dataActions match less its notDataActions, and an assignment at one secret
// grants that secret alone.
func TestKeyVault_RBACAuthorizesDataActions(t *testing.T) {
	rg := "kv-rbac-auth-rg"
	vault := uniqueAlnumName("kvrbacauth")
	createAuthTestVault(t, rg, vault, armkeyvault.VaultProperties{
		EnableRbacAuthorization: to.Ptr(true),
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(simCallerObjectID),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsAll)}},
		}},
	})
	vaultID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.KeyVault/vaults/" + vault
	secrets := kvSecretsClient(t, vault)

	_, err := secrets.SetSecret(ctx, "alpha", azsecrets.SetSecretParameters{Value: to.Ptr("a")}, nil)
	requireKVForbidden(t, err, "ForbiddenByRbac", "Action: 'Microsoft.KeyVault/vaults/secrets/setSecret/action'")

	customRole := func(name string, dataActions, notDataActions []string) string {
		defs, err := armauthorization.NewRoleDefinitionsClient(&fakeCredential{}, clientOpts())
		require.NoError(t, err)
		id := uuid.NewString()
		scope := "/subscriptions/" + subscriptionID
		_, err = defs.CreateOrUpdate(ctx, scope, id, armauthorization.RoleDefinition{
			Properties: &armauthorization.RoleDefinitionProperties{
				RoleName: to.Ptr(name),
				RoleType: to.Ptr("CustomRole"),
				Permissions: []*armauthorization.Permission{{
					DataActions:    to.SliceOfPtrs(dataActions...),
					NotDataActions: to.SliceOfPtrs(notDataActions...),
				}},
				AssignableScopes: []*string{to.Ptr(scope)},
			},
		}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = defs.Delete(ctx, scope, id, nil) })
		return id
	}
	allButDelete := customRole("Secrets all but delete "+vault,
		[]string{"Microsoft.KeyVault/vaults/secrets/*"}, []string{"Microsoft.KeyVault/vaults/secrets/delete"})
	grantRole(t, vaultID, allButDelete, simCallerObjectID)
	_, err = secrets.SetSecret(ctx, "alpha", azsecrets.SetSecretParameters{Value: to.Ptr("a")}, nil)
	require.NoError(t, err)
	got, err := secrets.GetSecret(ctx, "alpha", "", nil)
	require.NoError(t, err)
	assert.Equal(t, "a", *got.Value)
	_, err = secrets.DeleteSecret(ctx, "alpha", nil)
	requireKVForbidden(t, err, "ForbiddenByRbac", "Action: 'Microsoft.KeyVault/vaults/secrets/delete'")

	scoped := uniqueAlnumName("kvrbacscope")
	createAuthTestVault(t, rg, scoped, armkeyvault.VaultProperties{EnableRbacAuthorization: to.Ptr(true)})
	scopedID := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.KeyVault/vaults/" + scoped
	setter := customRole("Secret setter "+scoped, []string{"Microsoft.KeyVault/vaults/secrets/setSecret/action"}, nil)
	grantRole(t, scopedID, setter, simCallerObjectID)
	scopedSecrets := kvSecretsClient(t, scoped)
	for _, name := range []string{"alpha", "beta"} {
		_, err = scopedSecrets.SetSecret(ctx, name, azsecrets.SetSecretParameters{Value: to.Ptr(name)}, nil)
		require.NoError(t, err)
	}
	_, err = scopedSecrets.GetSecret(ctx, "alpha", "", nil)
	requireKVForbidden(t, err, "ForbiddenByRbac", "Action: 'Microsoft.KeyVault/vaults/secrets/getSecret/action'")
	grantRole(t, scopedID+"/secrets/alpha", kvSecretsUserRole, simCallerObjectID)
	got, err = scopedSecrets.GetSecret(ctx, "alpha", "", nil)
	require.NoError(t, err, "an assignment at the secret grants that secret")
	assert.Equal(t, "alpha", *got.Value)
	_, err = scopedSecrets.GetSecret(ctx, "beta", "", nil)
	requireKVForbidden(t, err, "ForbiddenByRbac", "/secrets/beta")
}

// TestKeyVault_NetworkRulesRefuseOutsideAddresses covers the vault firewall: a
// Deny default action refuses an address no IP rule names, and disabled public
// network access refuses every public request.
func TestKeyVault_NetworkRulesRefuseOutsideAddresses(t *testing.T) {
	rg := "kv-network-auth-rg"
	vault := uniqueAlnumName("kvnetauth")
	vaults := createAuthTestVault(t, rg, vault, armkeyvault.VaultProperties{
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(simCallerObjectID),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsAll)}},
		}},
		NetworkACLs: &armkeyvault.NetworkRuleSet{
			DefaultAction: to.Ptr(armkeyvault.NetworkRuleActionDeny),
			Bypass:        to.Ptr(armkeyvault.NetworkRuleBypassOptionsAzureServices),
		},
	})
	secrets := kvSecretsClient(t, vault)

	conn, err := net.Dial("tcp", strings.TrimPrefix(baseURL, "http://"))
	require.NoError(t, err)
	clientAddress := conn.LocalAddr().(*net.TCPAddr).IP.String()
	require.NoError(t, conn.Close())

	_, err = secrets.SetSecret(ctx, "net-secret", azsecrets.SetSecretParameters{Value: to.Ptr("v")}, nil)
	requireKVForbidden(t, err, "ForbiddenByFirewall", "Client address: "+clientAddress)

	_, err = vaults.Update(ctx, rg, vault, armkeyvault.VaultPatchParameters{
		Properties: &armkeyvault.VaultPatchProperties{NetworkACLs: &armkeyvault.NetworkRuleSet{
			DefaultAction: to.Ptr(armkeyvault.NetworkRuleActionDeny),
			Bypass:        to.Ptr(armkeyvault.NetworkRuleBypassOptionsAzureServices),
			IPRules:       []*armkeyvault.IPRule{{Value: to.Ptr(clientAddress + "/32")}},
		}},
	}, nil)
	require.NoError(t, err)
	_, err = secrets.SetSecret(ctx, "net-secret", azsecrets.SetSecretParameters{Value: to.Ptr("v")}, nil)
	require.NoError(t, err, "an IP rule naming the client's address admits it")

	updated, err := vaults.Update(ctx, rg, vault, armkeyvault.VaultPatchParameters{
		Properties: &armkeyvault.VaultPatchProperties{PublicNetworkAccess: to.Ptr("Disabled")},
	}, nil)
	require.NoError(t, err)
	require.NotNil(t, updated.Properties.PublicNetworkAccess)
	assert.Equal(t, "Disabled", *updated.Properties.PublicNetworkAccess)
	_, err = secrets.GetSecret(ctx, "net-secret", "", nil)
	requireKVForbidden(t, err, "ForbiddenByConnection", "Public network access is disabled")
}
