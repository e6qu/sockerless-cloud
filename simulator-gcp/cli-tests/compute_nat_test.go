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

	out, err = gcloudCLI("compute", "routers", "get-status", router,
		"--region="+region,
		"--format=json").CombinedOutput()
	require.NoError(t, err, "router get-status: %s", out)

	out, err = gcloudCLI("compute", "routers", "nats", "delete", nat,
		"--router="+router,
		"--region="+region,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "nat delete: %s", out)

	out, err = gcloudCLI("compute", "routers", "delete", router,
		"--region="+region,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "router delete: %s", out)

	out, err = gcloudCLI("compute", "addresses", "delete", address,
		"--region="+region,
		"--quiet").CombinedOutput()
	require.NoError(t, err, "address delete: %s", out)
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
