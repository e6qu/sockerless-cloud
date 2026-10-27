package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerVirtualNetworks(srv *sim.Server, armBase string, vnets sim.Store[VirtualNetwork], subnets sim.Store[Subnet]) {
	srv.HandleFunc("PUT "+armBase+"/virtualNetworks/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		vnetName := sim.PathParam(r, "vnetName")

		var req VirtualNetwork
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName)

		privateEndpointVNetPolicies := req.Properties.PrivateEndpointVNetPolicies
		if privateEndpointVNetPolicies == "" {
			privateEndpointVNetPolicies = "Disabled"
		}

		vnet := VirtualNetwork{
			ID:       resourceID,
			Name:     vnetName,
			Type:     "Microsoft.Network/virtualNetworks",
			Location: req.Location,
			Tags:     req.Tags,
			Properties: VNetProperties{
				AddressSpace:                req.Properties.AddressSpace,
				PrivateEndpointVNetPolicies: privateEndpointVNetPolicies,
				ProvisioningState:           "Succeeded",
			},
		}

		vnets.Put(resourceID, vnet)

		// Create inline subnets as their standalone PUTs do; `az network vnet create
		// --subnet-name` depends on it.
		for _, inline := range req.Properties.Subnets {
			if inline.Name == "" {
				AzureError(w, "InvalidRequestContent",
					"A subnet declared on a virtual network must carry a name.", http.StatusBadRequest)
				return
			}
			built := azureBuildSubnet(resourceID+"/subnets/"+inline.Name, inline.Name, inline)
			if code, message, status := azureMaterializeSubnet(built); code != "" {
				AzureError(w, code, message, status)
				return
			}
		}

		azureReapplyNAT()

		// Embed every subnet the network holds, inline or pre-existing.
		subnetList := subnets.Filter(func(s Subnet) bool {
			return strings.HasPrefix(s.ID, resourceID+"/subnets/")
		})
		vnet.Properties.Subnets = append(vnet.Properties.Subnets, subnetList...)
		vnets.Put(resourceID, vnet)

		// go-azure-sdk expects 200 for a synchronous create.
		sim.WriteJSON(w, http.StatusOK, vnet)
	})

	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		vnetName := sim.PathParam(r, "vnetName")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName)

		vnet, ok := vnets.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/virtualNetworks/%s' under resource group '%s' was not found.", vnetName, rg)
			return
		}

		vnet.Properties.Subnets = nil
		subnetList := subnets.Filter(func(s Subnet) bool {
			return strings.HasPrefix(s.ID, resourceID+"/subnets/")
		})
		vnet.Properties.Subnets = append(vnet.Properties.Subnets, subnetList...)

		sim.WriteJSON(w, http.StatusOK, vnet)
	})

	srv.HandleFunc("DELETE "+armBase+"/virtualNetworks/{vnetName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		vnetName := sim.PathParam(r, "vnetName")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName)

		// The engine refuses to remove a network a container is attached to, Azure's
		// own refusal of a network in use.
		if err := azureRemoveVNetNetwork(r.Context(), resourceID); err != nil {
			AzureErrorf(w, "InUseSubnetCannotBeDeleted", http.StatusBadRequest,
				"Virtual network %s is in use and cannot be deleted. In order to delete the virtual network, delete all the resources within it: %v", resourceID, err)
			return
		}
		vnets.Delete(resourceID)
		subnetList := subnets.Filter(func(s Subnet) bool {
			return strings.HasPrefix(s.ID, resourceID+"/subnets/")
		})
		for _, s := range subnetList {
			subnets.Delete(s.ID)
			azureForgetSubnetLedger(s.ID)
		}
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("PUT "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		vnetName := sim.PathParam(r, "vnetName")
		subnetName := sim.PathParam(r, "subnetName")

		var req Subnet
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName, "subnets", subnetName)

		// Azure rejects a subnet write under a missing network with the parent's
		// ResourceNotFound.
		vnetID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName)
		if _, ok := vnets.Get(vnetID); !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.Network/virtualNetworks/%s' under resource group '%s' was not found.", vnetName, rg)
			return
		}

		sn := azureBuildSubnet(resourceID, subnetName, req)
		if code, message, status := azureMaterializeSubnet(sn); code != "" {
			AzureError(w, code, message, status)
			return
		}
		azureReapplyNAT()

		// go-azure-sdk expects 200 for a synchronous create.
		sim.WriteJSON(w, http.StatusOK, sn)
	})

	srv.HandleFunc("GET "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		vnetName := sim.PathParam(r, "vnetName")
		subnetName := sim.PathParam(r, "subnetName")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName, "subnets", subnetName)

		sn, ok := subnets.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'subnets/%s' under virtualNetworks '%s' was not found.", subnetName, vnetName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, sn)
	})

	srv.HandleFunc("DELETE "+armBase+"/virtualNetworks/{vnetName}/subnets/{subnetName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		vnetName := sim.PathParam(r, "vnetName")
		subnetName := sim.PathParam(r, "subnetName")
		resourceID := armResourceID(sub, rg, "Microsoft.Network", "virtualNetworks", vnetName, "subnets", subnetName)

		subnets.Delete(resourceID)
		azureForgetSubnetLedger(resourceID)
		w.WriteHeader(http.StatusOK)
	})
}
