package main

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Microsoft.Resources/tags/default manages tags on any scope without a full
// PUT of the resource.
type TagsResource struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Type       string   `json:"type"`
	Properties TagsBody `json:"properties"`
}

type TagsBody struct {
	Tags map[string]string `json:"tags"`
}

type TagsPatchRequest struct {
	Operation  string   `json:"operation"`
	Properties TagsBody `json:"properties"`
}

// Only subscriptions and management groups, which have no record of their own,
// keep tags here; a resource group's tags live on its record and a resource's
// on the resource, through the registry. Azure Resource Manager keeps one set
// per scope, so tags/default and the scope's API agree. Keyed by lowercased
// scope.
var tagsStore sim.Store[TagsResource]

const tagsDefaultMarker = "/providers/microsoft.resources/tags/default"

func registerTags(srv *sim.Server) {
	tagsStore = sim.MakeStore[TagsResource](srv.DB(), "azure_tags_default")

	// Subscription and resource-group scopes are fixed-depth, so they mount as mux
	// routes.
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

	// Resource and management-group scopes end a variable-depth path, which Go's
	// ServeMux cannot match with a fixed suffix, so a wrapper dispatches them, as
	// authorization.go does role definitions.
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Match the marker case-insensitively against the original bytes: slicing by
			// an offset from a ToLower copy is unsafe, as case-folding can change byte
			// length.
			if len(r.URL.Path) < len(tagsDefaultMarker) ||
				!strings.EqualFold(r.URL.Path[len(r.URL.Path)-len(tagsDefaultMarker):], tagsDefaultMarker) {
				next.ServeHTTP(w, r)
				return
			}
			scope := strings.TrimSuffix(r.URL.Path[:len(r.URL.Path)-len(tagsDefaultMarker)], "/")
			if strings.HasPrefix(strings.ToLower(scope), "/subscriptions/") && !strings.Contains(strings.ToLower(scope), "/providers/") {
				// Subscription and resource-group scopes: the mux routes serve them.
				next.ServeHTTP(w, r)
				return
			}
			handleTagsDefault(w, r, scope)
		})
	})
}

func handleTagsDefaultRoute(w http.ResponseWriter, r *http.Request) {
	scope := armSubscriptionID(sim.PathParam(r, "subscriptionId"))
	if rg := sim.PathParam(r, "resourceGroupName"); rg != "" {
		scope += "/resourceGroups/" + rg
	}
	handleTagsDefault(w, r, scope)
}

// One holder per scope makes tags/default and the scope's own API two
// spellings of one state.
type tagScope struct {
	// The canonical tags/default ID, from the scope's stored spelling.
	id   string
	read func() map[string]string
	// An empty map clears the scope's tags.
	write func(map[string]string)
}

type tagScopeError struct {
	code    string
	message string
	status  int
}

// Subscriptions and management groups keep tags in tagsStore, resource groups
// on their record, and other scopes on the resource through the registry, so a
// tag written here is the one the resource's GET reports. A type the registry
// does not track is indistinguishable from an unknown one, so both answer the
// 404 Azure gives a scope resolving to no resource.
func resolveTagScope(scope string) (tagScope, *tagScopeError) {
	lower := strings.ToLower(strings.TrimSuffix(scope, "/"))

	typeKey, isResource := azureResourceTypeKeyOfID(scope)
	// The simulator keeps no record for a management group, so its tags live in
	// tagsStore.
	isManagementGroup := typeKey == "microsoft.management/managementgroups" &&
		strings.HasPrefix(lower, "/providers/")
	switch {
	case !isResource && strings.HasPrefix(lower, "/subscriptions/") && strings.Contains(lower, "/resourcegroups/"):
		return resolveResourceGroupTagScope(scope)

	case !isResource || isManagementGroup:
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

// So `az tag` and `az group show` report the same tags.
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

// Folds case as Azure Resource Manager does, and reports the store key.
func findResourceGroupByID(id string) (ResourceGroup, string, bool) {
	if azureResourceGroups == nil {
		return ResourceGroup{}, "", false
	}
	if group, ok := azureResourceGroups.Get(id); ok {
		return group, id, true
	}
	// This runs in a WrapHandler ahead of every request, so the case-insensitive
	// fallback reads an index.
	if group, ok := azureResourceGroupsByLowerID.Lookup(azureResourceGroups, strings.ToLower(id),
		func(g ResourceGroup) []string { return []string{strings.ToLower(g.ID)} }); ok {
		return group, group.ID, true
	}
	return ResourceGroup{}, "", false
}

var azureResourceGroupsByLowerID sim.GenerationIndex[ResourceGroup]

func handleTagsDefault(w http.ResponseWriter, r *http.Request, scope string) {
	holder, scopeErr := resolveTagScope(scope)
	if scopeErr != nil {
		AzureError(w, scopeErr.code, scopeErr.message, scopeErr.status)
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
			AzureError(w, "InvalidRequestContent", "failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			AzureError(w, "InvalidRequestContent", "failed to parse request body: "+err.Error(), http.StatusBadRequest)
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
			AzureError(w, "InvalidRequestContent",
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
		AzureError(w, "MethodNotAllowed", "method "+r.Method+" not supported on tags/default", http.StatusMethodNotAllowed)
	}
}
