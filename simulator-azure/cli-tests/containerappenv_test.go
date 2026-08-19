package azure_cli_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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
	runCLI(t, azRest("DELETE", url, ""))

	// ARM DELETE is a 202 LRO: the environment stays observable in
	// provisioningState=Deleting until the operation completes, then GET
	// fails with 404. Poll like a real client.
	deadline := time.Now().Add(10 * time.Second)
	var failure string
	for time.Now().Before(deadline) {
		out, err := azRest("GET", url, "").CombinedOutput()
		if err != nil {
			failure = string(out)
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	assert.Contains(t, failure, "ResourceNotFound",
		"a deleted managed environment must answer GET with ResourceNotFound once the"+
			" delete operation completes, got: %s", failure)
}
