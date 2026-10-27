package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGcloudComputeGlobalAddress_CRUD(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	out, err := gcloudCLI("compute", "regions", "list",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.True(t, strings.Contains(string(out), "us-central1"), "regions: %s", out)
}

// TestGcloudComputeSubnetsList_EmptyRegion lists the subnetworks of a region
// that has none. With one --regions value gcloud calls the regional route
//
//	GET /compute/v1/projects/{project}/regions/{region}/subnetworks
//
// rather than the aggregated one.
func TestGcloudComputeSubnetsList_EmptyRegion(t *testing.T) {
	t.Parallel()
	out, err := gcloudCLI("compute", "networks", "subnets", "list",
		"--regions=europe-west4",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "subnets list: %s", out)
	assert.Empty(t, strings.TrimSpace(string(out)), "subnets list: %s", out)
}
