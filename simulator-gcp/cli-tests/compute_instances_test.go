package gcp_cli_test

import (
	"strings"
	"testing"

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
	body := strings.ToLower(string(out))
	require.Contains(t, body, name)
	require.Contains(t, body, "running")

	out, err = gcloudCLI("compute", "instances", "stop", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "stop: %s", out)

	out, err = gcloudCLI("compute", "instances", "start", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "start: %s", out)

	out, err = gcloudCLI("compute", "instances", "delete", name,
		"--zone="+zone,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)
}
