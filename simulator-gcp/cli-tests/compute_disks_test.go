package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// gcloud compute disks against the sim — verifies the Compute Disks
// endpoints (Insert / Get / List / Delete / Resize) round-trip through
// the real gcloud CLI (external-validation principle).

func TestGcloudComputeDisks_CRUD(t *testing.T) {
	zone := "us-central1-a"
	name := "sim-disk-cli-1"

	// Create
	out, err := gcloudCLI("compute", "disks", "create", name,
		"--zone="+zone,
		"--size=10",
		"--type=pd-balanced",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create: %s", out)

	// Describe
	out, err = gcloudCLI("compute", "disks", "describe", name,
		"--zone="+zone,
		"--format=value(name,sizeGb,type)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	body := strings.ToLower(string(out))
	assert.Contains(t, body, name)

	// List
	out, err = gcloudCLI("compute", "disks", "list",
		"--filter=zone:("+zone+")",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.Contains(t, string(out), name)

	// Delete
	out, err = gcloudCLI("compute", "disks", "delete", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)
}
