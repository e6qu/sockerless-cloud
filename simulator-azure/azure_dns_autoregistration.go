package main

import (
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// azureAutoRegistrationTTL is the TTL Azure Private DNS gives an
// auto-registered record.
const azureAutoRegistrationTTL = 10

var azureAutoRegistrationMu sync.Mutex

// azureReconcileAutoRegisteredRecords makes every private zone's
// auto-registered A records match the virtual machines Azure registers: each
// machine whose primary interface has an address in a virtual network linked
// to the zone with registration enabled, under its computer name. A record
// set someone created by hand keeps its name; Azure registers nothing over it.
func azureReconcileAutoRegisteredRecords() {
	if azurePrivateDNSZones == nil || azurePrivateDNSVNetLinks == nil || azureVMs == nil || azureNICs == nil {
		return
	}
	azureAutoRegistrationMu.Lock()
	defer azureAutoRegistrationMu.Unlock()

	registeringZones := map[string][]string{}
	for _, link := range azurePrivateDNSVNetLinks.List() {
		if !link.Properties.RegistrationEnabled || link.Properties.VirtualNetwork == nil {
			continue
		}
		zoneID, _, ok := strings.Cut(link.ID, "/virtualNetworkLinks/")
		if !ok {
			continue
		}
		if _, exists := azurePrivateDNSZones.Get(zoneID); !exists {
			continue
		}
		vnet := strings.ToLower(link.Properties.VirtualNetwork.ID)
		registeringZones[vnet] = append(registeringZones[vnet], zoneID)
	}

	want := map[string]RecordSet{}
	for _, vm := range azureVMs.List() {
		vnet, address, ok := azureVMPrimaryAddress(vm)
		if !ok {
			continue
		}
		host := strings.ToLower(azureVMComputerName(vm))
		for _, zoneID := range registeringZones[vnet] {
			zone, _ := azurePrivateDNSZones.Get(zoneID)
			id := zoneID + "/A/" + host
			want[strings.ToLower(id)] = RecordSet{
				ID:   id,
				Name: host,
				Type: "Microsoft.Network/privateDnsZones/A",
				Properties: RecordSetProperties{
					TTL:              azureAutoRegistrationTTL,
					Fqdn:             host + "." + strings.TrimSuffix(zone.Name, ".") + ".",
					IsAutoRegistered: true,
					ARecords:         []ARecord{{IPv4Address: address}},
				},
			}
		}
	}

	for _, set := range azurePrivateDNSRecordSets.List() {
		if !set.Properties.IsAutoRegistered {
			continue
		}
		desired, keep := want[strings.ToLower(set.ID)]
		if !keep {
			azurePrivateDNSRecordSets.Delete(set.ID)
			continue
		}
		delete(want, strings.ToLower(set.ID))
		if len(set.Properties.ARecords) != 1 || set.Properties.ARecords[0].IPv4Address != desired.Properties.ARecords[0].IPv4Address {
			desired.Etag = sim.NewUUID()
			azurePrivateDNSRecordSets.Put(set.ID, desired)
		}
	}
	for _, desired := range want {
		if _, taken := azurePrivateDNSRecordSets.Get(desired.ID); taken {
			continue
		}
		desired.Etag = sim.NewUUID()
		azurePrivateDNSRecordSets.Put(desired.ID, desired)
	}
}

// azureVMComputerName is the host name a machine registers under: its OS
// profile's computer name, which defaults to the machine's name.
func azureVMComputerName(vm VirtualMachine) string {
	if name, _ := vm.Properties.OSProfile["computerName"].(string); name != "" {
		return name
	}
	return vm.Name
}

// azureVMPrimaryAddress reads the virtual network and private address of the
// machine's primary interface's primary IP configuration.
func azureVMPrimaryAddress(vm VirtualMachine) (vnet, address string, ok bool) {
	refs := vm.Properties.NetworkProfile.NetworkInterfaces
	if len(refs) == 0 {
		return "", "", false
	}
	primary := refs[0]
	for _, ref := range refs {
		if p, _ := ref.Properties["primary"].(bool); p {
			primary = ref
			break
		}
	}
	nic, found := azureNICs.Get(primary.ID)
	if !found || len(nic.Properties.IPConfigurations) == 0 {
		return "", "", false
	}
	ipcfg := azurePrimaryIPConfig(nic)
	if ipcfg.Properties.PrivateIPAddress == "" || ipcfg.Properties.Subnet == nil {
		return "", "", false
	}
	return strings.ToLower(azureVNetIDOfSubnet(ipcfg.Properties.Subnet.ID)), ipcfg.Properties.PrivateIPAddress, true
}
