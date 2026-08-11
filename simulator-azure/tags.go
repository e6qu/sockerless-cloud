package main

import (
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// TagsResource mirrors Microsoft.Resources/tags/default — the canonical
// surface for managing tags on any ARM scope (subscription, resource
// group, or individual resource) without having to PUT the full
// resource. Keyed by the lowercased scope resource ID.
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
			scope := strings.ToLower(r.URL.Path[:len(r.URL.Path)-len(tagsDefaultMarker)])
			scope = strings.TrimSuffix(scope, "/")
			if strings.HasPrefix(scope, "/subscriptions/") && !strings.Contains(scope, "/providers/") {
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
	handleTagsDefault(w, r, strings.ToLower(scope))
}

func handleTagsDefault(w http.ResponseWriter, r *http.Request, scope string) {
	id := scope + "/providers/Microsoft.Resources/tags/default"
	switch r.Method {
	case http.MethodGet:
		tags, ok := tagsStore.Get(scope)
		if !ok {
			tags = TagsResource{
				ID:         id,
				Name:       "default",
				Type:       "Microsoft.Resources/tags",
				Properties: TagsBody{Tags: map[string]string{}},
			}
		}
		sim.WriteJSON(w, http.StatusOK, tags)
	case http.MethodPut:
		var req TagsResource
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Properties.Tags == nil {
			req.Properties.Tags = map[string]string{}
		}
		stored := TagsResource{
			ID:         id,
			Name:       "default",
			Type:       "Microsoft.Resources/tags",
			Properties: req.Properties,
		}
		tagsStore.Put(scope, stored)
		sim.WriteJSON(w, http.StatusOK, stored)
	case http.MethodPatch:
		var req TagsPatchRequest
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		current, _ := tagsStore.Get(scope)
		if current.Properties.Tags == nil {
			current.Properties.Tags = map[string]string{}
		}
		switch strings.ToLower(req.Operation) {
		case "merge":
			for k, v := range req.Properties.Tags {
				current.Properties.Tags[k] = v
			}
		case "replace":
			tags := map[string]string{}
			for k, v := range req.Properties.Tags {
				tags[k] = v
			}
			current.Properties.Tags = tags
		case "delete":
			for k := range req.Properties.Tags {
				delete(current.Properties.Tags, k)
			}
		default:
			sim.AzureError(w, "InvalidRequestContent",
				"tags PATCH `operation` must be one of Merge, Replace, Delete; got "+req.Operation,
				http.StatusBadRequest)
			return
		}
		stored := TagsResource{
			ID:         id,
			Name:       "default",
			Type:       "Microsoft.Resources/tags",
			Properties: TagsBody{Tags: current.Properties.Tags},
		}
		tagsStore.Put(scope, stored)
		sim.WriteJSON(w, http.StatusOK, stored)
	case http.MethodDelete:
		tagsStore.Delete(scope)
		sim.WriteJSON(w, http.StatusOK, TagsResource{
			ID:         id,
			Name:       "default",
			Type:       "Microsoft.Resources/tags",
			Properties: TagsBody{Tags: map[string]string{}},
		})
	default:
		sim.AzureError(w, "MethodNotAllowed", "method "+r.Method+" not supported on tags/default", http.StatusMethodNotAllowed)
	}
}
