package azure_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const caeAPIVersion = "2023-05-01"

func caeURL(path string) string {
	return armURL("Microsoft.App", path, caeAPIVersion)
}

func TestContainerAppEnv_CreateAndShow(t *testing.T) {
	url := caeURL("managedEnvironments/cli-test-env")

	out := runCLI(t, azRest("PUT", url,
		`{"location":"eastus","properties":{}}`))

	var env struct {
		Name       string `json:"name"`
		Location   string `json:"location"`
		Properties struct {
			ProvisioningState string `json:"provisioningState"`
			DefaultDomain     string `json:"defaultDomain"`
		} `json:"properties"`
	}
	parseJSON(t, out, &env)
	assert.Equal(t, "cli-test-env", env.Name)
	assert.Equal(t, "eastus", env.Location)
	assert.Equal(t, "Succeeded", env.Properties.ProvisioningState)
	assert.NotEmpty(t, env.Properties.DefaultDomain)

	// GET
	out = runCLI(t, azRest("GET", url, ""))
	parseJSON(t, out, &env)
	assert.Equal(t, "cli-test-env", env.Name)

	// Cleanup
	runCLI(t, azRest("DELETE", url, ""))
}

func TestContainerAppEnv_Delete(t *testing.T) {
	url := caeURL("managedEnvironments/delete-test-env")
	runCLI(t, azRest("PUT", url, `{"location":"eastus","properties":{}}`))
	azRestLongRunning(t, "DELETE", url, "")

	out, err := azRest("GET", url, "").CombinedOutput()
	require.Error(t, err, "GET after the delete operation completes must fail: %s", out)
	failure := string(out)
	assert.Contains(t, failure, "ResourceNotFound",
		"a deleted managed environment must answer GET with ResourceNotFound once the"+
			" delete operation completes, got: %s", failure)
}
