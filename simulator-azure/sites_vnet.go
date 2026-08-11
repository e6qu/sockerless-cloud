package main

import (
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// App Service regional VNet integration (the "swift" virtual network
// connection). A site joins a subnet delegated to Microsoft.Web/serverFarms;
// the sim realizes that subnet as a Docker user-defined network
// (dockerNetForVNet) and attaches the site's container to it, so sites on the
// same VNet reach each other by name — the App Service container fabric.

// SwiftVirtualNetwork mirrors armappservice.SwiftVirtualNetwork (the body of
// PUT .../sites/{name}/networkConfig/virtualNetwork).
type SwiftVirtualNetwork struct {
	ID         string                        `json:"id,omitempty"`
	Name       string                        `json:"name,omitempty"`
	Type       string                        `json:"type,omitempty"`
	Properties SwiftVirtualNetworkProperties `json:"properties"`
}

// SwiftVirtualNetworkProperties mirrors armappservice.SwiftVirtualNetworkProperties.
type SwiftVirtualNetworkProperties struct {
	SubnetResourceID string `json:"subnetResourceId,omitempty"`
	SwiftSupported   bool   `json:"swiftSupported,omitempty"`
}

var azfSwiftConnections sim.Store[SwiftVirtualNetwork]

func registerSiteVNetIntegration(srv *sim.Server, armBase string, sites sim.Store[Site]) {
	azfSwiftConnections = sim.MakeStore[SwiftVirtualNetwork](srv.DB(), "azf_swift_vnet")

	// The operation path is .../sites/{name}/networkConfig/virtualNetwork, but the
	// resource's canonical ARM id (and the value clients like
	// terraform-provider-azurerm parse from the response) is the config
	// sub-resource .../sites/{name}/config/virtualNetwork.
	swiftID := func(siteID string) string { return siteID + "/config/virtualNetwork" }

	// PUT — create/update the swift VNet connection (regional VNet integration).
	srv.HandleFunc("PUT "+armBase+"/sites/{siteName}/networkConfig/virtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		siteID := sitesResourceID(sub, rg, name)

		site, ok := sites.Get(siteID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Web/sites/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		var req SwiftVirtualNetwork
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		subnetID := req.Properties.SubnetResourceID
		vnetName := vnetNameFromSubnetID(subnetID)
		if vnetName == "" {
			sim.AzureError(w, "InvalidRequestContent", "subnetResourceId is required and must reference a subnet", http.StatusBadRequest)
			return
		}

		dockerNet := dockerNetForVNet(vnetName)
		if _, err := sim.EnsureDockerNetwork(dockerNet); err != nil {
			sim.AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "failed to realize App Service VNet network: %v", err)
			return
		}

		// Record the VNet network on the site. A `services:` container (no HTTP
		// function bootstrap, e.g. redis) is never invoked, so VNet integration
		// is its run trigger — start it now on the network. An HTTP function site
		// is started by its own invoke (asynchronously); we must not block here
		// waiting for its bootstrap, so just record the network and attach it if
		// it's already running (its startLocked attaches to the recorded network
		// when it next starts).
		s := site
		inst := azfInstanceFor(s.Name)
		inst.mu.Lock()
		inst.dockerNetwork = dockerNet
		var startErr error
		if !hasAzureFunctionHTTPBootstrap(&s) && siteContainerImage(&s) != "" {
			startErr = inst.ensureStarted(&s)
		} else if inst.containerID != "" && sim.ContainerRunning(inst.containerID) {
			_ = sim.ConnectContainerToNetwork(inst.containerID, dockerNet, siteNetAliases(&s))
		}
		inst.mu.Unlock()
		if startErr != nil {
			sim.AzureErrorf(w, "InternalServerError", http.StatusInternalServerError, "failed to integrate site %q into VNet: %v", name, startErr)
			return
		}

		resp := SwiftVirtualNetwork{
			ID:   swiftID(siteID),
			Name: "virtualNetwork",
			Type: "Microsoft.Web/sites/config",
			Properties: SwiftVirtualNetworkProperties{
				SubnetResourceID: subnetID,
				SwiftSupported:   true,
			},
		}
		azfSwiftConnections.Put(siteID, resp)
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// GET — read back the swift VNet connection.
	srv.HandleFunc("GET "+armBase+"/sites/{siteName}/networkConfig/virtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		siteID := sitesResourceID(sub, rg, name)
		conn, ok := azfSwiftConnections.Get(siteID)
		if !ok {
			// No integration: return an empty connection resource (real Azure
			// returns a virtualNetwork resource with empty properties).
			conn = SwiftVirtualNetwork{ID: swiftID(siteID), Name: "virtualNetwork", Type: "Microsoft.Web/sites/config"}
		}
		sim.WriteJSON(w, http.StatusOK, conn)
	})

	// DELETE — remove the swift VNet connection (disconnect the container).
	srv.HandleFunc("DELETE "+armBase+"/sites/{siteName}/networkConfig/virtualNetwork", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "siteName")
		siteID := sitesResourceID(sub, rg, name)

		if conn, ok := azfSwiftConnections.Get(siteID); ok {
			vnetName := vnetNameFromSubnetID(conn.Properties.SubnetResourceID)
			if site, sOk := sites.Get(siteID); sOk && vnetName != "" {
				inst := azfInstanceFor(site.Name)
				inst.mu.Lock()
				if inst.containerID != "" {
					_ = sim.DisconnectContainerFromNetwork(inst.containerID, dockerNetForVNet(vnetName))
				}
				inst.dockerNetwork = ""
				inst.mu.Unlock()
			}
			azfSwiftConnections.Delete(siteID)
		}
		w.WriteHeader(http.StatusOK)
	})
}

// sitesResourceID builds the ARM resource ID for an App Service site.
func sitesResourceID(sub, rg, name string) string {
	return "/subscriptions/" + sub + "/resourceGroups/" + rg + "/providers/Microsoft.Web/sites/" + name
}

// realizeCNAMEAsSiteDockerAlias makes recordName resolve to the App Service site
// whose DefaultHostName is the CNAME's target, by re-attaching that site's
// container to its VNet network with the alias — the same mechanism
// realizeCNAMEAsDockerAlias uses for ACA Apps. This is how the sim realizes a
// Private DNS record for an App Service site locally; it is not
// sockerless-specific (any CNAME → a site's default hostname resolves this way).
// No-op when no site matches or the site isn't VNet-integrated yet.
func realizeCNAMEAsSiteDockerAlias(cname string) {
	target := strings.TrimSuffix(cname, ".")
	if target == "" {
		return
	}
	for _, site := range azfSites.List() {
		if !strings.EqualFold(strings.TrimSuffix(site.Properties.DefaultHostName, "."), target) {
			continue
		}
		s := site
		inst := azfInstanceFor(s.Name)
		inst.mu.Lock()
		cid := inst.containerID
		dockerNet := inst.dockerNetwork
		inst.mu.Unlock()
		if cid == "" || dockerNet == "" {
			return
		}
		// Docker can't add an alias to a live endpoint, so re-attach with the
		// full set: the site's identity names + every CNAME pointing at it.
		aliases := dedupeStrings(append(siteNetAliases(&s), cnameAliasesForTarget(target)...))
		_ = sim.DisconnectContainerFromNetwork(cid, dockerNet)
		_ = sim.ConnectContainerToNetwork(cid, dockerNet, aliases)
		return
	}
}
