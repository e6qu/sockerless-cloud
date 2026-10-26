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
