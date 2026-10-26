package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGcloudComputePacketMirroring_CRUD drives `gcloud compute
// packet-mirrorings`. gcloud builds network, collectorIlb and mirroredResources
// from its own flags, resolving each to a resource URL, so the described policy
// must resolve each member to the resource its flag named.
func TestGcloudComputePacketMirroring_CRUD(t *testing.T) {
	t.Parallel()
	const (
		region  = "us-central1"
		name    = "sim-pm-cli-1"
		network = "sim-pm-cli-net"
		subnet  = "sim-pm-cli-subnet"
		ilb     = "sim-pm-cli-ilb"
		backend = "sim-pm-cli-backend"
		health  = "sim-pm-cli-hc"
	)

	// gcloud resolves each referenced resource to a URL when it builds the
	// request, so they must exist.
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

	// The policy's collectorIlb resolves through this INTERNAL forwarding rule and
	// its regional backend service to the instances that receive mirrored traffic.
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
		"--region="+region, "--format=json").CombinedOutput()
	require.NoError(t, err, "describe: %s", out)
	var policy struct {
		Name    string `json:"name"`
		Enable  string `json:"enable"`
		Network struct {
			URL string `json:"url"`
		} `json:"network"`
		CollectorIlb struct {
			URL string `json:"url"`
		} `json:"collectorIlb"`
		MirroredResources struct {
			Subnetworks []struct {
				URL string `json:"url"`
			} `json:"subnetworks"`
		} `json:"mirroredResources"`
	}
	parseDescribedResource(t, string(out), &policy)
	assert.Equal(t, name, policy.Name)
	assert.Equal(t, "TRUE", policy.Enable)
	assert.True(t, strings.HasSuffix(policy.Network.URL, "/networks/"+network),
		"the policy's network resolves to the network the flag named: %q", policy.Network.URL)
	assert.True(t, strings.HasSuffix(policy.CollectorIlb.URL, "/forwardingRules/"+ilb),
		"the policy's collector resolves to the internal load balancer: %q", policy.CollectorIlb.URL)
	require.Len(t, policy.MirroredResources.Subnetworks, 1,
		"--mirrored-subnets builds one mirrored subnetwork: %s", out)
	assert.True(t, strings.HasSuffix(policy.MirroredResources.Subnetworks[0].URL, "/subnetworks/"+subnet),
		"the mirrored subnetwork resolves to the subnet the flag named: %q",
		policy.MirroredResources.Subnetworks[0].URL)

	out, err = gcloudCLI("compute", "packet-mirrorings", "list",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "list: %s", out)
	assert.Contains(t, string(out), name)

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
