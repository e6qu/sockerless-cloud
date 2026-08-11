package azure_cli_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStorageARMManagementCLI drives the Microsoft.Storage management-plane
// catalog + account-action operations through `az rest`, the same ARM wire path
// a real `az storage account` invocation produces. The simulator's storage
// account create + the regenerateKey / listKeys / checkNameAvailability / skus /
// operations endpoints are exercised end to end.
func TestStorageARMManagementCLI(t *testing.T) {
	const apiVersion = "2024-01-01"
	account := "clistoragearm"

	// Create the storage account.
	acctURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s?api-version=%s",
		baseURL, subscriptionID, resourceGroup, account, apiVersion)
	createBody := `{"location":"eastus","kind":"StorageV2","sku":{"name":"Standard_LRS"}}`
	runCLI(t, azRest("PUT", acctURL, createBody))
	t.Cleanup(func() { _ = azRest("DELETE", acctURL, "").Run() })

	// Operations_List.
	opsOut := runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.Storage/operations?api-version="+apiVersion, ""))
	var ops struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, opsOut, &ops)
	require.NotEmpty(t, ops.Value)

	// Skus_List.
	skusURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Storage/skus?api-version=%s", baseURL, subscriptionID, apiVersion)
	skusOut := runCLI(t, azRest("GET", skusURL, ""))
	var skus struct {
		Value []struct {
			Name string `json:"name"`
		} `json:"value"`
	}
	parseJSON(t, skusOut, &skus)
	require.NotEmpty(t, skus.Value)

	// CheckNameAvailability.
	checkURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Storage/checkNameAvailability?api-version=%s", baseURL, subscriptionID, apiVersion)
	checkOut := runCLI(t, azRest("POST", checkURL, `{"name":"freshuniquename999","type":"Microsoft.Storage/storageAccounts"}`))
	var check struct {
		NameAvailable bool `json:"nameAvailable"`
	}
	parseJSON(t, checkOut, &check)
	assert.True(t, check.NameAvailable)

	// regenerateKey.
	regenURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/regenerateKey?api-version=%s",
		baseURL, subscriptionID, resourceGroup, account, apiVersion)
	regenOut := runCLI(t, azRest("POST", regenURL, `{"keyName":"key1"}`))
	var regen struct {
		Keys []struct {
			KeyName     string `json:"keyName"`
			Permissions string `json:"permissions"`
		} `json:"keys"`
	}
	parseJSON(t, regenOut, &regen)
	require.NotEmpty(t, regen.Keys)
	assert.Equal(t, "Full", regen.Keys[0].Permissions)

	// ListAccountSas.
	sasURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s/ListAccountSas?api-version=%s",
		baseURL, subscriptionID, resourceGroup, account, apiVersion)
	sasOut := runCLI(t, azRest("POST", sasURL, `{"signedServices":"b","signedResourceTypes":"s","signedPermission":"r","signedExpiry":"2030-01-01T00:00:00Z"}`))
	var sas struct {
		AccountSasToken string `json:"accountSasToken"`
	}
	parseJSON(t, sasOut, &sas)
	require.NotEmpty(t, sas.AccountSasToken)
}
