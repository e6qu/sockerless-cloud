package azure_sdk_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/to"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/appservice/armappservice/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/security/keyvault/azsecrets"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// SDK coverage for the managed identity endpoint of an App Service app: the
// app's code asks IDENTITY_ENDPOINT for a token, presenting IDENTITY_HEADER,
// and gets one for its own system-assigned identity or for the user-assigned
// identity it names — never for an identity it does not have.
//
//	GET {IDENTITY_ENDPOINT}?api-version=2019-08-01&resource={resource}[&client_id|principal_id|object_id|mi_res_id]
//	  IDENTITY_ENDPOINT is http://<platform host>/msi/token

type appIdentityToken struct {
	status  int
	body    map[string]any
	claims  map[string]any
	message string
}

// appIdentityTokenFor asks the app's workload to request a token through its
// identity endpoint with the given query.
func appIdentityTokenFor(t *testing.T, app string, query url.Values) appIdentityToken {
	t.Helper()
	status, raw := siteGet(t, app, "/identity-token?"+query.Encode())
	out := appIdentityToken{status: status}
	if status == http.StatusNotFound {
		out.message = raw
		return out
	}
	require.NoError(t, json.Unmarshal([]byte(raw), &out.body), "%s", raw)
	if token, ok := out.body["access_token"].(string); ok {
		out.claims = decodeJWTClaims(t, token)
	}
	out.message, _ = out.body["message"].(string)
	return out
}

func TestSDK_WebApps_IdentityEndpointMintsTheAppsOwnIdentity(t *testing.T) {
	rg, name := "sdk-identity-endpoint-rg", "sdk-identity-endpoint-app"
	client := createFilesWebApp(t, rg, name, nil)

	identities, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	createIdentity := func(n string) armmsi.Identity {
		resp, err := identities.CreateOrUpdate(ctx, rg, n, armmsi.Identity{Location: to.Ptr("eastus")}, nil)
		require.NoError(t, err)
		t.Cleanup(func() { _, _ = identities.Delete(ctx, rg, n, nil) })
		return resp.Identity
	}
	attached := createIdentity("sdk-identity-endpoint-attached")
	other := createIdentity("sdk-identity-endpoint-other")

	// An app without a managed identity has no identity endpoint.
	status, _ := siteGet(t, name, "/env/IDENTITY_ENDPOINT")
	assert.Equal(t, http.StatusNotFound, status, "an app without an identity has no IDENTITY_ENDPOINT")

	updated, err := client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Identity: &armappservice.ManagedServiceIdentity{
			Type:                   to.Ptr(armappservice.ManagedServiceIdentityTypeSystemAssignedUserAssigned),
			UserAssignedIdentities: map[string]*armappservice.UserAssignedIdentity{*attached.ID: {}},
		},
	}, nil)
	require.NoError(t, err)
	principal := *updated.Identity.PrincipalID
	_, err = client.Restart(ctx, rg, name, nil)
	require.NoError(t, err)
	assert.NotEmpty(t, siteEnv(t, name, "IDENTITY_ENDPOINT"))
	assert.NotEmpty(t, siteEnv(t, name, "IDENTITY_HEADER"))

	const vaultResource = "https://vault.azure.net"
	system := appIdentityTokenFor(t, name, url.Values{"resource": {vaultResource}})
	require.Equal(t, http.StatusOK, system.status, "%v", system.body)
	assert.Equal(t, principal, system.claims["oid"], "the token is the app's system-assigned identity's")
	assert.Equal(t, vaultResource, system.claims["aud"])
	assert.Equal(t, "https://sts.windows.net/"+simTenantID+"/", system.claims["iss"])
	assert.Equal(t, *updated.ID, system.claims["xms_mirid"])
	assert.Equal(t, vaultResource, system.body["resource"])
	assert.Equal(t, "Bearer", system.body["token_type"])
	assert.NotEmpty(t, system.body["expires_on"])

	for _, selector := range []url.Values{
		{"client_id": {*attached.Properties.ClientID}},
		{"principal_id": {*attached.Properties.PrincipalID}},
		{"object_id": {*attached.Properties.PrincipalID}},
		{"mi_res_id": {*attached.ID}},
	} {
		selector.Set("resource", vaultResource)
		got := appIdentityTokenFor(t, name, selector)
		require.Equal(t, http.StatusOK, got.status, "%v: %v", selector, got.body)
		assert.Equal(t, *attached.Properties.PrincipalID, got.claims["oid"], "%v", selector)
		assert.Equal(t, *attached.Properties.ClientID, got.body["client_id"], "%v", selector)
	}

	refused := appIdentityTokenFor(t, name, url.Values{"resource": {vaultResource}, "client_id": {*other.Properties.ClientID}})
	assert.Equal(t, http.StatusBadRequest, refused.status, "an identity the app does not have")
	assert.Equal(t, "Unable to load the proper Managed Identity.", refused.message)

	wrongHeader := appIdentityTokenFor(t, name, url.Values{"resource": {vaultResource}, "identity-header": {"not-this-apps-secret"}})
	assert.Equal(t, http.StatusUnauthorized, wrongHeader.status)
	noResource := appIdentityTokenFor(t, name, url.Values{})
	assert.Equal(t, http.StatusBadRequest, noResource.status)

	// The token is the app's identity wherever it is presented: a vault whose
	// access policy names the system-assigned identity serves it the secret.
	vault := uniqueAlnumName("sdkidendpoint")
	createAuthTestVault(t, rg, vault, armkeyvault.VaultProperties{
		AccessPolicies: []*armkeyvault.AccessPolicyEntry{{
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(principal),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsGet)}},
		}, {
			TenantID:    to.Ptr(simTenantID),
			ObjectID:    to.Ptr(simCallerObjectID),
			Permissions: &armkeyvault.Permissions{Secrets: []*armkeyvault.SecretPermissions{to.Ptr(armkeyvault.SecretPermissionsSet)}},
		}},
	})
	_, err = kvSecretsClient(t, vault).SetSecret(ctx, "app-secret", azsecrets.SetSecretParameters{Value: to.Ptr("for-the-app")}, nil)
	require.NoError(t, err)
	readAs := func(token string) (int, string) {
		req, err := http.NewRequest(http.MethodGet, baseURL+"/secrets/app-secret?api-version=7.6", nil)
		require.NoError(t, err)
		req.Host = kvDataPlaneHost(vault)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		return resp.StatusCode, string(body)
	}
	status, body := readAs(system.body["access_token"].(string))
	require.Equal(t, http.StatusOK, status, body)
	assert.Contains(t, body, "for-the-app")
	userToken := appIdentityTokenFor(t, name, url.Values{"resource": {vaultResource}, "client_id": {*attached.Properties.ClientID}})
	status, body = readAs(userToken.body["access_token"].(string))
	assert.Equal(t, http.StatusForbidden, status, "the vault grants the user-assigned identity nothing: %s", body)

	// Without an identity the app has no endpoint again.
	_, err = client.Update(ctx, rg, name, armappservice.SitePatchResource{
		Identity: &armappservice.ManagedServiceIdentity{Type: to.Ptr(armappservice.ManagedServiceIdentityTypeNone)},
	}, nil)
	require.NoError(t, err)
	_, err = client.Restart(ctx, rg, name, nil)
	require.NoError(t, err)
	status, _ = siteGet(t, name, "/env/IDENTITY_ENDPOINT")
	assert.Equal(t, http.StatusNotFound, status)
}
