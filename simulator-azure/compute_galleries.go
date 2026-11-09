package main

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// Azure Compute Gallery, against GalleryRP's gallery.json (2023-07-03): a
// gallery holds image definitions, and a definition holds image versions. A
// version's OS disk is an OCI image, captured from a virtual machine or taken
// from the managed image or gallery image version it names, recorded in
// azureImageBackings under the version's id so imageReference.id resolves it
// as it resolves a managed image.

type Gallery struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties GalleryProperties `json:"properties"`
}

type GalleryProperties struct {
	Description       string                   `json:"description,omitempty"`
	Identifier        *GalleryIdentifier       `json:"identifier,omitempty"`
	ProvisioningState string                   `json:"provisioningState,omitempty"`
	SharingProfile    *GallerySharingProfile   `json:"sharingProfile,omitempty"`
	SoftDeletePolicy  *GallerySoftDeletePolicy `json:"softDeletePolicy,omitempty"`
}

type GalleryIdentifier struct {
	UniqueName string `json:"uniqueName"`
}

type GallerySharingProfile struct {
	Permissions          string         `json:"permissions,omitempty"`
	CommunityGalleryInfo map[string]any `json:"communityGalleryInfo,omitempty"`
}

type GallerySoftDeletePolicy struct {
	IsSoftDeleteEnabled *bool `json:"isSoftDeleteEnabled,omitempty"`
}

type galleryUpdate struct {
	Tags       map[string]string `json:"tags"`
	Properties *struct {
		Description      *string                  `json:"description"`
		SharingProfile   *GallerySharingProfile   `json:"sharingProfile"`
		SoftDeletePolicy *GallerySoftDeletePolicy `json:"softDeletePolicy"`
	} `json:"properties"`
}

type GalleryImage struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type"`
	Location   string                 `json:"location"`
	Tags       map[string]string      `json:"tags,omitempty"`
	Properties GalleryImageProperties `json:"properties"`
}

type GalleryImageProperties struct {
	Description         string                  `json:"description,omitempty"`
	Eula                string                  `json:"eula,omitempty"`
	PrivacyStatementURI string                  `json:"privacyStatementUri,omitempty"`
	ReleaseNoteURI      string                  `json:"releaseNoteUri,omitempty"`
	OSType              string                  `json:"osType"`
	OSState             string                  `json:"osState"`
	HyperVGeneration    string                  `json:"hyperVGeneration,omitempty"`
	EndOfLifeDate       string                  `json:"endOfLifeDate,omitempty"`
	Identifier          *GalleryImageIdentifier `json:"identifier"`
	Recommended         map[string]any          `json:"recommended,omitempty"`
	Disallowed          map[string]any          `json:"disallowed,omitempty"`
	PurchasePlan        map[string]any          `json:"purchasePlan,omitempty"`
	ProvisioningState   string                  `json:"provisioningState,omitempty"`
	Features            []map[string]any        `json:"features,omitempty"`
	Architecture        string                  `json:"architecture,omitempty"`
}

type GalleryImageIdentifier struct {
	Publisher string `json:"publisher"`
	Offer     string `json:"offer"`
	SKU       string `json:"sku"`
}

// galleryImageUpdate is GalleryImageUpdate: every member a PATCH may carry,
// nil when absent.
type galleryImageUpdate struct {
	Tags       map[string]string `json:"tags"`
	Properties *struct {
		Description         *string                 `json:"description"`
		Eula                *string                 `json:"eula"`
		PrivacyStatementURI *string                 `json:"privacyStatementUri"`
		ReleaseNoteURI      *string                 `json:"releaseNoteUri"`
		OSType              *string                 `json:"osType"`
		OSState             *string                 `json:"osState"`
		HyperVGeneration    *string                 `json:"hyperVGeneration"`
		EndOfLifeDate       *string                 `json:"endOfLifeDate"`
		Identifier          *GalleryImageIdentifier `json:"identifier"`
		Recommended         map[string]any          `json:"recommended"`
		Disallowed          map[string]any          `json:"disallowed"`
		PurchasePlan        map[string]any          `json:"purchasePlan"`
		Features            []map[string]any        `json:"features"`
		Architecture        *string                 `json:"architecture"`
	} `json:"properties"`
}

type GalleryImageVersion struct {
	ID         string                        `json:"id"`
	Name       string                        `json:"name"`
	Type       string                        `json:"type"`
	Location   string                        `json:"location"`
	Tags       map[string]string             `json:"tags,omitempty"`
	Properties GalleryImageVersionProperties `json:"properties"`
}

type GalleryImageVersionProperties struct {
	PublishingProfile *GalleryPublishingProfile  `json:"publishingProfile,omitempty"`
	ProvisioningState string                     `json:"provisioningState,omitempty"`
	StorageProfile    GalleryImageVersionStorage `json:"storageProfile"`
	SafetyProfile     *GalleryImageVersionSafety `json:"safetyProfile,omitempty"`
	ReplicationStatus *GalleryReplicationStatus  `json:"replicationStatus,omitempty"`
	SecurityProfile   map[string]any             `json:"securityProfile,omitempty"`
}

type GalleryPublishingProfile struct {
	TargetRegions           []GalleryTargetRegion `json:"targetRegions,omitempty"`
	ReplicaCount            *int32                `json:"replicaCount,omitempty"`
	ExcludeFromLatest       *bool                 `json:"excludeFromLatest,omitempty"`
	PublishedDate           string                `json:"publishedDate,omitempty"`
	EndOfLifeDate           string                `json:"endOfLifeDate,omitempty"`
	StorageAccountType      string                `json:"storageAccountType,omitempty"`
	ReplicationMode         string                `json:"replicationMode,omitempty"`
	TargetExtendedLocations []map[string]any      `json:"targetExtendedLocations,omitempty"`
}

type GalleryTargetRegion struct {
	Name                 string         `json:"name"`
	RegionalReplicaCount *int32         `json:"regionalReplicaCount,omitempty"`
	StorageAccountType   string         `json:"storageAccountType,omitempty"`
	Encryption           map[string]any `json:"encryption,omitempty"`
	ExcludeFromLatest    *bool          `json:"excludeFromLatest,omitempty"`
}

type GalleryImageVersionStorage struct {
	Source         *GalleryArtifactVersionFullSrc `json:"source,omitempty"`
	OSDiskImage    *GalleryDiskImage              `json:"osDiskImage,omitempty"`
	DataDiskImages []map[string]any               `json:"dataDiskImages,omitempty"`
}

type GalleryArtifactVersionFullSrc struct {
	ID                      string `json:"id,omitempty"`
	VirtualMachineID        string `json:"virtualMachineId,omitempty"`
	CommunityGalleryImageID string `json:"communityGalleryImageId,omitempty"`
}

type GalleryDiskImage struct {
	SizeInGB    int32                   `json:"sizeInGB,omitempty"`
	HostCaching string                  `json:"hostCaching,omitempty"`
	Source      *GalleryDiskImageSource `json:"source,omitempty"`
}

type GalleryDiskImageSource struct {
	ID               string `json:"id,omitempty"`
	URI              string `json:"uri,omitempty"`
	StorageAccountID string `json:"storageAccountId,omitempty"`
}

type GalleryImageVersionSafety struct {
	AllowDeletionOfReplicatedLocations *bool `json:"allowDeletionOfReplicatedLocations,omitempty"`
	ReportedForPolicyViolation         bool  `json:"reportedForPolicyViolation"`
}

type GalleryReplicationStatus struct {
	AggregatedState string                     `json:"aggregatedState"`
	Summary         []GalleryRegionReplication `json:"summary"`
}

type GalleryRegionReplication struct {
	Region   string `json:"region"`
	State    string `json:"state"`
	Details  string `json:"details"`
	Progress int32  `json:"progress"`
}

type galleryImageVersionUpdate struct {
	Tags       map[string]string `json:"tags"`
	Properties *struct {
		PublishingProfile *GalleryPublishingProfile   `json:"publishingProfile"`
		StorageProfile    *GalleryImageVersionStorage `json:"storageProfile"`
		SafetyProfile     *GalleryImageVersionSafety  `json:"safetyProfile"`
	} `json:"properties"`
}

var (
	azureGalleries             sim.Store[Gallery]
	azureGalleryImages         sim.Store[GalleryImage]
	azureGalleryImageVersions  sim.Store[GalleryImageVersion]
	azureGalleryNamePattern    = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.]{0,78}[A-Za-z0-9])?$`)
	azureGalleryImageNameRegex = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_.-]{0,78}[A-Za-z0-9])?$`)
	azureGalleryVersionPattern = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`)
	// azureGalleryImageRefPattern matches a gallery image definition id, or a
	// version id when the last group is present.
	azureGalleryImageRefPattern = regexp.MustCompile(`(?i)^/subscriptions/[^/]+/resourceGroups/[^/]+/providers/Microsoft\.Compute/galleries/[^/]+/images/[^/]+(/versions/[^/]+)?$`)
)

const (
	azureGalleryType             = "Microsoft.Compute/galleries"
	azureGalleryImageType        = "Microsoft.Compute/galleries/images"
	azureGalleryImageVersionType = "Microsoft.Compute/galleries/images/versions"
)

func azureLookupFold[T any](store sim.Store[T], id string, idOf func(T) string) (T, bool) {
	if item, ok := store.Get(id); ok {
		return item, true
	}
	found := store.Filter(func(item T) bool { return strings.EqualFold(idOf(item), id) })
	if len(found) == 0 {
		var zero T
		return zero, false
	}
	return found[0], true
}

func azureLookupGallery(id string) (Gallery, bool) {
	return azureLookupFold(azureGalleries, id, func(g Gallery) string { return g.ID })
}

func azureLookupGalleryImage(id string) (GalleryImage, bool) {
	return azureLookupFold(azureGalleryImages, id, func(i GalleryImage) string { return i.ID })
}

func azureLookupGalleryImageVersion(id string) (GalleryImageVersion, bool) {
	return azureLookupFold(azureGalleryImageVersions, id, func(v GalleryImageVersion) string { return v.ID })
}

func azureHasChildren[T any](store sim.Store[T], parentID string, idOf func(T) string) bool {
	prefix := strings.ToLower(parentID) + "/"
	return len(store.Filter(func(item T) bool { return strings.HasPrefix(strings.ToLower(idOf(item)), prefix) })) > 0
}

func azureGalleryParentMissing(w http.ResponseWriter, name string) {
	AzureErrorf(w, "ParentResourceNotFound", http.StatusNotFound,
		"Can not perform requested operation on nested resource. Parent resource '%s' not found.", name)
}

// azureGalleryNotFound is what GalleryRP answers for a definition or version it
// does not hold.
func azureGalleryNotFound(w http.ResponseWriter) {
	AzureError(w, "NotFound", "The entity was not found in this Azure location.", http.StatusNotFound)
}

func azureGalleryVersionLess(a, b string) bool {
	pa, pb := azureGalleryVersionPattern.FindStringSubmatch(a), azureGalleryVersionPattern.FindStringSubmatch(b)
	for i := 1; i <= 3; i++ {
		x, _ := strconv.ParseInt(pa[i], 10, 64)
		y, _ := strconv.ParseInt(pb[i], 10, 64)
		if x != y {
			return x < y
		}
	}
	return false
}

func azureValidGalleryVersionName(name string) bool {
	parts := azureGalleryVersionPattern.FindStringSubmatch(name)
	if parts == nil {
		return false
	}
	for _, part := range parts[1:] {
		if _, err := strconv.ParseInt(part, 10, 32); err != nil {
			return false
		}
	}
	return true
}

// azureGalleryVersionExcluded reports whether latest skips the version for a
// machine in location: the version says so, or its target region there does.
func azureGalleryVersionExcluded(version GalleryImageVersion, location string) bool {
	profile := version.Properties.PublishingProfile
	if profile == nil {
		return false
	}
	for _, region := range profile.TargetRegions {
		if azureSameLocation(region.Name, location) && region.ExcludeFromLatest != nil {
			return *region.ExcludeFromLatest
		}
	}
	return profile.ExcludeFromLatest != nil && *profile.ExcludeFromLatest
}

func azureSameLocation(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.ReplaceAll(s, " ", "")) }
	return norm(a) == norm(b)
}

// azureResolveGalleryImage resolves an imageReference.id naming a gallery image
// version, or an image definition, whose newest version latest admits for a
// machine in location. It returns the image and the definition's osState.
func azureResolveGalleryImage(id, location string) (vminstance.Image, string, *azureVMRequestFault) {
	notFound := &azureVMRequestFault{code: "ResourceNotFound", status: http.StatusNotFound,
		message: fmt.Sprintf("The Resource %q referenced by properties.storageProfile.imageReference was not found.", id)}
	definitionID, versionName := id, ""
	if i := strings.LastIndex(sim.ASCIIFold(id), "/versions/"); i >= 0 {
		definitionID, versionName = id[:i], id[i+len("/versions/"):]
	}
	definition, ok := azureLookupGalleryImage(definitionID)
	if !ok {
		return vminstance.Image{}, "", notFound
	}
	var chosen GalleryImageVersion
	if versionName != "" {
		version, ok := azureLookupGalleryImageVersion(definition.ID + "/versions/" + versionName)
		if !ok {
			return vminstance.Image{}, "", notFound
		}
		chosen = version
	} else {
		prefix := strings.ToLower(definition.ID) + "/versions/"
		for _, version := range azureGalleryImageVersions.Filter(func(v GalleryImageVersion) bool {
			return strings.HasPrefix(strings.ToLower(v.ID), prefix) && v.Properties.ProvisioningState == "Succeeded"
		}) {
			if azureGalleryVersionExcluded(version, location) {
				continue
			}
			if chosen.ID == "" || azureGalleryVersionLess(chosen.Name, version.Name) {
				chosen = version
			}
		}
		if chosen.ID == "" {
			return vminstance.Image{}, "", &azureVMRequestFault{code: "ResourceNotFound", status: http.StatusNotFound,
				message: fmt.Sprintf("The gallery image %q referenced by properties.storageProfile.imageReference has no image version a machine in %s can use as its latest.", definition.ID, location)}
		}
	}
	if chosen.Properties.ProvisioningState != "Succeeded" {
		return vminstance.Image{}, "", &azureVMRequestFault{code: "OperationNotAllowed", status: http.StatusConflict,
			message: fmt.Sprintf("Gallery image version %q is %s; a machine can be created from it once it has Succeeded.", chosen.ID, chosen.Properties.ProvisioningState)}
	}
	image, ok := azureImageBackings.Get(azureImageBackingKey(chosen.ID))
	if !ok {
		return vminstance.Image{}, "", notFound
	}
	return image, definition.Properties.OSState, nil
}

func azureIsGalleryImageRef(id string) bool {
	return azureGalleryImageRefPattern.MatchString(id)
}

func azureGalleryLROHeaders(w http.ResponseWriter, r *http.Request, location, opID string, withLocation bool) {
	asyncOperation, monitor := computeOperationURLs(r, sim.PathParam(r, "subscriptionId"), location, opID)
	w.Header().Set("Azure-AsyncOperation", asyncOperation)
	if withLocation {
		w.Header().Set("Location", monitor)
	}
	setAzureAsyncOperationRetryAfter(w, opID)
}

func azureGalleryListPage[T any](w http.ResponseWriter, r *http.Request, store sim.Store[T], prefix string, idOf func(T) string, view func(T) T) {
	prefix = strings.ToLower(prefix)
	all := store.Filter(func(item T) bool {
		id := strings.ToLower(idOf(item))
		// A list names its own level only: a gallery list holds no images.
		return strings.HasPrefix(id, prefix) && !strings.Contains(id[len(prefix):], "/")
	})
	page, next, ok := armPage(w, r, all, func(item T) string { return strings.ToLower(idOf(item)) })
	if !ok {
		return
	}
	value := make([]T, 0, len(page))
	for _, item := range page {
		value = append(value, view(item))
	}
	out := map[string]any{"value": value}
	if next != "" {
		out["nextLink"] = armNextLink(r, next)
	}
	sim.WriteJSON(w, http.StatusOK, out)
}

// azureGallerySharingFault refuses the sharing the simulator does not serve.
func azureGallerySharingFault(profile *GallerySharingProfile, softDelete *GallerySoftDeletePolicy) *azureVMRequestFault {
	if profile != nil && profile.Permissions != "" {
		switch {
		case strings.EqualFold(profile.Permissions, "Private"):
			profile.Permissions = "Private"
		case strings.EqualFold(profile.Permissions, "Groups"):
			profile.Permissions = "Groups"
		case strings.EqualFold(profile.Permissions, "Community"):
			return azureDiskFault("NotImplemented", http.StatusNotImplemented,
				"The simulator publishes no community galleries, so a gallery cannot be shared with the community.")
		default:
			return azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"The value '%s' of parameter sharingProfile.permissions is invalid.", profile.Permissions)
		}
	}
	if softDelete != nil && softDelete.IsSoftDeleteEnabled != nil && *softDelete.IsSoftDeleteEnabled {
		return azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator deletes gallery image versions outright, so it keeps no soft-deleted versions to restore.")
	}
	return nil
}

var azureGalleryOSTypes = []string{"Windows", "Linux"}
var azureGalleryOSStates = []string{"Generalized", "Specialized"}
var azureGalleryHyperVGenerations = []string{"V1", "V2"}
var azureGalleryArchitectures = []string{"x64", "Arm64"}

func azureGalleryEnum(field, value string, allowed []string) (string, *azureVMRequestFault) {
	for _, a := range allowed {
		if strings.EqualFold(a, value) {
			return a, nil
		}
	}
	return "", azureDiskFault("InvalidParameter", http.StatusBadRequest, "The value '%s' of parameter %s is invalid.", value, field)
}

// azureNormalizeGalleryImage validates a definition's create and fills the
// defaults GalleryRP applies.
func azureNormalizeGalleryImage(props *GalleryImageProperties) *azureVMRequestFault {
	if props.OSType == "" {
		return azureDiskFault("InvalidParameter", http.StatusBadRequest, "Required parameter 'osType' is missing (null).")
	}
	if props.OSState == "" {
		return azureDiskFault("InvalidParameter", http.StatusBadRequest, "Required parameter 'osState' is missing (null).")
	}
	if props.Identifier == nil || props.Identifier.Publisher == "" || props.Identifier.Offer == "" || props.Identifier.SKU == "" {
		return azureDiskFault("InvalidParameter", http.StatusBadRequest, "Required parameter 'identifier' is missing (null): it names a publisher, an offer and a sku.")
	}
	var fault *azureVMRequestFault
	if props.OSType, fault = azureGalleryEnum("osType", props.OSType, azureGalleryOSTypes); fault != nil {
		return fault
	}
	if props.OSState, fault = azureGalleryEnum("osState", props.OSState, azureGalleryOSStates); fault != nil {
		return fault
	}
	if props.HyperVGeneration == "" {
		props.HyperVGeneration = "V1"
	}
	if props.HyperVGeneration, fault = azureGalleryEnum("hyperVGeneration", props.HyperVGeneration, azureGalleryHyperVGenerations); fault != nil {
		return fault
	}
	if props.Architecture == "" {
		props.Architecture = "x64"
	}
	if props.Architecture, fault = azureGalleryEnum("architecture", props.Architecture, azureGalleryArchitectures); fault != nil {
		return fault
	}
	return nil
}

// azureGalleryImageImmutable reports the first creation-only member a write
// changes.
func azureGalleryImageImmutable(stored, requested GalleryImageProperties) string {
	switch {
	case !strings.EqualFold(stored.OSType, requested.OSType):
		return "osType"
	case !strings.EqualFold(stored.OSState, requested.OSState):
		return "osState"
	case !strings.EqualFold(stored.HyperVGeneration, requested.HyperVGeneration):
		return "hyperVGeneration"
	case !strings.EqualFold(stored.Architecture, requested.Architecture):
		return "architecture"
	case !strings.EqualFold(stored.Identifier.Publisher, requested.Identifier.Publisher) ||
		!strings.EqualFold(stored.Identifier.Offer, requested.Identifier.Offer) ||
		!strings.EqualFold(stored.Identifier.SKU, requested.Identifier.SKU):
		return "identifier"
	}
	return ""
}

// azureNormalizePublishingProfile fills the publishing defaults: the gallery's
// own region as the one target region, one replica, Standard_LRS.
func azureNormalizePublishingProfile(profile *GalleryPublishingProfile, location string) *GalleryPublishingProfile {
	if profile == nil {
		profile = &GalleryPublishingProfile{}
	}
	if profile.ReplicaCount == nil {
		one := int32(1)
		profile.ReplicaCount = &one
	}
	if profile.ExcludeFromLatest == nil {
		exclude := false
		profile.ExcludeFromLatest = &exclude
	}
	if profile.StorageAccountType == "" {
		profile.StorageAccountType = "Standard_LRS"
	}
	if len(profile.TargetRegions) == 0 {
		profile.TargetRegions = []GalleryTargetRegion{{Name: location}}
	}
	for i := range profile.TargetRegions {
		region := &profile.TargetRegions[i]
		if region.RegionalReplicaCount == nil {
			count := *profile.ReplicaCount
			region.RegionalReplicaCount = &count
		}
		if region.StorageAccountType == "" {
			region.StorageAccountType = profile.StorageAccountType
		}
		if region.ExcludeFromLatest == nil {
			exclude := false
			region.ExcludeFromLatest = &exclude
		}
	}
	return profile
}

// azureGalleryVersionView fills replicationStatus when $expand asks for it.
func azureGalleryVersionView(version GalleryImageVersion, expand string) GalleryImageVersion {
	version.Properties.ReplicationStatus = nil
	if !strings.EqualFold(expand, "ReplicationStatus") {
		return version
	}
	state, regionState, progress := "InProgress", "Replicating", int32(0)
	switch version.Properties.ProvisioningState {
	case "Succeeded":
		state, regionState, progress = "Completed", "Completed", 100
	case "Failed":
		state, regionState = "Failed", "Failed"
	}
	status := &GalleryReplicationStatus{AggregatedState: state, Summary: []GalleryRegionReplication{}}
	if profile := version.Properties.PublishingProfile; profile != nil {
		for _, region := range profile.TargetRegions {
			status.Summary = append(status.Summary, GalleryRegionReplication{Region: region.Name, State: regionState, Progress: progress})
		}
	}
	version.Properties.ReplicationStatus = status
	return version
}

// azureGallerySource is what a version copies, a managed image's or another
// version's backing, or captures, a machine's OS disk.
type azureGallerySource struct {
	vm      *VirtualMachine
	backing *vminstance.Image
	osState string
}

// azureResolveGalleryVersionSource validates a version's source and returns
// what the version captures or copies.
func azureResolveGalleryVersionSource(ctx context.Context, storage GalleryImageVersionStorage, definition GalleryImage) (azureGallerySource, *azureVMRequestFault) {
	if len(storage.DataDiskImages) > 0 {
		return azureGallerySource{}, azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator captures a gallery image version's OS disk only, so it takes no dataDiskImages.")
	}
	var src GalleryArtifactVersionFullSrc
	if storage.Source != nil {
		src = *storage.Source
	}
	switch {
	case src.CommunityGalleryImageID != "":
		return azureGallerySource{}, azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator publishes no community galleries, so %s cannot be a source.", src.CommunityGalleryImageID)
	case src.VirtualMachineID != "" || strings.Contains(strings.ToLower(src.ID), "/providers/microsoft.compute/virtualmachines/"):
		vmID := src.VirtualMachineID
		if vmID == "" {
			vmID = src.ID
		}
		return azureGalleryVMSource(vmID, definition)
	case strings.Contains(strings.ToLower(src.ID), "/providers/microsoft.compute/images/"):
		image, ok := azureLookupFold(azureComputeImages, src.ID, func(i ComputeImage) string { return i.ID })
		if !ok {
			return azureGallerySource{}, azureDiskFault("ResourceNotFound", http.StatusNotFound,
				"The Resource %q referenced by storageProfile.source.id was not found.", src.ID)
		}
		backing, ok := azureImageBackings.Get(azureImageBackingKey(image.ID))
		if !ok {
			return azureGallerySource{}, azureDiskFault("ResourceNotFound", http.StatusNotFound,
				"The Resource %q referenced by storageProfile.source.id holds no OS disk.", src.ID)
		}
		return azureGallerySource{backing: &backing, osState: "Generalized"}, nil
	case azureIsGalleryImageRef(src.ID) && strings.Contains(strings.ToLower(src.ID), "/versions/"):
		version, ok := azureLookupGalleryImageVersion(src.ID)
		if !ok {
			return azureGallerySource{}, azureDiskFault("ResourceNotFound", http.StatusNotFound,
				"The Resource %q referenced by storageProfile.source.id was not found.", src.ID)
		}
		backing, ok := azureImageBackings.Get(azureImageBackingKey(version.ID))
		if !ok || version.Properties.ProvisioningState != "Succeeded" {
			return azureGallerySource{}, azureDiskFault("OperationNotAllowed", http.StatusConflict,
				"Gallery image version %q is %s; it can be a source once it has Succeeded.", version.ID, version.Properties.ProvisioningState)
		}
		parentID := version.ID[:strings.LastIndex(sim.ASCIIFold(version.ID), "/versions/")]
		parent, _ := azureLookupGalleryImage(parentID)
		return azureGallerySource{backing: &backing, osState: parent.Properties.OSState}, nil
	case src.ID != "":
		return azureGallerySource{}, azureDiskFault("InvalidParameter", http.StatusBadRequest,
			"The value %q of parameter storageProfile.source.id is invalid: it names a managed image, a virtual machine or a gallery image version.", src.ID)
	}
	if storage.OSDiskImage == nil || storage.OSDiskImage.Source == nil {
		return azureGallerySource{}, azureDiskFault("InvalidParameter", http.StatusBadRequest,
			"Required parameter 'storageProfile.source' or 'storageProfile.osDiskImage.source' is missing (null).")
	}
	disk := storage.OSDiskImage.Source
	if disk.URI != "" {
		if disk.StorageAccountID == "" {
			return azureGallerySource{}, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"Required parameter 'storageProfile.osDiskImage.source.storageAccountId' is missing (null): a VHD source names its storage account.")
		}
		backing, err := azureResolveImageBlobURI(ctx, disk.URI)
		if err != nil {
			return azureGallerySource{}, azureDiskFault("InvalidParameter", http.StatusBadRequest,
				"The value of parameter storageProfile.osDiskImage.source.uri is invalid: %v", err)
		}
		return azureGallerySource{backing: &backing, osState: definition.Properties.OSState}, nil
	}
	return azureGallerySource{}, azureDiskFault("NotImplemented", http.StatusNotImplemented,
		"The simulator keeps a managed disk as a filesystem, not a bootable OS disk, so %s cannot be a gallery image version's OS disk.", disk.ID)
}

func azureGalleryVMSource(vmID string, definition GalleryImage) (azureGallerySource, *azureVMRequestFault) {
	vm, ok := azureLookupFold(azureVMs, vmID, func(v VirtualMachine) string { return v.ID })
	if !ok {
		return azureGallerySource{}, azureDiskFault("ResourceNotFound", http.StatusNotFound,
			"The Resource %q referenced by storageProfile.source.virtualMachineId was not found.", vmID)
	}
	if len(anySlice(vm.Properties.StorageProfile["dataDisks"])) > 0 {
		return azureGallerySource{}, azureDiskFault("NotImplemented", http.StatusNotImplemented,
			"The simulator captures a gallery image version's OS disk only; detach the data disks of %q first.", vm.ID)
	}
	generalized, _ := azureVMGeneralized.Get(vm.ID)
	switch {
	case definition.Properties.OSState == "Generalized" && !generalized:
		return azureGallerySource{}, azureDiskFault("OperationNotAllowed", http.StatusConflict,
			"Creating a Generalized image version from VM %q is not allowed because it has not been generalized.", vm.ID)
	case definition.Properties.OSState == "Specialized" && generalized:
		return azureGallerySource{}, azureDiskFault("OperationNotAllowed", http.StatusConflict,
			"Creating a Specialized image version from VM %q is not allowed because it has been generalized.", vm.ID)
	}
	return azureGallerySource{vm: &vm, osState: definition.Properties.OSState}, nil
}

func registerComputeGalleries(srv *sim.Server) {
	azureGalleries = sim.MakeStore[Gallery](srv.DB(), "compute_galleries")
	azureGalleryImages = sim.MakeStore[GalleryImage](srv.DB(), "compute_gallery_images")
	azureGalleryImageVersions = sim.MakeStore[GalleryImageVersion](srv.DB(), "compute_gallery_image_versions")
	// A capture whose operation died with the previous process never finished.
	for _, version := range azureGalleryImageVersions.Filter(func(v GalleryImageVersion) bool {
		return v.Properties.ProvisioningState == "Creating"
	}) {
		azureGalleryImageVersions.Update(version.ID, func(v *GalleryImageVersion) { v.Properties.ProvisioningState = "Failed" })
	}
	logger := srv.Logger()

	const armBase = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute"
	galleryID := func(r *http.Request) string {
		return armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "galleries", sim.PathParam(r, "galleryName"))
	}
	imageID := func(r *http.Request) string {
		return galleryID(r) + "/images/" + sim.PathParam(r, "galleryImageName")
	}
	versionID := func(r *http.Request) string {
		return imageID(r) + "/versions/" + sim.PathParam(r, "galleryImageVersionName")
	}
	readBody := func(w http.ResponseWriter, r *http.Request, into any) bool {
		if err := sim.ReadJSON(r, into); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return false
		}
		return true
	}
	writeFault := func(w http.ResponseWriter, fault *azureVMRequestFault) {
		AzureError(w, fault.code, fault.message, fault.status)
	}

	srv.HandleFunc("PUT "+armBase+"/galleries/{galleryName}", func(w http.ResponseWriter, r *http.Request) {
		var req Gallery
		if !readBody(w, r, &req) {
			return
		}
		name := sim.PathParam(r, "galleryName")
		if !azureGalleryNamePattern.MatchString(name) {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The gallery name '%s' is invalid. It may contain only letters, digits, underscores and periods, must start and end with a letter or digit, and is at most 80 characters.", name)
			return
		}
		if req.Location == "" {
			AzureError(w, "LocationRequired", "The location property is required for this definition.", http.StatusBadRequest)
			return
		}
		if fault := azureGallerySharingFault(req.Properties.SharingProfile, req.Properties.SoftDeletePolicy); fault != nil {
			writeFault(w, fault)
			return
		}
		id := galleryID(r)
		gallery := Gallery{ID: id, Name: name, Type: azureGalleryType, Location: req.Location, Tags: req.Tags, Properties: req.Properties}
		status := http.StatusCreated
		if prev, exists := azureLookupGallery(id); exists {
			status = http.StatusOK
			gallery.ID, gallery.Name, gallery.Location = prev.ID, prev.Name, prev.Location
		}
		gallery.Properties.Identifier = &GalleryIdentifier{UniqueName: sim.PathParam(r, "subscriptionId") + "-" + strings.ToUpper(name)}
		gallery.Properties.ProvisioningState = "Succeeded"
		azureGalleries.Put(gallery.ID, gallery)
		azureGalleryLROHeaders(w, r, gallery.Location, issueAzureAsyncOperation(nil), false)
		sim.WriteJSON(w, status, gallery)
	})

	srv.HandleFunc("PATCH "+armBase+"/galleries/{galleryName}", func(w http.ResponseWriter, r *http.Request) {
		var update galleryUpdate
		if !readBody(w, r, &update) {
			return
		}
		gallery, ok := azureLookupGallery(galleryID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource 'Microsoft.Compute/galleries/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "galleryName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		if update.Tags != nil {
			gallery.Tags = update.Tags
		}
		if p := update.Properties; p != nil {
			if fault := azureGallerySharingFault(p.SharingProfile, p.SoftDeletePolicy); fault != nil {
				writeFault(w, fault)
				return
			}
			if p.Description != nil {
				gallery.Properties.Description = *p.Description
			}
			if p.SharingProfile != nil {
				gallery.Properties.SharingProfile = p.SharingProfile
			}
			if p.SoftDeletePolicy != nil {
				gallery.Properties.SoftDeletePolicy = p.SoftDeletePolicy
			}
		}
		azureGalleries.Put(gallery.ID, gallery)
		azureGalleryLROHeaders(w, r, gallery.Location, issueAzureAsyncOperation(nil), false)
		sim.WriteJSON(w, http.StatusOK, gallery)
	})

	srv.HandleFunc("GET "+armBase+"/galleries/{galleryName}", func(w http.ResponseWriter, r *http.Request) {
		gallery, ok := azureLookupGallery(galleryID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource 'Microsoft.Compute/galleries/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "galleryName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, gallery)
	})

	srv.HandleFunc("DELETE "+armBase+"/galleries/{galleryName}", func(w http.ResponseWriter, r *http.Request) {
		gallery, ok := azureLookupGallery(galleryID(r))
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if azureHasChildren(azureGalleryImages, gallery.ID, func(i GalleryImage) string { return i.ID }) {
			AzureError(w, "CannotDeleteResource", "Can not delete resource before nested resources are deleted.", http.StatusConflict)
			return
		}
		azureGalleries.Delete(gallery.ID)
		azureGalleryLROHeaders(w, r, gallery.Location, issueAzureAsyncOperation(nil), true)
		w.WriteHeader(http.StatusAccepted)
	})

	srv.HandleFunc("GET "+armBase+"/galleries", func(w http.ResponseWriter, r *http.Request) {
		prefix := armResourceID(sim.PathParam(r, "subscriptionId"), sim.PathParam(r, "resourceGroupName"), "Microsoft.Compute", "galleries") + "/"
		azureGalleryListPage(w, r, azureGalleries, prefix, func(g Gallery) string { return g.ID }, func(g Gallery) Gallery { return g })
	})
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.Compute/galleries", func(w http.ResponseWriter, r *http.Request) {
		prefix := strings.ToLower(armSubscriptionID(sim.PathParam(r, "subscriptionId")) + "/")
		all := azureGalleries.Filter(func(g Gallery) bool { return strings.HasPrefix(strings.ToLower(g.ID), prefix) })
		page, next, ok := armPage(w, r, all, func(g Gallery) string { return strings.ToLower(g.ID) })
		if !ok {
			return
		}
		out := map[string]any{"value": page}
		if next != "" {
			out["nextLink"] = armNextLink(r, next)
		}
		sim.WriteJSON(w, http.StatusOK, out)
	})

	srv.HandleFunc("PUT "+armBase+"/galleries/{galleryName}/images/{galleryImageName}", func(w http.ResponseWriter, r *http.Request) {
		var req GalleryImage
		if !readBody(w, r, &req) {
			return
		}
		gallery, ok := azureLookupGallery(galleryID(r))
		if !ok {
			azureGalleryParentMissing(w, sim.PathParam(r, "galleryName"))
			return
		}
		name := sim.PathParam(r, "galleryImageName")
		if !azureGalleryImageNameRegex.MatchString(name) {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The gallery image name '%s' is invalid. It may contain only letters, digits, underscores, hyphens and periods, must start and end with a letter or digit, and is at most 80 characters.", name)
			return
		}
		if req.Location == "" {
			AzureError(w, "LocationRequired", "The location property is required for this definition.", http.StatusBadRequest)
			return
		}
		id := gallery.ID + "/images/" + name
		prev, exists := azureLookupGalleryImage(id)
		// A rewrite that leaves out a defaulted member keeps the stored value.
		if exists && req.Properties.HyperVGeneration == "" {
			req.Properties.HyperVGeneration = prev.Properties.HyperVGeneration
		}
		if exists && req.Properties.Architecture == "" {
			req.Properties.Architecture = prev.Properties.Architecture
		}
		if fault := azureNormalizeGalleryImage(&req.Properties); fault != nil {
			writeFault(w, fault)
			return
		}
		image := GalleryImage{ID: id, Name: name, Type: azureGalleryImageType, Location: req.Location, Tags: req.Tags, Properties: req.Properties}
		status := http.StatusCreated
		if exists {
			if member := azureGalleryImageImmutable(prev.Properties, req.Properties); member != "" {
				AzureErrorf(w, "PropertyChangeNotAllowed", http.StatusConflict, "Changing property 'galleryImage.properties.%s' is not allowed.", member)
				return
			}
			status = http.StatusOK
			image.ID, image.Name, image.Location = prev.ID, prev.Name, prev.Location
		}
		image.Properties.ProvisioningState = "Succeeded"
		azureGalleryImages.Put(image.ID, image)
		azureGalleryLROHeaders(w, r, image.Location, issueAzureAsyncOperation(nil), false)
		sim.WriteJSON(w, status, image)
	})

	srv.HandleFunc("PATCH "+armBase+"/galleries/{galleryName}/images/{galleryImageName}", func(w http.ResponseWriter, r *http.Request) {
		var update galleryImageUpdate
		if !readBody(w, r, &update) {
			return
		}
		image, ok := azureLookupGalleryImage(imageID(r))
		if !ok {
			azureGalleryNotFound(w)
			return
		}
		if update.Tags != nil {
			image.Tags = update.Tags
		}
		if p := update.Properties; p != nil {
			requested := image.Properties
			setString := func(dst *string, src *string) {
				if src != nil {
					*dst = *src
				}
			}
			setString(&requested.OSType, p.OSType)
			setString(&requested.OSState, p.OSState)
			setString(&requested.HyperVGeneration, p.HyperVGeneration)
			setString(&requested.Architecture, p.Architecture)
			if p.Identifier != nil {
				requested.Identifier = p.Identifier
			}
			if member := azureGalleryImageImmutable(image.Properties, requested); member != "" {
				AzureErrorf(w, "PropertyChangeNotAllowed", http.StatusConflict, "Changing property 'galleryImage.properties.%s' is not allowed.", member)
				return
			}
			setString(&requested.Description, p.Description)
			setString(&requested.Eula, p.Eula)
			setString(&requested.PrivacyStatementURI, p.PrivacyStatementURI)
			setString(&requested.ReleaseNoteURI, p.ReleaseNoteURI)
			setString(&requested.EndOfLifeDate, p.EndOfLifeDate)
			if p.Recommended != nil {
				requested.Recommended = p.Recommended
			}
			if p.Disallowed != nil {
				requested.Disallowed = p.Disallowed
			}
			if p.PurchasePlan != nil {
				requested.PurchasePlan = p.PurchasePlan
			}
			if p.Features != nil {
				requested.Features = p.Features
			}
			requested.OSType, requested.OSState = image.Properties.OSType, image.Properties.OSState
			requested.HyperVGeneration, requested.Architecture = image.Properties.HyperVGeneration, image.Properties.Architecture
			requested.Identifier = image.Properties.Identifier
			image.Properties = requested
		}
		azureGalleryImages.Put(image.ID, image)
		azureGalleryLROHeaders(w, r, image.Location, issueAzureAsyncOperation(nil), false)
		sim.WriteJSON(w, http.StatusOK, image)
	})

	srv.HandleFunc("GET "+armBase+"/galleries/{galleryName}/images/{galleryImageName}", func(w http.ResponseWriter, r *http.Request) {
		image, ok := azureLookupGalleryImage(imageID(r))
		if !ok {
			azureGalleryNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, image)
	})

	srv.HandleFunc("DELETE "+armBase+"/galleries/{galleryName}/images/{galleryImageName}", func(w http.ResponseWriter, r *http.Request) {
		image, ok := azureLookupGalleryImage(imageID(r))
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if azureHasChildren(azureGalleryImageVersions, image.ID, func(v GalleryImageVersion) string { return v.ID }) {
			AzureError(w, "CannotDeleteResource", "Can not delete resource before nested resources are deleted.", http.StatusConflict)
			return
		}
		azureGalleryImages.Delete(image.ID)
		azureGalleryLROHeaders(w, r, image.Location, issueAzureAsyncOperation(nil), true)
		w.WriteHeader(http.StatusAccepted)
	})

	srv.HandleFunc("GET "+armBase+"/galleries/{galleryName}/images", func(w http.ResponseWriter, r *http.Request) {
		gallery, ok := azureLookupGallery(galleryID(r))
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound, "The Resource 'Microsoft.Compute/galleries/%s' under resource group '%s' was not found.",
				sim.PathParam(r, "galleryName"), sim.PathParam(r, "resourceGroupName"))
			return
		}
		azureGalleryListPage(w, r, azureGalleryImages, gallery.ID+"/images/", func(i GalleryImage) string { return i.ID }, func(i GalleryImage) GalleryImage { return i })
	})

	srv.HandleFunc("PUT "+armBase+"/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}", func(w http.ResponseWriter, r *http.Request) {
		var req GalleryImageVersion
		if !readBody(w, r, &req) {
			return
		}
		definition, ok := azureLookupGalleryImage(imageID(r))
		if !ok {
			azureGalleryParentMissing(w, sim.PathParam(r, "galleryImageName"))
			return
		}
		name := sim.PathParam(r, "galleryImageVersionName")
		if !azureValidGalleryVersionName(name) {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The gallery image version name '%s' is invalid. It must follow the Major(int32).Minor(int32).Patch(int32) format.", name)
			return
		}
		if req.Location == "" {
			AzureError(w, "LocationRequired", "The location property is required for this definition.", http.StatusBadRequest)
			return
		}
		id := definition.ID + "/versions/" + name
		if prev, exists := azureLookupGalleryImageVersion(id); exists {
			if azureGallerySourceChanged(prev.Properties.StorageProfile, req.Properties.StorageProfile) {
				AzureError(w, "PropertyChangeNotAllowed", "Changing property 'galleryImageVersion.properties.storageProfile' is not allowed.", http.StatusConflict)
				return
			}
			published := azureGalleryPublishedDate(prev)
			prev.Tags = req.Tags
			prev.Properties.PublishingProfile = azureNormalizePublishingProfile(req.Properties.PublishingProfile, prev.Location)
			prev.Properties.PublishingProfile.PublishedDate = published
			if req.Properties.SafetyProfile != nil {
				prev.Properties.SafetyProfile = azureNormalizeSafetyProfile(req.Properties.SafetyProfile)
			}
			azureGalleryImageVersions.Put(prev.ID, prev)
			azureGalleryLROHeaders(w, r, prev.Location, issueAzureAsyncOperation(nil), false)
			sim.WriteJSON(w, http.StatusOK, azureGalleryVersionView(prev, ""))
			return
		}
		source, fault := azureResolveGalleryVersionSource(r.Context(), req.Properties.StorageProfile, definition)
		if fault != nil {
			writeFault(w, fault)
			return
		}
		if !strings.EqualFold(source.osState, definition.Properties.OSState) {
			AzureErrorf(w, "InvalidParameter", http.StatusBadRequest,
				"The source of a gallery image version is %s, but gallery image %q is %s.", source.osState, definition.ID, definition.Properties.OSState)
			return
		}
		version := GalleryImageVersion{ID: id, Name: name, Type: azureGalleryImageVersionType, Location: req.Location, Tags: req.Tags}
		version.Properties.PublishingProfile = azureNormalizePublishingProfile(req.Properties.PublishingProfile, req.Location)
		version.Properties.PublishingProfile.PublishedDate = time.Now().UTC().Format(time.RFC3339Nano)
		version.Properties.SafetyProfile = azureNormalizeSafetyProfile(req.Properties.SafetyProfile)
		version.Properties.SecurityProfile = req.Properties.SecurityProfile
		version.Properties.StorageProfile = req.Properties.StorageProfile
		if version.Properties.StorageProfile.OSDiskImage == nil {
			version.Properties.StorageProfile.OSDiskImage = &GalleryDiskImage{}
		}
		if version.Properties.StorageProfile.OSDiskImage.HostCaching == "" {
			version.Properties.StorageProfile.OSDiskImage.HostCaching = "ReadWrite"
		}
		if source.backing != nil {
			version.Properties.ProvisioningState = "Succeeded"
			azureGalleryImageVersions.Put(id, version)
			azureImageBackings.Put(azureImageBackingKey(id), *source.backing)
			azureGalleryLROHeaders(w, r, version.Location, issueAzureAsyncOperation(nil), false)
			sim.WriteJSON(w, http.StatusCreated, version)
			return
		}
		vm := *source.vm
		version.Properties.ProvisioningState = "Creating"
		azureGalleryImageVersions.Put(id, version)
		opID := startAzureAsyncOperationOutcome(func() *AsyncOperationError {
			captured, err := azureCommitVMImage(context.Background(), vm, "gallery")
			state := "Succeeded"
			if err != nil {
				logger.Error().Err(err).Str("version", id).Msg("failed to capture the gallery image version's source machine")
				state = "Failed"
			}
			if azureGalleryImageVersions.Update(id, func(v *GalleryImageVersion) { v.Properties.ProvisioningState = state }) && err == nil {
				azureImageBackings.Put(azureImageBackingKey(id), captured)
			}
			if err != nil {
				return &AsyncOperationError{Code: "OperationNotAllowed", Message: err.Error()}
			}
			return nil
		})
		azureGalleryLROHeaders(w, r, version.Location, opID, false)
		sim.WriteJSON(w, http.StatusCreated, version)
	})

	srv.HandleFunc("PATCH "+armBase+"/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}", func(w http.ResponseWriter, r *http.Request) {
		var update galleryImageVersionUpdate
		if !readBody(w, r, &update) {
			return
		}
		version, ok := azureLookupGalleryImageVersion(versionID(r))
		if !ok {
			azureGalleryNotFound(w)
			return
		}
		if update.Tags != nil {
			version.Tags = update.Tags
		}
		if p := update.Properties; p != nil {
			if p.StorageProfile != nil && azureGallerySourceChanged(version.Properties.StorageProfile, *p.StorageProfile) {
				AzureError(w, "PropertyChangeNotAllowed", "Changing property 'galleryImageVersion.properties.storageProfile' is not allowed.", http.StatusConflict)
				return
			}
			if p.PublishingProfile != nil {
				published := azureGalleryPublishedDate(version)
				version.Properties.PublishingProfile = azureNormalizePublishingProfile(p.PublishingProfile, version.Location)
				version.Properties.PublishingProfile.PublishedDate = published
			}
			if p.SafetyProfile != nil {
				version.Properties.SafetyProfile = azureNormalizeSafetyProfile(p.SafetyProfile)
			}
		}
		azureGalleryImageVersions.Put(version.ID, version)
		azureGalleryLROHeaders(w, r, version.Location, issueAzureAsyncOperation(nil), false)
		sim.WriteJSON(w, http.StatusOK, version)
	})

	srv.HandleFunc("GET "+armBase+"/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}", func(w http.ResponseWriter, r *http.Request) {
		version, ok := azureLookupGalleryImageVersion(versionID(r))
		if !ok {
			azureGalleryNotFound(w)
			return
		}
		sim.WriteJSON(w, http.StatusOK, azureGalleryVersionView(version, r.URL.Query().Get("$expand")))
	})

	srv.HandleFunc("DELETE "+armBase+"/galleries/{galleryName}/images/{galleryImageName}/versions/{galleryImageVersionName}", func(w http.ResponseWriter, r *http.Request) {
		version, ok := azureLookupGalleryImageVersion(versionID(r))
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		azureGalleryImageVersions.Delete(version.ID)
		azureImageBackings.Delete(azureImageBackingKey(version.ID))
		azureGalleryLROHeaders(w, r, version.Location, issueAzureAsyncOperation(nil), true)
		w.WriteHeader(http.StatusAccepted)
	})

	srv.HandleFunc("GET "+armBase+"/galleries/{galleryName}/images/{galleryImageName}/versions", func(w http.ResponseWriter, r *http.Request) {
		image, ok := azureLookupGalleryImage(imageID(r))
		if !ok {
			azureGalleryNotFound(w)
			return
		}
		expand := r.URL.Query().Get("$expand")
		azureGalleryListPage(w, r, azureGalleryImageVersions, image.ID+"/versions/", func(v GalleryImageVersion) string { return v.ID },
			func(v GalleryImageVersion) GalleryImageVersion { return azureGalleryVersionView(v, expand) })
	})

	registerComputeGalleryUnserved(srv)
}

// A gallery application installs packages through a machine's
// applicationProfile, and sharing publishes a gallery through the
// SharedGalleries and CommunityGalleries APIs; the simulator serves none of
// them.
func registerComputeGalleryUnserved(srv *sim.Server) {
	const base = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Compute/galleries/{galleryName}"
	applications := func(w http.ResponseWriter, r *http.Request) {
		AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"The simulator installs no gallery applications, so gallery %q holds no application definitions or versions.", sim.PathParam(r, "galleryName"))
	}
	srv.HandleFunc("PUT "+base+"/applications/{galleryApplicationName}", applications)
	srv.HandleFunc("PATCH "+base+"/applications/{galleryApplicationName}", applications)
	srv.HandleFunc("GET "+base+"/applications/{galleryApplicationName}", applications)
	srv.HandleFunc("DELETE "+base+"/applications/{galleryApplicationName}", applications)
	srv.HandleFunc("GET "+base+"/applications", applications)
	srv.HandleFunc("PUT "+base+"/applications/{galleryApplicationName}/versions/{galleryApplicationVersionName}", applications)
	srv.HandleFunc("PATCH "+base+"/applications/{galleryApplicationName}/versions/{galleryApplicationVersionName}", applications)
	srv.HandleFunc("GET "+base+"/applications/{galleryApplicationName}/versions/{galleryApplicationVersionName}", applications)
	srv.HandleFunc("DELETE "+base+"/applications/{galleryApplicationName}/versions/{galleryApplicationVersionName}", applications)
	srv.HandleFunc("GET "+base+"/applications/{galleryApplicationName}/versions", applications)
	srv.HandleFunc("POST "+base+"/share", func(w http.ResponseWriter, r *http.Request) {
		AzureErrorf(w, "NotImplemented", http.StatusNotImplemented,
			"The simulator serves no SharedGalleries or CommunityGalleries APIs, so gallery %q cannot be shared beyond its subscription.", sim.PathParam(r, "galleryName"))
	})
}

func azureGalleryPublishedDate(version GalleryImageVersion) string {
	if version.Properties.PublishingProfile != nil {
		return version.Properties.PublishingProfile.PublishedDate
	}
	return ""
}

func azureNormalizeSafetyProfile(profile *GalleryImageVersionSafety) *GalleryImageVersionSafety {
	if profile == nil {
		profile = &GalleryImageVersionSafety{}
	}
	if profile.AllowDeletionOfReplicatedLocations == nil {
		allow := false
		profile.AllowDeletionOfReplicatedLocations = &allow
	}
	profile.ReportedForPolicyViolation = false
	return profile
}

// azureGallerySourceChanged reports whether a write names a source other than
// the version's own; a write that names none keeps it, as GalleryRP's update
// without a source id does.
func azureGallerySourceChanged(stored, requested GalleryImageVersionStorage) bool {
	if src := requested.Source; src != nil && (src.ID != "" || src.VirtualMachineID != "" || src.CommunityGalleryImageID != "") {
		var have GalleryArtifactVersionFullSrc
		if stored.Source != nil {
			have = *stored.Source
		}
		if !strings.EqualFold(have.ID, src.ID) || !strings.EqualFold(have.VirtualMachineID, src.VirtualMachineID) ||
			!strings.EqualFold(have.CommunityGalleryImageID, src.CommunityGalleryImageID) {
			return true
		}
	}
	if requested.OSDiskImage != nil && requested.OSDiskImage.Source != nil {
		var have GalleryDiskImageSource
		if stored.OSDiskImage != nil && stored.OSDiskImage.Source != nil {
			have = *stored.OSDiskImage.Source
		}
		src := requested.OSDiskImage.Source
		if !strings.EqualFold(have.ID, src.ID) || have.URI != src.URI {
			return true
		}
	}
	return false
}
