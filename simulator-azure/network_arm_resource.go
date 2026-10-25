package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
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
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", s.collection, sim.PathParam(r, s.nameParam))
}

// resources is the spec's store as the shared ARM operations read it, with
// the not-found error naming the request's resource and resource group.
func (s azureNetworkResourceSpec[T]) resources(r *http.Request) armResources[T] {
	return armResources[T]{
		store:   s.store,
		id:      func(res *T) string { return s.header(res).ID },
		project: s.project,
		notFound: func(w http.ResponseWriter, _ string) {
			azureNetworkResourceNotFound(w, s.resourceType, sim.PathParam(r, s.nameParam), sim.PathParam(r, "resourceGroupName"))
		},
	}
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
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := s.resourceID(r)
		name := sim.PathParam(r, s.nameParam)
		head := s.header(&req)
		if head.Location == "" {
			AzureErrorf(w, "LocationRequired", http.StatusBadRequest,
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
				azureWriteProvisioningError(w, s.resourceType, name, err)
				return
			}
		}
		s.store.Put(id, req)
		// The Microsoft.Network resource provider answers a create or update
		// with the provisioned resource once it reaches a terminal state; a
		// terminal 200 carrying the resource completes the SDK's long-running
		// operation on the first response.
		sim.WriteJSON(w, http.StatusOK, s.resources(r).projected(req))
	})

	srv.HandleFunc("GET "+armBase+"/"+s.collection+"/{"+s.nameParam+"}", func(w http.ResponseWriter, r *http.Request) {
		armGet(w, s.resources(r), s.resourceID(r))
	})

	srv.HandleFunc("DELETE "+armBase+"/"+s.collection+"/{"+s.nameParam+"}", func(w http.ResponseWriter, r *http.Request) {
		status := s.deleteStatus
		if status == 0 {
			status = http.StatusOK
		}
		// ARM reports a delete of an absent resource as a no-content success,
		// so a repeated delete is idempotent.
		armDelete(w, r, s.resources(r), s.resourceID(r), armDeletion[T]{
			status: status, absentStatus: http.StatusNoContent, after: s.afterDelete,
		})
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
			sim.WriteJSON(w, http.StatusOK, s.resources(r).projected(res))
		})
	}

	srv.HandleFunc("GET "+armBase+"/"+s.collection, func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", s.collection) + "/"
		armList(w, s.resources(r), prefix)
	})

	srv.HandleFunc("GET "+subBase+"/"+s.subCollection, func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		armList(w, s.resources(r), prefix)
	})
}

// azureNetworkResourceNotFound writes the ARM error the Microsoft.Network
// resource provider returns for a read of a resource that does not exist.
func azureNetworkResourceNotFound(w http.ResponseWriter, resourceType, name, rg string) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource '%s/%s' under resource group '%s' was not found.", resourceType, name, rg)
}

// azureNetworkEtag is the weak entity tag Microsoft.Network stamps on every
// resource and child resource; clients treat it as opaque and only compare it
// for change detection.
func azureNetworkEtag() string {
	return `W/"` + sim.NewUUID() + `"`
}

// azureWriteProvisioningError answers a create whose provisioning failed: with
// the request's own error when the resource's addresses could not be leased
// as asked, and as the resource provider's failure otherwise.
func azureWriteProvisioningError(w http.ResponseWriter, resourceType, name string, err error) {
	var fault *azureAddressFault
	switch {
	case errors.As(err, &fault):
		AzureError(w, fault.code, fault.message, fault.status)
	case errors.Is(err, ipam.ErrAddressInUse):
		AzureErrorf(w, "PrivateIPAddressInUse", http.StatusBadRequest,
			"%s %q requests a private IP address that is already allocated to another resource: %v", resourceType, name, err)
	case errors.Is(err, ipam.ErrNoAvailableIP):
		AzureErrorf(w, "SubnetIsFull", http.StatusBadRequest,
			"The subnet of %s %q does not have enough capacity for another IP address: %v", resourceType, name, err)
	default:
		AzureErrorf(w, "InternalServerError", http.StatusInternalServerError,
			"failed to provision %s %q: %v", resourceType, name, err)
	}
}
