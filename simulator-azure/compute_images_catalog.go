package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
	"github.com/e6qu/sockerless-cloud/sim/workload"
)

// A marketplace image names the OCI image a machine created from it boots,
// pinned by digest per architecture. The location's publishers listing and
// imageReference resolution read the same table, so what lists is what boots.

type azureMarketplaceImage struct {
	Publisher, Offer, SKU, Version string
	Image                          vminstance.Image
}

var azureMarketplaceImages = []azureMarketplaceImage{
	{Publisher: "Canonical", Offer: "ubuntu-24_04-lts", SKU: "server", Version: "24.04.202510170",
		Image: vminstance.Image{OCI: "mirror.gcr.io/library/ubuntu:24.04@sha256:24737cef17f4e22b1977df7d9a05e77c4e28b7da5b8911f8d8ccda136bf3573c", Architecture: "linux/amd64"}},
	{Publisher: "Canonical", Offer: "ubuntu-24_04-lts", SKU: "server-arm64", Version: "24.04.202510170",
		Image: vminstance.Image{OCI: "mirror.gcr.io/library/ubuntu:24.04@sha256:24737cef17f4e22b1977df7d9a05e77c4e28b7da5b8911f8d8ccda136bf3573c", Architecture: "linux/arm64"}},
	{Publisher: "Debian", Offer: "debian-12", SKU: "12", Version: "0.20251017.2318",
		Image: vminstance.Image{OCI: "mirror.gcr.io/library/debian:12@sha256:d74df7598108a173a621eda4d58a2adbf850b2d9a9d81bc2cca1a6c21174a880", Architecture: "linux/amd64"}},
	{Publisher: "Debian", Offer: "debian-12", SKU: "12-arm64", Version: "0.20251017.2318",
		Image: vminstance.Image{OCI: "mirror.gcr.io/library/debian:12@sha256:d74df7598108a173a621eda4d58a2adbf850b2d9a9d81bc2cca1a6c21174a880", Architecture: "linux/arm64"}},
}

// "latest" takes the newest version of the SKU.
func azureMarketplaceImageFor(publisher, offer, sku, version string) (azureMarketplaceImage, bool) {
	var found azureMarketplaceImage
	ok := false
	for _, img := range azureMarketplaceImages {
		if !strings.EqualFold(img.Publisher, publisher) || !strings.EqualFold(img.Offer, offer) || !strings.EqualFold(img.SKU, sku) {
			continue
		}
		if version == "" || strings.EqualFold(version, "latest") {
			if !ok || img.Version > found.Version {
				found, ok = img, true
			}
			continue
		}
		if strings.EqualFold(img.Version, version) {
			return img, true
		}
	}
	return found, ok
}

func azureImageArchitecture(image vminstance.Image) string {
	if strings.HasSuffix(image.Architecture, "/arm64") {
		return "Arm64"
	}
	return "x64"
}

func azureVMImageResourceID(sub, location string, parts ...string) string {
	id := fmt.Sprintf("/Subscriptions/%s/Providers/Microsoft.Compute/Locations/%s", sub, location)
	for i := 0; i+1 < len(parts); i += 2 {
		id += "/" + parts[i] + "/" + parts[i+1]
	}
	return id
}

func azureVMImageNotFound(w http.ResponseWriter) {
	AzureError(w, "NotFound", "Artifact: VMImage was not found.", http.StatusNotFound)
}

func registerVirtualMachineImages(srv *sim.Server) {
	const base = "/subscriptions/{subscriptionId}/providers/Microsoft.Compute/locations/{location}/publishers"
	resource := func(sub, location, name string, parts ...string) map[string]any {
		return map[string]any{"name": name, "location": location, "id": azureVMImageResourceID(sub, location, parts...)}
	}
	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		sub, location := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location")
		var out []map[string]any
		seen := map[string]bool{}
		for _, img := range azureMarketplaceImages {
			if seen[img.Publisher] {
				continue
			}
			seen[img.Publisher] = true
			out = append(out, resource(sub, location, img.Publisher, "Publishers", img.Publisher))
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})
	srv.HandleFunc("GET "+base+"/{publisherName}/artifacttypes/vmimage/offers", func(w http.ResponseWriter, r *http.Request) {
		sub, location := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location")
		publisher := sim.PathParam(r, "publisherName")
		out := []map[string]any{}
		seen := map[string]bool{}
		for _, img := range azureMarketplaceImages {
			if !strings.EqualFold(img.Publisher, publisher) || seen[img.Offer] {
				continue
			}
			seen[img.Offer] = true
			out = append(out, resource(sub, location, img.Offer, "Publishers", img.Publisher, "ArtifactTypes", "VMImage", "Offers", img.Offer))
		}
		if len(out) == 0 {
			azureVMImageNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})
	srv.HandleFunc("GET "+base+"/{publisherName}/artifacttypes/vmimage/offers/{offer}/skus", func(w http.ResponseWriter, r *http.Request) {
		sub, location := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location")
		publisher, offer := sim.PathParam(r, "publisherName"), sim.PathParam(r, "offer")
		out := []map[string]any{}
		seen := map[string]bool{}
		for _, img := range azureMarketplaceImages {
			if !strings.EqualFold(img.Publisher, publisher) || !strings.EqualFold(img.Offer, offer) || seen[img.SKU] {
				continue
			}
			seen[img.SKU] = true
			out = append(out, resource(sub, location, img.SKU, "Publishers", img.Publisher, "ArtifactTypes", "VMImage", "Offers", img.Offer, "Skus", img.SKU))
		}
		if len(out) == 0 {
			azureVMImageNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})
	srv.HandleFunc("GET "+base+"/{publisherName}/artifacttypes/vmimage/offers/{offer}/skus/{skus}/versions", func(w http.ResponseWriter, r *http.Request) {
		sub, location := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location")
		publisher, offer, sku := sim.PathParam(r, "publisherName"), sim.PathParam(r, "offer"), sim.PathParam(r, "skus")
		out := []map[string]any{}
		for _, img := range azureMarketplaceImages {
			if !strings.EqualFold(img.Publisher, publisher) || !strings.EqualFold(img.Offer, offer) || !strings.EqualFold(img.SKU, sku) {
				continue
			}
			out = append(out, resource(sub, location, img.Version, "Publishers", img.Publisher, "ArtifactTypes", "VMImage", "Offers", img.Offer, "Skus", img.SKU, "Versions", img.Version))
		}
		if len(out) == 0 {
			azureVMImageNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})
	srv.HandleFunc("GET "+base+"/{publisherName}/artifacttypes/vmimage/offers/{offer}/skus/{skus}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		sub, location := sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "location")
		img, ok := azureMarketplaceImageFor(sim.PathParam(r, "publisherName"), sim.PathParam(r, "offer"), sim.PathParam(r, "skus"), sim.PathParam(r, "version"))
		if !ok || strings.EqualFold(sim.PathParam(r, "version"), "latest") {
			azureVMImageNotFound(w)
			return
		}
		out := resource(sub, location, img.Version, "Publishers", img.Publisher, "ArtifactTypes", "VMImage", "Offers", img.Offer, "Skus", img.SKU, "Versions", img.Version)
		out["properties"] = map[string]any{
			"osDiskImage":                  map[string]any{"operatingSystem": "Linux"},
			"dataDiskImages":               []any{},
			"hyperVGeneration":             "V2",
			"architecture":                 azureImageArchitecture(img.Image),
			"features":                     []any{},
			"imageDeprecationStatus":       map[string]any{"imageState": "Active"},
			"automaticOSUpgradeProperties": map[string]any{"automaticOSUpgradeSupported": false},
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})
}

// A customer image (Microsoft.Compute/images) is an OCI image too: committed
// by a capture and named by the blob it wrote, named by
// storageProfile.osDisk.blobUri as an OCI reference, or a sourceVirtualMachine's
// filesystem committed at create.

type ComputeImage struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Location   string                 `json:"location"`
	Tags       map[string]string      `json:"tags,omitempty"`
	Properties ComputeImageProperties `json:"properties"`
}

type ComputeImageProperties struct {
	SourceVirtualMachine *SubResource   `json:"sourceVirtualMachine,omitempty"`
	StorageProfile       map[string]any `json:"storageProfile,omitempty"`
	HyperVGeneration     string         `json:"hyperVGeneration,omitempty"`
	ProvisioningState    string         `json:"provisioningState,omitempty"`
}

var (
	azureComputeImages sim.Store[ComputeImage]
	// Keyed by lowercased image resource ID or captured blob URI.
	azureImageBackings sim.Store[vminstance.Image]
)

func azureImageBackingKey(key string) string { return strings.ToLower(key) }

func azureRecordCapturedImage(blobURI string, image vminstance.Image) {
	azureImageBackings.Put(azureImageBackingKey(blobURI), image)
}

// azureResolveImageBlobURI resolves a blob a capture wrote, or an OCI
// reference the engine pulls.
func azureResolveImageBlobURI(ctx context.Context, uri string) (vminstance.Image, error) {
	if image, ok := azureImageBackings.Get(azureImageBackingKey(uri)); ok {
		return image, nil
	}
	if strings.Contains(uri, "://") {
		return vminstance.Image{}, fmt.Errorf("blob %s holds no captured disk", uri)
	}
	platform, err := workload.LocalImagePlatform(ctx, sim.ResolveLocalImage(uri), "")
	if err != nil {
		return vminstance.Image{}, fmt.Errorf("OCI image %s: %w", uri, err)
	}
	return vminstance.Image{OCI: sim.ResolveLocalImage(uri), Architecture: platform}, nil
}

func azureComputeImageBlobURI(props ComputeImageProperties) string {
	osDisk, _ := props.StorageProfile["osDisk"].(map[string]any)
	uri, _ := osDisk["blobUri"].(string)
	return uri
}

func registerComputeImages(srv *sim.Server) {
	azureComputeImages = sim.MakeStore[ComputeImage](srv.DB(), "compute_images")
	azureImageBackings = sim.MakeStore[vminstance.Image](srv.DB(), "compute_image_backings")
	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"
	imageID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "images", sim.PathParam(r, "imageName"))
	}

	srv.HandleFunc("PUT "+armBase+"/images/{imageName}", func(w http.ResponseWriter, r *http.Request) {
		var req ComputeImage
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if req.Location == "" {
			AzureError(w, "LocationRequired", "The location property is required for this definition.", http.StatusBadRequest)
			return
		}
		id := imageID(r)
		image := ComputeImage{
			ID: id, Name: sim.PathParam(r, "imageName"), Type: "Microsoft.Compute/images",
			Location: req.Location, Tags: req.Tags, Properties: req.Properties,
		}
		var backing vminstance.Image
		switch {
		case image.Properties.SourceVirtualMachine != nil && image.Properties.SourceVirtualMachine.ID != "":
			vm, ok := azureVMs.Get(image.Properties.SourceVirtualMachine.ID)
			if !ok {
				AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q referenced by properties.sourceVirtualMachine was not found.", image.Properties.SourceVirtualMachine.ID)
				return
			}
			if generalized, _ := azureVMGeneralized.Get(vm.ID); !generalized {
				AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "Creating an image from VM %q is not allowed because it has not been generalized.", vm.ID)
				return
			}
			committed, err := azureCommitVMImage(r.Context(), vm, "image")
			if err != nil {
				AzureErrorf(w, "OperationNotAllowed", http.StatusConflict, "%v", err)
				return
			}
			backing = committed
			if image.Properties.StorageProfile == nil {
				image.Properties.StorageProfile = map[string]any{
					"osDisk":    map[string]any{"osType": "Linux", "osState": "Generalized", "caching": "ReadWrite", "storageAccountType": "Standard_LRS"},
					"dataDisks": []any{},
				}
			}
		case azureComputeImageBlobURI(image.Properties) != "":
			resolved, err := azureResolveImageBlobURI(r.Context(), azureComputeImageBlobURI(image.Properties))
			if err != nil {
				AzureErrorf(w, "InvalidParameter", http.StatusBadRequest, "The value of parameter properties.storageProfile.osDisk.blobUri is invalid: %v", err)
				return
			}
			backing = resolved
		default:
			AzureError(w, "InvalidParameter", "Required parameter 'properties.storageProfile.osDisk.blobUri' or 'properties.sourceVirtualMachine' is missing (null).", http.StatusBadRequest)
			return
		}
		if image.Properties.HyperVGeneration == "" {
			image.Properties.HyperVGeneration = "V2"
		}
		image.Properties.ProvisioningState = "Succeeded"
		created := true
		if _, ok := azureComputeImages.Get(id); ok {
			created = false
		}
		azureImageBackings.Put(azureImageBackingKey(id), backing)
		azureComputeImages.Put(id, image)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		sim.WriteJSON(w, status, image)
	})

	srv.HandleFunc("PATCH "+armBase+"/images/{imageName}", func(w http.ResponseWriter, r *http.Request) {
		var patch struct {
			Tags map[string]string `json:"tags"`
		}
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		id := imageID(r)
		if !azureComputeImages.Update(id, func(image *ComputeImage) {
			if patch.Tags != nil {
				image.Tags = patch.Tags
			}
		}) {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		image, _ := azureComputeImages.Get(id)
		sim.WriteJSON(w, http.StatusOK, image)
	})

	srv.HandleFunc("GET "+armBase+"/images/{imageName}", func(w http.ResponseWriter, r *http.Request) {
		id := imageID(r)
		image, ok := azureComputeImages.Get(id)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource %q was not found.", id)
			return
		}
		sim.WriteJSON(w, http.StatusOK, image)
	})

	srv.HandleFunc("DELETE "+armBase+"/images/{imageName}", func(w http.ResponseWriter, r *http.Request) {
		id := imageID(r)
		if !azureComputeImages.Delete(id) {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		azureImageBackings.Delete(azureImageBackingKey(id))
		w.WriteHeader(http.StatusOK)
	})

	srv.HandleFunc("GET "+armBase+"/images", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "images") + "/"
		items := azureComputeImages.Filter(func(i ComputeImage) bool { return strings.HasPrefix(strings.ToLower(i.ID), strings.ToLower(prefix)) })
		if items == nil {
			items = []ComputeImage{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})

	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/images", func(w http.ResponseWriter, r *http.Request) {
		prefix := armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/"
		items := azureComputeImages.Filter(func(i ComputeImage) bool { return strings.HasPrefix(strings.ToLower(i.ID), strings.ToLower(prefix)) })
		if items == nil {
			items = []ComputeImage{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": items})
	})
}

func azureVMImageReference(vm VirtualMachine) map[string]any {
	ref, _ := vm.Properties.StorageProfile["imageReference"].(map[string]any)
	return ref
}

func azureVMImageNotFoundFault(publisher, offer, sku, version string) *azureVMRequestFault {
	return &azureVMRequestFault{
		code:   "PlatformImageNotFound",
		status: http.StatusNotFound,
		message: fmt.Sprintf("The platform image '%s:%s:%s:%s' is not available. Verify that all fields in the storage profile are correct. For more details about storage profile information, please refer to https://aka.ms/storageprofile",
			publisher, offer, sku, version),
	}
}

// azureResolveVMImage resolves the marketplace image, the customer image by
// id, or the captured disk osDisk.image.uri names.
func azureResolveVMImage(ctx context.Context, vm VirtualMachine) (vminstance.Image, error) {
	osDisk, _ := vm.Properties.StorageProfile["osDisk"].(map[string]any)
	if diskImage, _ := osDisk["image"].(map[string]any); diskImage != nil {
		if uri, _ := diskImage["uri"].(string); uri != "" {
			return azureResolveImageBlobURI(ctx, uri)
		}
	}
	ref := azureVMImageReference(vm)
	if id, _ := ref["id"].(string); id != "" {
		if image, ok := azureImageBackings.Get(azureImageBackingKey(id)); ok {
			return image, nil
		}
		return vminstance.Image{}, &azureVMRequestFault{
			code:    "ResourceNotFound",
			status:  http.StatusNotFound,
			message: fmt.Sprintf("The Resource %q referenced by properties.storageProfile.imageReference was not found.", id),
		}
	}
	publisher, _ := ref["publisher"].(string)
	offer, _ := ref["offer"].(string)
	sku, _ := ref["sku"].(string)
	version, _ := ref["version"].(string)
	if publisher == "" && offer == "" && sku == "" {
		return vminstance.Image{}, &azureVMRequestFault{
			code:    "InvalidParameter",
			status:  http.StatusBadRequest,
			message: "Required parameter 'imageReference' is missing (null).",
		}
	}
	img, ok := azureMarketplaceImageFor(publisher, offer, sku, version)
	if !ok {
		return vminstance.Image{}, azureVMImageNotFoundFault(publisher, offer, sku, version)
	}
	return img.Image, nil
}
