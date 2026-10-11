package gcp_tf_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// applyFixture applies a fixture, destroys it when the test ends, and returns
// what the provider read back.
func applyFixture(t *testing.T, name string) tfOutputs {
	t.Helper()
	dir := filepath.Join("fixtures", name)
	cleanTerraformFixture(t, dir)
	out, err := runTimed(t, "terraform init "+name, terraformCmdInDir(dir, "init"))
	require.NoError(t, err, "terraform init failed:\n%s", out)
	t.Cleanup(func() {
		out, err := runTimed(t, "terraform destroy "+name, terraformCmdInDir(dir, "destroy", "-auto-approve"))
		require.NoError(t, err, "terraform destroy failed:\n%s", out)
	})
	out, err = runTimed(t, "terraform apply "+name, terraformCmdInDir(dir, "apply", "-auto-approve"))
	require.NoError(t, err, "terraform apply failed:\n%s", out)
	return readOutputsInDir(t, dir)
}

// requireNoDrift fails when a second plan of the fixture is not empty: a
// member the simulator drops or renames on read-back surfaces as drift.
func requireNoDrift(t *testing.T, name string) {
	t.Helper()
	out, err := runTimed(t, "terraform plan "+name, terraformCmdInDir(filepath.Join("fixtures", name), "plan", "-detailed-exitcode"))
	require.NoError(t, err, "%s re-plan is not empty:\n%s", name, out)
}

// TestTerraformResourceManagerCapabilityConfig creates a capability config on
// a folder through google_resource_manager_capability_config.
func TestTerraformResourceManagerCapabilityConfig(t *testing.T) {
	outputs := applyFixture(t, "capability-config")
	require.True(t, strings.HasPrefix(outputs.must(t, "name"), "folders/"), outputs.must(t, "name"))
	require.True(t, strings.HasSuffix(outputs.must(t, "name"), "/capabilityConfigs/tf-capcfg"), outputs.must(t, "name"))
	require.Equal(t, []any{"APP_MANAGEMENT"}, outputs.mustValue(t, "types"))
	require.True(t, strings.HasPrefix(outputs.must(t, "management_project"), "projects/"),
		"Cloud Resource Manager names the management project it made: %q", outputs.must(t, "management_project"))
	requireNoDrift(t, "capability-config")
}

// TestTerraformNetworkEdgeSecurityService creates a network edge security
// service over a Cloud Armor network policy and reads the region's service
// attachments through google_compute_service_attachments.
func TestTerraformNetworkEdgeSecurityService(t *testing.T) {
	outputs := applyFixture(t, "network-edge-security")
	require.Equal(t, "projects/test-project/regions/us-central1/networkEdgeSecurityServices/tf-edge-service",
		outputs.must(t, "edge_service_id"))
	require.True(t, strings.HasSuffix(outputs.must(t, "edge_service_policy"), "/regions/us-central1/securityPolicies/tf-edge-policy"),
		outputs.must(t, "edge_service_policy"))
	require.Equal(t, float64(0), outputs.mustNumber(t, "service_attachment_count"))
	requireNoDrift(t, "network-edge-security")
}

// TestTerraformEventarcPipelineIAM manages an Eventarc pipeline's IAM policy
// through the binding, member and policy resources and reads it back through
// the google_eventarc_pipeline_iam_policy data source.
func TestTerraformEventarcPipelineIAM(t *testing.T) {
	outputs := applyFixture(t, "eventarc-pipeline-iam")
	var policy struct {
		Bindings []struct {
			Role    string   `json:"role"`
			Members []string `json:"members"`
		} `json:"bindings"`
	}
	require.NoError(t, json.Unmarshal([]byte(outputs.must(t, "pipeline_policy")), &policy))
	members := map[string][]string{}
	for _, binding := range policy.Bindings {
		members[binding.Role] = binding.Members
	}
	require.Equal(t, []string{"user:viewer@example.com"}, members["roles/viewer"])
	require.Equal(t, []string{"user:editor@example.com"}, members["roles/editor"])
	require.NotEmpty(t, outputs.must(t, "governed_policy_etag"))
	requireNoDrift(t, "eventarc-pipeline-iam")
}

// TestTerraformCloudBuildWorkerPoolDataSource creates a Cloud Build private
// pool and reads it back through the google_cloudbuild_worker_pool data
// source.
func TestTerraformCloudBuildWorkerPoolDataSource(t *testing.T) {
	outputs := applyFixture(t, "cloudbuild-worker-pool")
	require.Equal(t, "tf-worker-pool", outputs.must(t, "data_name"))
	require.Equal(t, "Terraform worker pool", outputs.must(t, "data_display_name"))
	require.Equal(t, "e2-standard-4", outputs.must(t, "data_machine_type"))
	require.Equal(t, "RUNNING", outputs.must(t, "data_state"))
	requireNoDrift(t, "cloudbuild-worker-pool")
}
