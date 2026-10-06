package azure_tf_test

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestTerraformWebAppSlotApplyDestroy provisions a Linux web app with a
// staging deployment slot and makes the slot the active one with
// azurerm_web_app_active_slot, which swaps it into production
// (WebApps_SwapSlotWithProduction) and waits for the app's slotSwapStatus to
// name it. Each slot runs its own container on its own hostname; after the
// swap the app's hostname serves what the slot ran, and the slot's hostname
// what production ran.
func TestTerraformWebAppSlotApplyDestroy(t *testing.T) {
	dir := tfWorkspaceFrom(t, mustAbs("webslots"))
	out, err := runTimed(t, "terraform init", terraformCmd(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)

	out, err = runTimed(t, "terraform apply", terraformCmd(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)

	out, err = runTimed(t, "terraform plan", terraformCmd(dir, "plan", "-detailed-exitcode"))
	require.NoError(t, err, "terraform plan showed drift after apply (not idempotent):\n%s", out)

	outputs := readOutputs(t, dir)
	appHost := outputs.must(t, "app_hostname")
	slotHost := outputs.must(t, "slot_hostname")
	require.Equal(t, "tf-azrm-slots-app.azurewebsites.net", appHost)
	require.Equal(t, "tf-azrm-slots-app-staging.azurewebsites.net", slotHost)
	require.Equal(t,
		"/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/tf-azrm-slots-rg/providers/Microsoft.Web/sites/tf-azrm-slots-app/slots/staging",
		outputs.must(t, "active_slot_id"))
	require.NotEmpty(t, outputs.must(t, "last_successful_swap"))

	require.Equal(t, "staging-content", slotSiteServes(t, appHost), "production's hostname serves what the slot ran")
	require.Equal(t, "production-content", slotSiteServes(t, slotHost), "the slot's hostname serves what production ran")

	out, err = runTimed(t, "terraform destroy", terraformCmd(dir, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
}

// slotSiteServes sends one request to a site hostname through the simulator's
// front end and returns the answer.
func slotSiteServes(t *testing.T, host string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", simPort), nil)
	require.NoError(t, err)
	req.Host = host
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "%s answered: %s", host, body)
	return strings.TrimSpace(string(body))
}
