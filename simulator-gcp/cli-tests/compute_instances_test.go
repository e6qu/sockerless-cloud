package gcp_cli_test

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGcloudComputeInstances_Lifecycle(t *testing.T) {
	requireNetworkHost(t)
	zone := "us-central1-a"
	name := "sim-instance-cli-1"

	out, err := gcloudCLI("compute", "instances", "create", name,
		"--zone="+zone,
		"--machine-type=e2-micro",
		"--image=debian-12",
		"--image-project=debian-cloud",
		"--no-address",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create: %s", out)

	out, err = gcloudCLI("compute", "instances", "describe", name,
		"--zone="+zone,
		"--format=value(name,status)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	body := strings.Fields(strings.TrimSpace(string(out)))
	require.Equal(t, []string{name, "RUNNING"}, body,
		"describe reports the instance the create made, running")

	// Each transition below is read back off the instance rather than trusted
	// from the command's exit status: Compute Engine reports a stopped
	// instance as TERMINATED and a restarted one as RUNNING, and a lifecycle
	// verb that returned a DONE operation without moving the instance would
	// leave the previous status in place here.

	out, err = gcloudCLI("compute", "instances", "stop", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "stop: %s", out)
	require.Equal(t, "TERMINATED", computeInstanceStatus(t, name, zone),
		"a stopped instance reports TERMINATED")

	out, err = gcloudCLI("compute", "instances", "start", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "start: %s", out)
	require.Equal(t, "RUNNING", computeInstanceStatus(t, name, zone),
		"a restarted instance reports RUNNING again")

	out, err = gcloudCLI("compute", "instances", "delete", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)

	// The deleted instance is gone from both reads: describing it fails and
	// the zone's list no longer holds it.
	out, err = gcloudCLI("compute", "instances", "describe", name,
		"--zone="+zone, "--format=value(status)").CombinedOutput()
	require.Error(t, err, "describing a deleted instance must fail: %s", out)

	out, err = gcloudCLI("compute", "instances", "list",
		"--filter=zone:("+zone+")", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "instances list: %s", out)
	require.NotContains(t, string(out), name,
		"a deleted instance must not appear in the zone's instance list")
}

// computeInstanceStatus reads one instance's lifecycle status through the CLI.
func computeInstanceStatus(t *testing.T, name, zone string) string {
	t.Helper()
	out, err := gcloudCLI("compute", "instances", "describe", name,
		"--zone="+zone,
		"--format=value(status)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	return strings.TrimSpace(string(out))
}

// TestGcloudComputeInstances_AsyncInsertOperation drives the asynchronous
// contract through the vendor CLI. `gcloud compute instances create --async`
// documents itself as "Return immediately, without waiting for the operation
// in progress to complete", so it prints the zone operation Compute Engine
// answered with and leaves the boot running behind it; `gcloud compute
// operations describe` is then the client-side poll that reads the verdict.
//
// A simulator that booted inside the insert request could not serve this at
// all: --async would still have blocked for the whole boot, and the operation
// it printed would already have been DONE.
func TestGcloudComputeInstances_AsyncInsertOperation(t *testing.T) {
	requireNetworkHost(t)
	zone := "us-central1-a"
	name := "sim-instance-cli-async"
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "instances", "delete", name, "--zone="+zone, "--quiet").CombinedOutput()
	})

	out, err := gcloudCLI("compute", "instances", "create", name,
		"--zone="+zone,
		"--machine-type=e2-micro",
		"--image=debian-12",
		"--image-project=debian-cloud",
		"--no-address",
		"--async",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "async create: %s", out)
	// gcloud reports the operation it started on stderr before printing the
	// formatted value, and says so in as many words: "Instance creation in
	// progress for [NAME]: …/operations/operation-xxxx. Use [gcloud compute
	// operations describe URI] command to check the status of the
	// operation(s)." runCLI-style combined output therefore carries the notes
	// first and the operation name last.
	require.Contains(t, string(out), "Instance creation in progress",
		"--async must report the operation it left running: %s", out)
	operation := lastLine(string(out))
	require.NotEmpty(t, operation, "--async must print the operation it started")
	require.True(t, strings.HasPrefix(operation, "operation-"),
		"--async prints the zone operation name; got %q (full output %s)", operation, out)

	// The operation exists and describes itself as an instance insert.
	out, err = gcloudCLI("compute", "operations", "describe", operation,
		"--zone="+zone, "--format=value(operationType,targetLink)").CombinedOutput()
	require.NoError(t, err, "operations describe: %s", out)
	described := string(out)
	require.Contains(t, described, "insert")
	require.Contains(t, described, name)

	// Poll it to DONE the way a client that used --async does.
	require.Eventually(t, func() bool {
		status, err := gcloudCLI("compute", "operations", "describe", operation,
			"--zone="+zone, "--format=value(status)").CombinedOutput()
		return err == nil && strings.TrimSpace(string(status)) == "DONE"
	}, 5*time.Minute, 2*time.Second, "the insert operation never reached DONE")

	out, err = gcloudCLI("compute", "instances", "describe", name,
		"--zone="+zone, "--format=value(status)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	require.Equal(t, "RUNNING", strings.TrimSpace(string(out)),
		"the instance must be running once its insert operation is DONE")

	// The zone's operation list holds the operation rather than an empty set.
	out, err = gcloudCLI("compute", "operations", "list",
		"--filter=zone:("+zone+")", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "operations list: %s", out)
	require.Contains(t, string(out), operation,
		"the zone's operation list must hold the operation the insert handed out")
}
