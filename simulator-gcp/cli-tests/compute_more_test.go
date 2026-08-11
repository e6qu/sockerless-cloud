package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Lean gcloud-compute coverage for the control-plane CRUD added in
// compute_more.go: a global external address, a regional target pool, and
// the regions catalog read — each round-trips through the real gcloud CLI.

func TestGcloudComputeGlobalAddress_CRUD(t *testing.T) {
	name := "sim-gaddr-cli-1"
	out, err := gcloudCLI("compute", "addresses", "create", name,
		"--global", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create: %s", out)

	out, err = gcloudCLI("compute", "addresses", "describe", name,
		"--global", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	assert.Contains(t, string(out), name)

	out, err = gcloudCLI("compute", "addresses", "list", "--global",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.Contains(t, string(out), name)

	out, err = gcloudCLI("compute", "addresses", "delete", name,
		"--global", "--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)
}

func TestGcloudComputeTargetPool_CRUD(t *testing.T) {
	region := "us-central1"
	name := "sim-tp-cli-1"
	out, err := gcloudCLI("compute", "target-pools", "create", name,
		"--region="+region, "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create: %s", out)

	out, err = gcloudCLI("compute", "target-pools", "describe", name,
		"--region="+region, "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	assert.Contains(t, string(out), name)

	out, err = gcloudCLI("compute", "target-pools", "delete", name,
		"--region="+region, "--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)
}

func TestGcloudComputeRegions_List(t *testing.T) {
	out, err := gcloudCLI("compute", "regions", "list",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.True(t, strings.Contains(string(out), "us-central1"), "regions: %s", out)
}

// TestGcloudComputeSubnetsList_EmptyRegion exercises compute.subnetworks.list
// on a region with no subnets — the regional list route
//
//	GET /compute/v1/projects/{project}/regions/{region}/subnetworks
//
// which `gcloud compute networks subnets list --regions=<one region>` calls
// directly (rather than the aggregated form it uses without --regions). No
// real-execution host capability is involved in the read, so this runs
// everywhere; the create → list round trip lives with the other real-network
// CLI coverage in compute_nat_test.go.
func TestGcloudComputeSubnetsList_EmptyRegion(t *testing.T) {
	out, err := gcloudCLI("compute", "networks", "subnets", "list",
		"--regions=europe-west4",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "subnets list: %s", out)
	assert.Empty(t, strings.TrimSpace(string(out)), "subnets list: %s", out)
}
