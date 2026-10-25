package main

import (
	"net/http"
	"strings"
)

// AzureResourceGroupScopeMiddleware answers for Azure Resource Manager before a
// resource provider sees a request beneath a resource group: a group that does
// not exist refuses every method with 404 ResourceGroupNotFound, and a group
// whose delete is running refuses writes and actions with 409
// ResourceGroupBeingDeleted while its resources stay readable and deletable.
// The group's own delete reaches its resources through the mux directly
// (serveAzureInternal), so this check never sees it.
func AzureResourceGroupScopeMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isAzureARMPath(r.URL.Path) || azureARMPathUsesSessionAuth(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sub, rgName, ok := azureResourceGroupScope(r.URL.Path)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		group, exists := azureLookupResourceGroup(sub, rgName)
		if !exists {
			AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound,
				"Resource group '%s' could not be found.", rgName)
			return
		}
		if group.Properties.ProvisioningState == "Deleting" {
			switch r.Method {
			case http.MethodPut, http.MethodPatch, http.MethodPost:
				AzureErrorf(w, "ResourceGroupBeingDeleted", http.StatusConflict,
					"The resource group '%s' is in deprovisioning state and cannot perform this operation.", rgName)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// azureResourceGroupScope reports the subscription and group of a path that
// addresses something beneath a resource group. The group's own path is not
// beneath it: its handlers answer for a missing or deleting group themselves.
func azureResourceGroupScope(path string) (sub, group string, ok bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 5 || segments[0] != "subscriptions" || !strings.EqualFold(segments[2], "resourceGroups") ||
		segments[1] == "" || segments[3] == "" {
		return "", "", false
	}
	return segments[1], segments[3], true
}

// azureLookupResourceGroup finds a group the way ARM does, ignoring the case
// of the subscription ID and the group name.
func azureLookupResourceGroup(sub, name string) (ResourceGroup, bool) {
	id := armResourceGroupID(sub, name)
	if group, ok := azureResourceGroups.Get(id); ok {
		return group, true
	}
	matches := azureResourceGroups.Filter(func(rg ResourceGroup) bool {
		return strings.EqualFold(rg.ID, id)
	})
	if len(matches) == 0 {
		return ResourceGroup{}, false
	}
	return matches[0], true
}
