package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIdentity_CreateUserAssigned(t *testing.T) {
	// Ensure resource group
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "identity-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	resp, err := client.CreateOrUpdate(ctx, "identity-rg", "test-identity", armmsi.Identity{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	assert.Equal(t, "test-identity", *resp.Name)
	assert.NotEmpty(t, *resp.Properties.PrincipalID)
	assert.NotEmpty(t, *resp.Properties.ClientID)
	// A managed identity lives in a real tenant; the all-zero GUID is invalid,
	// so the sim must surface a stable non-zero tenantId.
	require.NotNil(t, resp.Properties.TenantID)
	assert.NotEqual(t, "00000000-0000-0000-0000-000000000000", *resp.Properties.TenantID,
		"managed identity tenantId must be a real (non-zero) GUID")
}

func TestIdentity_GetUserAssigned(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "get-id-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, "get-id-rg", "get-identity", armmsi.Identity{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	resp, err := client.Get(ctx, "get-id-rg", "get-identity", nil)
	require.NoError(t, err)
	assert.Equal(t, "get-identity", *resp.Name)
}

func TestIdentity_DeleteUserAssigned(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "del-id-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)

	_, err = client.CreateOrUpdate(ctx, "del-id-rg", "del-identity", armmsi.Identity{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	_, err = client.Delete(ctx, "del-id-rg", "del-identity", nil)
	require.NoError(t, err)
}

// TestIdentity_PrincipalResolvesAsServicePrincipal verifies that creating a
// user-assigned managed identity also materializes a directory service
// principal whose id equals the identity's principalId — so
// GET /v1.0/servicePrincipals/{principalId} resolves it with
// servicePrincipalType "ManagedIdentity", exactly as real Azure does.
func TestIdentity_PrincipalResolvesAsServicePrincipal(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "sp-id-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := client.CreateOrUpdate(ctx, "sp-id-rg", "sp-identity", armmsi.Identity{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	principalID := *resp.Properties.PrincipalID
	clientID := *resp.Properties.ClientID

	spResp, err := http.Get(baseURL + "/v1.0/servicePrincipals/" + principalID)
	require.NoError(t, err)
	defer spResp.Body.Close()
	require.Equal(t, http.StatusOK, spResp.StatusCode,
		"the MI principalId must resolve as a servicePrincipal")
	var sp struct {
		ID                   string `json:"id"`
		AppID                string `json:"appId"`
		ServicePrincipalType string `json:"servicePrincipalType"`
	}
	require.NoError(t, json.NewDecoder(spResp.Body).Decode(&sp))
	assert.Equal(t, principalID, sp.ID)
	assert.Equal(t, clientID, sp.AppID)
	assert.Equal(t, "ManagedIdentity", sp.ServicePrincipalType)
}

// TestIdentity_DeletedPrincipalUnregistersServicePrincipal verifies the
// directory SP is removed when the backing managed identity is deleted.
func TestIdentity_DeletedPrincipalUnregistersServicePrincipal(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "sp-del-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	client, err := armmsi.NewUserAssignedIdentitiesClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	resp, err := client.CreateOrUpdate(ctx, "sp-del-rg", "sp-del-identity", armmsi.Identity{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)
	principalID := *resp.Properties.PrincipalID

	_, err = client.Delete(ctx, "sp-del-rg", "sp-del-identity", nil)
	require.NoError(t, err)

	spResp, err := http.Get(baseURL + "/v1.0/servicePrincipals/" + principalID)
	require.NoError(t, err)
	defer spResp.Body.Close()
	assert.Equal(t, http.StatusNotFound, spResp.StatusCode,
		"deleting the MI must remove its directory service principal")
}

// TestIdentity_CreateOrUpdateStatusCodes verifies the ARM PUT createOrUpdate
// status-code contract: 201 Created for a brand-new identity, 200 OK when the
// same resource is updated. The armmsi SDK hides the status code, so this
// drives the endpoint over raw HTTP.
func TestIdentity_CreateOrUpdateStatusCodes(t *testing.T) {
	rgClient, err := armresources.NewResourceGroupsClient(subscriptionID, &fakeCredential{}, clientOpts())
	require.NoError(t, err)
	_, err = rgClient.CreateOrUpdate(ctx, "id-status-rg", armresources.ResourceGroup{
		Location: ptrStr("eastus"),
	}, nil)
	require.NoError(t, err)

	url := baseURL + "/subscriptions/" + subscriptionID +
		"/resourceGroups/id-status-rg/providers/Microsoft.ManagedIdentity/userAssignedIdentities/status-identity?api-version=2023-01-31"
	put := func() int {
		req, _ := http.NewRequestWithContext(ctx, "PUT", url, strings.NewReader(`{"location":"eastus"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", simARMBearer)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}

	assert.Equal(t, http.StatusCreated, put(), "first PUT of a new identity must return 201 Created")
	assert.Equal(t, http.StatusOK, put(), "second PUT of the existing identity must return 200 OK")
}
