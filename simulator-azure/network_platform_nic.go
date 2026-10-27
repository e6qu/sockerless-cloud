package main

import (
	"context"
	"net"
	"net/http"
)

// A private endpoint gets an interface in its subnet, and a private link
// service one per NAT IP configuration. Azure exposes these platform-owned
// interfaces as ordinary Microsoft.Network/networkInterfaces, and the
// simulator leases their addresses from the subnet's ledger like any other.

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

// An interface already stored keeps its address.
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

func azureDeletePlatformNIC(_ context.Context, nicID string) error {
	if nic, ok := azureNICs.Get(nicID); ok {
		azureReleaseNICAddresses(nic)
	}
	azureNICs.Delete(nicID)
	return nil
}

// Empty when the interface is gone.
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

// A write naming an absent subnet fails with the subnet's own
// ResourceNotFound, as the resource provider does.
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
