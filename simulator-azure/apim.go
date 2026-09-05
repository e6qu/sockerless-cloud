package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.ApiManagement ARM control plane. Real Azure exposes
// ~60 ops across Service / Apis / Operations / Products /
// Subscriptions / Backends / NamedValues / Policy. The sim
// implements the Service + Api + Product + Subscription slice —
// sufficient for terraform-provider-azurerm `azurerm_api_management*`
// resources.

type APIMService struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location,omitempty"`
	Sku        map[string]any    `json:"sku,omitempty"`
	Properties map[string]any    `json:"properties,omitempty"`
	Tags       map[string]string `json:"tags,omitempty"`
}

type APIMApi struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type APIMProduct struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

type APIMSubscription struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// APIMOperation is one operation under an API. Real Azure paths:
// `Microsoft.ApiManagement/service/{svc}/apis/{api}/operations/{op}`.
type APIMOperation struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// APIMBackend models a backend endpoint registered under a service.
type APIMBackend struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

// APIMNamedValue models a per-service named value (typed key/value
// used for variable substitution inside policies).
type APIMNamedValue struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Properties map[string]any `json:"properties,omitempty"`
}

var (
	apimServices      sim.Store[APIMService]
	apimApis          sim.Store[APIMApi]
	apimProducts      sim.Store[APIMProduct]
	apimSubscriptions sim.Store[APIMSubscription]
	apimOperations    sim.Store[APIMOperation]
	apimBackends      sim.Store[APIMBackend]
	apimNamedValues   sim.Store[APIMNamedValue]
	apimDeleted       sim.Store[APIMService] // soft-deleted services awaiting purge
)

func registerAPIM(srv *sim.Server) {
	makeAzureKeyGens(srv)
	apimServices = sim.MakeStore[APIMService](srv.DB(), "apim_services")
	apimApis = sim.MakeStore[APIMApi](srv.DB(), "apim_apis")
	apimProducts = sim.MakeStore[APIMProduct](srv.DB(), "apim_products")
	apimSubscriptions = sim.MakeStore[APIMSubscription](srv.DB(), "apim_subscriptions")
	apimOperations = sim.MakeStore[APIMOperation](srv.DB(), "apim_operations")
	apimBackends = sim.MakeStore[APIMBackend](srv.DB(), "apim_backends")
	apimNamedValues = sim.MakeStore[APIMNamedValue](srv.DB(), "apim_named_values")
	apimDeleted = sim.MakeStore[APIMService](srv.DB(), "apim_deleted_services")

	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.ApiManagement/service"

	srv.HandleFunc("PUT "+base+"/{name}", handleAPIMCreateService)
	srv.HandleFunc("GET "+base+"/{name}", handleAPIMGetService)
	srv.HandleFunc("DELETE "+base+"/{name}", handleAPIMDeleteService)
	srv.HandleFunc("GET "+base, handleAPIMListServicesByRG)

	// Soft-deleted-service (purge) surface — subscription+location scoped, no
	// resourceGroup. terraform-provider-azurerm purges by default on destroy:
	// after DELETE service it GETs the deleted service then DELETEs (purges) it.
	const deletedBase = "/subscriptions/{subscriptionId}/providers/Microsoft.ApiManagement/locations/{location}/deletedServices/{name}"
	srv.HandleFunc("GET "+deletedBase, handleAPIMGetDeletedService)
	srv.HandleFunc("DELETE "+deletedBase, handleAPIMPurgeDeletedService)

	srv.HandleFunc("PUT "+base+"/{name}/apis/{api}", handleAPIMCreateApi)
	srv.HandleFunc("GET "+base+"/{name}/apis/{api}", handleAPIMGetApi)
	srv.HandleFunc("DELETE "+base+"/{name}/apis/{api}", handleAPIMDeleteApi)
	srv.HandleFunc("GET "+base+"/{name}/apis", handleAPIMListApis)

	srv.HandleFunc("PUT "+base+"/{name}/products/{product}", handleAPIMCreateProduct)
	srv.HandleFunc("GET "+base+"/{name}/products/{product}", handleAPIMGetProduct)
	srv.HandleFunc("DELETE "+base+"/{name}/products/{product}", handleAPIMDeleteProduct)
	srv.HandleFunc("GET "+base+"/{name}/products", handleAPIMListProducts)

	srv.HandleFunc("PUT "+base+"/{name}/subscriptions/{sub}", handleAPIMCreateSubscription)
	srv.HandleFunc("GET "+base+"/{name}/subscriptions/{sub}", handleAPIMGetSubscription)
	srv.HandleFunc("DELETE "+base+"/{name}/subscriptions/{sub}", handleAPIMDeleteSubscription)
	srv.HandleFunc("GET "+base+"/{name}/subscriptions", handleAPIMListSubscriptions)
	// AzurePathNormalizationMiddleware lowercases action verbs, so the route
	// must be registered lowercase (`listsecrets`) to match the provider's
	// `POST .../listSecrets` after normalization.
	srv.HandleFunc("POST "+base+"/{name}/subscriptions/{sub}/listsecrets", handleAPIMListSubscriptionSecrets)

	// Operations: scoped to an API. Path:
	// /service/{svc}/apis/{api}/operations/{op}.
	srv.HandleFunc("PUT "+base+"/{name}/apis/{api}/operations/{op}", handleAPIMCreateOperation)
	srv.HandleFunc("GET "+base+"/{name}/apis/{api}/operations/{op}", handleAPIMGetOperation)
	srv.HandleFunc("DELETE "+base+"/{name}/apis/{api}/operations/{op}", handleAPIMDeleteOperation)
	srv.HandleFunc("GET "+base+"/{name}/apis/{api}/operations", handleAPIMListOperations)

	// Backends: scoped to the service.
	srv.HandleFunc("PUT "+base+"/{name}/backends/{backend}", handleAPIMCreateBackend)
	srv.HandleFunc("GET "+base+"/{name}/backends/{backend}", handleAPIMGetBackend)
	srv.HandleFunc("DELETE "+base+"/{name}/backends/{backend}", handleAPIMDeleteBackend)
	srv.HandleFunc("GET "+base+"/{name}/backends", handleAPIMListBackends)

	// NamedValues: scoped to the service.
	srv.HandleFunc("PUT "+base+"/{name}/namedValues/{nv}", handleAPIMCreateNamedValue)
	srv.HandleFunc("GET "+base+"/{name}/namedValues/{nv}", handleAPIMGetNamedValue)
	srv.HandleFunc("DELETE "+base+"/{name}/namedValues/{nv}", handleAPIMDeleteNamedValue)
	srv.HandleFunc("GET "+base+"/{name}/namedValues", handleAPIMListNamedValues)

	registerAPIMMore(srv)
}

// apimRedactNamedValue returns a copy of the named value with the secret
// `value` stripped from properties. A named value's secret is write-only on the
// real service: GET / LIST / PUT / PATCH never echo it; only listValue does.
func apimRedactNamedValue(nv APIMNamedValue) APIMNamedValue {
	if nv.Properties == nil {
		return nv
	}
	if _, ok := nv.Properties["value"]; !ok {
		return nv
	}
	redacted := make(map[string]any, len(nv.Properties))
	for k, v := range nv.Properties {
		if k == "value" {
			continue
		}
		redacted[k] = v
	}
	nv.Properties = redacted
	return nv
}

// Sub-resource handlers — each follows the same shape: PUT replaces,
// GET returns the row or 404, DELETE removes, LIST filters by parent.
// Store keys are `<sub>/<rg>/<service>[/api]/<resource>` so cascade
// delete + per-service filter stay cheap.

func apimOperationKey(sub, rg, svc, api, op string) string {
	return sub + "/" + rg + "/" + svc + "/" + api + "/" + op
}
func apimBackendKey(sub, rg, svc, b string) string { return sub + "/" + rg + "/" + svc + "/" + b }
func apimNamedValueKey(sub, rg, svc, n string) string {
	return sub + "/" + rg + "/" + svc + "/" + n
}

func handleAPIMCreateOperation(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	svc := sim.PathParam(r, "name")
	api := sim.PathParam(r, "api")
	op := sim.PathParam(r, "op")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	o := APIMOperation{
		ID:         apimServiceID(sub, rg, svc) + "/apis/" + api + "/operations/" + op,
		Name:       op,
		Type:       "Microsoft.ApiManagement/service/apis/operations",
		Properties: req.Properties,
	}
	apimOperations.Put(apimOperationKey(sub, rg, svc, api, op), o)
	sim.WriteJSON(w, http.StatusOK, o)
}
func handleAPIMGetOperation(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, api, op := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "api"), sim.PathParam(r, "op")
	o, ok := apimOperations.Get(apimOperationKey(sub, rg, svc, api, op))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found", op)
		return
	}
	sim.WriteJSON(w, http.StatusOK, o)
}
func handleAPIMDeleteOperation(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, api, op := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "api"), sim.PathParam(r, "op")
	if !apimOperations.Delete(apimOperationKey(sub, rg, svc, api, op)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Operation %q not found", op)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func handleAPIMListOperations(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, api := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "api")
	prefix := apimServiceID(sub, rg, svc) + "/apis/" + api + "/operations/"
	all := apimOperations.Filter(func(o APIMOperation) bool { return strings.HasPrefix(o.ID, prefix) })
	if all == nil {
		all = []APIMOperation{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleAPIMCreateBackend(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, b := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "backend")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	be := APIMBackend{
		ID:         apimServiceID(sub, rg, svc) + "/backends/" + b,
		Name:       b,
		Type:       "Microsoft.ApiManagement/service/backends",
		Properties: req.Properties,
	}
	apimBackends.Put(apimBackendKey(sub, rg, svc, b), be)
	sim.WriteJSON(w, http.StatusOK, be)
}
func handleAPIMGetBackend(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, b := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "backend")
	be, ok := apimBackends.Get(apimBackendKey(sub, rg, svc, b))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Backend %q not found", b)
		return
	}
	sim.WriteJSON(w, http.StatusOK, be)
}
func handleAPIMDeleteBackend(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, b := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "backend")
	if !apimBackends.Delete(apimBackendKey(sub, rg, svc, b)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "Backend %q not found", b)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func handleAPIMListBackends(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	prefix := apimServiceID(sub, rg, svc) + "/backends/"
	all := apimBackends.Filter(func(b APIMBackend) bool { return strings.HasPrefix(b.ID, prefix) })
	if all == nil {
		all = []APIMBackend{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func handleAPIMCreateNamedValue(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, n := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv")
	var req struct {
		Properties map[string]any `json:"properties"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	nv := APIMNamedValue{
		ID:         apimServiceID(sub, rg, svc) + "/namedValues/" + n,
		Name:       n,
		Type:       "Microsoft.ApiManagement/service/namedValues",
		Properties: req.Properties,
	}
	apimNamedValues.Put(apimNamedValueKey(sub, rg, svc, n), nv)
	sim.WriteJSON(w, http.StatusOK, apimRedactNamedValue(nv))
}
func handleAPIMGetNamedValue(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, n := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv")
	nv, ok := apimNamedValues.Get(apimNamedValueKey(sub, rg, svc, n))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "NamedValue %q not found", n)
		return
	}
	sim.WriteJSON(w, http.StatusOK, apimRedactNamedValue(nv))
}
func handleAPIMDeleteNamedValue(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc, n := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"), sim.PathParam(r, "nv")
	if !apimNamedValues.Delete(apimNamedValueKey(sub, rg, svc, n)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "NamedValue %q not found", n)
		return
	}
	w.WriteHeader(http.StatusOK)
}
func handleAPIMListNamedValues(w http.ResponseWriter, r *http.Request) {
	sub, rg, svc := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")
	prefix := apimServiceID(sub, rg, svc) + "/namedValues/"
	all := apimNamedValues.Filter(func(nv APIMNamedValue) bool { return strings.HasPrefix(nv.ID, prefix) })
	for i := range all {
		all[i] = apimRedactNamedValue(all[i])
	}
	if all == nil {
		all = []APIMNamedValue{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
}

func apimServiceID(sub, rg, name string) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ApiManagement/service/%s", sub, rg, name)
}

func handleAPIMCreateService(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	var req APIMService
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := apimServiceID(sub, rg, name)
	s := APIMService{
		ID:       id,
		Name:     name,
		Type:     "Microsoft.ApiManagement/service",
		Location: req.Location,
		Sku:      req.Sku,
		Tags:     req.Tags,
		Properties: map[string]any{
			"provisioningState": "Succeeded",
			"gatewayUrl":        fmt.Sprintf("%s://%s", azureRequestScheme(r), azureEndpointHost(r, name, "azure-api")),
			"portalUrl":         fmt.Sprintf("%s://%s", azureRequestScheme(r), azureEndpointHost(r, name, "portal", "azure-api")),
			"managementApiUrl":  fmt.Sprintf("%s://%s", azureRequestScheme(r), azureEndpointHost(r, name, "management", "azure-api")),
		},
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			s.Properties[k] = v
		}
		s.Properties["provisioningState"] = "Succeeded"
	}
	if s.Sku == nil {
		s.Sku = map[string]any{"name": "Developer", "capacity": 1}
	}
	apimServices.Put(id, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIMGetService(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	s, ok := apimServices.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func apimDeletedKey(sub, location, name string) string {
	return sub + "/" + location + "/" + name
}

func handleAPIMDeleteService(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	id := apimServiceID(sub, sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	svc, ok := apimServices.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	apimServices.Delete(id)
	// Move the service to the soft-deleted store so the provider's default
	// purge step (GET + DELETE deletedServices) can find and purge it.
	apimDeleted.Put(apimDeletedKey(sub, svc.Location, svc.Name), svc)
	prefix := id + "/"
	for _, a := range apimApis.List() {
		if strings.HasPrefix(a.ID, prefix) {
			apimApis.Delete(a.ID)
		}
	}
	for _, p := range apimProducts.List() {
		if strings.HasPrefix(p.ID, prefix) {
			apimProducts.Delete(p.ID)
		}
	}
	for _, s := range apimSubscriptions.List() {
		if strings.HasPrefix(s.ID, prefix) {
			apimSubscriptions.Delete(s.ID)
			apimDropSubscriptionKeyGens(s.ID)
		}
	}
	rg := sim.PathParam(r, "resourceGroupName")
	name := sim.PathParam(r, "name")
	for _, o := range apimOperations.List() {
		if rest, found := strings.CutPrefix(o.ID, id+"/apis/"); found {
			if api, op, ok := strings.Cut(rest, "/operations/"); ok {
				apimOperations.Delete(apimOperationKey(sub, rg, name, api, op))
			}
		}
	}
	for _, b := range apimBackends.List() {
		if strings.HasPrefix(b.ID, prefix) {
			apimBackends.Delete(apimBackendKey(sub, rg, name, b.Name))
		}
	}
	for _, nv := range apimNamedValues.List() {
		if strings.HasPrefix(nv.ID, prefix) {
			apimNamedValues.Delete(apimNamedValueKey(sub, rg, name, nv.Name))
		}
	}
	for _, c := range apimChildren.List() {
		if strings.HasPrefix(strings.ToLower(c.ID), strings.ToLower(prefix)) {
			apimChildren.Delete(strings.ToLower(c.ID))
		}
	}
	// All APIM deletes return a synchronous 200: terraform-provider-azurerm's
	// sub-resource delete clients require 200 (not 202), and its service
	// DeleteThenPoll treats a 200 as an immediately-complete LRO. A bare 202
	// with no Location header makes both error "unexpected status 202".
	w.WriteHeader(http.StatusOK)
}

// handleAPIMGetDeletedService serves the soft-deleted service (DeletedService
// contract). The provider GETs this both before create (404 → name is free)
// and during purge (200 → proceed to DELETE/purge).
func handleAPIMGetDeletedService(w http.ResponseWriter, r *http.Request) {
	sub, location, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location"), sim.PathParam(r, "name")
	svc, ok := apimDeleted.Get(apimDeletedKey(sub, location, name))
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "deleted service not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"id":       fmt.Sprintf("/subscriptions/%s/providers/Microsoft.ApiManagement/locations/%s/deletedServices/%s", sub, location, name),
		"name":     name,
		"location": location,
		"type":     "Microsoft.ApiManagement/deletedservices",
		"properties": map[string]any{
			"serviceId": svc.ID,
		},
	})
}

// handleAPIMPurgeDeletedService hard-deletes (purges) the soft-deleted service.
func handleAPIMPurgeDeletedService(w http.ResponseWriter, r *http.Request) {
	sub, location, name := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location"), sim.PathParam(r, "name")
	if !apimDeleted.Delete(apimDeletedKey(sub, location, name)) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "deleted service not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleAPIMListServicesByRG(w http.ResponseWriter, r *http.Request) {
	prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ApiManagement/service/",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"))
	var all []APIMService
	for _, s := range apimServices.List() {
		if strings.HasPrefix(s.ID, prefix) {
			all = append(all, s)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	filtered, err := azureApplyListQuery(all, r)
	if err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	all = filtered
	page, next := armPage(r, all)
	if page == nil {
		page = []APIMService{}
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleAPIMCreateApi(w http.ResponseWriter, r *http.Request) {
	parent := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := apimServices.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	apiName := sim.PathParam(r, "api")
	var req APIMApi
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := parent + "/apis/" + apiName
	a := APIMApi{
		ID: id, Name: apiName, Type: "Microsoft.ApiManagement/service/apis",
		Properties: map[string]any{"path": apiName},
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			a.Properties[k] = v
		}
	}
	// ARM Microsoft.ApiManagement/service/apis defaults, applied only when the
	// request omits them. terraform-provider-azurerm reads apiRevision (default
	// "1"); a missing revision forces a replacement on every plan.
	if v, ok := a.Properties["apiRevision"]; !ok || v == nil || v == "" {
		a.Properties["apiRevision"] = "1"
	}
	if _, ok := a.Properties["isCurrent"]; !ok {
		a.Properties["isCurrent"] = true
	}
	// The create request carries the API kind as apiType
	// (ApiCreateOrUpdateProperties); read shapes expose it as type
	// (ApiEntityBaseContract). Translate and never echo apiType back.
	if v, ok := a.Properties["apiType"]; ok && v != nil && v != "" {
		a.Properties["type"] = v
	}
	delete(a.Properties, "apiType")
	if v, ok := a.Properties["type"]; !ok || v == nil || v == "" {
		a.Properties["type"] = "http"
	}
	apimApis.Put(id, a)
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleAPIMGetApi(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/apis/" + sim.PathParam(r, "api")
	a, ok := apimApis.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "api not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, a)
}

func handleAPIMDeleteApi(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/apis/" + sim.PathParam(r, "api")
	if !apimApis.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "api not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleAPIMListApis(w http.ResponseWriter, r *http.Request) {
	prefix := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/apis/"
	var all []APIMApi
	for _, a := range apimApis.List() {
		if strings.HasPrefix(a.ID, prefix) {
			all = append(all, a)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID < all[j].ID })
	filtered, err := azureApplyListQuery(all, r)
	if err != nil {
		AzureError(w, "BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	all = filtered
	page, next := armPage(r, all)
	if page == nil {
		page = []APIMApi{}
	}
	out := map[string]any{"value": page}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

func handleAPIMCreateProduct(w http.ResponseWriter, r *http.Request) {
	parent := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := apimServices.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	pName := sim.PathParam(r, "product")
	var req APIMProduct
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	id := parent + "/products/" + pName
	p := APIMProduct{
		ID: id, Name: pName, Type: "Microsoft.ApiManagement/service/products",
		Properties: map[string]any{"displayName": pName, "state": "published"},
	}
	if req.Properties != nil {
		for k, v := range req.Properties {
			p.Properties[k] = v
		}
	}
	apimProducts.Put(id, p)
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIMGetProduct(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/products/" + sim.PathParam(r, "product")
	p, ok := apimProducts.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, p)
}

func handleAPIMDeleteProduct(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/products/" + sim.PathParam(r, "product")
	if !apimProducts.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "product not found")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleAPIMListProducts(w http.ResponseWriter, r *http.Request) {
	prefix := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/products/"
	var out []APIMProduct
	for _, p := range apimProducts.List() {
		if strings.HasPrefix(p.ID, prefix) {
			out = append(out, p)
		}
	}
	if out == nil {
		out = []APIMProduct{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}

func handleAPIMCreateSubscription(w http.ResponseWriter, r *http.Request) {
	parent := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name"))
	if _, ok := apimServices.Get(parent); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "service not found")
		return
	}
	sName := sim.PathParam(r, "sub")
	var req APIMSubscription
	if err := sim.ReadJSON(r, &req); err != nil {
		AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
		return
	}
	// The contract requires both the scope the subscription is for and its
	// state. The state has a default the service applies; the scope names what
	// is being subscribed to and only the caller knows it, so a create without
	// one is refused rather than stored as a subscription to nothing.
	if req.Properties == nil {
		req.Properties = map[string]any{}
	}
	if scope, ok := req.Properties["scope"].(string); !ok || scope == "" {
		AzureErrorf(w, "ValidationError", http.StatusBadRequest,
			"Property \"scope\" is required for Microsoft.ApiManagement/service/subscriptions.")
		return
	}
	id := parent + "/subscriptions/" + sName
	s := APIMSubscription{
		ID: id, Name: sName, Type: "Microsoft.ApiManagement/service/subscriptions",
		Properties: map[string]any{"state": "active"},
	}
	for k, v := range req.Properties {
		s.Properties[k] = v
	}
	apimSubscriptions.Put(id, s)
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIMGetSubscription(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/subscriptions/" + sim.PathParam(r, "sub")
	s, ok := apimSubscriptions.Get(id)
	if !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleAPIMDeleteSubscription(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/subscriptions/" + sim.PathParam(r, "sub")
	if !apimSubscriptions.Delete(id) {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	apimDropSubscriptionKeyGens(id)
	w.WriteHeader(http.StatusOK)
}

// handleAPIMListSubscriptionSecrets returns the subscription's primary and
// secondary keys (ARM SubscriptionKeysContract). terraform-provider-azurerm's
// azurerm_api_management_subscription Read calls POST .../listSecrets after
// create to populate primary_key/secondary_key; without it the create hangs.
func handleAPIMListSubscriptionSecrets(w http.ResponseWriter, r *http.Request) {
	id := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) +
		"/subscriptions/" + sim.PathParam(r, "sub")
	if _, ok := apimSubscriptions.Get(id); !ok {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "subscription not found")
		return
	}
	sim.WriteJSON(w, http.StatusOK, apimSubscriptionKeysBody(id))
}

// apimSubscriptionKeysBody is the SubscriptionKeysContract listSecrets
// returns, reflecting every regeneratePrimaryKey / regenerateSecondaryKey
// performed so far.
func apimSubscriptionKeysBody(id string) map[string]any {
	return map[string]any{
		"primaryKey":   azureKeyMaterial32(id, "apim-subscription-primary"),
		"secondaryKey": azureKeyMaterial32(id, "apim-subscription-secondary"),
	}
}

// apimDropSubscriptionKeyGens removes a deleted subscription's key-rotation
// state so a later subscription created under the same ID starts from fresh
// key material.
func apimDropSubscriptionKeyGens(id string) {
	azureDropKeyGens(id, "apim-subscription-primary", "apim-subscription-secondary")
}

func handleAPIMListSubscriptions(w http.ResponseWriter, r *http.Request) {
	prefix := apimServiceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), sim.PathParam(r, "name")) + "/subscriptions/"
	var out []APIMSubscription
	for _, s := range apimSubscriptions.List() {
		if strings.HasPrefix(s.ID, prefix) {
			out = append(out, s)
		}
	}
	if out == nil {
		out = []APIMSubscription{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"value": out})
}
