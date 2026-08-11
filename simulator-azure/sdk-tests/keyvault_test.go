package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestKeyVault_ARM_CRUD pins the ARM control plane: vault create, get,
// list, delete. Real Azure: PUT / GET / DELETE on
// `Microsoft.KeyVault/vaults/{name}`.
func TestKeyVault_ARM_CRUD(t *testing.T) {
	rg := "kv-rg"
	ensureRG(t, rg)

	vaultName := "sdk-test-vault"
	defer func() {
		req, _ := http.NewRequest("DELETE",
			baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.KeyVault/vaults/"+vaultName+"?api-version=2024-04-01-preview",
			nil)
		req.Header.Set("Authorization", simARMBearer)
		http.DefaultClient.Do(req)
	}()

	body := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"tenantId": "00000000-0000-0000-0000-000000000000",
			"sku": map[string]any{
				"family": "A",
				"name":   "standard",
			},
			"enableRbacAuthorization": true,
		},
	}
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest("PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.KeyVault/vaults/"+vaultName+"?api-version=2024-04-01-preview",
		strings.NewReader(string(data)))
	req.Header.Set("Authorization", simARMBearer)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	respBody, _ := io.ReadAll(resp.Body)
	var vault map[string]any
	require.NoError(t, json.Unmarshal(respBody, &vault))
	props := vault["properties"].(map[string]any)
	vaultURI, _ := props["vaultUri"].(string)
	require.NotEmpty(t, vaultURI, "vaultUri must be set on create")
	assert.Contains(t, vaultURI, ".vault.", "vaultUri should follow real-Azure subdomain format")
	assert.Equal(t, "Succeeded", props["provisioningState"])

	// GET round-trips.
	getReq, _ := http.NewRequest("GET",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.KeyVault/vaults/"+vaultName+"?api-version=2024-04-01-preview",
		nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)
}

func TestKeyVault_ARMPatchAccessPolicyAdvertisedEndpointAndDeletedVault(t *testing.T) {
	rg := "kv-patch-rg"
	ensureRG(t, rg)
	vaultName := "sdkpatchvault"
	vaultURL := baseURL + "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.KeyVault/vaults/" + vaultName + "?api-version=2024-11-01"

	createBody, _ := json.Marshal(map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"tenantId": "00000000-0000-0000-0000-000000000000",
			"sku": map[string]any{
				"family": "A",
				"name":   "standard",
			},
		},
	})
	createReq, _ := http.NewRequest("PUT", vaultURL, strings.NewReader(string(createBody)))
	createReq.Header.Set("Authorization", simARMBearer)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()
	require.Equal(t, http.StatusOK, createResp.StatusCode)

	patchBody := `{"tags":{"env":"sdk"},"properties":{"enableRbacAuthorization":true}}`
	patchReq, _ := http.NewRequest("PATCH", vaultURL, strings.NewReader(patchBody))
	patchReq.Header.Set("Authorization", simARMBearer)
	patchReq.Header.Set("Content-Type", "application/json")
	patchResp, err := http.DefaultClient.Do(patchReq)
	require.NoError(t, err)
	defer patchResp.Body.Close()
	require.Equal(t, http.StatusOK, patchResp.StatusCode)
	var patched map[string]any
	require.NoError(t, json.NewDecoder(patchResp.Body).Decode(&patched))
	props := patched["properties"].(map[string]any)
	expectedVaultURI := fmt.Sprintf("https://%s.vault.shim.localhost:%s/", vaultName, strings.TrimPrefix(baseURL, "http://127.0.0.1:"))
	assert.Equal(t, expectedVaultURI, props["vaultUri"])
	assert.Equal(t, true, props["enableRbacAuthorization"])

	accessPolicyBody := `{"properties":{"accessPolicies":[{"tenantId":"00000000-0000-0000-0000-000000000000","objectId":"11111111-1111-1111-1111-111111111111","permissions":{"secrets":["get","list"]}}]}}`
	apReq, _ := http.NewRequest("PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.KeyVault/vaults/"+vaultName+"/accessPolicies/add?api-version=2024-11-01",
		strings.NewReader(accessPolicyBody))
	apReq.Header.Set("Authorization", simARMBearer)
	apReq.Header.Set("Content-Type", "application/json")
	apResp, err := http.DefaultClient.Do(apReq)
	require.NoError(t, err)
	defer apResp.Body.Close()
	require.Equal(t, http.StatusOK, apResp.StatusCode)
	var ap map[string]any
	require.NoError(t, json.NewDecoder(apResp.Body).Decode(&ap))
	apProps := ap["properties"].(map[string]any)
	require.Len(t, apProps["accessPolicies"].([]any), 1)

	deleteReq, _ := http.NewRequest("DELETE", vaultURL, nil)
	deleteReq.Header.Set("Authorization", simARMBearer)
	deleteResp, err := http.DefaultClient.Do(deleteReq)
	require.NoError(t, err)
	deleteResp.Body.Close()
	require.Equal(t, http.StatusOK, deleteResp.StatusCode)

	deletedReq, _ := http.NewRequest("GET",
		baseURL+"/subscriptions/"+subscriptionID+"/providers/Microsoft.KeyVault/locations/eastus/deletedVaults/"+vaultName+"?api-version=2024-11-01",
		nil)
	deletedReq.Header.Set("Authorization", simARMBearer)
	deletedResp, err := http.DefaultClient.Do(deletedReq)
	require.NoError(t, err)
	defer deletedResp.Body.Close()
	require.Equal(t, http.StatusOK, deletedResp.StatusCode)
	var deleted map[string]any
	require.NoError(t, json.NewDecoder(deletedResp.Body).Decode(&deleted))
	assert.Equal(t, vaultName, deleted["name"])

	purgeReq, _ := http.NewRequest("POST",
		baseURL+"/subscriptions/"+subscriptionID+"/providers/Microsoft.KeyVault/locations/eastus/deletedVaults/"+vaultName+"/purge?api-version=2024-11-01",
		nil)
	purgeReq.Header.Set("Authorization", simARMBearer)
	purgeResp, err := http.DefaultClient.Do(purgeReq)
	require.NoError(t, err)
	purgeResp.Body.Close()
	require.Equal(t, http.StatusAccepted, purgeResp.StatusCode)
	require.NotEmpty(t, purgeResp.Header.Get("Location"))
	// GET /subscriptions/{subscriptionId}/providers/Microsoft.KeyVault/locations/{location}/deletedVaults/{name}/purge/operation
	// is the documented Location-based long-running-operation completion route.
	purgePollReq, _ := http.NewRequest("GET", purgeResp.Header.Get("Location"), nil)
	purgePollReq.Header.Set("Authorization", simARMBearer)
	purgePollResp, err := http.DefaultClient.Do(purgePollReq)
	require.NoError(t, err)
	purgePollResp.Body.Close()
	require.Equal(t, http.StatusOK, purgePollResp.StatusCode)
}

// TestKeyVault_DataPlane_SetGetDelete pins the secret data plane:
// PUT /secrets/{name} → GET → DELETE round-trip with the Host header
// set to `<vault>.vault.<sim-host>` matching real-Azure subdomain
// routing.
func TestKeyVault_DataPlane_SetGetDelete(t *testing.T) {
	rg := "kv-data-rg"
	ensureRG(t, rg)
	vaultName := "data-vault"
	defer func() {
		req, _ := http.NewRequest("DELETE",
			baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.KeyVault/vaults/"+vaultName+"?api-version=2024-04-01-preview",
			nil)
		req.Header.Set("Authorization", simARMBearer)
		http.DefaultClient.Do(req)
	}()

	createBody, _ := json.Marshal(map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"tenantId": "00000000-0000-0000-0000-000000000000",
		},
	})
	createReq, _ := http.NewRequest("PUT",
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.KeyVault/vaults/"+vaultName+"?api-version=2024-04-01-preview",
		strings.NewReader(string(createBody)))
	createReq.Header.Set("Authorization", simARMBearer)
	createReq.Header.Set("Content-Type", "application/json")
	createResp, err := http.DefaultClient.Do(createReq)
	require.NoError(t, err)
	createResp.Body.Close()

	// Data plane URL = baseURL but Host header matches `<vault>.vault.<sim-host>`.
	host := strings.TrimPrefix(baseURL, "http://")
	dataPlaneHost := vaultName + ".vault." + host

	// SET secret.
	setBody, _ := json.Marshal(map[string]any{
		"value": "hunter2",
		"tags":  map[string]string{"env": "prod"},
	})
	setReq, _ := http.NewRequest("PUT",
		baseURL+"/secrets/db-password?api-version=7.4",
		strings.NewReader(string(setBody)))
	setReq.Header.Set("Authorization", simARMBearer)
	setReq.Header.Set("Content-Type", "application/json")
	setReq.Host = dataPlaneHost
	setResp, err := http.DefaultClient.Do(setReq)
	require.NoError(t, err)
	defer setResp.Body.Close()
	require.Equal(t, http.StatusOK, setResp.StatusCode, "SetSecret must succeed")

	// GET secret back.
	getReq, _ := http.NewRequest("GET",
		baseURL+"/secrets/db-password?api-version=7.4", nil)
	getReq.Header.Set("Authorization", simARMBearer)
	getReq.Host = dataPlaneHost
	getResp, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	defer getResp.Body.Close()
	require.Equal(t, http.StatusOK, getResp.StatusCode)
	body, _ := io.ReadAll(getResp.Body)
	var secret map[string]any
	require.NoError(t, json.Unmarshal(body, &secret))
	assert.Equal(t, "hunter2", secret["value"])

	// DELETE secret.
	delReq, _ := http.NewRequest("DELETE",
		baseURL+"/secrets/db-password?api-version=7.4", nil)
	delReq.Header.Set("Authorization", simARMBearer)
	delReq.Host = dataPlaneHost
	delResp, err := http.DefaultClient.Do(delReq)
	require.NoError(t, err)
	delResp.Body.Close()
	require.Equal(t, http.StatusOK, delResp.StatusCode)

	// Subsequent GET must 404.
	gone, err := http.DefaultClient.Do(getReq)
	require.NoError(t, err)
	gone.Body.Close()
	assert.Equal(t, http.StatusNotFound, gone.StatusCode, "GET after delete must 404")
}
