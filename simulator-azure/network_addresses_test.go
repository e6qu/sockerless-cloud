package main

import (
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// Azure hands a subnet's first four addresses to itself, so the first
// interface address it leases is the fifth.
func TestAzureSubnetLeasesPastTheReservedAddresses(t *testing.T) {
	ledger, err := ipam.NewIPAMWithReserved("10.0.1.0/24", net.ParseIP("10.0.1.1"), azureSubnetReservation)
	if err != nil {
		t.Fatal(err)
	}
	if first, err := ledger.Reserve("nic", nil); err != nil || first.String() != "10.0.1.4" {
		t.Fatalf("first lease = %s, %v; want 10.0.1.4", first, err)
	}
	for _, reserved := range []string{"10.0.1.2", "10.0.1.3", "10.0.1.255"} {
		if _, err := ledger.Reserve("static", net.ParseIP(reserved)); err == nil {
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

// A network interface leases its addresses from its subnet's ledger: a
// dynamic configuration takes the next free address past the reserved ones, a
// static one takes the address it names, an address another interface holds
// is refused, a rewrite keeps the addresses of the configurations it keeps and
// frees the ones it drops first, and the ledger rebuilds from the stored
// interfaces.
func TestAzureNICLeasesFollowTheSubnetLedger(t *testing.T) {
	priorSubnets, priorNICs, priorGateways := azureSubnets, azureNICs, azureApplicationGateways
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	azureNICs = sim.MakeStore[NetworkInterface](nil, "test_nics")
	azureApplicationGateways = sim.MakeStore[ApplicationGateway](nil, "test_appgws")
	defer func() { azureSubnets, azureNICs, azureApplicationGateways = priorSubnets, priorNICs, priorGateways }()

	subnetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet/subnets/default"
	azureForgetSubnetLedger(subnetID)
	defer azureForgetSubnetLedger(subnetID)
	azureSubnets.Put(subnetID, Subnet{ID: subnetID, Properties: SubnetProperties{AddressPrefix: "10.0.1.0/24"}})

	ipcfg := func(nicID, name, method, address string) NetworkInterfaceIPConfiguration {
		return NetworkInterfaceIPConfiguration{
			ID: nicID + "/ipConfigurations/" + name, Name: name,
			Properties: NetworkInterfaceIPConfigurationProperties{
				Subnet: &SubResource{ID: subnetID}, PrivateIPAllocationMethod: method, PrivateIPAddress: address,
			},
		}
	}
	nicID := func(name string) string {
		return "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/" + name
	}
	addresses := func(nic NetworkInterface) []string {
		var out []string
		for _, c := range nic.Properties.IPConfigurations {
			out = append(out, c.Properties.PrivateIPAddress)
		}
		return out
	}

	a := NetworkInterface{ID: nicID("a"), Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{ipcfg(nicID("a"), "ipconfig1", "Dynamic", "")}}}
	if err := azureLeaseNICAddresses(&a, nil); err != nil || a.Properties.IPConfigurations[0].Properties.PrivateIPAddress != "10.0.1.4" {
		t.Fatalf("first dynamic lease = %v, %v; want 10.0.1.4", addresses(a), err)
	}
	azureNICs.Put(a.ID, a)

	taken := NetworkInterface{ID: nicID("b"), Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{ipcfg(nicID("b"), "ipconfig1", "Static", "10.0.1.4")}}}
	if err := azureLeaseNICAddresses(&taken, nil); !errors.Is(err, ipam.ErrAddressInUse) {
		t.Fatalf("a static address another interface holds leased: %v", err)
	}

	b := NetworkInterface{ID: nicID("b"), Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{
		ipcfg(nicID("b"), "ipconfig1", "Dynamic", ""), ipcfg(nicID("b"), "ipconfig2", "Static", "10.0.1.10"),
	}}}
	if err := azureLeaseNICAddresses(&b, nil); err != nil {
		t.Fatal(err)
	}
	if got := addresses(b); got[0] != "10.0.1.5" || got[1] != "10.0.1.10" {
		t.Fatalf("leases = %v, want the next free address and the static one", got)
	}
	azureNICs.Put(b.ID, b)

	rewritten := NetworkInterface{ID: nicID("b"), Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{
		ipcfg(nicID("b"), "ipconfig1", "Dynamic", ""), ipcfg(nicID("b"), "ipconfig3", "Static", "10.0.1.10"),
	}}}
	if err := azureLeaseNICAddresses(&rewritten, &b); err != nil {
		t.Fatalf("a rewrite that moves a dropped configuration's address to a new one failed: %v", err)
	}
	if got := addresses(rewritten); got[0] != "10.0.1.5" || got[1] != "10.0.1.10" {
		t.Fatalf("rewrite leases = %v, want the kept address and the moved one", got)
	}
	azureNICs.Put(rewritten.ID, rewritten)

	azureReleaseNICAddresses(a)
	azureNICs.Delete(a.ID)
	c := NetworkInterface{ID: nicID("c"), Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{ipcfg(nicID("c"), "ipconfig1", "Dynamic", "")}}}
	if err := azureLeaseNICAddresses(&c, nil); err != nil || addresses(c)[0] != "10.0.1.4" {
		t.Fatalf("a released address was not leased again: %v, %v", addresses(c), err)
	}
	azureNICs.Put(c.ID, c)

	azureForgetSubnetLedger(subnetID)
	d := NetworkInterface{ID: nicID("d"), Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{ipcfg(nicID("d"), "ipconfig1", "Dynamic", "")}}}
	if err := azureLeaseNICAddresses(&d, nil); err != nil || addresses(d)[0] != "10.0.1.6" {
		t.Fatalf("the ledger rebuilt from the store leased %v, %v; want 10.0.1.6 past the stored interfaces", addresses(d), err)
	}
}

// A virtual network's resolver sits at the second usable address of its
// lowest subnet, the address Azure reserves for Azure DNS there, and two
// virtual networks of one name in different resource groups realize two
// engine networks.
func TestAzureVNetResolverAddressAndNetworkNames(t *testing.T) {
	prior := azureSubnets
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	defer func() { azureSubnets = prior }()
	vnetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
	prefix := netip.MustParsePrefix("10.20.0.0/16")
	if got := azureVNetResolverAddress(vnetID, prefix); got != netip.MustParseAddr("10.20.0.2") {
		t.Fatalf("resolver of a virtual network with no subnet = %s, want 10.20.0.2", got)
	}
	azureSubnets.Put(vnetID+"/subnets/b", Subnet{ID: vnetID + "/subnets/b", Properties: SubnetProperties{AddressPrefix: "10.20.5.0/24"}})
	azureSubnets.Put(vnetID+"/subnets/a", Subnet{ID: vnetID + "/subnets/a", Properties: SubnetProperties{AddressPrefix: "10.20.3.0/24"}})
	if got := azureVNetResolverAddress(vnetID, prefix); got != netip.MustParseAddr("10.20.3.2") {
		t.Fatalf("resolver = %s, want the lowest subnet's .2", got)
	}
	if got := azureVNetMicroVMAgentAddress(vnetID, prefix); got != netip.MustParseAddr("10.20.3.3") {
		t.Fatalf("node agent address = %s, want the lowest subnet's .3", got)
	}
	other := "/subscriptions/sub/resourceGroups/other/providers/Microsoft.Network/virtualNetworks/vnet"
	if dockerNetForVNet(vnetID) == dockerNetForVNet(other) {
		t.Fatalf("two virtual networks named vnet share the engine network %s", dockerNetForVNet(vnetID))
	}
	if dockerNetForVNet(vnetID) != dockerNetForVNet(strings.ToUpper(vnetID)) {
		t.Fatal("the engine network's name depends on the resource id's case")
	}
}

// A connection that names a whole virtual network leases from the network's
// first subnet, and from none when the network has no subnet.
func TestAzureWholeVNetConnectionLeasesFromTheFirstSubnet(t *testing.T) {
	priorSubnets := azureSubnets
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	defer func() { azureSubnets = priorSubnets }()

	vnetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/whole"
	vnet := VirtualNetwork{ID: vnetID}
	if got := azureWholeVNetSubnet(vnet); got != "" {
		t.Fatalf("a virtual network without a subnet leases from %q", got)
	}
	for _, name := range []string{"second", "first"} {
		azureSubnets.Put(vnetID+"/subnets/"+name, Subnet{ID: vnetID + "/subnets/" + name, Name: name,
			Properties: SubnetProperties{AddressPrefix: "10.9.1.0/24"}})
	}
	if got := azureWholeVNetSubnet(vnet); got != vnetID+"/subnets/first" {
		t.Fatalf("a network listing no subnet leases from %q, want the subnet whose ID sorts first", got)
	}
	vnet.Properties.Subnets = []Subnet{{ID: vnetID + "/subnets/second"}, {ID: vnetID + "/subnets/first"}}
	subnetID := azureWholeVNetSubnet(vnet)
	if subnetID != vnetID+"/subnets/second" {
		t.Fatalf("the connection leases from %q, want the network's first listed subnet", subnetID)
	}
	azureForgetSubnetLedger(subnetID)
	defer azureForgetSubnetLedger(subnetID)
	site, err := azureLeaseWorkloadAddress(subnetID, "site")
	if err != nil {
		t.Fatal(err)
	}
	nic, err := azureLeaseSubnetAddress(subnetID, "nic", nil)
	if err != nil {
		t.Fatal(err)
	}
	if nic.String() == site {
		t.Fatalf("an interface leased the address %s the connection holds", site)
	}
}
