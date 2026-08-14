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

// handleMoveResources serves both Resources_MoveResources and
// Resources_ValidateMoveResources: the validate spelling runs every check the
// move runs — the resources exist, the target resource group exists, the
// resource type supports a cross-group move — without mutating anything,
// while the move spelling re-homes the resources (and their whole child
// subtree) into the target resource group in the real stores. The simulator
// completes the work inside the request and answers 204 No Content, the
// synchronous completion both move contracts document; a failed check
// answers the ARM error envelope directly.
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
	validateOnly := strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/validateMoveResources")
	if moveErr := moveAzureResources(sub, req.TargetResourceGroup, req.Resources, validateOnly); moveErr != nil {
		sim.AzureError(w, moveErr.Code, moveErr.Message, azureMoveErrorStatus(moveErr.Code))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// azureMoveErrorStatus maps a move-validation error code onto the HTTP
// status Azure Resource Manager pairs it with: absent resources and groups
// are 404s, malformed input is a 400, and an unsupported move is the 409
// Conflict a real move-validation failure reports.
func azureMoveErrorStatus(code string) int {
	switch code {
	case "ResourceGroupNotFound", "ResourceNotFound":
		return http.StatusNotFound
	case "ResourceMoveNotSupported":
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// moveAzureResources validates (and, unless validateOnly, performs) a
// cross-resource-group move. targetResourceGroup arrives either as a bare
// name or as the full resource-group ID, as real clients send both. The
// per-type work is dispatched through resourceMoveHooks (resource_move.go);
// a type no provider slice has hooked answers ARM's ResourceMoveNotSupported.
func moveAzureResources(sub, targetResourceGroup string, resources []string, validateOnly bool) *AsyncOperationError {
	targetRG := targetResourceGroup
	if i := strings.LastIndex(strings.ToLower(targetRG), "/resourcegroups/"); i >= 0 {
		targetRG = targetRG[i+len("/resourcegroups/"):]
	}
	if targetRG == "" {
		return &AsyncOperationError{Code: "InvalidRequestContent", Message: "The 'targetResourceGroup' member is required."}
	}
	if _, ok := azureResourceGroups.Get(fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, targetRG)); !ok {
		return &AsyncOperationError{Code: "ResourceGroupNotFound",
			Message: fmt.Sprintf("Resource group '%s' could not be found.", targetRG)}
	}
	type plannedMove struct {
		oldID, newID string
		hook         resourceMoveHook
	}
	var plan []plannedMove
	for _, resID := range resources {
		typeKey, rg, ok := parseMovableResourceID(resID)
		if !ok {
			return &AsyncOperationError{Code: "InvalidResourceId",
				Message: fmt.Sprintf("The resource id '%s' is not a valid resource-group-scoped resource ID.", resID)}
		}
		hook, supported := resourceMoveHooks[typeKey]
		if !supported {
			return &AsyncOperationError{Code: "ResourceMoveNotSupported",
				Message: fmt.Sprintf("Resources of type '%s' do not support the move operation in this simulator.", typeKey)}
		}
		if !hook.exists(resID) {
			return &AsyncOperationError{Code: "ResourceNotFound",
				Message: fmt.Sprintf("The resource '%s' was not found.", resID)}
		}
		newID := strings.Replace(resID, "/resourceGroups/"+rg+"/", "/resourceGroups/"+targetRG+"/", 1)
		plan = append(plan, plannedMove{oldID: resID, newID: newID, hook: hook})
	}
	if validateOnly {
		return nil
	}
	// A resource's tags live on the resource itself, so re-keying the record
	// carries them into the destination group, which is what real Azure
	// Resource Manager does with a moved resource's tags.
	for _, m := range plan {
		m.hook.move(m.oldID, m.newID, targetRG)
	}
	return nil
}

// parseMovableResourceID splits a resource-group-scoped ARM resource ID into
// its lowercased provider/type key and its resource-group name.
func parseMovableResourceID(resID string) (typeKey, rg string, ok bool) {
	segs := strings.Split(strings.Trim(resID, "/"), "/")
	// subscriptions/{sub}/resourceGroups/{rg}/providers/{ns}/{type}/{name}
	if len(segs) < 8 || !strings.EqualFold(segs[0], "subscriptions") ||
		!strings.EqualFold(segs[2], "resourceGroups") || !strings.EqualFold(segs[4], "providers") {
		return "", "", false
	}
	return strings.ToLower(segs[5] + "/" + segs[6]), segs[3], true
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
