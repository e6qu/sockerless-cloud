package azure_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for the managed identities of container apps and Container Apps
// jobs, and their secrets that reference Key Vault:
//
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/containerApps/{containerAppName}
//	POST .../containerApps/{containerAppName}/listSecrets
//	PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App/jobs/{jobName}
//	POST .../jobs/{jobName}/start
//	GET {IDENTITY_ENDPOINT}?api-version=2019-08-01&resource={resource}[&client_id]

// acaIdentityCLIScript prints the secrets the workload sees and the tokens its
// identity endpoint issues the system-assigned identity and the user-assigned
// identity $UAI_CLIENT_ID names, each token's payload on a line of its own.
const acaIdentityCLIScript = `echo "db=$DB plain=$PLAIN"
token() { wget -qO- --header "X-IDENTITY-HEADER: $IDENTITY_HEADER" "$IDENTITY_ENDPOINT?api-version=2019-08-01&resource=https://vault.azure.net$1" | sed -e 's/.*"access_token": *"[^.]*\.\([^.]*\)\..*/claims=\1/'; }
token ""
token "&client_id=$UAI_CLIENT_ID"
echo done`

// acaIdentityCLIServe keeps an app's replica running after its script, and
// ends it as soon as the platform stops the replica: sh as PID 1 ignores
// SIGTERM unless it traps it.
const acaIdentityCLIServe = `trap 'exit 0' TERM
sleep 3600 &
wait`

type cliIdentity struct {
	Type                   string `json:"type"`
	PrincipalID            string `json:"principalId"`
	UserAssignedIdentities map[string]struct {
		PrincipalID string `json:"principalId"`
		ClientID    string `json:"clientId"`
	} `json:"userAssignedIdentities"`
}

// cliTokenClaims decodes the claims= lines acaIdentityCLIScript prints.
func cliTokenClaims(t *testing.T, lines []string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range lines {
		payload, ok := strings.CutPrefix(line, "claims=")
		if !ok {
			continue
		}
		raw, err := base64.RawURLEncoding.DecodeString(payload)
		require.NoError(t, err, line)
		var claims map[string]any
		require.NoError(t, json.Unmarshal(raw, &claims), line)
		out = append(out, claims)
	}
	return out
}

func TestContainerApps_CLI_ManagedIdentityAndKeyVaultSecrets(t *testing.T) {
	var uai struct {
		ID         string `json:"id"`
		Properties struct {
			ClientID    string `json:"clientId"`
			PrincipalID string `json:"principalId"`
		} `json:"properties"`
	}
	uaiURL := armURL("Microsoft.ManagedIdentity", "userAssignedIdentities/cli-aca-identity", "2023-01-31")
	parseJSON(t, runCLI(t, azRest("PUT", uaiURL, `{"location":"eastus"}`)), &uai)
	t.Cleanup(func() { _ = azRest("DELETE", uaiURL, "").Run() })

	vault := "cli-aca-id-vault"
	createKVVaultCLI(t, vault)
	runCLI(t, kvDataRest("PUT", vault, "/secrets/db-password", `{"value":"hunter2"}`))
	secretURL := "https://" + vault + ".vault.azure.net/secrets/db-password"
	envID, _ := cliLogsEnvironment(t)

	appName := "cli-aca-identity-app"
	appURL := acaURL("containerApps/" + appName)
	appBody := func(script string) string {
		return fmt.Sprintf(`{
			"location": "eastus",
			"identity": {"type": "SystemAssigned,UserAssigned", "userAssignedIdentities": {%q: {}}},
			"properties": {
				"environmentId": %q,
				"configuration": {"secrets": [
					{"name": "db", "keyVaultUrl": %q, "identity": %q},
					{"name": "plain", "value": "plain-value"}
				]},
				"template": {
					"containers": [{
						"name": "main",
						"image": "public.ecr.aws/docker/library/alpine:latest",
						"command": ["sh", "-c", %q],
						"env": [
							{"name": "DB", "secretRef": "db"},
							{"name": "PLAIN", "secretRef": "plain"},
							{"name": "UAI_CLIENT_ID", "value": %q}
						]
					}],
					"scale": {"minReplicas": 1, "maxReplicas": 1}
				}
			}
		}`, uai.ID, envID, secretURL, uai.ID, script, uai.Properties.ClientID)
	}
	grant := func(principal string) {
		runCLI(t, azRest("PUT", armURL("Microsoft.KeyVault", "vaults/"+vault+"/accessPolicies/add", "2024-11-01"),
			fmt.Sprintf(`{"properties":{"accessPolicies":[{"tenantId":%q,"objectId":%q,"permissions":{"secrets":["get"]}}]}}`,
				simTenantID, principal)))
	}

	// The vault does not grant the identity the secret names yet.
	refused := runCLIExpectFailure(t, azRest("PUT", appURL, appBody(acaIdentityCLIScript+"\n"+acaIdentityCLIServe)))
	assert.Contains(t, refused, "Unable to get value using Managed identity "+uai.ID)

	grant(uai.Properties.PrincipalID)
	var created struct {
		Identity cliIdentity `json:"identity"`
	}
	parseJSON(t, runCLI(t, azRest("PUT", appURL, appBody(acaIdentityCLIScript+"\n"+acaIdentityCLIServe))), &created)
	t.Cleanup(func() { _ = azRest("DELETE", appURL, "").Run() })
	assert.Equal(t, "SystemAssigned,UserAssigned", created.Identity.Type)
	require.NotEmpty(t, created.Identity.PrincipalID)
	assert.Equal(t, uai.Properties.PrincipalID, created.Identity.UserAssignedIdentities[uai.ID].PrincipalID)
	assert.Equal(t, uai.Properties.ClientID, created.Identity.UserAssignedIdentities[uai.ID].ClientID)

	lines := waitForContainerAppLogLine(t, "ContainerAppName_s", appName, "done", 90*time.Second)
	assert.Contains(t, lines, "db=hunter2 plain=plain-value", "the workload sees the Key Vault secret's value: %q", lines)
	claims := cliTokenClaims(t, lines)
	require.Len(t, claims, 2, "%q", lines)
	assert.Equal(t, created.Identity.PrincipalID, claims[0]["oid"], "the system-assigned identity's token")
	assert.Equal(t, uai.Properties.PrincipalID, claims[1]["oid"], "the user-assigned identity's token")

	var listed struct {
		Value []struct {
			Name        string `json:"name"`
			KeyVaultURL string `json:"keyVaultUrl"`
			Identity    string `json:"identity"`
		} `json:"value"`
	}
	parseJSON(t, runCLI(t, azRest("POST", acaURL("containerApps/"+appName+"/listSecrets"), "")), &listed)
	byName := map[string]string{}
	for _, s := range listed.Value {
		byName[s.Name] = s.KeyVaultURL + " " + s.Identity
	}
	assert.Equal(t, secretURL+" "+uai.ID, byName["db"])

	// A job reads the same secret as its user-assigned identity.
	jobName := "cli-aca-identity-job"
	jobURL := acaURL("jobs/" + jobName)
	runCLI(t, azRest("PUT", jobURL, fmt.Sprintf(`{
		"location": "eastus",
		"identity": {"type": "UserAssigned", "userAssignedIdentities": {%q: {}}},
		"properties": {
			"environmentId": %q,
			"configuration": {
				"replicaTimeout": 60,
				"triggerType": "Manual",
				"secrets": [{"name": "db", "keyVaultUrl": %q, "identity": %q}, {"name": "plain", "value": "plain-value"}]
			},
			"template": {"containers": [{
				"name": "app",
				"image": "public.ecr.aws/docker/library/alpine:latest",
				"command": ["sh", "-c", %q],
				"env": [
					{"name": "DB", "secretRef": "db"},
					{"name": "PLAIN", "secretRef": "plain"},
					{"name": "UAI_CLIENT_ID", "value": %q}
				]
			}]}
		}
	}`, uai.ID, envID, secretURL, uai.ID, acaIdentityCLIScript, uai.Properties.ClientID)))
	t.Cleanup(func() { _ = azRest("DELETE", jobURL, "").Run() })
	var started struct {
		Name string `json:"name"`
	}
	parseJSON(t, runCLI(t, azRest("POST", armURL("Microsoft.App", "jobs/"+jobName+"/start", acaAPIVersion), "")), &started)
	require.NotEmpty(t, started.Name)
	jobLines := waitForContainerAppLogLine(t, "ContainerGroupName_s", jobName, "done", 90*time.Second)
	assert.Contains(t, jobLines, "db=hunter2 plain=plain-value", "%q", jobLines)
	jobClaims := cliTokenClaims(t, jobLines)
	require.Len(t, jobClaims, 1, "a job with only a user-assigned identity gets no system-assigned token: %q", jobLines)
	assert.Equal(t, uai.Properties.PrincipalID, jobClaims[0]["oid"])
}
