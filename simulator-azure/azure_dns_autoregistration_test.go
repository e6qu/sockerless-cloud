package main

import (
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A virtual machine in a virtual network linked to a private zone with
// registration enabled has an A record under its computer name, which follows
// the link, the machine and its address, and never replaces a record someone
// created by hand.
func TestAzureAutoRegistrationFollowsLinksAndMachines(t *testing.T) {
	zones, sets, links, vms, nics := azurePrivateDNSZones, azurePrivateDNSRecordSets, azurePrivateDNSVNetLinks, azureVMs, azureNICs
	t.Cleanup(func() {
		azurePrivateDNSZones, azurePrivateDNSRecordSets, azurePrivateDNSVNetLinks, azureVMs, azureNICs = zones, sets, links, vms, nics
	})
	azurePrivateDNSZones = sim.MakeStore[PrivateDnsZone](nil, "dns_zones")
	azurePrivateDNSRecordSets = sim.MakeStore[RecordSet](nil, "dns_record_sets")
	azurePrivateDNSVNetLinks = sim.MakeStore[VNetLink](nil, "dns_vnet_links")
	azureVMs = sim.MakeStore[VirtualMachine](nil, "compute_virtual_machines")
	azureNICs = sim.MakeStore[NetworkInterface](nil, "network_interfaces")

	const rg = "/subscriptions/sub/resourceGroups/rg/providers"
	vnet := rg + "/Microsoft.Network/virtualNetworks/app"
	zoneID := rg + "/Microsoft.Network/privateDnsZones/corp.internal"
	linkID := zoneID + "/virtualNetworkLinks/app"
	nicID := rg + "/Microsoft.Network/networkInterfaces/web-nic"
	vmID := rg + "/Microsoft.Compute/virtualMachines/web"
	azurePrivateDNSZones.Put(zoneID, PrivateDnsZone{ID: zoneID, Name: "corp.internal"})
	azureNICs.Put(nicID, NetworkInterface{ID: nicID, Properties: NetworkInterfaceProperties{
		IPConfigurations: []NetworkInterfaceIPConfiguration{{Name: "ipconfig1", Properties: NetworkInterfaceIPConfigurationProperties{
			Subnet:           &SubResource{ID: vnet + "/subnets/default"},
			PrivateIPAddress: "10.4.0.5",
		}}},
	}})
	azureVMs.Put(vmID, VirtualMachine{ID: vmID, Name: "web", Properties: VMProperties{
		OSProfile:      map[string]any{"computerName": "Web01"},
		NetworkProfile: VMNetworkProfile{NetworkInterfaces: []VMNetworkInterfaceRef{{ID: nicID}}},
	}})
	record := func(name string) (RecordSet, bool) {
		return azurePrivateDNSRecordSets.Get(zoneID + "/A/" + name)
	}

	azurePrivateDNSVNetLinks.Put(linkID, VNetLink{ID: linkID, Properties: VNetLinkProperties{VirtualNetwork: &VNetLinkVNet{ID: vnet}}})
	azureReconcileAutoRegisteredRecords()
	if _, ok := record("web01"); ok {
		t.Fatal("a link without registration registers nothing")
	}

	azurePrivateDNSVNetLinks.Update(linkID, func(l *VNetLink) { l.Properties.RegistrationEnabled = true })
	azureReconcileAutoRegisteredRecords()
	got, ok := record("web01")
	if !ok || !got.Properties.IsAutoRegistered || got.Properties.TTL != azureAutoRegistrationTTL ||
		len(got.Properties.ARecords) != 1 || got.Properties.ARecords[0].IPv4Address != "10.4.0.5" ||
		got.Properties.Fqdn != "web01.corp.internal." {
		t.Fatalf("registered record = %+v, %v", got, ok)
	}
	served := azurePrivateRecordSetRRs("corp.internal.", got)
	if len(served) != 1 || served[0].String() != "web01.corp.internal.\t10\tIN\tA\t10.4.0.5" {
		t.Fatalf("served as %v", served)
	}

	azureNICs.Update(nicID, func(n *NetworkInterface) { n.Properties.IPConfigurations[0].Properties.PrivateIPAddress = "10.4.0.9" })
	azureReconcileAutoRegisteredRecords()
	if got, _ := record("web01"); got.Properties.ARecords[0].IPv4Address != "10.4.0.9" {
		t.Fatalf("the record follows the address: %+v", got)
	}

	manualID := zoneID + "/A/db01"
	azurePrivateDNSRecordSets.Put(manualID, RecordSet{ID: manualID, Name: "db01", Properties: RecordSetProperties{TTL: 300, ARecords: []ARecord{{IPv4Address: "10.4.0.200"}}}})
	dbID := rg + "/Microsoft.Compute/virtualMachines/db01"
	azureVMs.Put(dbID, VirtualMachine{ID: dbID, Name: "db01", Properties: VMProperties{
		NetworkProfile: VMNetworkProfile{NetworkInterfaces: []VMNetworkInterfaceRef{{ID: nicID}}},
	}})
	azureReconcileAutoRegisteredRecords()
	if got, _ := record("db01"); got.Properties.IsAutoRegistered || got.Properties.ARecords[0].IPv4Address != "10.4.0.200" {
		t.Fatalf("a record created by hand stays: %+v", got)
	}

	azureVMs.Delete(vmID)
	azureReconcileAutoRegisteredRecords()
	if _, ok := record("web01"); ok {
		t.Fatal("a deleted machine's record goes")
	}
	azureVMs.Put(vmID, VirtualMachine{ID: vmID, Name: "web", Properties: VMProperties{
		NetworkProfile: VMNetworkProfile{NetworkInterfaces: []VMNetworkInterfaceRef{{ID: nicID}}},
	}})
	azureReconcileAutoRegisteredRecords()
	if _, ok := record("web"); !ok {
		t.Fatal("a machine without a computer name registers under its own name")
	}
	azurePrivateDNSVNetLinks.Delete(linkID)
	azureReconcileAutoRegisteredRecords()
	if _, ok := record("web"); ok {
		t.Fatal("deleting the link removes its registrations")
	}
	if _, ok := record("db01"); !ok {
		t.Fatal("deleting the link leaves records created by hand")
	}
}
