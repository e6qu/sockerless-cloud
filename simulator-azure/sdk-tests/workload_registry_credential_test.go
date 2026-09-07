package azure_sdk_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A Container Apps Job whose configuration names its registry with a managed
// identity pulls its image as that identity, as the platform does; a job that
// names no credential pulls anonymously and the registry refuses it.
func TestContainerApps_JobPullsFromItsRegistryAsItsIdentity(t *testing.T) {
	const rg = "aca-registry-identity-rg"
	loginServer, image := acrTasksPushImage(t, rg, "acaregidentityreg", "acaregidentityacct", "sockerless-overlay/aca")
	identity := "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/aca-pull-identity"

	acaPutJob(t, rg, "registry-identity-job", image, []string{"cat", "/opt/payload"}, map[string]any{
		"registries": []map[string]any{{"server": loginServer, "identity": identity}},
	})
	execName := acaStartExecution(t, rg, "registry-identity-job")
	execution := acaWaitExecution(t, rg, "registry-identity-job", execName)
	props, _ := execution["properties"].(map[string]any)
	assert.Equal(t, "Succeeded", props["status"], "the job pulls its image as the identity its registry entry names: %v", execution)

	acaPutJob(t, rg, "registry-anonymous-job", image, []string{"cat", "/opt/payload"}, nil)
	execName = acaStartExecution(t, rg, "registry-anonymous-job")
	execution = acaWaitExecution(t, rg, "registry-anonymous-job", execName)
	props, _ = execution["properties"].(map[string]any)
	assert.Equal(t, "Failed", props["status"], "a job that names no registry credential pulls anonymously, which the registry refuses: %v", execution)
}

// acaPutJob creates a manual-trigger Job running image with args, with extra
// configuration entries merged in.
func acaPutJob(t *testing.T, rg, jobName, image string, args []string, configuration map[string]any) {
	t.Helper()
	ensureRG(t, rg)
	config := map[string]any{"triggerType": "Manual", "replicaTimeout": 60}
	for k, v := range configuration {
		config[k] = v
	}
	job := map[string]any{
		"location": "eastus",
		"properties": map[string]any{
			"configuration": config,
			"template": map[string]any{
				"containers": []map[string]any{{"name": "worker", "image": image, "args": args}},
			},
		},
	}
	body, _ := json.Marshal(job)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut,
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.App/jobs/"+jobName+"?api-version=2024-03-01",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Contains(t, []int{200, 201}, resp.StatusCode)
}

// A Functions site whose configuration asks for Azure Container Registry
// managed-identity credentials pulls its image as that identity when its
// host starts, as App Service does.
func TestFunctions_SitePullsFromItsRegistryAsItsIdentity(t *testing.T) {
	const rg = "functions-registry-identity-rg"
	loginServer, image := acrTasksPushImage(t, rg, "funcregidentityreg", "funcregidentityacct", "sockerless-overlay/azf")

	props := map[string]any{
		"serverFarmId": "/subscriptions/" + subscriptionID + "/resourceGroups/" + rg + "/providers/Microsoft.Web/serverFarms/test-plan",
		"siteConfig": map[string]any{
			"linuxFxVersion":             "DOCKER|" + image,
			"acrUseManagedIdentityCreds": true,
			"acrUserManagedIdentityID":   "11111111-2222-3333-4444-555555555555",
			"appSettings": []map[string]any{
				{"name": "DOCKER_REGISTRY_SERVER_URL", "value": "https://" + loginServer},
				{"name": "SOCKERLESS_CMD", "value": "WyJjYXQiLCAiL29wdC9wYXlsb2FkIl0="},
			},
		},
	}
	site := map[string]any{"location": "eastus", "kind": "functionapp", "properties": props}
	body, _ := json.Marshal(site)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut,
		baseURL+"/subscriptions/"+subscriptionID+"/resourceGroups/"+rg+"/providers/Microsoft.Web/sites/registry-identity-site?api-version=2023-12-01",
		strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", simARMBearer)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)

	out := azureInvokeFunction(t, "registry-identity-site")
	assert.Contains(t, string(out), "pushed-as-the-run", "the site's host pulled the image from the registry as the site's identity")
}
