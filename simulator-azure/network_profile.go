package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A network profile holds the configuration a container platform applies when
// placing a container group in a virtual network: one interface configuration
// per interface, each naming its subnet. Azure realizes nothing on write, so
// the create validates subnets and stamps child identity, and
// containerNetworkInterfaces stays empty until a container group is deployed
// against it, as in Azure.

type NetworkProfile struct {
	azureNetworkResourceHeader
	Properties NetworkProfileProperties `json:"properties"`
}

type NetworkProfileProperties struct {
	ContainerNetworkInterfaces              []ContainerNetworkInterface              `json:"containerNetworkInterfaces,omitempty"`
	ContainerNetworkInterfaceConfigurations []ContainerNetworkInterfaceConfiguration `json:"containerNetworkInterfaceConfigurations,omitempty"`
	ResourceGUID                            string                                   `json:"resourceGuid,omitempty"`
	ProvisioningState                       string                                   `json:"provisioningState,omitempty"`
}

type ContainerNetworkInterfaceConfiguration struct {
	ID         string                                           `json:"id,omitempty"`
	Name       string                                           `json:"name,omitempty"`
	Type       string                                           `json:"type,omitempty"`
	Etag       string                                           `json:"etag,omitempty"`
	Properties ContainerNetworkInterfaceConfigurationProperties `json:"properties"`
}

type ContainerNetworkInterfaceConfigurationProperties struct {
	IPConfigurations           []IPConfigurationProfile `json:"ipConfigurations,omitempty"`
	ContainerNetworkInterfaces []SubResource            `json:"containerNetworkInterfaces,omitempty"`
	ProvisioningState          string                   `json:"provisioningState,omitempty"`
}

type IPConfigurationProfile struct {
	ID         string                           `json:"id,omitempty"`
	Name       string                           `json:"name,omitempty"`
	Type       string                           `json:"type,omitempty"`
	Etag       string                           `json:"etag,omitempty"`
	Properties IPConfigurationProfileProperties `json:"properties"`
}

type IPConfigurationProfileProperties struct {
	Subnet            *SubResource `json:"subnet,omitempty"`
	ProvisioningState string       `json:"provisioningState,omitempty"`
}

type ContainerNetworkInterface struct {
	ID         string         `json:"id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Type       string         `json:"type,omitempty"`
	Etag       string         `json:"etag,omitempty"`
	Properties map[string]any `json:"properties,omitempty"`
}

var azureNetworkProfiles sim.Store[NetworkProfile]

func registerNetworkProfiles(srv *sim.Server) {
	azureNetworkProfiles = sim.MakeStore[NetworkProfile](srv.DB(), "network_profiles")

	registerAzureNetworkResource(srv, azureNetworkResourceSpec[NetworkProfile]{
		collection:   "networkProfiles",
		nameParam:    "networkProfileName",
		resourceType: "Microsoft.Network/networkProfiles",
		store:        azureNetworkProfiles,
		header: func(np *NetworkProfile) *azureNetworkResourceHeader {
			return &np.azureNetworkResourceHeader
		},
		validate: func(w http.ResponseWriter, _ *http.Request, np *NetworkProfile) bool {
			for _, cfg := range np.Properties.ContainerNetworkInterfaceConfigurations {
				for _, ipcfg := range cfg.Properties.IPConfigurations {
					subnetID := ""
					if ipcfg.Properties.Subnet != nil {
						subnetID = ipcfg.Properties.Subnet.ID
					}
					if _, ok := azureRequireSubnet(w, subnetID); !ok {
						return false
					}
				}
			}
			return true
		},
		provision: func(_ context.Context, np *NetworkProfile, previous *NetworkProfile) error {
			np.Properties.ResourceGUID = sim.NewUUID()
			if previous != nil && previous.Properties.ResourceGUID != "" {
				np.Properties.ResourceGUID = previous.Properties.ResourceGUID
			}
			np.Properties.ProvisioningState = "Succeeded"
			for i := range np.Properties.ContainerNetworkInterfaceConfigurations {
				cfg := &np.Properties.ContainerNetworkInterfaceConfigurations[i]
				if cfg.Name == "" {
					cfg.Name = fmt.Sprintf("cnic%d", i+1)
				}
				cfg.ID = armChildID(np.ID, "containerNetworkInterfaceConfigurations", cfg.Name)
				cfg.Type = "Microsoft.Network/networkProfiles/containerNetworkInterfaceConfigurations"
				cfg.Etag = azureNetworkEtag()
				cfg.Properties.ProvisioningState = "Succeeded"
				for j := range cfg.Properties.IPConfigurations {
					ipcfg := &cfg.Properties.IPConfigurations[j]
					if ipcfg.Name == "" {
						ipcfg.Name = fmt.Sprintf("ipconfig%d", j+1)
					}
					ipcfg.ID = armChildID(cfg.ID, "ipConfigurations", ipcfg.Name)
					ipcfg.Type = "Microsoft.Network/networkProfiles/containerNetworkInterfaceConfigurations/ipConfigurations"
					ipcfg.Etag = azureNetworkEtag()
					ipcfg.Properties.ProvisioningState = "Succeeded"
				}
			}
			return nil
		},
	})
}
