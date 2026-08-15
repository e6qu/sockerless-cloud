package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// Microsoft Graph directory-object machinery, shared by the application,
// service principal, group, and user slices.
//
// Microsoft Graph serves one directory under two versions — the v1.0 endpoint
// at https://graph.microsoft.com/v1.0 and the beta endpoint at
// https://graph.microsoft.com/beta ("Microsoft Graph REST API endpoints",
// https://learn.microsoft.com/en-us/graph/api/overview) — over the same
// objects. Clients mix the two freely: terraform-provider-azuread reads
// `oauth2RequirePostResponse` on an application and `showInAddressList` on a
// user from beta because v1.0 models neither, and drives the whole group
// family through beta. Every directory route the simulator serves is therefore
// mounted under both prefixes over one store, and the only difference between
// the two responses is the members Graph models on beta alone.
const (
	graphV1   = "/v1.0"
	graphBeta = "/beta"
)

// graphVersionPrefix reports which Graph version served a request, so the
// references a response generates stay inside the version the client called.
func graphVersionPrefix(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, graphBeta+"/") {
		return graphBeta
	}
	return graphV1
}

// graphBetaOnlyMembers lists, per entity set, the members Microsoft Graph
// models on beta but not on v1.0, mapped to the value the beta endpoint
// answers with when the member was never set. A v1.0 response never carries
// them; a beta response always does.
//
//   - `oauth2RequirePostResponse` on an application is beta-only, which is why
//     terraform-provider-azuread reads and writes it through beta
//     (https://github.com/microsoftgraph/msgraph-metadata/issues/273).
//   - `showInAddressList` on a user is served by beta; it is nullable, and
//     Graph answers null until it is set
//     (https://developer.microsoft.com/en-us/graph/known-issues/?search=14972).
//   - `samlMetadataUrl` on a service principal is beta-only, which is why
//     terraform-provider-azuread reads it with a beta `$select`
//     (https://learn.microsoft.com/en-us/graph/api/resources/serviceprincipal).
var graphBetaOnlyMembers = map[string]map[string]any{
	"applications":      {"oauth2RequirePostResponse": false},
	"users":             {"showInAddressList": nil},
	"servicePrincipals": {"samlMetadataUrl": nil},
}

// graphBody is one decoded Microsoft Graph request body: the entity's own
// properties, plus the navigation-property bindings OData carries alongside
// them. `POST /applications` with `"owners@odata.bind": [...]` creates the
// application and its owner references in one request
// (https://learn.microsoft.com/en-us/graph/api/resources/application).
type graphBody struct {
	Props map[string]json.RawMessage
	Binds map[string][]string
}

// graphDecodeBody reads a Graph request body, splitting the OData annotations
// and navigation-property bindings out of the entity's own properties. An
// empty body decodes to an empty document, which is what a POST with no body
// (addPassword) sends.
func graphDecodeBody(r *http.Request) (graphBody, error) {
	out := graphBody{Props: map[string]json.RawMessage{}, Binds: map[string][]string{}}
	raw := map[string]json.RawMessage{}
	if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
		if err == io.EOF {
			return out, nil
		}
		return out, err
	}
	for k, v := range raw {
		switch {
		case strings.HasSuffix(k, "@odata.bind"):
			nav := strings.TrimSuffix(k, "@odata.bind")
			out.Binds[nav] = append(out.Binds[nav], graphBindTargets(v)...)
		case strings.HasPrefix(k, "@odata."), strings.Contains(k, "@odata."):
			// Instance annotations (@odata.type, @odata.context) describe the
			// payload rather than the entity.
		default:
			out.Props[k] = v
		}
	}
	return out, nil
}

// graphBindTargets reads the object IDs out of a navigation-property binding,
// which OData writes as either a single reference URL or an array of them.
func graphBindTargets(raw json.RawMessage) []string {
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		out := make([]string, 0, len(many))
		for _, u := range many {
			if id := graphRefObjectID(u); id != "" {
				out = append(out, id)
			}
		}
		return out
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		if id := graphRefObjectID(one); id != "" {
			return []string{id}
		}
	}
	return nil
}

// graphRefObjectID extracts the object ID from a directory-object reference
// URL — the trailing segment of, for example,
// https://graph.microsoft.com/v1.0/directoryObjects/{id}.
func graphRefObjectID(ref string) string {
	ref = strings.TrimRight(strings.TrimSpace(ref), "/")
	if ref == "" {
		return ""
	}
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// graphMergeProps folds a decoded body's properties into a stored property
// bag. The members the simulator models on the typed record, the members Graph
// assigns itself, and the write-only members Graph never echoes back are named
// in `serviceOwned` and are not stored here.
func graphMergeProps(dst map[string]json.RawMessage, props map[string]json.RawMessage, serviceOwned map[string]bool) map[string]json.RawMessage {
	if dst == nil {
		dst = map[string]json.RawMessage{}
	}
	for k, v := range props {
		if serviceOwned[k] {
			continue
		}
		dst[k] = v
	}
	return dst
}

// graphDoc renders a directory object's wire document: the members the
// simulator models on its typed record, then every other property the client
// stored, then the members that exist only on the version being served.
func graphDoc(r *http.Request, entitySet string, typed map[string]any, props map[string]json.RawMessage) map[string]any {
	doc := make(map[string]any, len(typed)+len(props)+2)
	for k, v := range typed {
		doc[k] = v
	}
	for k, v := range props {
		doc[k] = v
	}
	betaOnly := graphBetaOnlyMembers[entitySet]
	if graphVersionPrefix(r) == graphBeta {
		for k, def := range betaOnly {
			if _, ok := doc[k]; !ok {
				doc[k] = def
			}
		}
	} else {
		for k := range betaOnly {
			delete(doc, k)
		}
	}
	return doc
}

// graphApplySelect narrows a rendered document to the `$select` the client
// asked for. Graph always answers with the object's id alongside the selected
// members ("Use query parameters to customize responses",
// https://learn.microsoft.com/en-us/graph/query-parameters).
func graphApplySelect(r *http.Request, doc map[string]any) map[string]any {
	sel := strings.TrimSpace(r.URL.Query().Get("$select"))
	if sel == "" {
		return doc
	}
	keep := map[string]bool{"@odata.context": true, "id": true}
	for _, f := range strings.Split(sel, ",") {
		if f = strings.TrimSpace(f); f != "" {
			keep[f] = true
		}
	}
	out := make(map[string]any, len(keep))
	for k, v := range doc {
		if keep[k] {
			out[k] = v
		}
	}
	return out
}

// graphFilterDocs evaluates an OData `$filter` over rendered documents. Graph
// answers an unparseable filter with 400 Request_UnsupportedQuery
// (https://learn.microsoft.com/en-us/graph/filter-query-parameter).
func graphFilterDocs(w http.ResponseWriter, r *http.Request, docs []map[string]any) ([]map[string]any, bool) {
	filter := strings.TrimSpace(r.URL.Query().Get("$filter"))
	if filter == "" {
		return docs, true
	}
	node, err := azureParseODataFilter(filter)
	if err != nil {
		sim.AzureError(w, "Request_UnsupportedQuery", err.Error(), http.StatusBadRequest)
		return nil, false
	}
	out := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		flat, err := graphFlattenDoc(doc)
		if err != nil {
			sim.AzureError(w, "Request_UnsupportedQuery", err.Error(), http.StatusBadRequest)
			return nil, false
		}
		if node.eval(flat) {
			out = append(out, doc)
		}
	}
	return out, true
}

// graphFlattenDoc re-decodes a rendered document so the OData evaluator sees
// plain Go values rather than the json.RawMessage the stored property bag
// holds.
func graphFlattenDoc(doc map[string]any) (map[string]any, error) {
	encoded, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(encoded, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// graphCollection writes a Graph collection response, honouring `$select` per
// item and `$count` — which Graph serves as the `@odata.count` member and only
// for requests that carry `ConsistencyLevel: eventual`
// ("Advanced query capabilities on directory objects",
// https://learn.microsoft.com/en-us/graph/aad-advanced-queries).
func graphCollection(w http.ResponseWriter, r *http.Request, context string, docs []map[string]any) {
	values := make([]map[string]any, 0, len(docs))
	for _, doc := range docs {
		values = append(values, graphApplySelect(r, doc))
	}
	body := map[string]any{
		"@odata.context": context,
		"value":          values,
	}
	if graphCountRequested(r) {
		if !graphEventualConsistency(r) {
			sim.AzureError(w, "Request_UnsupportedQuery",
				"Request with $count=true requires the ConsistencyLevel:eventual header.",
				http.StatusBadRequest)
			return
		}
		body["@odata.count"] = len(values)
	}
	sim.WriteJSON(w, http.StatusOK, body)
}

// graphCountRequested reports whether the client asked for `$count=true`.
func graphCountRequested(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("$count")), "true")
}

// graphEventualConsistency reports whether the client opted into the eventually
// consistent directory index by sending `ConsistencyLevel: eventual`, which
// Microsoft Graph requires for `$count`, `$search`, and the advanced `$filter`
// operators on directory objects.
func graphEventualConsistency(r *http.Request) bool {
	return strings.EqualFold(strings.TrimSpace(r.Header.Get("ConsistencyLevel")), "eventual")
}

// ---------------------------------------------------------------------------
// Directory object resolution
// ---------------------------------------------------------------------------

// entraDirectoryObjectDoc renders any directory object by its object ID in the
// polymorphic shape Graph's directoryObjects collection answers with: the
// concrete `@odata.type` plus that type's members. Clients dispatch on the type
// annotation — terraform-provider-azuread sorts group owners into users,
// service principals, and groups by reading it back from
// `GET /directoryObjects/{id}`.
func entraDirectoryObjectDoc(r *http.Request, id string) (map[string]any, bool) {
	if u, ok := entraUsersStore.Get(id); ok {
		doc := entraUserDoc(r, u)
		doc["@odata.type"] = "#microsoft.graph.user"
		return doc, true
	}
	if id == entraDefaultUser.OID {
		doc := entraUserDoc(r, entraDefaultUser)
		doc["@odata.type"] = "#microsoft.graph.user"
		return doc, true
	}
	if g, ok := entraGraphGroupStore.Get(id); ok {
		doc := entraGroupDoc(r, g)
		doc["@odata.type"] = "#microsoft.graph.group"
		return doc, true
	}
	if sp, ok := entraServicePrincipalStore.Get(id); ok {
		doc := entraServicePrincipalDoc(r, sp)
		doc["@odata.type"] = "#microsoft.graph.servicePrincipal"
		return doc, true
	}
	if a, ok := entraApplicationStore.Get(id); ok {
		doc := entraApplicationDoc(r, a)
		doc["@odata.type"] = "#microsoft.graph.application"
		return doc, true
	}
	return nil, false
}

// entraGraphNotFound answers with the message Microsoft Graph returns when a
// request addresses a directory object that is not there.
func entraGraphNotFound(w http.ResponseWriter, id string) {
	sim.AzureError(w, "Request_ResourceNotFound",
		fmt.Sprintf("Resource '%s' does not exist or one of its queried reference-property objects are not present.", id),
		http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// owners
// ---------------------------------------------------------------------------

// entraDirectoryOwner records one directory object owning another — the
// `owners` navigation property Graph exposes on applications, service
// principals, and groups.
type entraDirectoryOwner struct {
	ObjectID string `json:"objectId"`
	OwnerID  string `json:"ownerId"`
}

var entraDirectoryOwnerStore sim.Store[entraDirectoryOwner]

func entraOwnerKey(objectID, ownerID string) string { return objectID + "/" + ownerID }

// entraAddOwners records owner references, ignoring duplicates the way Graph's
// reference collection does not — Graph answers a duplicate `$ref` POST with
// 400, so the caller checks first.
func entraAddOwners(objectID string, ownerIDs []string) {
	for _, ownerID := range ownerIDs {
		if ownerID == "" {
			continue
		}
		entraDirectoryOwnerStore.Put(entraOwnerKey(objectID, ownerID), entraDirectoryOwner{ObjectID: objectID, OwnerID: ownerID})
	}
}

func entraOwnersOf(objectID string) []string {
	rows := entraDirectoryOwnerStore.Filter(func(o entraDirectoryOwner) bool { return o.ObjectID == objectID })
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.OwnerID)
	}
	return out
}

// entraDropOwners removes every owner reference held against an object, which
// is what deleting the object does to its reference collection.
func entraDropOwners(objectID string) {
	for _, ownerID := range entraOwnersOf(objectID) {
		entraDirectoryOwnerStore.Delete(entraOwnerKey(objectID, ownerID))
	}
}

// entraOwnerExists reports whether the object exists at all, so an owner route
// can answer 404 for an unknown parent exactly as Graph does.
func entraOwnableExists(objectID string) bool {
	if _, ok := entraApplicationStore.Get(objectID); ok {
		return true
	}
	if _, ok := entraServicePrincipalStore.Get(objectID); ok {
		return true
	}
	_, ok := entraGraphGroupStore.Get(objectID)
	return ok
}

// handleGraphListOwners serves `GET /{version}/{set}/{id}/owners`, the owner
// reference collection Graph exposes on applications, service principals, and
// groups.
func handleGraphListOwners(w http.ResponseWriter, r *http.Request, objectID string) {
	if !entraOwnableExists(objectID) {
		entraGraphNotFound(w, objectID)
		return
	}
	docs := make([]map[string]any, 0)
	for _, ownerID := range entraOwnersOf(objectID) {
		if doc, ok := entraDirectoryObjectDoc(r, ownerID); ok {
			docs = append(docs, doc)
		}
	}
	graphCollection(w, r, "$metadata#directoryObjects", docs)
}

// handleGraphAddOwnerRef serves `POST /{version}/{set}/{id}/owners/$ref`.
func handleGraphAddOwnerRef(w http.ResponseWriter, r *http.Request, objectID string) {
	if !entraOwnableExists(objectID) {
		entraGraphNotFound(w, objectID)
		return
	}
	var req struct {
		ODataID string `json:"@odata.id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	ownerID := graphRefObjectID(req.ODataID)
	if ownerID == "" {
		sim.AzureError(w, "Request_BadRequest", "@odata.id is required", http.StatusBadRequest)
		return
	}
	if _, ok := entraDirectoryObjectDoc(r, ownerID); !ok {
		entraGraphNotFound(w, ownerID)
		return
	}
	entraAddOwners(objectID, []string{ownerID})
	w.WriteHeader(http.StatusNoContent)
}

// handleGraphRemoveOwnerRef serves
// `DELETE /{version}/{set}/{id}/owners/{ownerId}/$ref`.
func handleGraphRemoveOwnerRef(w http.ResponseWriter, r *http.Request, objectID, ownerID string) {
	if !entraOwnableExists(objectID) {
		entraGraphNotFound(w, objectID)
		return
	}
	if !entraDirectoryOwnerStore.Delete(entraOwnerKey(objectID, ownerID)) {
		entraGraphNotFound(w, ownerID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// manager
// ---------------------------------------------------------------------------

// entraUserManager records a user's manager — the `manager` navigation
// property on a Graph user.
type entraUserManager struct {
	UserID    string `json:"userId"`
	ManagerID string `json:"managerId"`
}

var entraUserManagerStore sim.Store[entraUserManager]

// registerEntraDirectory mounts the routes that are not scoped to a single
// entity set: the polymorphic directoryObjects reads, and the user manager
// navigation property. Both Graph versions serve them.
func registerEntraDirectory(srv *sim.Server) {
	entraDirectoryOwnerStore = sim.MakeStore[entraDirectoryOwner](srv.DB(), "entra_directory_owners")
	entraUserManagerStore = sim.MakeStore[entraUserManager](srv.DB(), "entra_user_managers")

	srv.HandleFunc("GET /v1.0/directoryObjects/{objectId}", handleGraphGetDirectoryObject)
	srv.HandleFunc("GET /beta/directoryObjects/{objectId}", handleGraphGetDirectoryObject)

	srv.HandleFunc("GET /v1.0/users/{userId}/manager", handleGraphGetManager)
	srv.HandleFunc("GET /beta/users/{userId}/manager", handleGraphGetManager)
	srv.HandleFunc("PUT /v1.0/users/{userId}/manager/$ref", handleGraphSetManagerRef)
	srv.HandleFunc("PUT /beta/users/{userId}/manager/$ref", handleGraphSetManagerRef)
	srv.HandleFunc("DELETE /v1.0/users/{userId}/manager/$ref", handleGraphRemoveManagerRef)
	srv.HandleFunc("DELETE /beta/users/{userId}/manager/$ref", handleGraphRemoveManagerRef)
}

func handleGraphGetDirectoryObject(w http.ResponseWriter, r *http.Request) {
	id := sim.PathParam(r, "objectId")
	doc, ok := entraDirectoryObjectDoc(r, id)
	if !ok {
		entraGraphNotFound(w, id)
		return
	}
	doc["@odata.context"] = "$metadata#directoryObjects/$entity"
	sim.WriteJSON(w, http.StatusOK, graphApplySelect(r, doc))
}

func handleGraphGetManager(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	if _, ok := entraLookupUser(userID); !ok {
		entraGraphNotFound(w, userID)
		return
	}
	link, ok := entraUserManagerStore.Get(userID)
	if !ok {
		entraGraphNotFound(w, userID)
		return
	}
	doc, ok := entraDirectoryObjectDoc(r, link.ManagerID)
	if !ok {
		entraGraphNotFound(w, link.ManagerID)
		return
	}
	doc["@odata.context"] = "$metadata#directoryObjects/$entity"
	sim.WriteJSON(w, http.StatusOK, graphApplySelect(r, doc))
}

func handleGraphSetManagerRef(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	if _, ok := entraLookupUser(userID); !ok {
		entraGraphNotFound(w, userID)
		return
	}
	var req struct {
		ODataID string `json:"@odata.id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		sim.AzureError(w, "Request_BadRequest", err.Error(), http.StatusBadRequest)
		return
	}
	managerID := graphRefObjectID(req.ODataID)
	if managerID == "" {
		sim.AzureError(w, "Request_BadRequest", "@odata.id is required", http.StatusBadRequest)
		return
	}
	if _, ok := entraDirectoryObjectDoc(r, managerID); !ok {
		entraGraphNotFound(w, managerID)
		return
	}
	entraUserManagerStore.Put(userID, entraUserManager{UserID: userID, ManagerID: managerID})
	w.WriteHeader(http.StatusNoContent)
}

func handleGraphRemoveManagerRef(w http.ResponseWriter, r *http.Request) {
	userID := sim.PathParam(r, "userId")
	if !entraUserManagerStore.Delete(userID) {
		entraGraphNotFound(w, userID)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
