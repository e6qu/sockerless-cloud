package gcp_cli_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// `gcloud compute packet-mirrorings` drives the policy through the real CLI.
// The CLI builds the resource's nested members — network, collectorIlb,
// mirroredResources, filter — from its own flags, so this covers the request
// shape a real operator sends rather than the one the SDK test hand-assembles.
func TestGcloudComputePacketMirroring_CRUD(t *testing.T) {
	// The policy references a network and subnetwork, and creating those
	// provisions real Linux network fabric — so this runs where that fabric
	// exists, exactly as the other gcloud Compute networking tests do.
	requireNetworkHost(t)
	const (
		region  = "us-central1"
		name    = "sim-pm-cli-1"
		network = "sim-pm-cli-net"
		subnet  = "sim-pm-cli-subnet"
		ilb     = "sim-pm-cli-ilb"
		backend = "sim-pm-cli-backend"
		health  = "sim-pm-cli-hc"
	)

	// The policy references a network, a subnetwork and the collector
	// forwarding rule; gcloud resolves each to a URL when it builds the
	// request, so they have to exist.
	out, err := gcloudCLI("compute", "networks", "create", network,
		"--subnet-mode=custom", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create network: %s", out)
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "networks", "delete", network, "--quiet").CombinedOutput()
	})

	out, err = gcloudCLI("compute", "networks", "subnets", "create", subnet,
		"--network="+network, "--region="+region, "--range=10.99.0.0/24",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create subnet: %s", out)
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "networks", "subnets", "delete", subnet,
			"--region="+region, "--quiet").CombinedOutput()
	})

	// An INTERNAL forwarding rule fronts a regional backend service, and that
	// chain is not incidental: it is what the policy's collectorIlb resolves
	// through to reach the instances the mirrored traffic is delivered to.
	out, err = gcloudCLI("compute", "health-checks", "create", "tcp", health,
		"--region="+region, "--port=80", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create health check: %s", out)
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "health-checks", "delete", health,
			"--region="+region, "--quiet").CombinedOutput()
	})

	out, err = gcloudCLI("compute", "backend-services", "create", backend,
		"--region="+region, "--load-balancing-scheme=INTERNAL", "--protocol=TCP",
		"--health-checks="+health, "--health-checks-region="+region,
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create backend service: %s", out)
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "backend-services", "delete", backend,
			"--region="+region, "--quiet").CombinedOutput()
	})

	out, err = gcloudCLI("compute", "forwarding-rules", "create", ilb,
		"--region="+region, "--load-balancing-scheme=INTERNAL",
		"--network="+network, "--subnet="+subnet,
		"--ports=80", "--backend-service="+backend,
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create collector forwarding rule: %s", out)
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "forwarding-rules", "delete", ilb,
			"--region="+region, "--quiet").CombinedOutput()
	})

	out, err = gcloudCLI("compute", "packet-mirrorings", "create", name,
		"--region="+region, "--network="+network, "--collector-ilb="+ilb,
		"--mirrored-subnets="+subnet, "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "create packet mirroring: %s", out)
	t.Cleanup(func() {
		_, _ = gcloudCLI("compute", "packet-mirrorings", "delete", name,
			"--region="+region, "--quiet").CombinedOutput()
	})

	out, err = gcloudCLI("compute", "packet-mirrorings", "describe", name,
		"--region="+region, "--format=value(name,enable)").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	assert.Contains(t, string(out), name)
	assert.Contains(t, string(out), "TRUE")

	out, err = gcloudCLI("compute", "packet-mirrorings", "list",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.Contains(t, string(out), name)

	// Disabling is the operator's way to stop a policy mirroring without
	// deleting it.
	out, err = gcloudCLI("compute", "packet-mirrorings", "update", name,
		"--region="+region, "--no-enable", "--format=value(name)").CombinedOutput()
	require.NoError(t, err, "update: %s", out)

	out, err = gcloudCLI("compute", "packet-mirrorings", "describe", name,
		"--region="+region, "--format=value(enable)").CombinedOutput()
	require.NoError(t, err, "describe after update: %s", out)
	assert.Contains(t, string(out), "FALSE")

	out, err = gcloudCLI("compute", "packet-mirrorings", "delete", name,
		"--region="+region, "--quiet").CombinedOutput()
	require.NoError(t, err, "delete: %s", out)
}
