package gcp_cli_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// debianFamily is the Debian 12 image family of the engine's architecture,
// which an instance booted on a container backend runs.
func debianFamily() string {
	if runtime.GOARCH == "arm64" {
		return "debian-12-arm64"
	}
	return "debian-12"
}

// cliStartupScript counts its boots on the disk and reads the metadata server
// by the name every guest resolves it under, both onto the serial console, and
// records its boot as a guest attribute.
const cliStartupScript = `#!/bin/bash
n=0
[ -f /var/lib/sockerless-boots ] && n=$(cat /var/lib/sockerless-boots)
n=$((n+1))
echo "$n" >/var/lib/sockerless-boots
echo "boot=$n"
exec 3<>/dev/tcp/metadata.google.internal/80
printf 'GET /computeMetadata/v1/instance/name HTTP/1.0\r\nHost: metadata.google.internal\r\nMetadata-Flavor: Google\r\n\r\n' >&3
echo "name=$(sed -e '1,/^\r$/d' <&3)"
exec 3<>/dev/tcp/metadata.google.internal/80
printf 'PUT /computeMetadata/v1/instance/guest-attributes/startup/boot HTTP/1.0\r\nHost: metadata.google.internal\r\nMetadata-Flavor: Google\r\nContent-Length: %d\r\n\r\n%s' "${#n}" "$n" >&3
echo "guest-attribute=$(head -1 <&3 | tr -d '\r')"
`

func TestGcloudComputeInstances_Lifecycle(t *testing.T) {
	t.Parallel()
	zone := "us-central1-a"
	name := "sim-instance-cli-1"

	out, err := gcloudCLI("compute", "images", "describe-from-family", debianFamily(),
		"--project=debian-cloud", "--format=value(name,family,status)").CombinedOutput()
	require.NoError(t, err, "describe-from-family: %s", out)
	family := strings.Fields(strings.TrimSpace(string(out)))
	require.Len(t, family, 3, "describe-from-family: %s", out)
	require.True(t, strings.HasPrefix(family[0], "debian-12-bookworm-"), "the family resolves to a versioned image: %s", out)
	require.Equal(t, []string{debianFamily(), "READY"}, family[1:])

	out, err = gcloudCLI("compute", "machine-types", "describe", "e2-micro",
		"--zone="+zone, "--format=value(guestCpus,memoryMb)").CombinedOutput()
	require.NoError(t, err, "machine-types describe: %s", out)
	require.Equal(t, "2\t1024", strings.TrimSpace(string(out)), "the machine type catalog reports e2-micro's shape")

	script := filepath.Join(tmpDir, "cli-startup-script.sh")
	require.NoError(t, os.WriteFile(script, []byte(cliStartupScript), 0o644))
	out, err = gcloudCLI("compute", "instances", "create", name,
		"--zone="+zone,
		"--machine-type=e2-micro",
		"--image-family="+debianFamily(),
		"--image-project=debian-cloud",
		"--metadata-from-file=startup-script="+script,
		"--metadata=enable-guest-attributes=TRUE",
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

	// The startup script ran on the machine and wrote to its serial console,
	// where it reached the metadata server through the subnetwork's sidecar.
	console := gcloudSerialPortOutput(t, name, zone, "startup-script exit status 0")
	require.Contains(t, console, "startup-script: boot=1")
	require.Contains(t, console, "startup-script: name="+name)
	second, err := gcloudCLI("compute", "instances", "get-serial-port-output", name,
		"--zone="+zone, "--port=2").Output()
	require.NoError(t, err, "serial port 2")
	require.NotContains(t, string(second), "startup-script", "only the first serial port carries the console")

	// The guest attribute the script wrote through the metadata server reads
	// back from outside the instance.
	require.Regexp(t, `startup-script: guest-attribute=HTTP/1\.[01] 200 OK`, console)
	out, err = gcloudCLI("compute", "instances", "get-guest-attributes", name,
		"--zone="+zone, "--query-path=startup/", "--format=value(namespace,key,value)").CombinedOutput()
	require.NoError(t, err, "get-guest-attributes: %s", out)
	require.Equal(t, "startup\tboot\t1", strings.TrimSpace(string(out)))

	// Read each transition back off the instance: Compute Engine reports a stopped
	// instance as TERMINATED and a restarted one as RUNNING, so a verb that
	// returned DONE without moving the instance fails here.

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
	// The disk survived the stop: the second boot counts on from the first.
	require.Contains(t, gcloudSerialPortOutput(t, name, zone, "startup-script: boot=2"), "startup-script: boot=1")

	// An image captured from the stopped instance's boot disk.
	out, err = gcloudCLI("compute", "instances", "stop", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "stop: %s", out)
	out, err = gcloudCLI("compute", "images", "create", name+"-image",
		"--source-disk="+name, "--source-disk-zone="+zone,
		"--format=value(name,status)").CombinedOutput()
	require.NoError(t, err, "images create: %s", out)
	defer runCLI(t, gcloudCLI("compute", "images", "delete", name+"-image", "--quiet"))
	out, err = gcloudCLI("compute", "images", "describe", name+"-image",
		"--format=value(status,sourceDisk)").CombinedOutput()
	require.NoError(t, err, "images describe: %s", out)
	described := strings.Fields(strings.TrimSpace(string(out)))
	require.Len(t, described, 2, "images describe: %s", out)
	require.Equal(t, "READY", described[0])
	require.Contains(t, described[1], "/zones/"+zone+"/disks/"+name)

	out, err = gcloudCLI("compute", "instances", "delete", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)

	out, err = gcloudCLI("compute", "instances", "describe", name,
		"--zone="+zone, "--format=value(status)").CombinedOutput()
	require.Error(t, err, "describing a deleted instance must fail: %s", out)

	out, err = gcloudCLI("compute", "instances", "list",
		"--filter=zone:("+zone+")", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "instances list: %s", out)
	require.NotContains(t, string(out), name,
		"a deleted instance must not appear in the zone's instance list")
}

// gcloudSerialPortOutput reads the instance's serial console through the CLI
// until it carries marker; the console has no wait of its own.
func gcloudSerialPortOutput(t *testing.T, name, zone, marker string) string {
	t.Helper()
	var console string
	require.Eventually(t, func() bool {
		out, err := gcloudCLI("compute", "instances", "get-serial-port-output", name, "--zone="+zone).CombinedOutput()
		console = string(out)
		return err == nil && strings.Contains(console, marker)
	}, 30*time.Second, 500*time.Millisecond, "the serial console never carried %q; last output:\n%s", marker, console)
	return console
}

func computeInstanceStatus(t *testing.T, name, zone string) string {
	t.Helper()
	out, err := gcloudCLI("compute", "instances", "describe", name,
		"--zone="+zone,
		"--format=value(status)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	return strings.TrimSpace(string(out))
}

// TestGcloudComputeInstances_AsyncInsertOperation runs `gcloud compute
// instances create --async`, which prints the zone operation and returns while
// the boot runs behind it, then polls `gcloud compute operations describe`. A
// simulator that booted inside the insert request would block --async and print
// an operation already DONE.
func TestGcloudComputeInstances_AsyncInsertOperation(t *testing.T) {
	t.Parallel()
	zone := "us-central1-a"
	name := "sim-instance-cli-async"
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "instances", "delete", name, "--zone="+zone, "--quiet").CombinedOutput()
	})

	out, err := gcloudCLI("compute", "instances", "create", name,
		"--zone="+zone,
		"--machine-type=e2-micro",
		"--image-family="+debianFamily(),
		"--image-project=debian-cloud",
		"--no-address",
		"--async",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "async create: %s", out)
	// gcloud writes "Instance creation in progress for [NAME]: …/operations/…" to
	// stderr before the formatted value, so the combined output ends with the
	// operation name.
	require.Contains(t, string(out), "Instance creation in progress",
		"--async must report the operation it left running: %s", out)
	operation := lastLine(string(out))
	require.NotEmpty(t, operation, "--async must print the operation it started")
	require.True(t, strings.HasPrefix(operation, "operation-"),
		"--async prints the zone operation name; got %q (full output %s)", operation, out)

	out, err = gcloudCLI("compute", "operations", "describe", operation,
		"--zone="+zone, "--format=value(operationType,targetLink)").CombinedOutput()
	require.NoError(t, err, "operations describe: %s", out)
	described := string(out)
	require.Contains(t, described, "insert")
	require.Contains(t, described, name)

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
