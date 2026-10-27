package main

import (
	"net/http"
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

// resourcesarm.go's PATCH and exportTemplate handlers read this store too.
var azureResourceGroups sim.Store[ResourceGroup]

func registerResourceGroups(srv *sim.Server) {
	resourceGroups := sim.MakeStore[ResourceGroup](srv.DB(), "resource_groups")
	azureResourceGroups = resourceGroups
	// A delete running when the previous process stopped failed with it, and Azure
	// Resource Manager rolls a group whose delete failed back to Succeeded.
	for _, rg := range resourceGroups.List() {
		if rg.Properties.ProvisioningState == "Deleting" {
			resourceGroups.Update(rg.ID, func(stale *ResourceGroup) {
				stale.Properties.ProvisioningState = "Succeeded"
			})
		}
	}

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

		resourceID := armResourceGroupID(sub, rgName)

		current, exists := resourceGroups.Get(resourceID)
		if exists && current.Properties.ProvisioningState == "Deleting" {
			AzureErrorf(w, "ResourceGroupBeingDeleted", http.StatusConflict,
				"The resource group '%s' is in deprovisioning state and cannot perform this operation.", rgName)
			return
		}

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

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := armResourceGroupID(sub, rgName)

		rg, ok := resourceGroups.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound,
				"Resource group '%s' could not be found.", rgName)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rg)
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := armSubscriptionID(sub) + "/resourceGroups/"
		all := resourceGroups.Filter(func(rg ResourceGroup) bool {
			return strings.HasPrefix(rg.ID, prefix)
		})
		// ResourceGroups_List pages by $top, as Resources_List does.
		page, next, ok := armPage(w, r, all, func(rg ResourceGroup) string { return strings.ToLower(rg.ID) })
		if !ok {
			return
		}
		if page == nil {
			page = []ResourceGroup{}
		}
		body := map[string]any{"value": page}
		if next != "" {
			body["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, body)
	})

	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		handleResourceGroupDelete(srv, w, r)
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/operationresults/{operationId}", handleSubscriptionOperationResult)

	// terraform-provider-azurerm polls this before deleting a group, refusing
	// while it still holds resources.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/resources", func(w http.ResponseWriter, r *http.Request) {
		handleAzureResourceList(w, r,
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
	})

	// `az resource list` scopes everything through $filter (`-g` is
	// `resourceGroup eq '<name>'`), and terraform-provider-azurerm filters
	// `resourceType eq 'Microsoft.KeyVault/vaults'` for its Key Vault cache; the
	// registry in resource_registry.go answers both.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/resources", func(w http.ResponseWriter, r *http.Request) {
		handleAzureResourceList(w, r, sim.PathParam(r, "subscriptionId"), "")
	})

	srv.HandleFunc("HEAD /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rgName := sim.PathParam(r, "resourceGroupName")
		resourceID := armResourceGroupID(sub, rgName)

		if _, ok := resourceGroups.Get(resourceID); ok {
			w.WriteHeader(http.StatusNoContent)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	})
}
