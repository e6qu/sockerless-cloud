package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Additional Microsoft.ApiManagement ARM control-plane operations layered on
// top of the Service / Api / Product / Subscription / Backend / NamedValue
// slice in apim.go: service-level actions (skus, checkNameAvailability,
// backup/restore LROs, getSsoToken), the PATCH (Update) verbs, the
// subscription / namedValue / backend POST actions, and the API and Product
// child collections (schemas, policies, diagnostics, releases, revisions,
// tags, operation policies/tags, product↔api and product↔group associations).

// apimChild is a generic ARM child resource served as the standard
// {id, name, type, properties} envelope. Leaf APIM sub-resources (schemas,
// policies, diagnostics, releases, tag assignments, operation policies/tags)
// are properties bags whose only contract is the property set the request
// carries, so one store backs them all keyed by the (lowercased) resource ID.
type apimChild struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var apimChildren sim.Store[apimChild]

func registerAPIMMore(srv *sim.Server) {
	apimChildren = sim.MakeStore[apimChild](srv.DB(), "apim_children")

	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service"
	const provider = "/subscriptions/{subscriptionId}/providers/Microsoft.ApiManagement"

	// Service-level Update + actions.
	srv.HandleFunc("PATCH "+base+"/{name}", handleAPIMUpdateService)
	srv.HandleFunc("GET "+base+"/{name}/skus", handleAPIMListServiceSkus)
	srv.HandleFunc("POST "+base+"/{name}/getssotoken", handleAPIMGetSsoToken)
	srv.HandleFunc("POST "+base+"/{name}/backup", handleAPIMServiceLRO)
	srv.HandleFunc("POST "+base+"/{name}/restore", handleAPIMServiceLRO)
	srv.HandleFunc("POST "+base+"/{name}/migrateToStv2", handleAPIMServiceLRO)
	srv.HandleFunc("POST "+base+"/{name}/applynetworkconfigurationupdates", handleAPIMServiceLRO)

	// Provider / subscription-scoped operations.
	srv.HandleFunc("GET /providers/Microsoft.ApiManagement/operations", handleAPIMListRestOperations)
	srv.HandleFunc("GET "+provider+"/service", handleAPIMListServicesBySub)
	srv.HandleFunc("GET "+provider+"/deletedServices", handleAPIMListDeletedServices)
	// AzurePathNormalizationMiddleware lowercases checkNameAvailability to
	// checknameavailability; getDomainOwnershipIdentifier is not in that
	// allowlist, so it must be registered at the SDK's casing.
	srv.HandleFunc("POST "+provider+"/checknameavailability", handleAPIMCheckNameAvailability)
	srv.HandleFunc("POST "+provider+"/getDomainOwnershipIdentifier", handleAPIMGetDomainOwnershipIdentifier)

	// GetEntityTag HEAD on the store-backed resources.
	srv.HandleFunc("HEAD "+base+"/{name}/apis/{api}", apimHeadFromStore(apimApiExists))
	srv.HandleFunc("HEAD "+base+"/{name}/apis/{api}/operations/{op}", apimHeadFromStore(apimOperationExists))
	srv.HandleFunc("HEAD "+base+"/{name}/products/{product}", apimHeadFromStore(apimProductExists))
	srv.HandleFunc("HEAD "+base+"/{name}/subscriptions/{sub}", apimHeadFromStore(apimSubscriptionExists))
	srv.HandleFunc("HEAD "+base+"/{name}/backends/{backend}", apimHeadFromStore(apimBackendExists))
	srv.HandleFunc("HEAD "+base+"/{name}/namedValues/{nv}", apimHeadFromStore(apimNamedValueExists))
	srv.HandleFunc("HEAD "+base+"/{name}/products/{product}/apis/{api}", apimChildHead)
	srv.HandleFunc("HEAD "+base+"/{name}/products/{product}/groups/{group}", apimChildHead)

	// API Update + child collections.
	srv.HandleFunc("PATCH "+base+"/{name}/apis/{api}", handleAPIMPatchChildStore(apimApisStore))
	srv.HandleFunc("PATCH "+base+"/{name}/apis/{api}/operations/{op}", handleAPIMPatchChildStore(apimOperationsStore))

	apimRegisterChild(srv, base+"/{name}/apis/{api}/schemas", "{schema}", "Microsoft.ApiManagement/service/apis/schemas", false, "contentType", "document")
	apimRegisterChild(srv, base+"/{name}/apis/{api}/policies", "{policy}", "Microsoft.ApiManagement/service/apis/policies", false, "value")
	apimRegisterChild(srv, base+"/{name}/apis/{api}/diagnostics", "{diag}", "Microsoft.ApiManagement/service/apis/diagnostics", true, "loggerId")
	apimRegisterChild(srv, base+"/{name}/apis/{api}/releases", "{release}", "Microsoft.ApiManagement/service/apis/releases", true)
	apimRegisterChild(srv, base+"/{name}/apis/{api}/tags", "{tag}", "Microsoft.ApiManagement/service/apis/tags", false)
	apimRegisterChild(srv, base+"/{name}/apis/{api}/operations/{op}/policies", "{policy}", "Microsoft.ApiManagement/service/apis/operations/policies", false, "value")
	apimRegisterChild(srv, base+"/{name}/apis/{api}/operations/{op}/tags", "{tag}", "Microsoft.ApiManagement/service/apis/operations/tags", false)

	// API revisions (read-only) + products-by-api.
	srv.HandleFunc("GET "+base+"/{name}/apis/{api}/revisions", handleAPIMListApiRevisions)
	srv.HandleFunc("GET "+base+"/{name}/apis/{api}/products", handleAPIMListApiProducts)

	// GraphQL API resolvers + resolver policies.
	apimRegisterChild(srv, base+"/{name}/apis/{api}/resolvers", "{resolver}", "Microsoft.ApiManagement/service/apis/resolvers", true)
	apimRegisterChild(srv, base+"/{name}/apis/{api}/resolvers/{resolver}/policies", "{policy}", "Microsoft.ApiManagement/service/apis/resolvers/policies", false, "value")

	// API issues + issue comments + issue attachments.
	apimRegisterChild(srv, base+"/{name}/apis/{api}/issues", "{issue}", "Microsoft.ApiManagement/service/apis/issues", true, "title", "description", "userId")
	apimRegisterChild(srv, base+"/{name}/apis/{api}/issues/{issue}/comments", "{comment}", "Microsoft.ApiManagement/service/apis/issues/comments", false, "text", "userId")
	apimRegisterChild(srv, base+"/{name}/apis/{api}/issues/{issue}/attachments", "{attachment}", "Microsoft.ApiManagement/service/apis/issues/attachments", false, "title", "contentFormat", "content")

	// API tag descriptions + API wiki.
	apimRegisterChild(srv, base+"/{name}/apis/{api}/tagDescriptions", "{tagDescription}", "Microsoft.ApiManagement/service/apis/tagDescriptions", false)
	apimRegisterChild(srv, base+"/{name}/apis/{api}/wikis", "default", "Microsoft.ApiManagement/service/apis/wikis", true)

	// Operations grouped by tag (read-only).
	srv.HandleFunc("GET "+base+"/{name}/apis/{api}/operationsByTags", handleAPIMOperationsByTags)

	// Product wiki.
	apimRegisterChild(srv, base+"/{name}/products/{product}/wikis", "default", "Microsoft.ApiManagement/service/products/wikis", true)

	// Product Update + child collections + associations.
	srv.HandleFunc("PATCH "+base+"/{name}/products/{product}", handleAPIMPatchChildStore(apimProductsStore))
	apimRegisterChild(srv, base+"/{name}/products/{product}/policies", "{policy}", "Microsoft.ApiManagement/service/products/policies", false, "value")
	apimRegisterChild(srv, base+"/{name}/products/{product}/tags", "{tag}", "Microsoft.ApiManagement/service/products/tags", false)

	srv.HandleFunc("GET "+base+"/{name}/products/{product}/apis", handleAPIMListProductApis)
	srv.HandleFunc("PUT "+base+"/{name}/products/{product}/apis/{api}", handleAPIMAddProductApi)
	srv.HandleFunc("DELETE "+base+"/{name}/products/{product}/apis/{api}", handleAPIMRemoveProductApi)
	srv.HandleFunc("GET "+base+"/{name}/products/{product}/groups", handleAPIMListProductGroups)
	srv.HandleFunc("PUT "+base+"/{name}/products/{product}/groups/{group}", handleAPIMAddProductGroup)
	srv.HandleFunc("DELETE "+base+"/{name}/products/{product}/groups/{group}", handleAPIMRemoveProductGroup)
	srv.HandleFunc("GET "+base+"/{name}/products/{product}/subscriptions", handleAPIMListProductSubscriptions)

	// Subscription Update + key regeneration.
	srv.HandleFunc("PATCH "+base+"/{name}/subscriptions/{sub}", handleAPIMPatchSubscription)
	srv.HandleFunc("POST "+base+"/{name}/subscriptions/{sub}/regeneratePrimaryKey", handleAPIMRegenerateSubKey)
	srv.HandleFunc("POST "+base+"/{name}/subscriptions/{sub}/regenerateSecondaryKey", handleAPIMRegenerateSubKey)

	// NamedValue Update + secret surfaces.
	srv.HandleFunc("PATCH "+base+"/{name}/namedValues/{nv}", handleAPIMPatchNamedValue)
	srv.HandleFunc("POST "+base+"/{name}/namedValues/{nv}/listValue", handleAPIMListNamedValueValue)
	srv.HandleFunc("POST "+base+"/{name}/namedValues/{nv}/refreshSecret", handleAPIMRefreshNamedValueSecret)

	// Backend Update + reconnect.
	srv.HandleFunc("PATCH "+base+"/{name}/backends/{backend}", handleAPIMPatchBackend)
	srv.HandleFunc("POST "+base+"/{name}/backends/{backend}/reconnect", handleAPIMBackendReconnect)
}

func apimReqPath(r *http.Request) string { return strings.TrimRight(r.URL.Path, "/") }

func apimLastSeg(p string) string {
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// apimRegisterChild mounts PUT/GET/DELETE/LIST (and PATCH when patch=true) for
// apimRegisterChild mounts one leaf child collection. required names the
// properties that child's contract declares required, taken from the vendored
// definition — a create omitting one is a resource the service would never
// hold, and the response describing it breaks the contract a client reads.
func apimRegisterChild(srv *sim.Server, parent, childVar, typeStr string, patch bool, required ...string) {
	item := parent + "/" + childVar
	srv.HandleFunc("PUT "+item, apimChildPut(typeStr, required))
	srv.HandleFunc("GET "+item, apimChildGet)
	srv.HandleFunc("HEAD "+item, apimChildHead)
	srv.HandleFunc("DELETE "+item, apimChildDelete)
	srv.HandleFunc("GET "+parent, apimChildList)
	if patch {
		srv.HandleFunc("PATCH "+item, apimChildPatch)
	}
}

// apimETag returns a deterministic ETag for a resource path. GetEntityTag (the
// HEAD operations) returns it in the ETag header with an empty body.
func apimETag(r *http.Request) string {
	return `"` + simListKey32(apimReqPath(r), "apim-etag") + `"`
}

// apimChildHead serves the GetEntityTag HEAD on a leaf child: 200 + ETag when
// the resource exists, 404 (empty body) otherwise.
func apimChildHead(w http.ResponseWriter, r *http.Request) {
	if _, ok := apimChildren.Get(strings.ToLower(apimReqPath(r))); !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("ETag", apimETag(r))
	w.WriteHeader(http.StatusOK)
}

// apimHeadFromStore builds a GetEntityTag HEAD handler whose existence check is
// the caller's store lookup.
func apimHeadFromStore(exists func(r *http.Request) bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !exists(r) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("ETag", apimETag(r))
		w.WriteHeader(http.StatusOK)
	}
}

func apimChildPut(typeStr string, required []string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path := apimReqPath(r)
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties == nil {
			req.Properties = map[string]any{}
		}
		for _, name := range required {
			if value, ok := req.Properties[name]; ok && value != nil {
				continue
			}
			AzureErrorf(w, "ValidationError", http.StatusBadRequest,
				"Property %q is required for %s.", name, typeStr)
			return
		}
		c := apimChild{ID: path, Name: apimLastSeg(path), Type: typeStr, Properties: req.Properties}
		apimChildren.Put(strings.ToLower(path), c)
		sim.WriteJSON(w, http.StatusOK, c)
	}
}

func apimChildGet(w http.ResponseWriter, r *http.Request) {
	path := apimReqPath(r)
	c, ok := apimChildren.Get(strings.ToLower(path))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "resource %q not found", apimLastSeg(path))
		return
	}
	sim.WriteJSON(w, http.StatusOK, c)
}

func apimChildPatch(w http.ResponseWriter, r *http.Request) {
	path := apimReqPath(r)
	c, ok := apimChildren.Get(strings.ToLower(path))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "resource %q not found", apimLastSeg(path))
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if c.Properties == nil {
		c.Properties = map[string]any{}
	}
	for k, v := range req.Properties {
		c.Properties[k] = v
	}
	apimChildren.Put(strings.ToLower(path), c)
	sim.WriteJSON(w, http.StatusOK, c)
}

func apimChildDelete(w http.ResponseWriter, r *http.Request) {
	path := apimReqPath(r)
	if !apimChildren.Delete(strings.ToLower(path)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "resource %q not found", apimLastSeg(path))
		return
	}
	w.WriteHeader(http.StatusOK)
}

func apimChildList(w http.ResponseWriter, r *http.Request) {
	prefix := strings.ToLower(apimReqPath(r)) + "/"
	var out []apimChild
	for _, c := range apimChildren.List() {
		lid := strings.ToLower(c.ID)
		if strings.HasPrefix(lid, prefix) && !strings.Contains(lid[len(prefix):], "/") {
			out = append(out, c)
		}
	}
	if out == nil {
		out = []apimChild{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func apimApiExists(r *http.Request) bool {
	_, ok := apimApis.Get(apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/apis/" + sim.PathParam(r, "api"))
	return ok
}

func apimOperationExists(r *http.Request) bool {
	_, ok := apimOperations.Get(apimOperationKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "api"), sim.PathParam(r, "op")))
	return ok
}

func apimProductExists(r *http.Request) bool {
	_, ok := apimProducts.Get(apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/products/" + sim.PathParam(r, "product"))
	return ok
}

func apimSubscriptionExists(r *http.Request) bool {
	_, ok := apimSubscriptions.Get(apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/subscriptions/" + sim.PathParam(r, "sub"))
	return ok
}

func apimBackendExists(r *http.Request) bool {
	_, ok := apimBackends.Get(apimBackendKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "backend")))
	return ok
}

func apimNamedValueExists(r *http.Request) bool {
	_, ok := apimNamedValues.Get(apimNamedValueKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv")))
	return ok
}

// apimStoreKind identifies which top-level store a PATCH targets so a single
// merge handler serves apis / operations / products.
type apimStoreKind int

const (
	apimApisStore apimStoreKind = iota
	apimOperationsStore
	apimProductsStore
)

func handleAPIMPatchChildStore(kind apimStoreKind) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Properties map[string]any `json:"properties"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
			return
		}
		svcID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
		switch kind {
		case apimApisStore:
			id := svcID + "/apis/" + sim.PathParam(r, "api")
			a, ok := apimApis.Get(id)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "api not found")
				return
			}
			apimMergeProps(&a.Properties, req.Properties)
			apimApis.Put(id, a)
			sim.WriteJSON(w, http.StatusOK, a)
		case apimOperationsStore:
			sub, rg, svc, api, op := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "api"), sim.PathParam(r, "op")
			key := apimOperationKey(sub, rg, svc, api, op)
			o, ok := apimOperations.Get(key)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "operation not found")
				return
			}
			apimMergeProps(&o.Properties, req.Properties)
			apimOperations.Put(key, o)
			sim.WriteJSON(w, http.StatusOK, o)
		case apimProductsStore:
			id := svcID + "/products/" + sim.PathParam(r, "product")
			p, ok := apimProducts.Get(id)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product not found")
				return
			}
			apimMergeProps(&p.Properties, req.Properties)
			apimProducts.Put(id, p)
			sim.WriteJSON(w, http.StatusOK, p)
		}
	}
}

func apimMergeProps(dst *map[string]any, src map[string]any) {
	if *dst == nil {
		*dst = map[string]any{}
	}
	for k, v := range src {
		(*dst)[k] = v
	}
}

func handleAPIMUpdateService(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	s, ok := apimServices.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	var req APIMService
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	if req.Tags != nil {
		s.Tags = req.Tags
	}
	if req.Sku != nil {
		s.Sku = req.Sku
	}
	apimMergeProps(&s.Properties, req.Properties)
	s.Properties["provisioningState"] = "Succeeded"
	apimServices.Put(id, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIMListServiceSkus(w http.ResponseWriter, r *http.Request) {
	if _, ok := apimServices.Get(apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	skus := []map[string]any{}
	for _, name := range []string{"Developer", "Basic", "Standard", "Premium"} {
		skus = append(skus, map[string]any{
			"resourceType": "Microsoft.ApiManagement/service",
			"sku":          map[string]any{"name": name},
			"capacity": map[string]any{
				"minimum": 1, "maximum": 10, "default": 1, "scaleType": "automatic",
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": skus})
}

func handleAPIMGetSsoToken(w http.ResponseWriter, r *http.Request) {
	name := sim.PathParam(r, "name")
	if _, ok := apimServices.Get(apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), name)); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	redirect := fmt.Sprintf("%s://%s/signin-sso?token=%s",
		azureRequestScheme(r), azureEndpointHost(r, name, "portal", "azure-api"),
		simListKey32(name, "apim-sso"))
	sim.WriteJSON(w, http.StatusOK, map[string]any{"redirectUri": redirect})
}

// handleAPIMServiceLRO backs the long-running service actions (backup, restore,
// migrateToStv2, applyNetworkConfigurationUpdates): a 202 with the standard
// Azure-AsyncOperation + Location headers and an operation that completes
// asynchronously, exactly as the real service does.
func handleAPIMServiceLRO(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	s, ok := apimServices.Get(apimServiceID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	location := s.Location
	if location == "" {
		location = "eastus"
	}
	opID := issueAzureAsyncOperation(nil)
	opURL := azureAsyncOperationHeader(r, sub, "Microsoft.ApiManagement", location, "operationStatuses", opID, "2022-08-01")
	writeAzureAsyncCreateHeaders(w, opURL, azureCurrentRequestURL(r))
	w.WriteHeader(http.StatusAccepted)
}

func handleAPIMListRestOperations(w http.ResponseWriter, r *http.Request) {
	op := func(name, resource, operation string) map[string]any {
		return map[string]any{
			"name":   name,
			"origin": "system",
			"display": map[string]any{
				"provider":  "Microsoft API Management",
				"resource":  resource,
				"operation": operation,
			},
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{
		op("Microsoft.ApiManagement/service/read", "Service", "Read"),
		op("Microsoft.ApiManagement/service/write", "Service", "Write"),
		op("Microsoft.ApiManagement/service/delete", "Service", "Delete"),
	}})
}

func handleAPIMListServicesBySub(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := "/subscriptions/" + sub + "/"
	var all []APIMService
	for _, s := range apimServices.List() {
		if strings.HasPrefix(s.ID, prefix) && strings.Contains(s.ID, "/providers/Microsoft.ApiManagement/service/") {
			all = append(all, s)
		}
	}
	if all == nil {
		all = []APIMService{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleAPIMListDeletedServices(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	prefix := "/subscriptions/" + sub + "/"
	out := []map[string]any{}
	for _, svc := range apimDeleted.List() {
		if !strings.HasPrefix(svc.ID, prefix) {
			continue
		}
		out = append(out, map[string]any{
			"id":       fmt.Sprintf("/subscriptions/%s/providers/Microsoft.ApiManagement/locations/%s/deletedServices/%s", sub, svc.Location, svc.Name),
			"name":     svc.Name,
			"type":     "Microsoft.ApiManagement/deletedservices",
			"location": svc.Location,
			"properties": map[string]any{
				"serviceId": svc.ID,
			},
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleAPIMCheckNameAvailability(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	taken := false
	for _, s := range apimServices.List() {
		if s.Name == req.Name {
			taken = true
			break
		}
	}
	resp := map[string]any{"nameAvailable": !taken, "reason": "Valid"}
	if taken {
		resp["reason"] = "AlreadyExists"
		resp["message"] = fmt.Sprintf("Api Management service name %q is already in use.", req.Name)
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleAPIMGetDomainOwnershipIdentifier(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"domainOwnershipIdentifier": simListKey32(sim.PathParam(r, "subscriptionId"), "apim-domain-ownership"),
	})
}

func handleAPIMListApiRevisions(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/apis/" + sim.PathParam(r, "api")
	a, ok := apimApis.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "api not found")
		return
	}
	rev := "1"
	if v, ok := a.Properties["apiRevision"].(string); ok && v != "" {
		rev = v
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []map[string]any{{
		"apiId":       a.ID,
		"apiRevision": rev,
		"isCurrent":   true,
		"isOnline":    true,
	}}})
}

func handleAPIMListApiProducts(w http.ResponseWriter, r *http.Request) {
	apiID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/apis/" + sim.PathParam(r, "api")
	out := []APIMProduct{}
	for _, assoc := range apimProductApiAssociations() {
		if assoc.apiID != apiID {
			continue
		}
		if p, ok := apimProducts.Get(assoc.productID); ok {
			out = append(out, p)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

// handleAPIMOperationsByTags lists the API's operations grouped with the tags
// assigned to them (Operation_ListByTags → TagResourceCollection). Each entry
// pairs one operation with one tag assigned to it; the operation-tag links are
// the per-operation tag children created via .../operations/{op}/tags/{tag}.
func handleAPIMOperationsByTags(w http.ResponseWriter, r *http.Request) {
	apiID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/apis/" + sim.PathParam(r, "api")
	if _, ok := apimApis.Get(apiID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "api not found")
		return
	}
	opTagType := "Microsoft.ApiManagement/service/apis/operations/tags"
	out := []map[string]any{}
	for _, c := range apimChildren.List() {
		if c.Type != opTagType {
			continue
		}
		// c.ID is <apiID>/operations/<op>/tags/<tag>.
		if !strings.HasPrefix(c.ID, apiID+"/operations/") {
			continue
		}
		rest := strings.TrimPrefix(c.ID, apiID+"/operations/")
		parts := strings.SplitN(rest, "/tags/", 2)
		if len(parts) != 2 {
			continue
		}
		opName, tagName := parts[0], parts[1]
		opEntry := map[string]any{
			"id":   apiID + "/operations/" + opName,
			"name": opName,
		}
		if o, ok := apimOperations.Get(apimOperationKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "api"), opName)); ok {
			if m, ok := o.Properties["method"].(string); ok {
				opEntry["method"] = m
			}
			if u, ok := o.Properties["urlTemplate"].(string); ok {
				opEntry["urlTemplate"] = u
			}
		}
		out = append(out, map[string]any{
			"tag": map[string]any{
				"id":   apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/tags/" + tagName,
				"name": tagName,
			},
			"operation": opEntry,
		})
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out, "count": len(out)})
}

const apimProductApiAssocType = "sockerless.internal/apim/product-api"
const apimProductGroupAssocType = "sockerless.internal/apim/product-group"

type apimProductApiAssoc struct{ productID, apiID string }

func apimProductApiAssociations() []apimProductApiAssoc {
	var out []apimProductApiAssoc
	for _, c := range apimChildren.List() {
		if c.Type != apimProductApiAssocType {
			continue
		}
		api, _ := c.Properties["apiId"].(string)
		prod, _ := c.Properties["productId"].(string)
		out = append(out, apimProductApiAssoc{productID: prod, apiID: api})
	}
	return out
}

func handleAPIMAddProductApi(w http.ResponseWriter, r *http.Request) {
	svcID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	productID := svcID + "/products/" + sim.PathParam(r, "product")
	if _, ok := apimProducts.Get(productID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product not found")
		return
	}
	apiID := svcID + "/apis/" + sim.PathParam(r, "api")
	a, ok := apimApis.Get(apiID)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "api not found")
		return
	}
	path := apimReqPath(r)
	apimChildren.Put(strings.ToLower(path), apimChild{
		ID: path, Name: apimLastSeg(path), Type: apimProductApiAssocType,
		Properties: map[string]any{"productId": productID, "apiId": apiID},
	})
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleAPIMRemoveProductApi(w http.ResponseWriter, r *http.Request) {
	if !apimChildren.Delete(strings.ToLower(apimReqPath(r))) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product api link not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleAPIMListProductApis(w http.ResponseWriter, r *http.Request) {
	svcID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	productID := svcID + "/products/" + sim.PathParam(r, "product")
	out := []APIMApi{}
	for _, assoc := range apimProductApiAssociations() {
		if assoc.productID != productID {
			continue
		}
		if a, ok := apimApis.Get(assoc.apiID); ok {
			out = append(out, a)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleAPIMAddProductGroup(w http.ResponseWriter, r *http.Request) {
	svcID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	productID := svcID + "/products/" + sim.PathParam(r, "product")
	if _, ok := apimProducts.Get(productID); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product not found")
		return
	}
	group := sim.PathParam(r, "group")
	path := apimReqPath(r)
	apimChildren.Put(strings.ToLower(path), apimChild{
		ID: path, Name: group, Type: apimProductGroupAssocType,
		Properties: map[string]any{"productId": productID, "groupId": group},
	})
	sim.WriteJSON(w, http.StatusOK, apimGroupContract(svcID, group))
}

func handleAPIMRemoveProductGroup(w http.ResponseWriter, r *http.Request) {
	if !apimChildren.Delete(strings.ToLower(apimReqPath(r))) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product group link not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleAPIMListProductGroups(w http.ResponseWriter, r *http.Request) {
	svcID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	productID := svcID + "/products/" + sim.PathParam(r, "product")
	out := []map[string]any{}
	for _, c := range apimChildren.List() {
		if c.Type != apimProductGroupAssocType {
			continue
		}
		if prod, _ := c.Properties["productId"].(string); prod != productID {
			continue
		}
		group, _ := c.Properties["groupId"].(string)
		out = append(out, apimGroupContract(svcID, group))
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func apimGroupContract(svcID, group string) map[string]any {
	return map[string]any{
		"id":   svcID + "/groups/" + group,
		"name": group,
		"type": "Microsoft.ApiManagement/service/groups",
		"properties": map[string]any{
			"displayName": group,
			"type":        "custom",
		},
	}
}

func handleAPIMListProductSubscriptions(w http.ResponseWriter, r *http.Request) {
	svcID := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	productID := svcID + "/products/" + sim.PathParam(r, "product")
	out := []APIMSubscription{}
	for _, s := range apimSubscriptions.List() {
		if !strings.HasPrefix(s.ID, svcID+"/subscriptions/") {
			continue
		}
		if scope, _ := s.Properties["scope"].(string); scope == productID {
			out = append(out, s)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleAPIMPatchSubscription(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/subscriptions/" + sim.PathParam(r, "sub")
	s, ok := apimSubscriptions.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	apimMergeProps(&s.Properties, req.Properties)
	apimSubscriptions.Put(id, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

// handleAPIMRegenerateSubKey serves both regeneratePrimaryKey and
// regenerateSecondaryKey. The action verb in the path selects which key slot
// rotates; a subsequent listSecrets returns the new material for that slot
// while the other slot keeps its value. Real API Management answers both with
// 204 No Content and returns the keys only through listSecrets.
func handleAPIMRegenerateSubKey(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/subscriptions/" + sim.PathParam(r, "sub")
	if _, ok := apimSubscriptions.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	slot := "apim-subscription-primary"
	if strings.HasSuffix(strings.ToLower(r.URL.Path), "/regeneratesecondarykey") {
		slot = "apim-subscription-secondary"
	}
	azureBumpKeyGen(id, slot, "")
	w.WriteHeader(http.StatusNoContent)
}

func handleAPIMPatchNamedValue(w http.ResponseWriter, r *http.Request) {
	key := apimNamedValueKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv"))
	nv, ok := apimNamedValues.Get(key)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "NamedValue not found")
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	apimMergeProps(&nv.Properties, req.Properties)
	apimNamedValues.Put(key, nv)
	sim.WriteJSON(w, http.StatusOK, apimRedactNamedValue(nv))
}

// handleAPIMListNamedValueValue surfaces the named value's secret (the only
// operation that returns it; GET/LIST/PUT redact it). NamedValueSecretContract
// is {value}.
func handleAPIMListNamedValueValue(w http.ResponseWriter, r *http.Request) {
	key := apimNamedValueKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv"))
	nv, ok := apimNamedValues.Get(key)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "NamedValue not found")
		return
	}
	value, _ := nv.Properties["value"].(string)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": value})
}

func handleAPIMRefreshNamedValueSecret(w http.ResponseWriter, r *http.Request) {
	key := apimNamedValueKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv"))
	nv, ok := apimNamedValues.Get(key)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "NamedValue not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, apimRedactNamedValue(nv))
}

func handleAPIMPatchBackend(w http.ResponseWriter, r *http.Request) {
	key := apimBackendKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "backend"))
	be, ok := apimBackends.Get(key)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Backend not found")
		return
	}
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	apimMergeProps(&be.Properties, req.Properties)
	apimBackends.Put(key, be)
	sim.WriteJSON(w, http.StatusOK, be)
}

func handleAPIMBackendReconnect(w http.ResponseWriter, r *http.Request) {
	key := apimBackendKey(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "backend"))
	if _, ok := apimBackends.Get(key); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Backend not found")
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
