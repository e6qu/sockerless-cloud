package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft.Resources control-plane surface beyond resource groups and the
// tags/default scope endpoint: resource-provider registration/listing, the
// legacy predefined tagNames catalog, the operations metadata list, resource
// group template export, and the cross-group move-resources LROs.

// PredefinedTag is one row in the subscription's legacy predefined-tag catalog
// (Microsoft.Resources tagNames). It carries the tag name and its known values;
// the wire shape (TagDetails / TagValue) is assembled on read.
type PredefinedTag struct {
	ID      string   `json:"id"`
	TagName string   `json:"tagName"`
	Values  []string `json:"values"`
}

var predefinedTags sim.Store[PredefinedTag]

func registerResourcesARM(srv *sim.Server) {
	predefinedTags = sim.MakeStore[PredefinedTag](srv.DB(), "azure_predefined_tags")

	// Operations metadata (tenant scope, shared by the resources +
	// subscriptions Swagger documents).
	srv.HandleFunc("GET /providers/Microsoft.Resources/operations", handleResourcesOperations)

	// Resource provider registration + listing.
	srv.HandleFunc("GET /providers", handleProvidersListTenant)
	srv.HandleFunc("GET /providers/{resourceProviderNamespace}", handleProviderGetTenant)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}", handleProviderGet)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/resourceTypes", handleProviderResourceTypes)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/providerPermissions", handleProviderPermissions)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/register", handleProviderRegister)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/unregister", handleProviderUnregister)

	// Resource group: PATCH (update tags / managedBy) + exportTemplate LRO.
	srv.HandleFunc("PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", handleResourceGroupUpdate)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/exportTemplate", handleResourceGroupExportTemplate)

	// Cross-group move LROs.
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources", handleMoveResources)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources", handleMoveResources)

	// Resource-provider registration at management-group scope.
	srv.HandleFunc("POST /providers/Microsoft.Management/managementGroups/{groupId}/providers/{resourceProviderNamespace}/register", handleProviderRegisterAtMG)

	// Legacy predefined tagNames catalog.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/tagNames", handleTagNamesList)
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/tagNames/{tagName}", handleTagNameCreate)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}", handleTagNameDelete)
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}", handleTagValueCreate)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}", handleTagValueDelete)
}

func handleResourcesOperations(w http.ResponseWriter, _ *http.Request) {
	op := func(name, op, res string) map[string]any {
		return map[string]any{
			"name": name,
			"display": map[string]any{
				"provider":  "Microsoft Resources",
				"resource":  res,
				"operation": op,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
		op("Microsoft.Resources/subscriptions/resourceGroups/read", "Get Resource Group", "Resource Group"),
		op("Microsoft.Resources/subscriptions/resourceGroups/write", "Create Resource Group", "Resource Group"),
		op("Microsoft.Resources/subscriptions/resourceGroups/delete", "Delete Resource Group", "Resource Group"),
		op("Microsoft.Resources/deployments/read", "Get Deployment", "Deployment"),
		op("Microsoft.Resources/deployments/write", "Create Deployment", "Deployment"),
		op("Microsoft.Resources/tags/read", "Get Tags", "Tags"),
		op("Microsoft.Resources/tags/write", "Set Tags", "Tags"),
	}})
}

// azureProviderObject builds a Provider wire object. When scope is empty the ID
// is tenant-scoped (/providers/{ns}); otherwise it is subscription-scoped.
func azureProviderObject(sub, ns string) map[string]any {
	id := "/providers/" + ns
	if sub != "" {
		id = fmt.Sprintf("/subscriptions/%s/providers/%s", sub, ns)
	}
	return map[string]any{
		"id":                id,
		"namespace":         ns,
		"registrationState": "Registered",
		"resourceTypes":     azureProviderResourceTypeEntries(ns),
	}
}

// azureKnownProvider reports whether ns is one of the catalogued providers,
// matched case-insensitively (the namespace casing real clients send varies).
func azureKnownProvider(ns string) (string, bool) {
	for _, known := range resourceProviderNamespaces {
		if strings.EqualFold(known, ns) {
			return known, true
		}
	}
	return "", false
}

func handleProvidersListTenant(w http.ResponseWriter, _ *http.Request) {
	out := make([]map[string]any, 0, len(resourceProviderNamespaces))
	for _, ns := range resourceProviderNamespaces {
		out = append(out, azureProviderObject("", ns))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleProviderGetTenant(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		sim.AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, azureProviderObject("", ns))
}

func handleProviderGet(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		sim.AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, azureProviderObject(sim.PathParam(r, "subscriptionId"), ns))
}

func handleProviderResourceTypes(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		sim.AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": azureProviderResourceTypeEntries(ns)})
}

func handleProviderPermissions(w http.ResponseWriter, _ *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
}

func handleProviderRegister(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		sim.AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, azureProviderObject(sim.PathParam(r, "subscriptionId"), ns))
}

func handleProviderUnregister(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		sim.AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	obj := azureProviderObject(sim.PathParam(r, "subscriptionId"), ns)
	obj["registrationState"] = "Unregistered"
	sim.WriteJSON(w, http.StatusOK, obj)
}

func handleResourceGroupUpdate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rgName := sim.PathParam(r, "resourceGroupName")
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)
	rg, ok := azureResourceGroups.Get(id)
	if !ok {
		sim.AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound, "Resource group %q could not be found.", rgName)
		return
	}
	var req struct {
		Tags      map[string]string `json:"tags,omitempty"`
		ManagedBy *string           `json:"managedBy,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Tags != nil {
		rg.Tags = req.Tags
	}
	azureResourceGroups.Put(id, rg)
	sim.WriteJSON(w, http.StatusOK, rg)
}

func handleResourceGroupExportTemplate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rgName := sim.PathParam(r, "resourceGroupName")
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)
	if _, ok := azureResourceGroups.Get(id); !ok {
		sim.AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound, "Resource group %q could not be found.", rgName)
		return
	}
	template := map[string]any{
		"$schema":        "https://schema.management.azure.com/schemas/2019-04-01/deploymentTemplate.json#",
		"contentVersion": "1.0.0.0",
		"parameters":     map[string]any{},
		"variables":      map[string]any{},
		"resources":      []any{},
		"outputs":        map[string]any{},
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"template": template})
}

func handleMoveResources(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	var req struct {
		Resources           []string `json:"resources"`
		TargetResourceGroup string   `json:"targetResourceGroup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	opID := issueAzureAsyncOperation(nil)
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.Resources", "global", "operationStatuses", opID, r.URL.Query().Get("api-version"))
	w.Header().Set("Azure-AsyncOperation", opURL)
	w.Header().Set("Retry-After", "0")
	w.WriteHeader(http.StatusAccepted)
}

// handleProviderRegisterAtMG registers a resource provider at management-group
// scope (Providers_RegisterAtManagementGroupScope). The operation returns no
// body; the registration is reflected on subsequent provider reads.
func handleProviderRegisterAtMG(w http.ResponseWriter, r *http.Request) {
	if _, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace")); !ok {
		sim.AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

// --- legacy predefined tagNames ---

func predefinedTagKey(sub, tagName string) string {
	return sub + "/" + tagName
}

func predefinedTagID(sub, tagName string) string {
	return fmt.Sprintf("/subscriptions/%s/tagNames/%s", sub, tagName)
}

func tagDetailsObject(sub string, t PredefinedTag) map[string]any {
	values := make([]map[string]any, 0, len(t.Values))
	for _, v := range t.Values {
		values = append(values, tagValueObject(sub, t.TagName, v))
	}
	return map[string]any{
		"id":      predefinedTagID(sub, t.TagName),
		"tagName": t.TagName,
		"count":   map[string]any{"type": "Total", "value": 0},
		"values":  values,
	}
}

func tagValueObject(sub, tagName, value string) map[string]any {
	return map[string]any{
		"id":       predefinedTagID(sub, tagName) + "/tagValues/" + value,
		"tagValue": value,
		"count":    map[string]any{"type": "Total", "value": 0},
	}
}

func handleTagNamesList(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	idPrefix := fmt.Sprintf("/subscriptions/%s/tagNames/", sub)
	tags := predefinedTags.Filter(func(t PredefinedTag) bool {
		return strings.HasPrefix(t.ID, idPrefix)
	})
	sort.Slice(tags, func(i, j int) bool { return tags[i].TagName < tags[j].TagName })
	out := make([]map[string]any, 0, len(tags))
	for _, t := range tags {
		out = append(out, tagDetailsObject(sub, t))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleTagNameCreate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	tagName := sim.PathParam(r, "tagName")
	key := predefinedTagKey(sub, tagName)
	t, exists := predefinedTags.Get(key)
	if !exists {
		t = PredefinedTag{ID: predefinedTagID(sub, tagName), TagName: tagName}
		predefinedTags.Put(key, t)
	}
	status := http.StatusOK
	if !exists {
		status = http.StatusCreated
	}
	sim.WriteJSON(w, status, tagDetailsObject(sub, t))
}

func handleTagNameDelete(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	tagName := sim.PathParam(r, "tagName")
	if predefinedTags.Delete(predefinedTagKey(sub, tagName)) {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}

func handleTagValueCreate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	tagName := sim.PathParam(r, "tagName")
	tagValue := sim.PathParam(r, "tagValue")
	key := predefinedTagKey(sub, tagName)
	t, exists := predefinedTags.Get(key)
	if !exists {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Tag name %q does not exist.", tagName)
		return
	}
	had := false
	for _, v := range t.Values {
		if v == tagValue {
			had = true
			break
		}
	}
	if !had {
		t.Values = append(t.Values, tagValue)
		predefinedTags.Put(key, t)
	}
	status := http.StatusOK
	if !had {
		status = http.StatusCreated
	}
	sim.WriteJSON(w, status, tagValueObject(sub, tagName, tagValue))
}

func handleTagValueDelete(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	tagName := sim.PathParam(r, "tagName")
	tagValue := sim.PathParam(r, "tagValue")
	key := predefinedTagKey(sub, tagName)
	t, exists := predefinedTags.Get(key)
	if !exists {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	kept := t.Values[:0]
	removed := false
	for _, v := range t.Values {
		if v == tagValue {
			removed = true
			continue
		}
		kept = append(kept, v)
	}
	t.Values = kept
	predefinedTags.Put(key, t)
	if removed {
		w.WriteHeader(http.StatusOK)
	} else {
		w.WriteHeader(http.StatusNoContent)
	}
}
