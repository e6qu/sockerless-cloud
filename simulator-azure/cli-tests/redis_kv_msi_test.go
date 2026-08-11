package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// createCLIRedisCache creates a Redis cache via the az CLI and waits for the
// async provisioning to settle to Succeeded.
func createCLIRedisCache(t *testing.T, name string) string {
	t.Helper()
	cacheURL := armURL("Microsoft.Cache", "Redis/"+name, "2024-11-01")
	runCLI(t, azRest("PUT", cacheURL, `{
		"location":"eastus",
		"properties":{"sku":{"name":"Basic","family":"C","capacity":1}}
	}`))
	t.Cleanup(func() { _ = azRest("DELETE", cacheURL, "").Run() })
	waitForCLIJSON(t, cacheURL, func(data string) bool {
		return strings.Contains(data, `"provisioningState": "Succeeded"`)
	})
	return cacheURL
}

func TestRedisCLI_KeysRebootAndSubResources(t *testing.T) {
	name := "cliredissub"
	createCLIRedisCache(t, name)

	// regenerateKey returns the full key pair with the regenerated key changed.
	regenOut := runCLI(t, azRest("POST", armURL("Microsoft.Cache", "Redis/"+name+"/regenerateKey", "2024-11-01"),
		`{"keyType":"Primary"}`))
	var keys struct {
		PrimaryKey   string `json:"primaryKey"`
		SecondaryKey string `json:"secondaryKey"`
	}
	parseJSON(t, regenOut, &keys)
	require.NotEmpty(t, keys.PrimaryKey)
	require.NotEqual(t, keys.PrimaryKey, keys.SecondaryKey)

	// forceReboot returns a status message.
	rebootOut := runCLI(t, azRest("POST", armURL("Microsoft.Cache", "Redis/"+name+"/forceReboot", "2024-11-01"),
		`{"rebootType":"AllNodes"}`))
	require.Contains(t, rebootOut, "message")

	// flush completes synchronously.
	_ = runCLI(t, azRest("POST", armURL("Microsoft.Cache", "Redis/"+name+"/flush", "2024-11-01"), ""))

	// Access policy round-trip.
	apURL := armURL("Microsoft.Cache", "Redis/"+name+"/accessPolicies/cli-policy", "2024-11-01")
	runCLI(t, azRest("PUT", apURL, `{"properties":{"permissions":"+get +set allkeys"}}`))
	apOut := runCLI(t, azRest("GET", apURL, ""))
	var ap struct {
		Name       string `json:"name"`
		Properties struct {
			Permissions       string `json:"permissions"`
			ProvisioningState string `json:"provisioningState"`
		} `json:"properties"`
	}
	parseJSON(t, apOut, &ap)
	require.Equal(t, "cli-policy", ap.Name)
	require.Equal(t, "+get +set allkeys", ap.Properties.Permissions)
	require.Equal(t, "Succeeded", ap.Properties.ProvisioningState)

	// Access policy assignment round-trip.
	apaURL := armURL("Microsoft.Cache", "Redis/"+name+"/accessPolicyAssignments/cli-assign", "2024-11-01")
	runCLI(t, azRest("PUT", apaURL, `{"properties":{"accessPolicyName":"cli-policy","objectId":"11111111-1111-1111-1111-111111111111","objectIdAlias":"cli-user"}}`))
	apaOut := runCLI(t, azRest("GET", apaURL, ""))
	require.Contains(t, apaOut, "cli-policy")

	// Patch schedule round-trip.
	psURL := armURL("Microsoft.Cache", "Redis/"+name+"/patchSchedules/default", "2024-11-01")
	runCLI(t, azRest("PUT", psURL, `{"properties":{"scheduleEntries":[{"dayOfWeek":"Monday","startHourUtc":2}]}}`))
	psOut := runCLI(t, azRest("GET", psURL, ""))
	require.Contains(t, psOut, "scheduleEntries")

	// Private endpoint connection + private link resources.
	pecURL := armURL("Microsoft.Cache", "Redis/"+name+"/privateEndpointConnections/cli-pec", "2024-11-01")
	runCLI(t, azRest("PUT", pecURL, `{"properties":{"privateLinkServiceConnectionState":{"status":"Approved"}}}`))
	pecOut := runCLI(t, azRest("GET", pecURL, ""))
	require.Contains(t, pecOut, "cli-pec")
	plrOut := runCLI(t, azRest("GET", armURL("Microsoft.Cache", "Redis/"+name+"/privateLinkResources", "2024-11-01"), ""))
	require.Contains(t, plrOut, "redisCache")
}

func TestRedisCLI_OperationsAndNameAvailability(t *testing.T) {
	opsOut := runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.Cache/operations?api-version=2024-11-01", ""))
	require.Contains(t, opsOut, "Microsoft.Cache/redis/read")

	// Redis returns a 200 with no body for an available name; runCLI fails the
	// test if the call returns a non-success status.
	checkURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.Cache/checkNameAvailability?api-version=2024-11-01", baseURL, subscriptionID)
	runCLI(t, azRest("POST", checkURL, `{"name":"cli-redis-unused-name","type":"Microsoft.Cache/redis"}`))
}

func TestKeyVaultCLI_MgmtSubResources(t *testing.T) {
	name := "clikvmgmt"
	vaultURL := armURL("Microsoft.KeyVault", "vaults/"+name, "2023-07-01")
	runCLI(t, azRest("PUT", vaultURL, `{
		"location":"eastus",
		"properties":{"tenantId":"11111111-1111-1111-1111-111111111111","sku":{"family":"A","name":"standard"},"enableRbacAuthorization":true}
	}`))
	t.Cleanup(func() { _ = azRest("DELETE", vaultURL, "").Run() })

	// Name availability — the just-created vault name is taken.
	checkURL := fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.KeyVault/checkNameAvailability?api-version=2023-07-01", baseURL, subscriptionID)
	checkOut := runCLI(t, azRest("POST", checkURL, fmt.Sprintf(`{"name":%q,"type":"Microsoft.KeyVault/vaults"}`, name)))
	require.Contains(t, checkOut, `"nameAvailable": false`)

	// Private endpoint connection round-trip.
	pecURL := armURL("Microsoft.KeyVault", "vaults/"+name+"/privateEndpointConnections/cli-pec", "2023-07-01")
	runCLI(t, azRest("PUT", pecURL, `{"properties":{"privateLinkServiceConnectionState":{"status":"Approved","description":"cli"}}}`))
	pecOut := runCLI(t, azRest("GET", pecURL, ""))
	require.Contains(t, pecOut, "cli-pec")

	// Private link resources.
	plrOut := runCLI(t, azRest("GET", armURL("Microsoft.KeyVault", "vaults/"+name+"/privateLinkResources", "2023-07-01"), ""))
	require.Contains(t, plrOut, `"vault"`)
}

func TestManagedIdentityCLI_FederatedAndList(t *testing.T) {
	name := "climsi"
	idURL := armURL("Microsoft.ManagedIdentity", "userAssignedIdentities/"+name, "2024-11-30")
	runCLI(t, azRest("PUT", idURL, `{"location":"eastus"}`))
	t.Cleanup(func() { _ = azRest("DELETE", idURL, "").Run() })

	// PATCH (Update) tags.
	updOut := runCLI(t, azRest("PATCH", idURL, `{"tags":{"env":"cli"}}`))
	require.Contains(t, updOut, "cli")

	// Federated identity credential round-trip.
	ficURL := armURL("Microsoft.ManagedIdentity", "userAssignedIdentities/"+name+"/federatedIdentityCredentials/gh", "2024-11-30")
	runCLI(t, azRest("PUT", ficURL, `{"properties":{"issuer":"https://token.actions.githubusercontent.com","subject":"repo:o/r:ref:refs/heads/main","audiences":["api://AzureADTokenExchange"]}}`))
	ficOut := runCLI(t, azRest("GET", ficURL, ""))
	require.Contains(t, ficOut, "token.actions.githubusercontent.com")

	ficListOut := runCLI(t, azRest("GET", armURL("Microsoft.ManagedIdentity", "userAssignedIdentities/"+name+"/federatedIdentityCredentials", "2024-11-30"), ""))
	require.Contains(t, ficListOut, "gh")

	// Subscription-scoped list + operations + system-assigned-by-scope.
	listOut := runCLI(t, azRest("GET", fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.ManagedIdentity/userAssignedIdentities?api-version=2024-11-30", baseURL, subscriptionID), ""))
	require.Contains(t, listOut, name)

	opsOut := runCLI(t, azRest("GET", baseURL+"/providers/Microsoft.ManagedIdentity/operations?api-version=2024-11-30", ""))
	require.Contains(t, opsOut, "Microsoft.ManagedIdentity/userAssignedIdentities/read")

	saOut := runCLI(t, azRest("GET", fmt.Sprintf("%s/subscriptions/%s/providers/Microsoft.ManagedIdentity/identities/default?api-version=2024-11-30", baseURL, subscriptionID), ""))
	require.Contains(t, saOut, "principalId")
}
