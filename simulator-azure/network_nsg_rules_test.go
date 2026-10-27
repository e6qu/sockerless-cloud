package main

import (
	"net/netip"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

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

func TestAzureAdmitIngressAppliesEveryStageInPriorityOrder(t *testing.T) {
	priorNSGs, priorSubnets, priorVnets, priorNICs := azureNSGs, azureSubnets, azureVnets, azureNICs
	azureNSGs = sim.MakeStore[NetworkSecurityGroup](nil, "test_nsgs")
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	azureVnets = sim.MakeStore[VirtualNetwork](nil, "test_vnets")
	azureNICs = sim.MakeStore[NetworkInterface](nil, "test_nics")
	defer func() {
		azureNSGs, azureSubnets, azureVnets, azureNICs = priorNSGs, priorSubnets, priorVnets, priorNICs
	}()

	vnetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
	subnetID := vnetID + "/subnets/default"
	nsgID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/web"
	azureVnets.Put(vnetID, VirtualNetwork{ID: vnetID, Properties: VNetProperties{AddressSpace: AddressSpace{AddressPrefixes: []string{"10.0.0.0/16"}}}})
	azureSubnets.Put(subnetID, Subnet{ID: subnetID, Properties: SubnetProperties{AddressPrefix: "10.0.1.0/24", NetworkSecurityGroup: &NSGReference{ID: nsgID}}})
	azureNSGs.Put(nsgID, NetworkSecurityGroup{ID: nsgID, Properties: NSGProperties{SecurityRules: []SecurityRule{
		{Name: "deny-ssh", Properties: SecurityRuleProperties{Protocol: "Tcp", SourceAddressPrefix: "*", DestinationPortRange: "22", Access: "Deny", Priority: 100, Direction: "Inbound"}},
		{Name: "allow-web", Properties: SecurityRuleProperties{Protocol: "Tcp", SourceAddressPrefix: "Internet", DestinationPortRanges: []string{"80", "443"}, Access: "Allow", Priority: 200, Direction: "Inbound"}},
	}}})
	nicID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic"
	azureNICs.Put(nicID, NetworkInterface{ID: nicID, Properties: NetworkInterfaceProperties{
		IPConfigurations: []NetworkInterfaceIPConfiguration{{Properties: NetworkInterfaceIPConfigurationProperties{
			Subnet: &SubResource{ID: subnetID}, PrivateIPAddress: "10.0.1.4",
		}}},
	}})

	client := netip.MustParseAddr("198.51.100.7")
	if err := azureAdmitIngress("Tcp", client, "10.0.1.4:80", 80); err != nil {
		t.Fatalf("tcp/80 from the Internet was refused: %v", err)
	}
	if err := azureAdmitIngress("Tcp", client, "10.0.1.4:22", 22); err == nil {
		t.Fatal("tcp/22 passed the deny rule")
	}
	if err := azureAdmitIngress("Tcp", client, "10.0.1.4:8080", 8080); err == nil {
		t.Fatal("tcp/8080 passed DenyAllInBound")
	}
	if err := azureAdmitIngress("Tcp", netip.MustParseAddr("10.0.5.9"), "10.0.1.4:8080", 8080); err != nil {
		t.Fatalf("tcp/8080 from the virtual network was refused by the default AllowVnetInBound: %v", err)
	}
	if err := azureAdmitIngress("Tcp", client, "10.0.9.9:22", 22); err != nil {
		t.Fatalf("a target no interface holds is not the simulator's to filter: %v", err)
	}
}
