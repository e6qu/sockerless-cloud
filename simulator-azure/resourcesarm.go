package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.Resources beyond resource groups and tags/default: provider
// registration and listing, the legacy tagNames catalog, operations, template
// export, and the move operations.

// The TagDetails and TagValue wire shape is assembled on read.
type PredefinedTag struct {
	ID      string   `json:"id"`
	TagName string   `json:"tagName"`
	Values  []string `json:"values"`
}

var predefinedTags sim.Store[PredefinedTag]

func registerResourcesARM(srv *sim.Server) {
	predefinedTags = sim.MakeStore[PredefinedTag](srv.DB(), "azure_predefined_tags")
	azureUnregisteredProviders = sim.MakeStore[azureProviderRegistration](
		srv.DB(), "azure_unregistered_providers")

	// Shared by the resources and subscriptions Swagger documents.
	srv.HandleFunc("GET /providers/Microsoft.Resources/operations", handleResourcesOperations)

	srv.HandleFunc("GET /providers", handleProvidersListTenant)
	srv.HandleFunc("GET /providers/{resourceProviderNamespace}", handleProviderGetTenant)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}", handleProviderGet)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/resourceTypes", handleProviderResourceTypes)
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/providerPermissions", handleProviderPermissions)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/register", handleProviderRegister)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/providers/{resourceProviderNamespace}/unregister", handleProviderUnregister)

	srv.HandleFunc("PATCH /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}", handleResourceGroupUpdate)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/exportTemplate", handleResourceGroupExportTemplate)

	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/moveResources", handleMoveResources)
	srv.HandleFunc("POST /subscriptions/{subscriptionId}/resourceGroups/{sourceResourceGroupName}/validateMoveResources", handleMoveResources)

	srv.HandleFunc("POST /providers/Microsoft.Management/managementGroups/{groupId}/providers/{resourceProviderNamespace}/register", handleProviderRegisterAtMG)

	// The generic-resource operations' request URLs are the typed resource paths,
	// so Go's mux precedence dispatches them to the typed routes. These patterns
	// add Azure Resource Manager's answer where no typed route serves:
	//
	//   - "/{resourceId}": one segment is never a valid resource ID, and Azure
	//     answers 404 MissingSubscription. It also routes the by-ID
	//     role-assignment and role-definition templates, whose multi-segment
	//     spellings the RBAC routes serve.
	//   - The parent-scoped provider path: an unknown namespace gets
	//     InvalidResourceNamespace; an implemented namespace gets a declared 501
	//     naming the unserved path.
	//
	// The existence checks (ResourceGroups_CheckExistence,
	// Resources_CheckExistence, Resources_CheckExistenceById) are HEADs declaring
	// only 204 or 404, but Go routes HEAD to the GET handler, which answers 200.
	// This middleware maps a 2xx read onto 204 and passes the 404 through, on
	// management-plane paths only, skipping providers whose swagger declares its
	// own HEAD (API Management's entity-tag reads, one Cosmos DB read). The storage
	// and registry data planes lie outside the prefix and keep HEAD's own meaning.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodHead || !strings.HasPrefix(strings.ToLower(r.URL.Path), "/subscriptions/") ||
				azureProviderOwnsHEAD(r.URL.Path) {
				next.ServeHTTP(w, r)
				return
			}
			recorder := httptest.NewRecorder()
			next.ServeHTTP(recorder, r)
			for key, values := range recorder.Header() {
				for _, value := range values {
					w.Header().Add(key, value)
				}
			}
			status := recorder.Code
			if status >= 200 && status < 300 {
				status = http.StatusNoContent
			}
			w.Header().Del("Content-Length")
			w.WriteHeader(status)
		})
	})

	byID := func(w http.ResponseWriter, r *http.Request) {
		AzureError(w, "MissingSubscription",
			"The request did not have a subscription or a valid tenant level resource provider.",
			http.StatusNotFound)
	}
	srv.HandleFunc("GET /{resourceId}", byID) // GET also routes HEAD (Resources_CheckExistenceById)
	srv.HandleFunc("PUT /{resourceId}", byID)
	srv.HandleFunc("PATCH /{resourceId}", byID)
	srv.HandleFunc("DELETE /{resourceId}", byID)

	const genericResource = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}"
	srv.HandleFunc("GET "+genericResource, handleGenericProviderResource) // GET also routes HEAD (Resources_CheckExistence)
	srv.HandleFunc("PUT "+genericResource, handleGenericProviderResource)
	srv.HandleFunc("PATCH "+genericResource, handleGenericProviderResource)
	srv.HandleFunc("DELETE "+genericResource, handleGenericProviderResource)

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/tagNames", handleTagNamesList)
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/tagNames/{tagName}", handleTagNameCreate)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}", handleTagNameDelete)
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}", handleTagValueCreate)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}", handleTagValueDelete)
}

// Their HEAD is a read with its own status vocabulary, not the existence
// check. TestAzureProviderOwnsHEADMatchesTheVendoredSwaggers derives the set
// from the specs, so a re-vendor adding a HEAD fails until this agrees.
var azureProviderOwnsHEADNamespaces = []string{
	"microsoft.apimanagement",
	"microsoft.documentdb",
}

func azureProviderOwnsHEAD(path string) bool {
	lower := strings.ToLower(path)
	for _, ns := range azureProviderOwnsHEADNamespaces {
		if strings.Contains(lower, "/providers/"+ns+"/") {
			return true
		}
	}
	return false
}

// Only addresses no typed route matched reach this handler. An unimplemented
// namespace gets Azure Resource Manager's refusal; an implemented one names an
// unserved path, declared as a 501 rather than a routing 404.
func handleGenericProviderResource(w http.ResponseWriter, r *http.Request) {
	requested := sim.PathParam(r, "resourceProviderNamespace")
	ns, known := azureKnownProvider(requested)
	if !known {
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound,
			"The resource namespace %q is invalid.", requested)
		return
	}
	AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
		"The simulator's %s provider serves no handler for the resource path %s/%s/%s.",
		ns, sim.PathParam(r, "parentResourcePath"), sim.PathParam(r, "resourceType"), sim.PathParam(r, "resourceName"))
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

// Registration is the default, so the store holds the exception: an entry
// means Unregistered.
type azureProviderRegistration struct {
	Subscription string `json:"subscription"`
	Namespace    string `json:"namespace"`
}

var azureUnregisteredProviders sim.Store[azureProviderRegistration]

func azureProviderRegistrationKey(sub, ns string) string {
	return sub + "|" + strings.ToLower(ns)
}

func azureProviderObject(sub, ns string) map[string]any {
	id := "/providers/" + ns
	state := "Registered"
	if sub != "" {
		id = armSubscriptionResourceID(sub, ns)
		// Registration is per subscription; the tenant listing has none to report.
		if _, unregistered := azureUnregisteredProviders.Get(
			azureProviderRegistrationKey(sub, ns)); unregistered {
			state = "Unregistered"
		}
	}
	return map[string]any{
		"id":                id,
		"namespace":         ns,
		"registrationState": state,
		"resourceTypes":     azureProviderResourceTypeEntries(ns),
	}
}

// Matched case-insensitively; clients vary the namespace casing.
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
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, azureProviderObject("", ns))
}

func handleProviderGet(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sim.WriteJSON(w, http.StatusOK, azureProviderObject(sim.PathParam(r, "subscriptionId"), ns))
}

func handleProviderResourceTypes(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
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
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	// Registration must persist: a client registers so the read that follows
	// says so, and Terraform polls that read.
	sub := sim.PathParam(r, "subscriptionId")
	azureUnregisteredProviders.Delete(azureProviderRegistrationKey(sub, ns))
	sim.WriteJSON(w, http.StatusOK, azureProviderObject(sub, ns))
}

func handleProviderUnregister(w http.ResponseWriter, r *http.Request) {
	ns, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace"))
	if !ok {
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	sub := sim.PathParam(r, "subscriptionId")
	azureUnregisteredProviders.Put(azureProviderRegistrationKey(sub, ns),
		azureProviderRegistration{Subscription: sub, Namespace: ns})
	sim.WriteJSON(w, http.StatusOK, azureProviderObject(sub, ns))
}

func handleResourceGroupUpdate(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rgName := sim.PathParam(r, "resourceGroupName")
	id := armResourceGroupID(sub, rgName)
	rg, ok := azureResourceGroups.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound, "Resource group %q could not be found.", rgName)
		return
	}
	var req struct {
		Tags      map[string]string `json:"tags,omitempty"`
		ManagedBy *string           `json:"managedBy,omitempty"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
	id := armResourceGroupID(sub, rgName)
	if _, ok := azureResourceGroups.Get(id); !ok {
		AzureErrorf(w, "ResourceGroupNotFound", http.StatusNotFound, "Resource group %q could not be found.", rgName)
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

// handleMoveResources serves Resources_MoveResources and
// Resources_ValidateMoveResources: validate runs every check the move runs —
// the resources and target group exist, the type moves — without mutating,
// and move re-homes the resources and their subtrees. Both complete in the
// request with 204, the synchronous completion both contracts document.
func handleMoveResources(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	var req struct {
		Resources           []string `json:"resources"`
		TargetResourceGroup string   `json:"targetResourceGroup"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
		return
	}
	validateOnly := strings.HasSuffix(strings.TrimSuffix(r.URL.Path, "/"), "/validateMoveResources")
	if moveErr := moveAzureResources(sub, req.TargetResourceGroup, req.Resources, validateOnly); moveErr != nil {
		if len(moveErr.Details) > 0 {
			writeAzureMoveValidationError(w, sub, moveErr)
			return
		}
		AzureError(w, moveErr.Code, moveErr.Message, azureMoveErrorStatus(moveErr.Code))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// Absent resources and groups are 404, malformed input 400, and an unsupported
// move or failed provider validation 409: "if validation fails, it returns
// HTTP response code 409 (Conflict) with an error message" (Resources -
// Validate Move Resources).
func azureMoveErrorStatus(code string) int {
	switch code {
	case "ResourceGroupNotFound", "ResourceNotFound":
		return http.StatusNotFound
	case "ResourceMoveNotSupported", azureMoveProviderValidationFailed:
		return http.StatusConflict
	default:
		return http.StatusBadRequest
	}
}

// ResourceMoveProviderValidationFailed, whose message says only that
// validation failed, with the per-resource reason in a `details` entry
// targeted at the resource type, as Azure's failed-move bodies carry.
func writeAzureMoveValidationError(w http.ResponseWriter, sub string, moveErr *AsyncOperationError) {
	message := fmt.Sprintf(
		"Resource move validation failed. Please see details. Diagnostic information: timestamp '%s', subscription id '%s', tracking id '%s', request correlation id '%s'.",
		time.Now().UTC().Format("20060102T150405Z"), sub, sim.NewUUID(), sim.NewUUID())
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(azureMoveErrorStatus(moveErr.Code))
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"code":    moveErr.Code,
			"message": message,
			"details": moveErr.Details,
		},
	})
}

// targetResourceGroup arrives as a bare name or a full group ID; clients send
// both.
func moveAzureResources(sub, targetResourceGroup string, resources []string, validateOnly bool) *AsyncOperationError {
	targetRG := targetResourceGroup
	if i := sim.CaseInsensitiveLastIndex(targetRG, "/resourcegroups/"); i >= 0 {
		targetRG = targetRG[i+len("/resourcegroups/"):]
	}
	if targetRG == "" {
		return &AsyncOperationError{Code: "InvalidRequestContent", Message: "The 'targetResourceGroup' member is required."}
	}
	if _, ok := azureResourceGroups.Get(armResourceGroupID(sub, targetRG)); !ok {
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
		// A few types are movable only for some instances.
		if hook.supported != nil {
			if ok, reason := hook.supported(resID); !ok {
				return &AsyncOperationError{Code: "ResourceMoveNotSupported",
					Message: fmt.Sprintf("The resource '%s' does not support the move operation: %s", resID, reason)}
			}
		}
		newID := strings.Replace(resID, "/resourceGroups/"+rg+"/", "/resourceGroups/"+targetRG+"/", 1)
		// A resource keeps its name, so a destination holding one of that type and
		// name is refused outright: re-homing would destroy it.
		if !strings.EqualFold(newID, resID) && hook.exists(newID) {
			return azureMoveNameConflict(resID)
		}
		plan = append(plan, plannedMove{oldID: resID, newID: newID, hook: hook})
	}
	if validateOnly {
		return nil
	}
	// Tags live on the resource, so re-keying carries them, as Azure does. Each
	// hook re-homes what its slice addresses by name and pins derived credentials;
	// the repointing pass then handles everything else beneath or referencing the
	// moved ID.
	for _, m := range plan {
		m.hook.move(m.oldID, m.newID, targetRG)
		azureRepointMovedResource(m.oldID, m.newID)
	}
	return nil
}

const azureMoveProviderValidationFailed = "ResourceMoveProviderValidationFailed"

// No error code is published for a destination holding the same name. A real
// failed move (learn.microsoft.com/answers/questions/2080196) shows
// ResourceMoveProviderValidationFailed with the message below verbatim, nested
// as Azure nests failed moves: a CannotMoveResource entry targeted at the type,
// the reason inside it. The innermost entry carries no code, as none is
// published or attested.
func azureMoveNameConflict(resID string) *AsyncOperationError {
	return &AsyncOperationError{
		Code:    azureMoveProviderValidationFailed,
		Message: "Resource move validation failed. Please see details.",
		Details: []map[string]any{{
			"code":    "CannotMoveResource",
			"target":  azureResourceTypeOf(resID),
			"message": "Cannot move one or more resources in the request. Please check details for information about each resource.",
			"details": []map[string]any{{
				"message": fmt.Sprintf(
					"The move resources request contains resources like '%s' which have the same name as a resource in the target resource group.",
					resID),
			}},
		}},
	}
}

// In the identifier's casing, the target a move failure names.
func azureResourceTypeOf(resID string) string {
	segs := strings.Split(strings.Trim(resID, "/"), "/")
	if len(segs) < 7 {
		return ""
	}
	return segs[5] + "/" + segs[6]
}

func parseMovableResourceID(resID string) (typeKey, rg string, ok bool) {
	segs := strings.Split(strings.Trim(resID, "/"), "/")
	// subscriptions/{sub}/resourceGroups/{rg}/providers/{ns}/{type}/{name}
	if len(segs) < 8 || !strings.EqualFold(segs[0], "subscriptions") ||
		!strings.EqualFold(segs[2], "resourceGroups") || !strings.EqualFold(segs[4], "providers") {
		return "", "", false
	}
	return strings.ToLower(segs[5] + "/" + segs[6]), segs[3], true
}

// Providers_RegisterAtManagementGroupScope returns no body.
func handleProviderRegisterAtMG(w http.ResponseWriter, r *http.Request) {
	if _, ok := azureKnownProvider(sim.PathParam(r, "resourceProviderNamespace")); !ok {
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func predefinedTagKey(sub, tagName string) string {
	return sub + "/" + tagName
}

func predefinedTagID(sub, tagName string) string {
	return armChildID(armSubscriptionID(sub), "tagNames", tagName)
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
	idPrefix := armChildID(armSubscriptionID(sub), "tagNames") + "/"
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
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Tag name %q does not exist.", tagName)
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
