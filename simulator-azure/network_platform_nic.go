package main

import (
	"context"
	"fmt"
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Several Microsoft.Network resources own network interfaces they did not ask
// the caller to create: a private endpoint gets one interface in the subnet it
// projects the linked service into, and a private link service gets one per NAT
// IP configuration. Azure exposes those interfaces under the ordinary
// Microsoft.Network/networkInterfaces type — they answer Get, they appear in
// the interface list, and their addresses come out of the subnet's own address
// space. The simulator realizes them the same way it realizes a caller-created
// interface: attached to the subnet's real fabric, which is what allocates the
// address, so a platform interface and a customer interface can never be handed
// the same one.

// azurePlatformNICSpec describes one platform-owned network interface.
type azurePlatformNICSpec struct {
	ID                        string
	Name                      string
	Location                  string
	SubnetID                  string
	IPConfigName              string
	RequestedIP               string
	AllocationMethod          string
	ApplicationSecurityGroups []SubResource
}

// azureCreatePlatformNIC attaches the interface to its subnet's fabric, records
// it under Microsoft.Network/networkInterfaces, and applies whatever network
// security groups govern the subnet. It returns the stored interface, whose
// primary IP configuration carries the address the subnet allocated.
func azureCreatePlatformNIC(ctx context.Context, spec azurePlatformNICSpec) (NetworkInterface, error) {
	allocation := spec.AllocationMethod
	if allocation == "" {
		allocation = "Dynamic"
	}
	if allocation == "Dynamic" {
		spec.RequestedIP = ""
	}
	privateIP, mac, err := azureCreateRealNIC(ctx, spec.ID, spec.SubnetID, spec.RequestedIP, azureNICMAC(spec.ID))
	if err != nil {
		return NetworkInterface{}, err
	}
	ipConfigName := spec.IPConfigName
	if ipConfigName == "" {
		ipConfigName = "ipconfig1"
	}
	nic := NetworkInterface{
		ID:       spec.ID,
		Name:     spec.Name,
		Type:     "Microsoft.Network/networkInterfaces",
		Location: spec.Location,
		Properties: NetworkInterfaceProperties{
			ProvisioningState: "Succeeded",
			MacAddress:        mac,
			IPConfigurations: []NetworkInterfaceIPConfiguration{{
				ID:   spec.ID + "/ipConfigurations/" + ipConfigName,
				Name: ipConfigName,
				Type: "Microsoft.Network/networkInterfaces/ipConfigurations",
				Properties: NetworkInterfaceIPConfigurationProperties{
					Subnet:                    &SubResource{ID: spec.SubnetID},
					PrivateIPAddress:          privateIP,
					PrivateIPAllocationMethod: allocation,
					PrivateIPAddressVersion:   "IPv4",
					Primary:                   true,
					ProvisioningState:         "Succeeded",
					ApplicationSecurityGroups: spec.ApplicationSecurityGroups,
				},
			}},
		},
	}
	azureNICs.Put(nic.ID, nic)
	if err := azureApplyRealNSGsToNIC(ctx, nic); err != nil {
		azureNICs.Delete(nic.ID)
		_ = azureDeleteRealNIC(ctx, nic.ID)
		return NetworkInterface{}, fmt.Errorf("apply network security groups to %s: %w", nic.ID, err)
	}
	return nic, nil
}

// azureDeletePlatformNIC releases the interface and its address back to the
// subnet.
func azureDeletePlatformNIC(ctx context.Context, nicID string) error {
	azureNICs.Delete(nicID)
	return azureDeleteRealNIC(ctx, nicID)
}

// azurePlatformNICPrivateIP reports the address of a platform interface's
// primary IP configuration, or the empty string when the interface is gone.
func azurePlatformNICPrivateIP(nicID string) string {
	nic, ok := azureNICs.Get(nicID)
	if !ok {
		return ""
	}
	for _, ipcfg := range nic.Properties.IPConfigurations {
		if ipcfg.Properties.PrivateIPAddress != "" {
			return ipcfg.Properties.PrivateIPAddress
		}
	}
	return ""
}

// azureRequireSubnet resolves a subnet reference the way the Microsoft.Network
// resource provider does: a write naming a subnet that does not exist is
// rejected with the subnet's own ResourceNotFound, before any fabric is built.
func azureRequireSubnet(w http.ResponseWriter, subnetID string) (Subnet, bool) {
	if subnetID == "" {
		sim.AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: a subnet reference is required.")
		return Subnet{}, false
	}
	subnet, ok := azureSubnets.Get(subnetID)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", subnetID)
		return Subnet{}, false
	}
	return subnet, true
}
