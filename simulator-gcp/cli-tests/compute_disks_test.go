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

	// Describe — the disk carries the size and type the create asked for, not
	// just the name it was addressed by.
	described := computeDiskJSON(t, name, zone)
	assert.Equal(t, name, described.Name)
	assert.Equal(t, "10", described.SizeGb)
	assert.True(t, strings.HasSuffix(described.Type, "/diskTypes/pd-balanced"),
		"the disk is the type the create asked for: %q", described.Type)
	assert.Equal(t, "READY", described.Status)

	// List
	out, err = gcloudCLI("compute", "disks", "list",
		"--filter=zone:("+zone+")",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.Contains(t, string(out), name)

	// Resize — the new size is read back off the disk, so a resize that
	// answered DONE without growing the disk fails here.
	out, err = gcloudCLI("compute", "disks", "resize", name,
		"--zone="+zone,
		"--size=20",
		"--quiet").CombinedOutput()
	require.NoError(t, err, "resize: %s", out)
	assert.Equal(t, "20", computeDiskJSON(t, name, zone).SizeGb,
		"the resize moved the disk's size")

	// Delete
	out, err = gcloudCLI("compute", "disks", "delete", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)

	out, err = gcloudCLI("compute", "disks", "describe", name,
		"--zone="+zone, "--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted disk must fail: %s", out)
}

// computeDisk is the subset of compute#disk this suite reads back. sizeGb is
// int64-as-string on the wire, the way the Compute Engine discovery document
// declares it.
type computeDisk struct {
	Name   string `json:"name"`
	SizeGb string `json:"sizeGb"`
	Type   string `json:"type"`
	Status string `json:"status"`
}

// computeDiskJSON describes one persistent disk through the CLI.
func computeDiskJSON(t *testing.T, name, zone string) computeDisk {
	t.Helper()
	out, err := gcloudCLI("compute", "disks", "describe", name,
		"--zone="+zone,
		"--format=json").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	var disk computeDisk
	parseJSONObject(t, string(out), &disk)
	return disk
}
