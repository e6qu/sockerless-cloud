package main

import (
	"context"
	"errors"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
)

// Every top-level tracked Microsoft.Network resource has the same six
// operations — CreateOrUpdate, Get, Delete, UpdateTags, list by resource group
// and by subscription — with the same envelope around its properties.
// azureNetworkResourceSpec captures that surface once; each resource supplies
// only what differs.

// Embedded in each resource type, so its fields marshal inline as the swagger
// declares them.
type azureNetworkResourceHeader struct {
	ID       string            `json:"id,omitempty"`
	Name     string            `json:"name,omitempty"`
	Type     string            `json:"type,omitempty"`
	Location string            `json:"location,omitempty"`
	Tags     map[string]string `json:"tags,omitempty"`
	Etag     string            `json:"etag,omitempty"`
}

type azureNetworkResourceSpec[T any] struct {
	collection string
	// Usually identical to collection; Microsoft.Network spells the
	// service-endpoint-policy list "ServiceEndpointPolicies", and the SDK sends
	// exactly that.
	subCollection string
	nameParam     string
	resourceType  string
	store         sim.Store[T]
	// Private endpoints and private link services have no UpdateTags; they retag
	// through a full create-or-update.
	noUpdateTags bool
	// Most resources document 200 for a completed delete; network watchers
	// document only 202 and 204, and their client rejects anything else. Zero
	// means 200.
	deleteStatus int

	header func(*T) *azureNetworkResourceHeader
	// provision applies the resource's own create/update semantics after the
	// envelope is stamped; previous is the stored record on an update. An error
	// means the host could not realize the resource, a retryable service
	// condition.
	provision func(ctx context.Context, res *T, previous *T) error
	// project refreshes the cross-resource references the service recomputes on
	// every read. Optional.
	project func(*T)
	// validate writes the error itself and returns false.
	validate func(w http.ResponseWriter, r *http.Request, res *T) bool
	// afterDelete releases what the resource owned.
	afterDelete func(ctx context.Context, id string, deleted T)
}

func (s azureNetworkResourceSpec[T]) resourceID(r *http.Request) string {
	return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Network", s.collection, sim.PathParam(r, s.nameParam))
}

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
		// A terminal 200 carrying the resource completes the SDK's long-running
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
		// Azure Resource Manager answers a delete of an absent resource with no
		// content, so a repeated delete is idempotent.
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

func azureNetworkResourceNotFound(w http.ResponseWriter, resourceType, name, rg string) {
	AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
		"The Resource '%s/%s' under resource group '%s' was not found.", resourceType, name, rg)
}

// Clients treat it as opaque and compare it only for change detection.
func azureNetworkEtag() string {
	return `W/"` + sim.NewUUID() + `"`
}

// The request's own error when addresses could not be leased as asked, the
// resource provider's failure otherwise.
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
