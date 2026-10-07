package azure_cli_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CLI coverage for what reaches an App Service app from outside its own
// configuration — Key Vault references resolved through the app's managed
// identity, and the content of the repository its source control names — as
// the Azure CLI drives them:
//
//	az webapp identity assign
//	az keyvault create --enable-rbac-authorization / az role assignment create
//	az webapp config appsettings set / az webapp config connection-string set
//	  GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/config/configreferences/appsettings/{appSettingKey}
//	az webapp deployment source config --manual-integration
//	  PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/sourcecontrols/web
//	az webapp deployment source sync
//	  POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{siteName}/sync
//	az webapp deployment source show / delete
//	az webapp log deployment list

// filesWebAppCLI creates a Linux custom-container web app running the
// container-command image's files-http server over its environment and its
// persistent /home share, and returns a reader of the app's answers.
func filesWebAppCLI(t *testing.T, az kuduCLIEnv, rg, plan, app string) func(path string) (string, error) {
	t.Helper()
	runCLI(t, az.command("group", "create", "-n", rg, "-l", "eastus", "-o", "json"))
	runCLI(t, az.command("appservice", "plan", "create", "-g", rg, "-n", plan,
		"--is-linux", "--sku", "S1", "-o", "json"))
	siteURL := fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s?api-version=2025-03-01",
		az.baseURL, subscriptionID, rg, app)
	runCLI(t, az.command("rest", "--method", "PUT", "--url", siteURL, "--body", fmt.Sprintf(`{
		"location": "eastus",
		"kind": "app,linux,container",
		"properties": {
			"serverFarmId": "/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/serverfarms/%s",
			"reserved": true,
			"siteConfig": {
				"linuxFxVersion": "DOCKER|%s",
				"appCommandLine": "files-http 80 /home/site/wwwroot",
				"appSettings": [{"name": "WEBSITES_ENABLE_APP_SERVICE_STORAGE", "value": "true"}]
			}
		}
	}`, subscriptionID, rg, plan, commandImageName), "-o", "json"))
	t.Cleanup(func() { runCLI(t, az.command("webapp", "delete", "-g", rg, "-n", app)) })
	return func(path string) (string, error) {
		out, stderr, err := runCLIStreamsResult(az.command("rest", "--method", "GET", "--url", az.baseURL+path,
			"--headers", "Host="+app+".azurewebsites.net", "--skip-authorization-header"))
		if err != nil {
			return "", fmt.Errorf("%w: %s", err, stderr)
		}
		return strings.TrimSpace(out), nil
	}
}

func TestWebAppKeyVaultReferences_ResolveThroughTheAppIdentity(t *testing.T) {
	az := kuduCLILogin(t)
	rg, plan, app, vault := "kvref-cli-rg", "kvref-cli-plan", "kvref-cli-app", "kvrefclivault"
	read := filesWebAppCLI(t, az, rg, plan, app)
	env := func(name string) string {
		t.Helper()
		out, err := read("/env/" + name)
		require.NoError(t, err)
		return out
	}

	var identity struct {
		PrincipalID string `json:"principalId"`
		TenantID    string `json:"tenantId"`
		Type        string `json:"type"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "identity", "assign", "-g", rg, "-n", app, "-o", "json")), &identity)
	require.NotEmpty(t, identity.PrincipalID)
	assert.Equal(t, "SystemAssigned", identity.Type)

	var created struct {
		ID string `json:"id"`
	}
	parseJSON(t, runCLI(t, az.command("keyvault", "create", "-n", vault, "-g", rg, "-l", "eastus",
		"--sku", "standard", "--enable-rbac-authorization", "true", "--no-self-perms", "-o", "json")), &created)
	kvGrantCaller(t, az.command, "Key Vault Secrets Officer", created.ID)
	runCLI(t, kvMoveDataPlane(az.azLoginEnv, vault, "PUT", "/secrets/db-password", `{"value":"hunter2"}`))
	runCLI(t, az.command("role", "assignment", "create", "--role", "Key Vault Secrets User",
		"--assignee-object-id", identity.PrincipalID, "--assignee-principal-type", "ServicePrincipal",
		"--scope", created.ID, "-o", "json"))

	dbRef := "@Microsoft.KeyVault(VaultName=" + vault + ";SecretName=db-password)"
	missingRef := "@Microsoft.KeyVault(VaultName=" + vault + ";SecretName=no-such-secret)"
	runCLI(t, az.command("webapp", "config", "appsettings", "set", "-g", rg, "-n", app,
		"--settings", "DB="+dbRef, "MISSING="+missingRef, "-o", "json"))
	runCLI(t, az.command("webapp", "config", "connection-string", "set", "-g", rg, "-n", app,
		"-t", "Custom", "--settings", "Main="+dbRef, "-o", "json"))
	assert.Equal(t, "hunter2", env("DB"), "the workload sees the secret's value")
	assert.Equal(t, "hunter2", env("CUSTOMCONNSTR_Main"))
	assert.Equal(t, missingRef, env("MISSING"), "an unresolved reference reaches the workload as written")

	refURL := func(key string) string {
		return fmt.Sprintf("%s/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Web/sites/%s/config/configreferences/appsettings/%s?api-version=2025-03-01",
			az.baseURL, subscriptionID, rg, app, key)
	}
	var ref struct {
		Properties struct {
			Status       string `json:"status"`
			Details      string `json:"details"`
			IdentityType struct {
				Type string `json:"type"`
			} `json:"identityType"`
		} `json:"properties"`
	}
	parseJSON(t, runCLI(t, az.command("rest", "--method", "GET", "--url", refURL("DB"), "-o", "json")), &ref)
	assert.Equal(t, "Resolved", ref.Properties.Status)
	assert.Equal(t, "SystemAssigned", ref.Properties.IdentityType.Type)
	parseJSON(t, runCLI(t, az.command("rest", "--method", "GET", "--url", refURL("MISSING"), "-o", "json")), &ref)
	assert.Equal(t, "SecretNotFound", ref.Properties.Status)
	assert.NotEmpty(t, ref.Properties.Details)

	// Without its identity the app cannot reach the vault.
	runCLI(t, az.command("webapp", "identity", "remove", "-g", rg, "-n", app, "-o", "json"))
	parseJSON(t, runCLI(t, az.command("rest", "--method", "GET", "--url", refURL("DB"), "-o", "json")), &ref)
	assert.Equal(t, "MSINotEnabled", ref.Properties.Status)
	runCLI(t, az.command("webapp", "restart", "-g", rg, "-n", app))
	assert.Equal(t, dbRef, env("DB"))
}

func TestWebAppSourceControl_DeploysAndSyncsTheRepository(t *testing.T) {
	az := kuduCLILogin(t)
	rg, plan, app := "scm-cli-rg", "scm-cli-plan", "scm-cli-app"
	read := filesWebAppCLI(t, az, rg, plan, app)
	repo := startGitHTTPRepo(t, "cli-site")
	first := repo.commit("main", "First page", map[string]string{"index.html": "first", "notes.txt": "removed later"})

	runCLI(t, az.command("webapp", "deployment", "source", "config", "-g", rg, "-n", app,
		"--repo-url", repo.URL, "--branch", "main", "--manual-integration", "-o", "json"))
	page, err := read("/files/index.html")
	require.NoError(t, err)
	assert.Equal(t, "first", page, "the configured branch's head is deployed")

	var shown struct {
		RepoURL             string `json:"repoUrl"`
		Branch              string `json:"branch"`
		IsManualIntegration bool   `json:"isManualIntegration"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "deployment", "source", "show", "-g", rg, "-n", app, "-o", "json")), &shown)
	assert.Equal(t, repo.URL, shown.RepoURL)
	assert.Equal(t, "main", shown.Branch)
	assert.True(t, shown.IsManualIntegration)

	repo.remove("notes.txt")
	second := repo.commit("main", "Second page", map[string]string{"index.html": "second"})
	runCLI(t, az.command("webapp", "deployment", "source", "sync", "-g", rg, "-n", app))
	page, err = read("/files/index.html")
	require.NoError(t, err)
	assert.Equal(t, "second", page, "a sync deploys the branch's current head")
	_, err = read("/files/notes.txt")
	require.Error(t, err, "a file the head no longer has leaves the app")

	var deployments []struct {
		ID          string `json:"id"`
		Author      string `json:"author"`
		AuthorEmail string `json:"author_email"`
		Message     string `json:"message"`
		Status      int    `json:"status"`
		Active      bool   `json:"active"`
	}
	parseJSON(t, runCLI(t, az.command("webapp", "log", "deployment", "list", "-g", rg, "-n", app, "-o", "json")), &deployments)
	byID := map[string]int{}
	for i, d := range deployments {
		byID[d.ID] = i
	}
	require.Contains(t, byID, first)
	require.Contains(t, byID, second)
	latest := deployments[byID[second]]
	assert.Equal(t, 4, latest.Status)
	assert.True(t, latest.Active)
	assert.Equal(t, "Ada Lovelace", latest.Author)
	assert.Equal(t, "ada@example.com", latest.AuthorEmail)
	assert.Equal(t, "Second page", latest.Message)
	assert.False(t, deployments[byID[first]].Active)

	runCLI(t, az.command("webapp", "deployment", "source", "delete", "-g", rg, "-n", app))
	_, _, err = runCLIStreamsResult(az.command("webapp", "deployment", "source", "sync", "-g", rg, "-n", app))
	require.Error(t, err, "an app without source control has nothing to sync")
}
