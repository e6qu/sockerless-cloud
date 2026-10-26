package azure_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestGraphCLI_RequiresAGraphToken drives Microsoft Graph through `az rest`:
// with no token (the request az sends for a URL it derives no resource for)
// and with an Azure Resource Manager token Graph refuses the call with
// InvalidAuthenticationToken; with a Graph token it serves it.
func TestGraphCLI_RequiresAGraphToken(t *testing.T) {
	t.Parallel()
	body := `{"displayName":"graph-auth-cli"}`
	url := baseURL + "/v1.0/applications"

	out, err := azRest("POST", url, body, "--headers", "Content-Type=application/json").CombinedOutput()
	require.Error(t, err, "az rest without a token succeeded: %s", out)
	require.Contains(t, string(out), "InvalidAuthenticationToken")
	require.Contains(t, string(out), "Access token is empty.")

	out, err = azRest("POST", url, body, "--headers", "Authorization=Bearer "+armBearer).CombinedOutput()
	require.Error(t, err, "az rest with an Azure Resource Manager token succeeded: %s", out)
	require.Contains(t, string(out), "Invalid audience")

	created := azGraph(t, "POST", "/v1.0/applications", map[string]any{"displayName": "graph-auth-cli"})
	id, _ := created["id"].(string)
	require.NotEmpty(t, id)
	out, err = azRest("DELETE", baseURL+"/v1.0/applications/"+id, "").CombinedOutput()
	require.NoError(t, err, "%s", strings.TrimSpace(string(out)))
}

// TestAzLogin_UserPasswordIsChecked creates a directory user through Microsoft
// Graph with a passwordProfile and signs `az login` in as that user: Entra
// refuses a wrong password with AADSTS50126 and signs the right one in.
func TestAzLogin_UserPasswordIsChecked(t *testing.T) {
	t.Parallel()
	env := startAzLoginSimulator(t)
	runCLI(t, env.command("cloud", "register", "-n", "sockerless-sim",
		"--endpoint-resource-manager", env.baseURL,
		"--endpoint-active-directory", env.baseURL+"/adfs",
		"--endpoint-active-directory-resource-id", "https://management.azure.com/",
		"--endpoint-active-directory-graph-resource-id", env.baseURL))
	runCLI(t, env.command("cloud", "set", "-n", "sockerless-sim"))
	runCLI(t, env.command("login", "--service-principal",
		"-u", "test-client-id", "-p", "test-client-secret",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))

	const upn = "az-login-user@sockerless-login.example"
	runCLI(t, env.command("rest", "--method", "POST", "--url", env.baseURL+"/v1.0/users",
		"--resource", "https://graph.microsoft.com",
		"--headers", "Content-Type=application/json",
		"--body", `{"accountEnabled":true,"displayName":"az login user","mailNickname":"az-login-user","userPrincipalName":"`+upn+`","passwordProfile":{"forceChangePasswordNextSignIn":false,"password":"Correct-Horse-1"}}`))
	runCLI(t, env.command("logout"))

	refused, err := env.command("login", "-u", upn, "-p", "wrong-password",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions").CombinedOutput()
	require.Error(t, err, "az login with a wrong password succeeded: %s", refused)
	require.Contains(t, string(refused), "AADSTS50126")

	runCLI(t, env.command("login", "-u", upn, "-p", "Correct-Horse-1",
		"--tenant", azLoginTenantID, "--allow-no-subscriptions"))
	var account struct {
		User struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"user"`
	}
	parseJSON(t, runCLI(t, env.command("account", "show", "-o", "json")), &account)
	require.Equal(t, upn, account.User.Name)
	require.Equal(t, "user", account.User.Type)
}
