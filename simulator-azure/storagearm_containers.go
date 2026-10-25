package main

import (
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerBlobContainersARM(srv *sim.Server, armBase string, storageAccounts sim.Store[StorageAccount]) {
	// Sockerless runner artifact / cache flows store blobs in
	// `Microsoft.Storage/storageAccounts/{a}/blobServices/default/containers/{c}`.
	// The ARM control plane creates the container entity; the data plane
	// (subdomain-routed below) handles blob upload/download. Without this
	// surface, terraform's `azurerm_storage_container` and the SDK's
	// `armstorage.NewBlobContainersClient` 404.
	blobContainers := sim.MakeStore[BlobContainer](srv.DB(), "blob_containers")
	azBlobContainers = blobContainers
	containerBasePath := armBase + "/storageAccounts/{accountName}/blobServices/default/containers"

	srv.HandleFunc("PUT "+containerBasePath+"/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		name := sim.PathParam(r, "containerName")

		var req BlobContainer
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", acct, "blobServices", "default", "containers", name)

		container := BlobContainer{
			ID:   resourceID,
			Name: name,
			Type: "Microsoft.Storage/storageAccounts/blobServices/containers",
			Properties: BlobContainerProps{
				PublicAccess:     req.Properties.PublicAccess,
				LeaseStatus:      "Unlocked",
				LeaseState:       "Available",
				HasImmutability:  false,
				HasLegalHold:     false,
				Metadata:         req.Properties.Metadata,
				LastModifiedTime: time.Now().UTC().Format(time.RFC3339),
			},
		}
		if container.Properties.PublicAccess == "" {
			container.Properties.PublicAccess = "None"
		}
		blobContainers.Put(resourceID, container)
		// A blob container is ONE object with two management surfaces: the
		// container the Azure Resource Manager plane creates here is the same
		// container the Blob data plane serves, so it comes into being on both.
		// Without this a container created through Microsoft.Storage — which is
		// how terraform-provider-azurerm creates every container — is invisible
		// to every client that addresses it at the account's blob endpoint.
		mirrorARMContainerToBlobPlane(acct, name, container.Properties.PublicAccess, container.Properties.Metadata)
		sim.WriteJSON(w, http.StatusOK, container)
	})

	srv.HandleFunc("GET "+containerBasePath+"/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		name := sim.PathParam(r, "containerName")
		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", acct, "blobServices", "default", "containers", name)
		container, ok := blobContainers.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"Blob container %q not found in storage account %q", name, acct)
			return
		}
		sim.WriteJSON(w, http.StatusOK, container)
	})

	srv.HandleFunc("DELETE "+containerBasePath+"/{containerName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		name := sim.PathParam(r, "containerName")
		resourceID := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", acct, "blobServices", "default", "containers", name)
		blobContainers.Delete(resourceID)
		// The same one object goes on both surfaces.
		removeARMContainerFromBlobPlane(acct, name)
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+containerBasePath, func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		acct := sim.PathParam(r, "accountName")
		prefix := armResourceID(sub, rg, "Microsoft.Storage", "storageAccounts", acct, "blobServices", "default", "containers") + "/"
		all := blobContainers.Filter(func(c BlobContainer) bool {
			return strings.HasPrefix(c.ID, prefix)
		})
		if all == nil {
			all = []BlobContainer{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": all})
	})
}

// BlobContainer is a `Microsoft.Storage/storageAccounts/blobServices/
// containers/{name}` resource — the ARM control-plane entity that
// terraform's `azurerm_storage_container` and the SDK's
// `armstorage.NewBlobContainersClient` create. Sockerless runner
// artifact / cache flows store blobs in containers; without ARM
// container CRUD, every cache step 404s.
type BlobContainer struct {
	ID         string             `json:"id"`
	Name       string             `json:"name"`
	Type       string             `json:"type"`
	Etag       string             `json:"etag,omitempty"`
	Properties BlobContainerProps `json:"properties"`
}

// BlobContainerProps mirrors the ARM `BlobContainerProperties` shape
// the SDK round-trips on Get/List.
type BlobContainerProps struct {
	PublicAccess     string            `json:"publicAccess,omitempty"`
	LeaseStatus      string            `json:"leaseStatus,omitempty"`
	LeaseState       string            `json:"leaseState,omitempty"`
	HasImmutability  bool              `json:"hasImmutabilityPolicy,omitempty"`
	HasLegalHold     bool              `json:"hasLegalHold,omitempty"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	LastModifiedTime string            `json:"lastModifiedTime,omitempty"`
}
