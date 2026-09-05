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
	azureUnregisteredProviders = sim.MakeStore[azureProviderRegistration](
		srv.DB(), "azure_unregistered_providers")

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

	// The generic-resource operations. Their real request URLs ARE the typed
	// resource paths — Resources_GetById's {resourceId} is "/subscriptions/…/
	// providers/{ns}/{type}/{name}", so a client calling the by-ID or
	// provider-path spelling puts the same bytes on the wire as the typed
	// operation and Go's mux precedence dispatches it to the typed provider
	// route. That precedence IS the generic dispatch; what these patterns add
	// is Azure Resource Manager's own answer for the instances no typed route
	// serves:
	//
	//   - "/{resourceId}" catches the single-segment spelling. One URL segment
	//     can never be a valid ARM identifier (every resource ID and every
	//     tenant-level provider path spans several segments), and real ARM
	//     refuses such a request with 404 MissingSubscription. The same
	//     pattern is the routing arm for the by-full-resource-ID templates of
	//     the Microsoft.Authorization role-assignment and role-definition
	//     documents ("/{roleAssignmentId}", "/{roleId}"), whose multi-segment
	//     spellings the scoped RBAC routes and middleware already serve.
	//   - The parent-scoped provider path catches addresses whose provider or
	//     type the simulator mounts no typed route for: an unknown namespace
	//     gets ARM's InvalidResourceNamespace refusal, a namespace the
	//     simulator does implement gets a declared 501 naming the unserved
	//     resource path rather than a bare mux 404.
	// Azure Resource Manager's existence checks — ResourceGroups_
	// CheckExistence, Resources_CheckExistence and Resources_CheckExistenceById
	// — are HEAD requests on the resource's own address, and each declares
	// exactly 204 or 404. Go routes a HEAD to the GET handler, which answers
	// the read's 200, so the check would report a status ARM never returns.
	// This maps the read's verdict onto the check's vocabulary: anything the
	// GET would answer 2xx becomes 204, and the read's own 404 passes through.
	// It is scoped to management-plane paths, and skips the providers whose
	// own swagger declares HEAD operations: API Management answers entity-tag
	// reads with 200 and an ETag, and Azure Cosmos DB has one such read too.
	// The storage and registry data planes are outside the prefix entirely
	// and keep HEAD's own meaning (blob and blob-exists).
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

	// Legacy predefined tagNames catalog.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/tagNames", handleTagNamesList)
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/tagNames/{tagName}", handleTagNameCreate)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}", handleTagNameDelete)
	srv.HandleFunc("PUT /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}", handleTagValueCreate)
	srv.HandleFunc("DELETE /subscriptions/{subscriptionId}/tagNames/{tagName}/tagValues/{tagValue}", handleTagValueDelete)
}

// azureProviderOwnsHEADNamespaces are the resource providers whose vendored
// swagger declares HEAD operations of its own. Their HEAD is a read with its
// own status vocabulary — API Management's entity-tag reads answer 200 with
// an ETag — not Azure Resource Manager's existence check, so the 204 mapping
// leaves them alone.
//
// TestAzureProviderOwnsHEADMatchesTheVendoredSwaggers derives the same set
// from the specs, so a re-vendor that gives another provider a HEAD
// operation fails until this list agrees.
var azureProviderOwnsHEADNamespaces = []string{
	"microsoft.apimanagement",
	"microsoft.documentdb",
}

// azureProviderOwnsHEAD reports whether the path addresses a provider that
// serves HEAD itself.
func azureProviderOwnsHEAD(path string) bool {
	lower := strings.ToLower(path)
	for _, ns := range azureProviderOwnsHEADNamespaces {
		if strings.Contains(lower, "/providers/"+ns+"/") {
			return true
		}
	}
	return false
}

// handleGenericProviderResource answers the parent-scoped generic-resource
// spellings (Resources_{CheckExistence,Get,CreateOrUpdate,Update,Delete}) for
// the only addresses that reach it: those no typed provider route matched,
// because Go's mux always prefers the typed route's literal segments. An
// address under a namespace the simulator does not implement gets Azure
// Resource Manager's own refusal; an address under an implemented namespace
// names a resource path the simulator serves no handler for, which is
// declared as a 501 gap rather than disguised as a routing 404.
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

// azureProviderObject builds a Provider wire object. When scope is empty the ID
// is tenant-scoped (/providers/{ns}); otherwise it is subscription-scoped.
// azureProviderRegistration records a namespace a subscription has
// unregistered. Registration is the default and the common case, so the store
// holds the exception: an entry means Unregistered, and its absence means
// Registered.
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
		id = fmt.Sprintf("/subscriptions/%s/providers/%s", sub, ns)
		// Registration is per subscription, so only a subscription-scoped read
		// can report one as unregistered. The tenant listing has no
		// subscription to be registered against.
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
	// Registering is what makes the state Registered, and the state has to
	// survive the call: a client registers precisely so the read that follows
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
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)
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
	id := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", sub, rgName)
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

// azureMoveErrorStatus maps a move-validation error code onto the HTTP
// status Azure Resource Manager pairs it with: absent resources and groups
// are 404s, malformed input is a 400, and an unsupported move or a failed
// provider validation is the 409 Conflict a real move-validation failure
// reports — "if validation fails, it returns HTTP response code 409
// (Conflict) with an error message" (Resources - Validate Move Resources).
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

// writeAzureMoveValidationError writes the nested error a provider validation
// failure carries. Azure Resource Manager reports one of these as a
// ResourceMoveProviderValidationFailed whose message states only that
// validation failed and carries the diagnostic coordinates of the attempt,
// with the per-resource reason in a `details` entry targeted at the resource
// type — the shape of the failed-move bodies Azure returns.
func writeAzureMoveValidationError(w http.ResponseWriter, sub string, moveErr *AsyncOperationError) {
	message := fmt.Sprintf(
		"Resource move validation failed. Please see details. Diagnostic information: timestamp '%s', subscription id '%s', tracking id '%s', request correlation id '%s'.",
		time.Now().UTC().Format("20060102T150405Z"), sub, generateUUID(), generateUUID())
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

// moveAzureResources validates (and, unless validateOnly, performs) a
// cross-resource-group move. targetResourceGroup arrives either as a bare
// name or as the full resource-group ID, as real clients send both. The
// per-type work is dispatched through resourceMoveHooks (resource_move.go);
// a type no provider slice has hooked answers ARM's ResourceMoveNotSupported.
func moveAzureResources(sub, targetResourceGroup string, resources []string, validateOnly bool) *AsyncOperationError {
	targetRG := targetResourceGroup
	if i := sim.CaseInsensitiveLastIndex(targetRG, "/resourcegroups/"); i >= 0 {
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
		// A few types are movable only for some of their instances, which ARM
		// can only decide once it has the resource in front of it.
		if hook.supported != nil {
			if ok, reason := hook.supported(resID); !ok {
				return &AsyncOperationError{Code: "ResourceMoveNotSupported",
					Message: fmt.Sprintf("The resource '%s' does not support the move operation: %s", resID, reason)}
			}
		}
		newID := strings.Replace(resID, "/resourceGroups/"+rg+"/", "/resourceGroups/"+targetRG+"/", 1)
		// A resource keeps its name across a move, so the destination group
		// must not already hold one of the same type under that name: the
		// destination identifier would address two resources, and re-homing
		// onto it would destroy the one already there. Azure Resource Manager
		// refuses the whole request instead.
		if !strings.EqualFold(newID, resID) && hook.exists(newID) {
			return azureMoveNameConflict(resID)
		}
		plan = append(plan, plannedMove{oldID: resID, newID: newID, hook: hook})
	}
	if validateOnly {
		return nil
	}
	// A resource's tags live on the resource itself, so re-keying the record
	// carries them into the destination group, which is what real Azure
	// Resource Manager does with a moved resource's tags.
	//
	// Each hook re-homes what its own slice addresses by name and pins the
	// credential material the resource ID derives; the repointing pass then
	// re-homes everything stored beneath the moved ID anywhere else and
	// re-points every reference held to it from outside the moved set.
	for _, m := range plan {
		m.hook.move(m.oldID, m.newID, targetRG)
		azureRepointMovedResource(m.oldID, m.newID)
	}
	return nil
}

// azureMoveProviderValidationFailed is the code Azure Resource Manager reports
// a move that a resource provider's validation refused under.
const azureMoveProviderValidationFailed = "ResourceMoveProviderValidationFailed"

// azureMoveNameConflict is the refusal for a move whose destination group
// already holds a resource of the same type and name.
//
// Microsoft publishes no error code for this case: the move-support and
// move-resource-group documentation never states the constraint, and the REST
// reference documents only that a failed validation "returns HTTP response
// code 409 (Conflict) with an error message" in the CloudError envelope. What
// the service answers is attested by a report of a real failed move
// (learn.microsoft.com/answers/questions/2080196), which carries the
// ResourceMoveProviderValidationFailed code and the message below naming the
// resources that "have the same name as a resource in the target resource
// group". That message is reproduced verbatim, in the nesting Azure's failed
// moves use — the provider-validation code outside, a CannotMoveResource entry
// targeted at the resource type, and the reason inside it. The innermost entry
// carries no code of its own because none is published or attested, and
// inventing one would put a code on the wire that no client could have seen
// from the service.
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

// azureResourceTypeOf is the fully qualified type of a resource-group-scoped
// resource ID, in the casing the identifier carries — the target a move
// failure names.
func azureResourceTypeOf(resID string) string {
	segs := strings.Split(strings.Trim(resID, "/"), "/")
	if len(segs) < 7 {
		return ""
	}
	return segs[5] + "/" + segs[6]
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
		AzureErrorf(w, "InvalidResourceNamespace", http.StatusNotFound, "The resource namespace %q is invalid.", sim.PathParam(r, "resourceProviderNamespace"))
		return
	}
	w.WriteHeader(http.StatusOK)
}

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
