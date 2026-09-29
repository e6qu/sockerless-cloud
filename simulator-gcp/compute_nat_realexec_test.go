//go:build realexec_host && linux

package main

import (
	"context"
	"testing"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/sim"
)

// A Cloud NAT gateway without NAT addresses of its own reserves one address,
// keeps it across reconfiguration, and returns it when the router goes.
func TestGCPRouterNATReleasesItsReservedAddress(t *testing.T) {
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		t.Fatalf("the Cloud NAT fabric test needs a real network host: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	gcpSubnetworks = sim.MakeStore[ComputeSubnetwork](nil, "test_subnetworks")
	gcpAddresses = sim.MakeStore[ComputeAddress](nil, "test_addresses")

	network := "projects/natp/global/networks/natnet"
	subnet := ComputeSubnetwork{
		SelfLink:       "projects/natp/regions/us-central1/subnetworks/natsub",
		Network:        "https://www.googleapis.com/compute/v1/" + network,
		IpCidrRange:    "10.207.0.0/24",
		GatewayAddress: gcpSubnetGateway("10.207.0.0/24"),
	}
	gcpSubnetworks.Put(subnet.SelfLink, subnet)
	if err := gcpCreateRealSubnetwork(ctx, subnet); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gcpFabric.TeardownNetwork(context.Background(), network, nil) })

	router := ComputeRouter{
		SelfLink: "projects/natp/regions/us-central1/routers/natrouter",
		Network:  network,
		Nats:     []ComputeRouterNAT{{Name: "nat", SourceSubnetworkIpRangesToNat: "ALL_SUBNETWORKS_ALL_IP_RANGES"}},
	}
	if err := gcpConfigureRealRouterNAT(ctx, router); err != nil {
		t.Fatal(err)
	}
	owner := gcpRouterNATOwner(router.SelfLink, "nat")
	reserved := gcpFabric.OwnedPublicIP(owner)
	if reserved == nil {
		t.Fatal("the gateway translated without reserving an address")
	}
	if err := gcpConfigureRealRouterNAT(ctx, router); err != nil {
		t.Fatal(err)
	}
	if again := gcpFabric.OwnedPublicIP(owner); !again.Equal(reserved) {
		t.Fatalf("reconfiguring reserved %s besides %s", again, reserved)
	}
	if err := gcpReleaseRealRouterNAT(ctx, router, nil); err != nil {
		t.Fatal(err)
	}
	if gcpFabric.OwnedPublicIP(owner) != nil {
		t.Fatal("the deleted router's gateway still owns its address")
	}
	lease, err := realexec.ReserveGCPPublicIPv4("next", reserved)
	if err != nil {
		t.Fatalf("the router's address did not return to the pool: %v", err)
	}
	realexec.ReleasePublicIPv4(lease)
}
