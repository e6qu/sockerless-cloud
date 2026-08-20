package main

import (
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// TagsResource mirrors Microsoft.Resources/tags/default — the canonical
// surface for managing tags on any ARM scope (subscription, resource
// group, or individual resource) without having to PUT the full
// resource.
type TagsResource struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Properties TagsBody `json:"properties"`
}

// TagsBody is the `properties` envelope returned by every operation
// against /providers/Microsoft.Resources/tags/default.
type TagsBody struct {
	Tags map[string]string `json:"tags"`
}

// TagsPatchRequest is the body of a PATCH against
// /providers/Microsoft.Resources/tags/default.
type TagsPatchRequest struct {
	Operation  string   `json:"operation"`
	Properties TagsBody `json:"properties"`
}

// tagsStore holds the tags of the two scopes that own no resource row of their
// own — a subscription and a management group. Every other scope's tags live
// with the thing they tag: a resource group's on its own record, a resource's
// on the resource, reached through the cross-slice registry. Azure Resource
// Manager keeps exactly one set of tags per scope, so tags/default and the
// scope's own API always report the same map. Keyed by the lowercased scope.
var tagsStore sim.Store[TagsResource]

const tagsDefaultMarker = "/providers/microsoft.resources/tags/default"

func registerTags(srv *sim.Server) {
	tagsStore = sim.MakeStore[TagsResource](srv.DB(), "azure_tags_default")

	// The subscription- and resource-group-scope spellings of
	// `Microsoft.Resources/tags/default` are fixed-depth, so they mount as
	// ordinary mux routes (Tags_GetAtScope / CreateOrUpdateAtScope /
	// UpdateAtScope / DeleteAtScope).
	for _, scopeBase := range []string{
		"/subscriptions/{subscriptionId}",
		"/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}",
	} {
		p := scopeBase + "/providers/Microsoft.Resources/tags/default"
		srv.HandleFunc("GET "+p, handleTagsDefaultRoute)
		srv.HandleFunc("PUT "+p, handleTagsDefaultRoute)
		srv.HandleFunc("PATCH "+p, handleTagsDefaultRoute)
		srv.HandleFunc("DELETE "+p, handleTagsDefaultRoute)
	}

	// Resource-scope and management-group-scope spellings sit at the end of a
	// variable-depth ARM scope path. Go 1.22 ServeMux can't match a
	// variable-depth scope prefix with a fixed suffix, so those are dispatched
	// via WrapHandler (same shape as authorization.go's role-definitions
	// dispatcher). Subscription / resource-group scopes — which carry no
	// embedded `/providers/` — fall through to the mux routes above.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Match the marker case-insensitively against the ORIGINAL path's
			// own bytes — slicing r.URL.Path by an offset derived from a
			// strings.ToLower copy is unsafe, because case-folding can change
			// byte length and push len(path)-len(marker) negative.
			if len(r.URL.Path) < len(tagsDefaultMarker) ||
				!strings.EqualFold(r.URL.Path[len(r.URL.Path)-len(tagsDefaultMarker):], tagsDefaultMarker) {
				next.ServeHTTP(w, r)
				return
			}
			scope := strings.TrimSuffix(r.URL.Path[:len(r.URL.Path)-len(tagsDefaultMarker)], "/")
			if strings.HasPrefix(strings.ToLower(scope), "/subscriptions/") && !strings.Contains(strings.ToLower(scope), "/providers/") {
				// Subscription / resource-group scope: served by the mux routes.
				next.ServeHTTP(w, r)
				return
			}
			handleTagsDefault(w, r, scope)
		})
	})
}

// handleTagsDefaultRoute serves the subscription- and resource-group-scope
// tags/default routes, reconstructing the scope from the path parameters.
func handleTagsDefaultRoute(w http.ResponseWriter, r *http.Request) {
	scope := "/subscriptions/" + sim.PathParam(r, "subscriptionId")
	if rg := sim.PathParam(r, "resourceGroupName"); rg != "" {
		scope += "/resourceGroups/" + rg
	}
	handleTagsDefault(w, r, scope)
}

// tagScope is the one set of tags an ARM scope holds, wherever it is stored.
// Resolving a scope to a single holder is what makes tags/default and the
// scope's own API two spellings of the same state rather than two planes.
type tagScope struct {
	// id is the canonical Microsoft.Resources/tags/default resource ID, built
	// from the scope's own stored spelling.
	id string
	// read returns the tags currently held at the scope.
	read func() map[string]string
	// write replaces them; an empty map clears the scope's tags.
	write func(map[string]string)
}

// tagScopeError is the ARM error a scope that holds no tags answers with.
type tagScopeError struct {
	code    string
	message string
	status  int
}

// resolveTagScope maps an ARM scope onto the single holder of its tags.
//
// A subscription and a management group have no resource row of their own, so
// their tags live in tagsStore. A resource group's tags are the ones its own
// record carries. Every other scope names a provider resource, resolved
// through the cross-slice registry (resource_registry.go) to the slice that
// stores it, so a tag written here is the tag the resource's own GET reports.
//
// A scope that names a resource type the registry does not track cannot be
// told apart from one that names a type Azure has never heard of, so both get
// the 404 real ARM answers for a scope that resolves to no resource.
func resolveTagScope(scope string) (tagScope, *tagScopeError) {
	lower := strings.ToLower(strings.TrimSuffix(scope, "/"))

	typeKey, isResource := azureResourceTypeKeyOfID(scope)
	// A management group is a resource of type Microsoft.Management/
	// managementGroups, but the simulator keeps no record for one, so its tags
	// have nowhere to live except the tags store.
	isManagementGroup := typeKey == "microsoft.management/managementgroups" &&
		strings.HasPrefix(lower, "/providers/")
	switch {
	case !isResource && strings.HasPrefix(lower, "/subscriptions/") && strings.Contains(lower, "/resourcegroups/"):
		return resolveResourceGroupTagScope(scope)

	case !isResource || isManagementGroup:
		// A subscription or a management group: no record of its own, so
		// tagsStore is the only home its tags have.
		id := scope + "/providers/Microsoft.Resources/tags/default"
		return tagScope{
			id: id,
			read: func() map[string]string {
				stored, ok := tagsStore.Get(lower)
				if !ok || stored.Properties.Tags == nil {
					return map[string]string{}
				}
				return stored.Properties.Tags
			},
			write: func(tags map[string]string) {
				if len(tags) == 0 {
					tagsStore.Delete(lower)
					return
				}
				tagsStore.Put(lower, TagsResource{
					ID:         id,
					Name:       "default",
					Type:       "Microsoft.Resources/tags",
					Properties: TagsBody{Tags: tags},
				})
			},
		}, nil
	}

	tracked, ok := azureTrackedResources[typeKey]
	if !ok {
		return tagScope{}, &tagScopeError{
			code:    "ResourceNotFound",
			message: fmt.Sprintf("The resource '%s' was not found.", scope),
			status:  http.StatusNotFound,
		}
	}
	canonical, _, exists := tracked.lookupTags(scope)
	if !exists {
		return tagScope{}, &tagScopeError{
			code:    "ResourceNotFound",
			message: fmt.Sprintf("The resource '%s' was not found.", scope),
			status:  http.StatusNotFound,
		}
	}
	return tagScope{
		id: canonical + "/providers/Microsoft.Resources/tags/default",
		read: func() map[string]string {
			_, tags, _ := tracked.lookupTags(scope)
			if tags == nil {
				return map[string]string{}
			}
			return tags
		},
		write: func(tags map[string]string) { tracked.writeTags(scope, tags) },
	}, nil
}

// resolveResourceGroupTagScope resolves a resource-group scope onto the group's
// own record, so `az tag` and `az group show` report the same tags.
func resolveResourceGroupTagScope(scope string) (tagScope, *tagScopeError) {
	group, _, ok := findResourceGroupByID(scope)
	if !ok {
		return tagScope{}, &tagScopeError{
			code:    "ResourceGroupNotFound",
			message: fmt.Sprintf("Resource group '%s' could not be found.", azureResourceGroupOfID(scope)),
			status:  http.StatusNotFound,
		}
	}
	return tagScope{
		id: group.ID + "/providers/Microsoft.Resources/tags/default",
		read: func() map[string]string {
			current, _, found := findResourceGroupByID(scope)
			if !found || current.Tags == nil {
				return map[string]string{}
			}
			return current.Tags
		},
		write: func(tags map[string]string) {
			current, storedKey, found := findResourceGroupByID(scope)
			if !found {
				return
			}
			if len(tags) == 0 {
				current.Tags = nil
			} else {
				current.Tags = tags
			}
			azureResourceGroups.Put(storedKey, current)
		},
	}, nil
}

// findResourceGroupByID reads a resource group by its ARM ID, folding case the
// way ARM does, and reports the key the store holds it under.
func findResourceGroupByID(id string) (ResourceGroup, string, bool) {
	if azureResourceGroups == nil {
		return ResourceGroup{}, "", false
	}
	if group, ok := azureResourceGroups.Get(id); ok {
		return group, id, true
	}
	// The case-insensitive fallback answers from an index keyed by the store's
	// generation rather than by decoding every group per request — this runs
	// inside a WrapHandler middleware, so every request into the simulator
	// paid it.
	if group, ok := azureResourceGroupsByLowerID.Lookup(azureResourceGroups, strings.ToLower(id),
		func(g ResourceGroup) []string { return []string{strings.ToLower(g.ID)} }); ok {
		return group, group.ID, true
	}
	return ResourceGroup{}, "", false
}

// azureResourceGroupsByLowerID answers the middleware's case-insensitive
// resource-group lookup without a per-request scan.
var azureResourceGroupsByLowerID sim.GenerationIndex[ResourceGroup]

func handleTagsDefault(w http.ResponseWriter, r *http.Request, scope string) {
	holder, scopeErr := resolveTagScope(scope)
	if scopeErr != nil {
		sim.AzureError(w, scopeErr.code, scopeErr.message, scopeErr.status)
		return
	}
	answer := func(tags map[string]string) {
		if tags == nil {
			tags = map[string]string{}
		}
		sim.WriteJSON(w, http.StatusOK, TagsResource{
			ID:         holder.id,
			Name:       "default",
			Type:       "Microsoft.Resources/tags",
			Properties: TagsBody{Tags: tags},
		})
	}

	switch r.Method {
	case http.MethodGet:
		answer(holder.read())
	case http.MethodPut:
		var req TagsResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		tags := map[string]string{}
		for k, v := range req.Properties.Tags {
			tags[k] = v
		}
		holder.write(tags)
		answer(tags)
	case http.MethodPatch:
		var req TagsPatchRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		tags := map[string]string{}
		for k, v := range holder.read() {
			tags[k] = v
		}
		switch strings.ToLower(req.Operation) {
		case "merge":
			for k, v := range req.Properties.Tags {
				tags[k] = v
			}
		case "replace":
			tags = map[string]string{}
			for k, v := range req.Properties.Tags {
				tags[k] = v
			}
		case "delete":
			for k := range req.Properties.Tags {
				delete(tags, k)
			}
		default:
			sim.AzureError(w, "InvalidRequestContent",
				"tags PATCH `operation` must be one of Merge, Replace, Delete; got "+req.Operation,
				http.StatusBadRequest)
			return
		}
		holder.write(tags)
		answer(tags)
	case http.MethodDelete:
		holder.write(nil)
		answer(nil)
	default:
		sim.AzureError(w, "MethodNotAllowed", "method "+r.Method+" not supported on tags/default", http.StatusMethodNotAllowed)
	}
}
