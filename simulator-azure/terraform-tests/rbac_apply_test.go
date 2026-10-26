package azure_tf_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// tfRBACGraph sends one Microsoft Graph request as the bootstrap principal.
func tfRBACGraph(t *testing.T, client *http.Client, method, path string, body any, want int) map[string]any {
	t.Helper()
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {"test-client-id"},
		"client_secret": {"test-client-secret"},
		"scope":         {"https://graph.microsoft.com/.default"},
	}
	resp, err := client.PostForm(baseURL+"/11111111-1111-1111-1111-111111111111/oauth2/v2.0/token", form)
	require.NoError(t, err)
	var token struct {
		AccessToken string `json:"access_token"`
	}
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&token))
	require.NoError(t, resp.Body.Close())

	raw, err := json.Marshal(body)
	require.NoError(t, err)
	req, err := http.NewRequest(method, baseURL+path, bytes.NewReader(raw))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err = client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, want, resp.StatusCode, "%s %s: %s", method, path, data)
	out := map[string]any{}
	if len(data) > 0 {
		require.NoError(t, json.Unmarshal(data, &out))
	}
	return out
}

// TestTerraformRoleAssignmentsGateTheProvider applies a workload's
// configuration as a service principal whose roles the operator's
// configuration assigns: as Reader on the resource group azurerm cannot create
// the workload's identity, as Contributor it can but cannot write a blob with
// its Microsoft Entra token into an account that refuses its keys, and with
// Storage Blob Data Contributor on the account it applies whole.
func TestTerraformRoleAssignmentsGateTheProvider(t *testing.T) {
	client, err := trustedHTTPClient(caCertFile)
	require.NoError(t, err)
	client.Timeout = 30 * time.Second
	app := tfRBACGraph(t, client, http.MethodPost, "/v1.0/applications", map[string]any{"displayName": "tf-azrm-rbac-workload"}, http.StatusCreated)
	appObjectID, _ := app["id"].(string)
	appID, _ := app["appId"].(string)
	sp := tfRBACGraph(t, client, http.MethodPost, "/v1.0/servicePrincipals", map[string]any{"appId": appID}, http.StatusCreated)
	principalID, _ := sp["id"].(string)
	password := tfRBACGraph(t, client, http.MethodPost, "/v1.0/applications/"+appObjectID+"/addPassword",
		map[string]any{"passwordCredential": map[string]any{"displayName": "terraform"}}, http.StatusOK)
	secret, _ := password["secretText"].(string)
	require.NotEmpty(t, secret)

	grant := tfWorkspaceFrom(t, mustAbs("rbac/grant"))
	workload := tfWorkspaceFrom(t, mustAbs("rbac/workload"))
	for _, dir := range []string{grant, workload} {
		out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
		require.NoError(t, err, "terraform init failed:\n%s", out)
	}

	grantVars := func(role string, blobData bool) []string {
		return []string{"-var", "principal_id=" + principalID, "-var", "role=" + role,
			"-var", "blob_data_role=" + map[bool]string{true: "true", false: "false"}[blobData]}
	}
	applyGrant := func(role string, blobData bool) {
		t.Helper()
		out, err := runTimed(t, "terraform apply (grant "+role+")",
			terraformCmd(grant, append([]string{"apply", "-auto-approve"}, grantVars(role, blobData)...)...))
		require.NoError(t, err, "terraform apply of the grants failed:\n%s", out)
	}
	applyGrant("Reader", false)
	out, err := runTimed(t, "terraform output", terraformCmd(grant, "output", "-raw", "container_id"))
	require.NoError(t, err, "terraform output failed:\n%s", out)
	workloadVars := []string{"-var", "client_id=" + appID, "-var", "client_secret=" + secret,
		"-var", "container_id=" + strings.TrimSpace(string(out))}
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy (workload)",
			terraformCmd(workload, append([]string{"destroy", "-auto-approve"}, workloadVars...)...))
		require.NoError(t, err, "terraform destroy of the workload failed:\n%s", out)
		out, err = runTimed(t, "terraform destroy (grant)",
			terraformCmd(grant, append([]string{"destroy", "-auto-approve"}, grantVars("Contributor", true)...)...))
		require.NoError(t, err, "terraform destroy of the grants failed:\n%s", out)
	})
	applyWorkload := func(label string) ([]byte, error) {
		return runTimed(t, "terraform apply (workload "+label+")",
			terraformCmd(workload, append([]string{"apply", "-auto-approve"}, workloadVars...)...))
	}

	out, err = applyWorkload("as Reader")
	require.Error(t, err, "Reader created the workload's resources:\n%s", out)
	require.Contains(t, string(out), "AuthorizationFailed")
	require.Contains(t, string(out), "Microsoft.ManagedIdentity/userAssignedIdentities/write")

	applyGrant("Contributor", false)
	out, err = applyWorkload("as Contributor")
	require.Error(t, err, "Contributor wrote a blob without a data role:\n%s", out)
	// The provider's existence check is a HEAD, whose 403 carries the error
	// code in a header the provider does not print.
	require.Contains(t, string(out), "checking for existing Blob")
	require.Contains(t, string(out), "unexpected status 403")

	applyGrant("Contributor", true)
	out, err = applyWorkload("with Storage Blob Data Contributor")
	require.NoError(t, err, "the workload did not apply with its roles:\n%s", out)
}
