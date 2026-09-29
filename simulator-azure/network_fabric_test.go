package main

import (
	"net"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/sim"
)

// A NIC behind a subnet NSG and an NSG of its own is filtered by both, the
// subnet's first: a packet reaches it only when each group allows it.
func TestAzureNSGsOnSubnetAndNICCompileToOrderedStages(t *testing.T) {
	priorNSGs, priorSubnets, priorVnets := azureNSGs, azureSubnets, azureVnets
	azureNSGs = sim.MakeStore[NetworkSecurityGroup](nil, "test_nsgs")
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	azureVnets = sim.MakeStore[VirtualNetwork](nil, "test_vnets")
	defer func() { azureNSGs, azureSubnets, azureVnets = priorNSGs, priorSubnets, priorVnets }()

	vnetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
	subnetID := vnetID + "/subnets/default"
	subnetNSG := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/subnet"
	nicNSG := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/nic"
	azureVnets.Put(vnetID, VirtualNetwork{ID: vnetID, Properties: VNetProperties{AddressSpace: AddressSpace{AddressPrefixes: []string{"10.0.0.0/16"}}}})
	azureSubnets.Put(subnetID, Subnet{ID: subnetID, Properties: SubnetProperties{AddressPrefix: "10.0.1.0/24", NetworkSecurityGroup: &NSGReference{ID: subnetNSG}}})
	allow := func(name, port string) SecurityRule {
		return SecurityRule{Name: name, Properties: SecurityRuleProperties{
			Protocol: "Tcp", SourceAddressPrefix: "*", DestinationPortRange: port, Access: "Allow", Priority: 100, Direction: "Inbound",
		}}
	}
	azureNSGs.Put(subnetNSG, NetworkSecurityGroup{ID: subnetNSG, Properties: NSGProperties{SecurityRules: []SecurityRule{allow("ssh", "22")}}})
	azureNSGs.Put(nicNSG, NetworkSecurityGroup{ID: nicNSG, Properties: NSGProperties{SecurityRules: []SecurityRule{allow("https", "443")}}})
	nic := NetworkInterface{
		ID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic",
		Properties: NetworkInterfaceProperties{
			NetworkSecurityGroup: &SubResource{ID: nicNSG},
			IPConfigurations: []NetworkInterfaceIPConfiguration{{
				Properties: NetworkInterfaceIPConfigurationProperties{Subnet: &SubResource{ID: subnetID}},
			}},
		},
	}
	stages, err := azureIngressPacketStages(nic)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 2 {
		t.Fatalf("stages = %d, want one per NSG", len(stages))
	}
	if stages[0][0].FromPort != 22 || stages[1][0].FromPort != 443 {
		t.Fatalf("stages = %+v, want the subnet NSG's tcp/22 first and the NIC NSG's tcp/443 second", stages)
	}
}

func TestAzureSecurityRuleRejectsAnUnparseablePort(t *testing.T) {
	if port := azureInvalidSecurityRulePort(SecurityRuleProperties{DestinationPortRanges: []string{"80", "8o8o"}}); port != "8o8o" {
		t.Fatalf("invalid port = %q, want 8o8o", port)
	}
	if port := azureInvalidSecurityRulePort(SecurityRuleProperties{DestinationPortRange: "*"}); port != "" {
		t.Fatalf("* rejected as %q", port)
	}
	if _, err := azurePacketRulesForSecurityRule(SecurityRuleProperties{Protocol: "Tcp", DestinationPortRange: "70000"}, "accept"); err == nil {
		t.Fatal("a port above 65535 compiled into a packet rule")
	}
}

// Azure hands a subnet's first four addresses to itself, so the first
// interface address it leases is the fifth.
func TestAzureSubnetLeasesPastTheReservedAddresses(t *testing.T) {
	ipam, err := realexec.NewIPAMWithReserved("10.0.1.0/24", net.ParseIP("10.0.1.1"), azureSubnetReservation)
	if err != nil {
		t.Fatal(err)
	}
	if first, err := ipam.Reserve("nic", nil); err != nil || first.String() != "10.0.1.4" {
		t.Fatalf("first lease = %s, %v; want 10.0.1.4", first, err)
	}
	for _, reserved := range []string{"10.0.1.2", "10.0.1.3", "10.0.1.255"} {
		if _, err := ipam.Reserve("static", net.ParseIP(reserved)); err == nil {
			t.Fatalf("reserved %s was leased", reserved)
		}
	}
}

func TestAzureNICMACSeparatesPermutedIDs(t *testing.T) {
	a := azureNICMAC("/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic-ab")
	b := azureNICMAC("/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic-ba")
	if a == b {
		t.Fatalf("interfaces nic-ab and nic-ba share MAC %s", a)
	}
}
