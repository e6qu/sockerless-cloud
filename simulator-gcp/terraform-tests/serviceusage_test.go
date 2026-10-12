package gcp_tf_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func serviceUsageState(t *testing.T, name string) string {
	t.Helper()
	var service struct {
		State string `json:"state"`
	}
	require.NoError(t, json.Unmarshal(simCall(t, "GET", "/v1/"+name, ""), &service))
	return service.State
}

// TestTerraformProjectService enables a service through google_project_service
// on a project that never enabled it, and disables it again on destroy:
// Service Usage reports the service DISABLED before the apply and after the
// destroy, and ENABLED in between.
func TestTerraformProjectService(t *testing.T) {
	const name = "projects/tf-service-usage/services/pubsub.googleapis.com"
	const fixture = "project-service"
	require.Equal(t, "DISABLED", serviceUsageState(t, name), "a never-enabled service is DISABLED")

	cleanTerraformFixture(t, "fixtures/"+fixture)
	out, err := runTimed(t, "terraform init "+fixture, terraformCmdInDir("fixtures/"+fixture, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	out, err = runTimed(t, "terraform apply "+fixture, terraformCmdInDir("fixtures/"+fixture, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	require.Equal(t, "tf-service-usage/pubsub.googleapis.com", readOutputsInDir(t, "fixtures/"+fixture).must(t, "id"))
	require.Equal(t, "ENABLED", serviceUsageState(t, name))
	requireNoDrift(t, fixture)

	out, err = runTimed(t, "terraform destroy "+fixture, terraformCmdInDir("fixtures/"+fixture, "destroy", "-auto-approve"))
	require.NoError(t, err, "terraform destroy failed:\n%s", out)
	require.Equal(t, "DISABLED", serviceUsageState(t, name), "disable_on_destroy disables the service")
}
