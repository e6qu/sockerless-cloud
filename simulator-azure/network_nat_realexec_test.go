//go:build realexec_host && linux

package main

import (
	"context"
	"strings"
	"testing"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/sim"
)

// A subnet's translation follows its NAT gateway association, and a gateway
// built on a public IP prefix returns the address it took when it is deleted.
func TestAzureNATGatewayFollowsItsSubnetAssociation(t *testing.T) {
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		t.Fatalf("the NAT gateway fabric test needs a real network host: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	priorVnets, priorSubnets, priorNATs := azureVnets, azureSubnets, azureNatGateways
	azureVnets = sim.MakeStore[VirtualNetwork](nil, "test_vnets")
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	azureNatGateways = sim.MakeStore[NatGateway](nil, "test_natgws")
	defer func() { azureVnets, azureSubnets, azureNatGateways = priorVnets, priorSubnets, priorNATs }()

	vnetID := "/subscriptions/s/resourceGroups/natrg/providers/Microsoft.Network/virtualNetworks/natvnet"
	gwID := "/subscriptions/s/resourceGroups/natrg/providers/Microsoft.Network/natGateways/natgw"
	azureVnets.Put(vnetID, VirtualNetwork{ID: vnetID, Properties: VNetProperties{AddressSpace: AddressSpace{AddressPrefixes: []string{"10.208.0.0/16"}}}})
	azureNatGateways.Put(gwID, NatGateway{ID: gwID, Properties: NatGatewayProps{PublicIPPrefixes: []SubResource{{ID: gwID + "-prefix"}}}})
	subnet := Subnet{ID: vnetID + "/subnets/private", Properties: SubnetProperties{AddressPrefix: "10.208.1.0/24", NatGateway: &SubResource{ID: gwID}}}
	azureSubnets.Put(subnet.ID, subnet)
	t.Cleanup(func() { _ = azureFabric.TeardownNetwork(context.Background(), vnetID, nil) })

	if err := azureCreateRealSubnet(ctx, subnet); err != nil {
		t.Fatal(err)
	}
	ip := azureFabric.OwnedPublicIP(gwID)
	if ip == nil {
		t.Fatal("the gateway translated without taking an address from its prefix")
	}
	ruleset := func() string {
		out, err := realexec.Runner{}.Output(ctx, "ip", "netns", "exec", azureFabric.Network(vnetID).NamespaceName, "nft", "list", "ruleset")
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !strings.Contains(ruleset(), ip.String()) {
		t.Fatalf("no translation to %s:\n%s", ip, ruleset())
	}

	subnet.Properties.NatGateway = nil
	azureSubnets.Put(subnet.ID, subnet)
	if err := azureCreateRealSubnet(ctx, subnet); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(ruleset(), "snat") {
		t.Fatalf("a subnet dissociated from its NAT gateway still translates:\n%s", ruleset())
	}

	if err := azureDeleteRealNATGateway(ctx, gwID); err != nil {
		t.Fatal(err)
	}
	lease, err := realexec.ReserveAzurePublicIPv4("next", ip)
	if err != nil {
		t.Fatalf("the deleted gateway's address did not return to the pool: %v", err)
	}
	realexec.ReleasePublicIPv4(lease)
}
