package azure_sdk_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContainerAppsEnv_StaticIPUniqueAndStable verifies the managed-environment
// staticIp is unique per environment (real Azure allocates a distinct IP per
// env) and stable across re-reads of the same environment. A single hardcoded
// value made every environment collide.
func TestContainerAppsEnv_StaticIPUniqueAndStable(t *testing.T) {
	rg := "env-staticip-rg"
	doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s?api-version=2023-07-01",
		baseURL, subscriptionID, rg), `{"location":"eastus"}`)

	staticIP := func(envName string) string {
		raw := doPUT(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s?api-version=2024-03-01",
			baseURL, subscriptionID, rg, envName), `{"location":"eastus","properties":{"zoneRedundant":false}}`)
		t.Cleanup(func() {
			doDELETE(t, fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/managedEnvironments/%s?api-version=2024-03-01",
				baseURL, subscriptionID, rg, envName))
		})
		var env struct {
			Properties struct {
				StaticIP string `json:"staticIp"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal([]byte(raw), &env))
		require.NotEmpty(t, env.Properties.StaticIP, "managed environment must report a staticIp")
		return env.Properties.StaticIP
	}

	ipA1 := staticIP("env-staticip-a")
	ipA2 := staticIP("env-staticip-a") // re-PUT same env: stable
	ipB := staticIP("env-staticip-b")  // different env: distinct

	assert.Equal(t, ipA1, ipA2, "the same environment must report a stable staticIp")
	assert.NotEqual(t, ipA1, ipB, "distinct environments must report distinct staticIps")
}
