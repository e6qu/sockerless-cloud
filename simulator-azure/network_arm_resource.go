package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The Microsoft.Network resource provider gives every top-level tracked
// resource the same six operations — CreateOrUpdate, Get, Delete, UpdateTags,
// list-by-resource-group and list-by-subscription — with the same ARM envelope
// (id/name/type/location/tags/etag) wrapping a resource-specific properties
// bag. azureNetworkResourceSpec captures that shared surface once so each
// resource file contributes only what actually differs: its properties, the
// defaults and cross-resource references the service computes, and whatever
// association work its create and delete perform.

// azureNetworkResourceHeader is the ARM Resource envelope. It is embedded in
// each Microsoft.Network resource type, so its fields marshal inline exactly as
// the swagger declares them.
type azureNetworkResourceHeader struct {
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name,omitempty"`
	Type     string            `json:"type,omitempty"`
	Location string            `json:"location,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Etag     string            `json:"etag,omitempty"`
}

// azureNetworkResourceSpec describes one top-level Microsoft.Network resource.
type azureNetworkResourceSpec[T any] struct {
	// collection is the resource-group-scoped path segment, e.g.
	// "applicationSecurityGroups".
	collection string
	// subCollection is the subscription-scoped spelling of the same segment.
	// It is normally identical to collection; Microsoft.Network spells the
	// service-endpoint-policy list "ServiceEndpointPolicies", and the SDK sends
	// exactly that, so the two are kept separate.
	subCollection string
	// nameParam is the swagger's path-parameter name for the resource name.
	nameParam string
	// resourceType is the ARM type string, e.g.
	// "Microsoft.Network/applicationSecurityGroups".
	resourceType string
	store        sim.Store[T]
	// noUpdateTags marks a resource type Microsoft.Network gives no UpdateTags
	// operation — private endpoints and private link services are retagged
	// through a full create-or-update instead.
	noUpdateTags bool
	// deleteStatus is the status a successful delete answers with. Most of the
	// provider's resources document 200 for a completed delete; network
	// watchers document only 202 and 204, and their SDK client rejects
	// anything else. Zero means 200.
	deleteStatus int

	// header returns the embedded ARM envelope of a resource so the shared
	// handlers can stamp identity and read tags.
	header func(*T) *azureNetworkResourceHeader
	// provision applies the resource's own create/update semantics: default
	// values, generated identifiers, child-resource identity, provisioning
	// state, and whatever fabric the resource owns. It runs after the ARM
	// envelope has been stamped, and receives the previously stored record when
	// this write updates an existing resource. An error means the host could not
	// realize the resource, which ARM reports as a retryable service condition.
	provision func(ctx context.Context, res *T, previous *T) error
	// project renders a stored record for a response, refreshing the
	// cross-resource references the real service recomputes on every read
	// (attached subnets, live connections). Optional.
	project func(*T)
	// validate rejects a request body the resource provider will not accept,
	// writing the ARM error itself and returning false.
	validate func(w http.ResponseWriter, r *http.Request, res *T) bool
	// afterDelete runs once the row is gone, releasing whatever the resource
	// owned.
	afterDelete func(ctx context.Context, id string, deleted T)
}

func (s azureNetworkResourceSpec[T]) resourceID(r *http.Request) string {
	return fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/%s/%s",
		sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"),
		s.collection, sim.PathParam(r, s.nameParam))
}

func (s azureNetworkResourceSpec[T]) projected(res T) T {
	if s.project != nil {
		s.project(&res)
	}
	return res
}

// registerAzureNetworkResource mounts the six shared operations.
func registerAzureNetworkResource[T any](srv *sim.Server, s azureNetworkResourceSpec[T]) {
	if s.subCollection == "" {
		s.subCollection = s.collection
	}
	armBase := azureNetworkArmBase()
	subBase := azureNetworkSubBase()

	srv.HandleFunc("PUT "+armBase+"/"+s.collection+"/{"+s.nameParam+"}", func(w http.ResponseWriter, r *http.Request) {
		var req T
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := s.resourceID(r)
		name := sim.PathParam(r, s.nameParam)
		head := s.header(&req)
		if head.Location == "" {
			sim.AzureErrorf(w, "LocationRequired", http.StatusBadRequest,
				"The location property is required for this definition.")
			return
		}
		if s.validate != nil && !s.validate(w, r, &req) {
			return
		}
		head.ID = id
		head.Name = name
		head.Type = s.resourceType
		head.Etag = azureNetworkEtag()
		var previous *T
		if existing, ok := s.store.Get(id); ok {
			previous = &existing
		}
		if s.provision != nil {
			if err := s.provision(r.Context(), &req, previous); err != nil {
				sim.AzureErrorf(w, "OperationNotAllowed", http.StatusServiceUnavailable,
					"failed to provision %s %q: %v", s.resourceType, name, err)
				return
			}
		}
		s.store.Put(id, req)
		// The Microsoft.Network resource provider answers a create or update
		// with the provisioned resource once it reaches a terminal state; a
		// terminal 200 carrying the resource completes the SDK's long-running
		// operation on the first response.
		sim.WriteJSON(w, http.StatusOK, s.projected(req))
	})

	srv.HandleFunc("GET "+armBase+"/"+s.collection+"/{"+s.nameParam+"}", func(w http.ResponseWriter, r *http.Request) {
		res, ok := s.store.Get(s.resourceID(r))
		if !ok {
			azureNetworkResourceNotFound(w, s.resourceType, sim.PathParam(r, s.nameParam), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, s.projected(res))
	})

	srv.HandleFunc("DELETE "+armBase+"/"+s.collection+"/{"+s.nameParam+"}", func(w http.ResponseWriter, r *http.Request) {
		id := s.resourceID(r)
		res, ok := s.store.Get(id)
		if !ok {
			// ARM reports a delete of an absent resource as a no-content
			// success, so a repeated delete is idempotent.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		s.store.Delete(id)
		if s.afterDelete != nil {
			s.afterDelete(r.Context(), id, res)
		}
		status := s.deleteStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
	})

	if !s.noUpdateTags {
		srv.HandleFunc("PATCH "+armBase+"/"+s.collection+"/{"+s.nameParam+"}", func(w http.ResponseWriter, r *http.Request) {
			id := s.resourceID(r)
			if !azureApplyTagsPatch(w, r, func(set map[string]string) bool {
				return s.store.Update(id, func(res *T) { s.header(res).Tags = set })
			}) {
				return
			}
			res, _ := s.store.Get(id)
			sim.WriteJSON(w, http.StatusOK, s.projected(res))
		})
	}

	srv.HandleFunc("GET "+armBase+"/"+s.collection, func(w http.ResponseWriter, r *http.Request) {
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/%s/",
			sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), s.collection)
		azureWriteList(w, s.listWithPrefix(prefix))
	})

	srv.HandleFunc("GET "+subBase+"/"+s.subCollection, func(w http.ResponseWriter, r *http.Request) {
		prefix := "/subscriptions/" + sim.PathParam(r, "subscriptionId") + "/"
		azureWriteList(w, s.listWithPrefix(prefix))
	})
}

func (s azureNetworkResourceSpec[T]) listWithPrefix(prefix string) []T {
	items := s.store.Filter(func(res T) bool {
		return strings.HasPrefix(s.header(&res).ID, prefix)
	})
	for i := range items {
		if s.project != nil {
			s.project(&items[i])
		}
	}
	return items
}

// azureNetworkResourceNotFound writes the ARM error the Microsoft.Network
// resource provider returns for a read of a resource that does not exist.
func azureNetworkResourceNotFound(w http.ResponseWriter, resourceType, name, rg string) {
	sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource '%s/%s' under resource group '%s' was not found.", resourceType, name, rg)
}

// azureNetworkEtag is the weak entity tag Microsoft.Network stamps on every
// resource and child resource; clients treat it as opaque and only compare it
// for change detection.
func azureNetworkEtag() string {
	return `W/"` + generateUUID() + `"`
}

// azureNetworkChildID composes the ARM id of a child resource collection member.
func azureNetworkChildID(parentID, collection, name string) string {
	return parentID + "/" + collection + "/" + name
}
