package main

import (
	"context"
	"net"
	"net/http"
)

// Several Microsoft.Network resources own network interfaces they did not ask
// the caller to create: a private endpoint gets one interface in the subnet it
// projects the linked service into, and a private link service gets one per NAT
// IP configuration. Azure exposes those interfaces under the ordinary
// Microsoft.Network/networkInterfaces type — they answer Get, they appear in
// the interface list, and their addresses come out of the subnet's own address
// space. The simulator leases them from the subnet's ledger exactly as it
// leases a caller-created interface's, so a platform interface and a customer
// interface can never be handed the same one.

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

// azureCreatePlatformNIC leases the interface's address from its subnet and
// records it under Microsoft.Network/networkInterfaces. It returns the stored
// interface, whose primary IP configuration carries the address the subnet
// allocated; an interface already stored keeps the address it holds.
func azureCreatePlatformNIC(_ context.Context, spec azurePlatformNICSpec) (NetworkInterface, error) {
	allocation := spec.AllocationMethod
	if allocation == "" {
		allocation = "Dynamic"
	}
	ipConfigName := spec.IPConfigName
	if ipConfigName == "" {
		ipConfigName = "ipconfig1"
	}
	ipConfigID := spec.ID + "/ipConfigurations/" + ipConfigName
	var requested net.IP
	if allocation != "Dynamic" {
		requested = net.ParseIP(spec.RequestedIP)
	} else if held := azurePlatformNICPrivateIP(spec.ID); held != "" {
		requested = net.ParseIP(held)
	}
	privateIP, err := azureLeaseSubnetAddress(spec.SubnetID, ipConfigID, requested)
	if err != nil {
		return NetworkInterface{}, err
	}
	nic := NetworkInterface{
		ID:       spec.ID,
		Name:     spec.Name,
		Type:     "Microsoft.Network/networkInterfaces",
		Location: spec.Location,
		Properties: NetworkInterfaceProperties{
			ProvisioningState: "Succeeded",
			MacAddress:        formatAzureMAC(azureNICMAC(spec.ID)),
			IPConfigurations: []NetworkInterfaceIPConfiguration{{
				ID:   ipConfigID,
				Name: ipConfigName,
				Type: "Microsoft.Network/networkInterfaces/ipConfigurations",
				Properties: NetworkInterfaceIPConfigurationProperties{
					Subnet:                    &SubResource{ID: spec.SubnetID},
					PrivateIPAddress:          privateIP.String(),
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
	return nic, nil
}

// azureDeletePlatformNIC releases the interface and its address back to the
// subnet.
func azureDeletePlatformNIC(_ context.Context, nicID string) error {
	if nic, ok := azureNICs.Get(nicID); ok {
		azureReleaseNICAddresses(nic)
	}
	azureNICs.Delete(nicID)
	return nil
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
// rejected with the subnet's own ResourceNotFound.
func azureRequireSubnet(w http.ResponseWriter, subnetID string) (Subnet, bool) {
	if subnetID == "" {
		AzureErrorf(w, "InvalidRequestFormat", http.StatusBadRequest,
			"The request format was unexpected: a subnet reference is required.")
		return Subnet{}, false
	}
	subnet, ok := azureSubnets.Get(subnetID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", subnetID)
		return Subnet{}, false
	}
	return subnet, true
}
