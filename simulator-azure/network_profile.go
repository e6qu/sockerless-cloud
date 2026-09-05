package main

import (
	"context"
	"fmt"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Network profiles (Microsoft.Network/networkProfiles) hold the network
// configuration a container platform applies when it puts a container group in
// a virtual network: one container network interface configuration per
// interface the platform should create, each naming the subnet its addresses
// come from. The profile is configuration — Azure realizes nothing when it is
// written, and realizes an interface only when a container group is deployed
// against it. So the profile's create validates the subnets it names and stamps
// child identity, and its read-only containerNetworkInterfaces property stays
// empty — the container platform is what fills it, and a profile nothing has
// been deployed against carries an empty collection in Azure too.

// NetworkProfile mirrors Microsoft.Network/networkProfiles.
type NetworkProfile struct {
	azureNetworkResourceHeader
	Properties NetworkProfileProperties `json:"properties"`
}

// NetworkProfileProperties holds the profile's interface configurations and the
// interfaces realized from them.
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

// ContainerNetworkInterfaceConfigurationProperties holds the template's IP
// configurations and the interfaces created from it.
type ContainerNetworkInterfaceConfigurationProperties struct {
	IPConfigurations           []IPConfigurationProfile `json:"ipConfigurations,omitempty"`
	ContainerNetworkInterfaces []SubResource            `json:"containerNetworkInterfaces,omitempty"`
	ProvisioningState          string                   `json:"provisioningState,omitempty"`
}

// IPConfigurationProfile names the subnet one templated interface draws from.
type IPConfigurationProfile struct {
	ID         string                           `json:"id,omitempty"`
	Name       string                           `json:"name,omitempty"`
	Type       string                           `json:"type,omitempty"`
	Etag       string                           `json:"etag,omitempty"`
	Properties IPConfigurationProfileProperties `json:"properties"`
}

// IPConfigurationProfileProperties holds the subnet reference.
type IPConfigurationProfileProperties struct {
	Subnet            *SubResource `json:"subnet,omitempty"`
	ProvisioningState string       `json:"provisioningState,omitempty"`
}

// ContainerNetworkInterface is an interface realized from a configuration when
// a container group is deployed against the profile.
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
			np.Properties.ResourceGUID = generateUUID()
			if previous != nil && previous.Properties.ResourceGUID != "" {
				np.Properties.ResourceGUID = previous.Properties.ResourceGUID
			}
			np.Properties.ProvisioningState = "Succeeded"
			for i := range np.Properties.ContainerNetworkInterfaceConfigurations {
				cfg := &np.Properties.ContainerNetworkInterfaceConfigurations[i]
				if cfg.Name == "" {
					cfg.Name = fmt.Sprintf("cnic%d", i+1)
				}
				cfg.ID = azureNetworkChildID(np.ID, "containerNetworkInterfaceConfigurations", cfg.Name)
				cfg.Type = "Microsoft.Network/networkProfiles/containerNetworkInterfaceConfigurations"
				cfg.Etag = azureNetworkEtag()
				cfg.Properties.ProvisioningState = "Succeeded"
				for j := range cfg.Properties.IPConfigurations {
					ipcfg := &cfg.Properties.IPConfigurations[j]
					if ipcfg.Name == "" {
						ipcfg.Name = fmt.Sprintf("ipconfig%d", j+1)
					}
					ipcfg.ID = azureNetworkChildID(cfg.ID, "ipConfigurations", ipcfg.Name)
					ipcfg.Type = "Microsoft.Network/networkProfiles/containerNetworkInterfaceConfigurations/ipConfigurations"
					ipcfg.Etag = azureNetworkEtag()
					ipcfg.Properties.ProvisioningState = "Succeeded"
				}
			}
			return nil
		},
	})
}
