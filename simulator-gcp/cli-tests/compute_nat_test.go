package gcp_cli_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGcloudComputeAddressAndRouterNAT(t *testing.T) {
	requireNetworkHost(t)
	region := "us-central1"
	network := "cli-nat-network"
	address := "cli-nat-address"
	router := "cli-nat-router"
	nat := "cli-manual-nat"

	out, err := gcloudCLI("compute", "networks", "create", network,
		"--subnet-mode=custom",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "network create: %s", out)

	out, err = gcloudCLI("compute", "addresses", "create", address,
		"--region="+region,
		"--network-tier=PREMIUM",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "address create: %s", out)

	out, err = gcloudCLI("compute", "addresses", "describe", address,
		"--region="+region,
		"--format=value(name,status,address)").CombinedOutput()
	require.NoError(t, err, "address describe: %s", out)
	body := strings.ToLower(string(out))
	require.Contains(t, body, address)
	require.Contains(t, body, "reserved")

	out, err = gcloudCLI("compute", "routers", "create", router,
		"--region="+region,
		"--network="+network,
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "router create: %s", out)

	out, err = gcloudCLI("compute", "routers", "nats", "create", nat,
		"--router="+router,
		"--region="+region,
		"--nat-external-ip-pool="+address,
		"--nat-custom-subnet-ip-ranges=all",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "nat create: %s", out)

	// The NAT the CLI patched onto the router is read back off the router:
	// gcloud turns --nat-external-ip-pool into MANUAL_ONLY plus the resolved
	// address URL, and resolves each --nat-custom-subnet-ip-ranges entry to a
	// subnetwork of the region, which the router has to be carrying.
	described := computeRouterJSON(t, router, region)
	require.Len(t, described.Nats, 1, "the router carries the NAT the CLI created")
	created := described.Nats[0]
	require.Equal(t, nat, created.Name)
	require.Equal(t, "MANUAL_ONLY", created.NatIPAllocateOption,
		"--nat-external-ip-pool is the manual allocation mode")
	require.Len(t, created.NatIPs, 1)
	require.True(t, strings.HasSuffix(created.NatIPs[0], "/addresses/"+address),
		"the NAT holds the reserved address it was pooled with: %q", created.NatIPs[0])
	require.Equal(t, "LIST_OF_SUBNETWORKS", created.SourceSubnetworkIPRangesToNat)
	require.Len(t, created.Subnetworks, 1)
	require.True(t, strings.HasSuffix(created.Subnetworks[0].Name, "/subnetworks/all"),
		"the NAT holds the subnetwork the flag named: %q", created.Subnetworks[0].Name)

	out, err = gcloudCLI("compute", "routers", "get-status", router,
		"--region="+region,
		"--format=json").CombinedOutput()
	require.NoError(t, err, "router get-status: %s", out)
	var status struct {
		Kind   string `json:"kind"`
		Result struct {
			Network string `json:"network"`
		} `json:"result"`
	}
	parseJSONObject(t, string(out), &status)
	require.Equal(t, "compute#routerStatusResponse", status.Kind)
	require.True(t, strings.HasSuffix(status.Result.Network, "/networks/"+network),
		"the status reports the router's network: %q", status.Result.Network)

	out, err = gcloudCLI("compute", "routers", "nats", "delete", nat,
		"--router="+router,
		"--region="+region,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "nat delete: %s", out)
	require.Empty(t, computeRouterJSON(t, router, region).Nats,
		"the deleted NAT is gone from the router")

	out, err = gcloudCLI("compute", "routers", "delete", router,
		"--region="+region,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "router delete: %s", out)
	out, err = gcloudCLI("compute", "routers", "describe", router,
		"--region="+region, "--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted router must fail: %s", out)

	out, err = gcloudCLI("compute", "addresses", "delete", address,
		"--region="+region,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "address delete: %s", out)
	out, err = gcloudCLI("compute", "addresses", "describe", address,
		"--region="+region, "--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted address must fail: %s", out)

	// The network the router was built on is torn down too, so the test leaves
	// nothing behind on the host's real network fabric.
	out, err = gcloudCLI("compute", "networks", "delete", network, "--quiet").CombinedOutput()
	require.NoError(t, err, "network delete: %s", out)
	out, err = gcloudCLI("compute", "networks", "describe", network, "--format=json").CombinedOutput()
	require.Error(t, err, "describing a deleted network must fail: %s", out)
}

// computeRouterNAT is the subset of compute#routerNat this suite reads back.
type computeRouterNAT struct {
	Name                          string   `json:"name"`
	NatIPAllocateOption           string   `json:"natIpAllocateOption"`
	NatIPs                        []string `json:"natIps"`
	SourceSubnetworkIPRangesToNat string   `json:"sourceSubnetworkIpRangesToNat"`
	Subnetworks                   []struct {
		Name string `json:"name"`
	} `json:"subnetworks"`
}

// computeRouter is the subset of compute#router this suite reads back.
type computeRouter struct {
	Name string             `json:"name"`
	Nats []computeRouterNAT `json:"nats"`
}

// computeRouterJSON describes a Cloud Router through the CLI.
func computeRouterJSON(t *testing.T, router, region string) computeRouter {
	t.Helper()
	out, err := gcloudCLI("compute", "routers", "describe", router,
		"--region="+region, "--format=json").CombinedOutput()
	require.NoError(t, err, "router describe: %s", out)
	var described computeRouter
	parseJSONObject(t, string(out), &described)
	require.Equal(t, router, described.Name)
	return described
}

// TestGcloudComputeSubnetsList exercises compute.subnetworks.list — the
// regional list the CLI calls for `gcloud compute networks subnets list
// --region`:
//
//	GET /compute/v1/projects/{project}/regions/{region}/subnetworks
func TestGcloudComputeSubnetsList(t *testing.T) {
	requireNetworkHost(t)
	region := "us-central1"
	network := "cli-subnet-list-net"
	subnet := "cli-subnet-list-a"

	out, err := gcloudCLI("compute", "networks", "create", network,
		"--subnet-mode=custom",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "network create: %s", out)

	out, err = gcloudCLI("compute", "networks", "subnets", "create", subnet,
		"--network="+network,
		"--region="+region,
		"--range=10.62.0.0/24",
		"--format=value(name)").CombinedOutput()
	require.NoError(t, err, "subnet create: %s", out)

	out, err = gcloudCLI("compute", "networks", "subnets", "list",
		"--regions="+region,
		"--format=value(name,ipCidrRange)").CombinedOutput()
	require.NoError(t, err, "subnet list: %s", out)
	require.Contains(t, string(out), subnet)
	require.Contains(t, string(out), "10.62.0.0/24")

	out, err = gcloudCLI("compute", "networks", "subnets", "delete", subnet,
		"--region="+region, "--quiet").CombinedOutput()
	require.NoError(t, err, "subnet delete: %s", out)

	out, err = gcloudCLI("compute", "networks", "delete", network, "--quiet").CombinedOutput()
	require.NoError(t, err, "network delete: %s", out)
}
