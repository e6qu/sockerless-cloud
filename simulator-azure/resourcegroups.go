package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

type ResourceGroup struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties struct {
		ProvisioningState string `json:"provisioningState"`
	} `json:"properties"`
}

// azureResourceGroups is the shared Microsoft.Resources/resourceGroups store,
// read by the resources-ARM PATCH/exportTemplate handlers in resourcesarm.go.
var azureResourceGroups sim.Store[ResourceGroup]

func registerResourceGroups(srv *sim.Server) {
	resourceGroups := sim.MakeStore[ResourceGroup](srv.DB(), "resource_groups")
	azureResourceGroups = resourceGroups

	// PUT - Create or update resource group
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")

		var req struct {
			Location string            `json:"location"`
			Tags     map[string]string `json:"tags,omitempty"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		_, exists := resourceGroups.Get(resourceID)

		rg := ResourceGroup{
			ID:       resourceID,
			Name:     rgName,
			Type:     "Microsoft.Resources/resourceGroups",
			Location: req.Location,
			Tags:     req.Tags,
		}
		rg.Properties.ProvisioningState = "Succeeded"
		resourceGroups.Put(resourceID, rg)

		if exists {
			sim.WriteJSON(w, http.StatusOK, rg)
		} else {
			sim.WriteJSON(w, http.StatusCreated, rg)
		}
	})

	// GET - Get resource group
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		rg, ok := resourceGroups.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound,
				"Resource group '%s' could not be found.", rgName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rg)
	})

	// GET - List resource groups
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/", sub)
		all := resourceGroups.Filter(func(rg ResourceGroup) bool {
			return strings.HasPrefix(rg.ID, prefix)
		})
		if all == nil {
			all = []ResourceGroup{}
		}
		sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})

	// DELETE - Delete resource group
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		resourceGroups.Delete(resourceID)
		w.WriteHeader(http.StatusOK)
	})

	// Resources_ListByResourceGroup. terraform-provider-azurerm polls this
	// before deleting a resource group, to refuse the delete while the group
	// still holds resources.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/resources", func(w http.ResponseWriter, r *http.Request) {
		handleAzureResourceList(w, r,
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
	})

	// Resources_List. The Azure CLI's `az resource list` reaches every scoping
	// it offers through this one route's `$filter` — `-g` is
	// `resourceGroup eq '<name>'`, not the resource-group-scoped route — and
	// terraform-provider-azurerm reads it with
	// `resourceType eq 'Microsoft.KeyVault/vaults'` while populating its Key
	// Vault cache. Both are answered from the cross-slice registry in
	// resource_registry.go.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resources", func(w http.ResponseWriter, r *http.Request) {
		handleAzureResourceList(w, r, sim.PathParam(r, "subscriptionId"), "")
	})

	// HEAD - Check resource group existence
	srv.HandleFunc("HEAD /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)

		if _, ok := resourceGroups.Get(resourceID); ok {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
}
