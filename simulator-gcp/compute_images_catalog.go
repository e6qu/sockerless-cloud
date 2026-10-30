package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/moby/moby/client"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/vminstance"
)

// gcpPublicImage is one image of the public catalog Google publishes under
// its image projects, as images.get reports it, and the OCI image an
// instance booted from it runs on a container backend.
type gcpPublicImage struct {
	Project     string
	Name        string
	Family      string
	Description string
	// Architecture is the Compute Engine spelling, X86_64 or ARM64.
	Architecture string
	Licenses     []string
	DiskSizeGb   int64
	Image        vminstance.Image
}

// gcpPublicImages is the catalog: Debian 12 and Ubuntu 24.04 LTS on both
// architectures, each mapped to its per-architecture Docker official image
// manifest on mirror.gcr.io.
var gcpPublicImages = []gcpPublicImage{
	{
		Project: "debian-cloud", Name: "debian-12-bookworm-v20251001", Family: "debian-12",
		Description:  "Debian, Debian GNU/Linux, 12 (bookworm), amd64 built on 20251001",
		Architecture: "X86_64", DiskSizeGb: 10,
		Licenses: []string{"projects/debian-cloud/global/licenses/debian-12-bookworm"},
		Image:    vminstance.Image{OCI: "mirror.gcr.io/library/debian:12@sha256:d74df7598108a173a621eda4d58a2adbf850b2d9a9d81bc2cca1a6c21174a880", Architecture: "linux/amd64"},
	},
	{
		Project: "debian-cloud", Name: "debian-12-bookworm-arm64-v20251001", Family: "debian-12-arm64",
		Description:  "Debian, Debian GNU/Linux, 12 (bookworm), arm64 built on 20251001",
		Architecture: "ARM64", DiskSizeGb: 10,
		Licenses: []string{"projects/debian-cloud/global/licenses/debian-12-bookworm"},
		Image:    vminstance.Image{OCI: "mirror.gcr.io/library/debian:12@sha256:d74df7598108a173a621eda4d58a2adbf850b2d9a9d81bc2cca1a6c21174a880", Architecture: "linux/arm64"},
	},
	{
		Project: "ubuntu-os-cloud", Name: "ubuntu-2404-noble-amd64-v20251001", Family: "ubuntu-2404-lts-amd64",
		Description:  "Canonical, Ubuntu, 24.04 LTS, amd64 noble image built on 2025-10-01",
		Architecture: "X86_64", DiskSizeGb: 10,
		Licenses: []string{"projects/ubuntu-os-cloud/global/licenses/ubuntu-2404-lts-amd64"},
		Image:    vminstance.Image{OCI: "mirror.gcr.io/library/ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852", Architecture: "linux/amd64"},
	},
	{
		Project: "ubuntu-os-cloud", Name: "ubuntu-2404-noble-arm64-v20251001", Family: "ubuntu-2404-lts-arm64",
		Description:  "Canonical, Ubuntu, 24.04 LTS, arm64 noble image built on 2025-10-01",
		Architecture: "ARM64", DiskSizeGb: 10,
		Licenses: []string{"projects/ubuntu-os-cloud/global/licenses/ubuntu-2404-lts-arm64"},
		Image:    vminstance.Image{OCI: "mirror.gcr.io/library/ubuntu:24.04@sha256:fe81560990fae86dc4743202b2cb4d3df98d0bcf949173cecef15ce1127ef852", Architecture: "linux/arm64"},
	},
}

// gcpPublicImageCreation is the creation time every catalog image reports:
// the catalog is fixed, so the time is too.
const gcpPublicImageCreation = "2025-10-01T10:00:00.000-07:00"

func gcpImageLink(project, name string) string {
	return fmt.Sprintf("projects/%s/global/images/%s", project, name)
}

func gcpPublicImageJSON(img gcpPublicImage) map[string]any {
	link := gcpImageLink(img.Project, img.Name)
	sum := sha256.Sum256([]byte(link))
	return map[string]any{
		"kind":              "compute#image",
		"id":                fmt.Sprintf("%d", uint64(sum[0])<<56>>1|uint64(sum[1])<<48|uint64(sum[2])<<40|uint64(sum[3])<<32|uint64(sum[4])<<24|uint64(sum[5])<<16|uint64(sum[6])<<8|uint64(sum[7])),
		"creationTimestamp": gcpPublicImageCreation,
		"name":              img.Name,
		"description":       img.Description,
		"sourceType":        "RAW",
		"rawDisk":           map[string]any{"containerType": "TAR", "source": ""},
		"status":            "READY",
		"archiveSizeBytes":  "1073741824",
		"diskSizeGb":        fmt.Sprintf("%d", img.DiskSizeGb),
		"licenses":          gcpComputeLinks(img.Licenses),
		"family":            img.Family,
		"selfLink":          computeSelfLink(link),
		"labelFingerprint":  "42WmSpB8rSM=",
		"guestOsFeatures": []map[string]any{
			{"type": "UEFI_COMPATIBLE"}, {"type": "VIRTIO_SCSI_MULTIQUEUE"}, {"type": "GVNIC"},
		},
		"storageLocations": []string{"us", "eu", "asia"},
		"architecture":     img.Architecture,
	}
}

func gcpComputeLinks(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		out = append(out, computeSelfLink(p))
	}
	return out
}

func gcpPublicImageByName(project, name string) (gcpPublicImage, bool) {
	for _, img := range gcpPublicImages {
		if img.Project == project && img.Name == name {
			return img, true
		}
	}
	return gcpPublicImage{}, false
}

func gcpPublicImageByFamily(project, family string) (gcpPublicImage, bool) {
	for _, img := range gcpPublicImages {
		if img.Project == project && img.Family == family {
			return img, true
		}
	}
	return gcpPublicImage{}, false
}

// gcpImageResource is an image as images.get answers it, from the catalog or
// from the project's own images.
func gcpImageResource(project, name string) (map[string]any, bool) {
	if img, ok := gcpPublicImageByName(project, name); ok {
		return gcpPublicImageJSON(img), true
	}
	if gcpComputeImages == nil {
		return nil, false
	}
	return gcpComputeImages.Get(gcpImageLink(project, name))
}

// gcpImageFromFamily is the newest image of a family that is not deprecated,
// which is what images.getFromFamily answers.
func gcpImageFromFamily(project, family string) (map[string]any, bool) {
	if img, ok := gcpPublicImageByFamily(project, family); ok {
		return gcpPublicImageJSON(img), true
	}
	if gcpComputeImages == nil {
		return nil, false
	}
	candidates := gcpComputeImages.Filter(func(m map[string]any) bool {
		link, _ := m["selfLink"].(string)
		got, _ := m["family"].(string)
		_, deprecated := m["deprecated"]
		return strings.Contains(link, "/projects/"+project+"/global/images/") && got == family && !deprecated
	})
	if len(candidates) == 0 {
		return nil, false
	}
	sort.Slice(candidates, func(i, j int) bool {
		ci, _ := candidates[i]["creationTimestamp"].(string)
		cj, _ := candidates[j]["creationTimestamp"].(string)
		return ci > cj
	})
	return candidates[0], true
}

// gcpImageList lists a project's images: the catalog's for an image project,
// the project's own otherwise.
func gcpImageList(project string) []map[string]any {
	var items []map[string]any
	for _, img := range gcpPublicImages {
		if img.Project == project {
			items = append(items, gcpPublicImageJSON(img))
		}
	}
	if gcpComputeImages != nil {
		prefix := computeSelfLink(gcpImageLink(project, ""))
		items = append(items, gcpComputeImages.Filter(func(m map[string]any) bool {
			link, _ := m["selfLink"].(string)
			return strings.HasPrefix(link, prefix)
		})...)
	}
	sort.Slice(items, func(i, j int) bool {
		ni, _ := items[i]["name"].(string)
		nj, _ := items[j]["name"].(string)
		return ni < nj
	})
	return items
}

// gcpResolvedImage is what an instance boots from: the image resource and
// the OCI image it runs on.
type gcpResolvedImage struct {
	SelfLink string
	Image    vminstance.Image
}

// errComputeImageNotFound is wrapped by gcpResolveImage for a reference no
// image answers.
var errComputeImageNotFound = errors.New("the referenced image resource cannot be found")

// gcpResolveImage resolves an image reference however a client spells it —
// a full URL, `projects/P/global/images/NAME`, `projects/P/global/images/family/F`,
// the `projects/P/zones/-/imageFamilyViews/F` gcloud sends for a family, or
// `global/images/NAME` in the caller's project — to the image and the OCI
// image behind it.
func gcpResolveImage(project, ref string) (gcpResolvedImage, error) {
	path := gcpComputeReferencePath(ref)
	if strings.HasPrefix(path, "global/images/") {
		path = "projects/" + project + "/" + path
	}
	parts := strings.Split(path, "/")
	var resource map[string]any
	var ok bool
	switch {
	case len(parts) == 6 && parts[0] == "projects" && parts[2] == "global" && parts[3] == "images" && parts[4] == "family":
		resource, ok = gcpImageFromFamily(parts[1], parts[5])
	case len(parts) == 6 && parts[0] == "projects" && parts[2] == "zones" && parts[4] == "imageFamilyViews":
		resource, ok = gcpImageFromFamily(parts[1], parts[5])
	case len(parts) == 5 && parts[0] == "projects" && parts[2] == "global" && parts[3] == "images":
		resource, ok = gcpImageResource(parts[1], parts[4])
	default:
		return gcpResolvedImage{}, fmt.Errorf("image reference %q is not an image path: %w", ref, errComputeImageNotFound)
	}
	if !ok {
		return gcpResolvedImage{}, fmt.Errorf("image %s: %w", ref, errComputeImageNotFound)
	}
	return gcpImageBacking(resource, 0)
}

// gcpComputeReferencePath is the projects/... path of a resource reference
// however a client spells it: a relative path, a full URL under the API's
// /compute/v1/ prefix, or a full URL under an endpoint override that
// carries no such prefix, as gcloud sends them.
func gcpComputeReferencePath(ref string) string {
	ref = strings.TrimSpace(ref)
	if i := strings.Index(ref, "/projects/"); i >= 0 {
		return ref[i+1:]
	}
	return gcpComputeResourcePath(ref)
}

// gcpImageBacking is the OCI image behind an image resource: the catalog
// entry's, the reference its rawDisk.source names, the capture of its
// sourceDisk, or its sourceImage's.
func gcpImageBacking(resource map[string]any, depth int) (gcpResolvedImage, error) {
	link, _ := resource["selfLink"].(string)
	path := gcpComputeResourcePath(link)
	parts := strings.Split(path, "/")
	if len(parts) == 5 {
		if img, ok := gcpPublicImageByName(parts[1], parts[4]); ok {
			return gcpResolvedImage{SelfLink: path, Image: img.Image}, nil
		}
	}
	platform := gcpImagePlatform(resource)
	if raw, _ := resource["rawDisk"].(map[string]any); raw != nil {
		if source, _ := raw["source"].(string); source != "" {
			return gcpResolvedImage{SelfLink: path, Image: vminstance.Image{OCI: source, Architecture: platform}}, nil
		}
	}
	if sourceDisk, _ := resource["sourceDisk"].(string); sourceDisk != "" {
		return gcpResolvedImage{SelfLink: path, Image: vminstance.Image{OCI: gcpCapturedImageReference(path), Architecture: platform}}, nil
	}
	if sourceImage, _ := resource["sourceImage"].(string); sourceImage != "" {
		if depth > 8 {
			return gcpResolvedImage{}, fmt.Errorf("image %s: sourceImage chain is circular", path)
		}
		resolved, err := gcpResolveImage(parts[1], sourceImage)
		if err != nil {
			return gcpResolvedImage{}, err
		}
		resolved.SelfLink = path
		return resolved, nil
	}
	return gcpResolvedImage{}, fmt.Errorf("image %s names no rawDisk.source, sourceDisk or sourceImage", path)
}

// gcpImagePlatform is the OCI platform of an image resource's architecture.
func gcpImagePlatform(resource map[string]any) string {
	if arch, _ := resource["architecture"].(string); strings.EqualFold(arch, "ARM64") {
		return "linux/arm64"
	}
	return "linux/amd64"
}

// gcpCapturedImageReference is the engine image an image captured from a
// disk is committed as: derived from the image's path, so a restart finds it
// without storing anything the API does not report.
func gcpCapturedImageReference(imagePath string) string {
	sum := sha256.Sum256([]byte(imagePath))
	return "sockerless-sim-gcp-image:" + hex.EncodeToString(sum[:8])
}

// gcpInstanceBootImage is the image an instance's boot disk was initialized
// from: its initializeParams.sourceImage, or the sourceImage of the disk it
// names.
func gcpInstanceBootImage(inst ComputeInstance) (gcpResolvedImage, error) {
	project := gcpProjectFromSelfLink(inst.SelfLink)
	for _, disk := range inst.Disks {
		if !disk.Boot && len(inst.Disks) > 1 {
			continue
		}
		if source, _ := disk.InitializeParams["sourceImage"].(string); source != "" {
			return gcpResolveImage(project, source)
		}
		if disk.Source != "" && gcpComputeZoneDisks != nil {
			if d, ok := gcpComputeZoneDisks.Get(gcpComputeResourcePath(disk.Source)); ok && d.SourceImage != "" {
				return gcpResolveImage(project, d.SourceImage)
			}
		}
	}
	return gcpResolvedImage{}, errors.New("the instance's boot disk names no source image")
}

// gcpRefuseUnbootableImage refuses an insert whose boot disk names an image
// nothing can boot, the way Compute Engine refuses the field.
func gcpRefuseUnbootableImage(inst ComputeInstance) error {
	for i, disk := range inst.Disks {
		source, _ := disk.InitializeParams["sourceImage"].(string)
		if source == "" {
			continue
		}
		if _, err := gcpResolveImage(gcpProjectFromSelfLink(inst.SelfLink), source); err != nil {
			return computeFieldRefusal{reason: "invalid", message: fmt.Sprintf(
				"Invalid value for field 'resource.disks[%d].initializeParams.sourceImage': '%s'. The referenced image resource cannot be found.", i, source)}
		}
	}
	return nil
}

// gcpRecordInstanceDisks creates the disk resources an insert's
// initializeParams describe, as Compute Engine does: named after the device
// (or the diskName asked for), sized and typed as asked, initialized from
// the resolved image and in use by the instance.
func gcpRecordInstanceDisks(project, zone string, inst *ComputeInstance) {
	if gcpComputeZoneDisks == nil {
		return
	}
	for i := range inst.Disks {
		disk := &inst.Disks[i]
		if disk.InitializeParams == nil {
			continue
		}
		if name, _ := disk.InitializeParams["diskName"].(string); name != "" {
			disk.Source = fmt.Sprintf("projects/%s/zones/%s/disks/%s", project, zone, name)
		}
		path := gcpComputeResourcePath(disk.Source)
		if _, exists := gcpComputeZoneDisks.Get(path); exists {
			continue
		}
		size := "10"
		switch v := disk.InitializeParams["diskSizeGb"].(type) {
		case string:
			if v != "" {
				size = v
			}
		case float64:
			size = strconv.FormatInt(int64(v), 10)
		}
		diskType, _ := disk.InitializeParams["diskType"].(string)
		d := ComputeDisk{
			Kind:              "compute#disk",
			Id:                computeNumericID(),
			Name:              path[strings.LastIndex(path, "/")+1:],
			SelfLink:          path,
			Zone:              fmt.Sprintf("projects/%s/zones/%s", project, zone),
			Status:            "READY",
			SizeGb:            gcpInt64(size),
			Type:              gcpCanonicalComputeRef(diskType, project, "zones/"+zone, "diskTypes", "pd-standard"),
			CreationTimestamp: time.Now().UTC().Format(time.RFC3339),
			Users:             []string{computeSelfLink(inst.SelfLink)},
			LabelFingerprint:  computeFingerprint(),
		}
		if source, _ := disk.InitializeParams["sourceImage"].(string); source != "" {
			if resolved, err := gcpResolveImage(project, source); err == nil {
				d.SourceImage = computeSelfLink(resolved.SelfLink)
			}
		}
		gcpComputeZoneDisks.Put(path, d)
	}
}

// gcpDeleteInstanceDisks removes the disks that go with the instance: the
// ones attached with autoDelete.
func gcpDeleteInstanceDisks(inst ComputeInstance) {
	if gcpComputeZoneDisks == nil {
		return
	}
	for _, disk := range inst.Disks {
		if disk.AutoDelete && disk.Source != "" {
			gcpComputeZoneDisks.Delete(gcpComputeResourcePath(disk.Source))
		}
	}
}

// gcpInstanceBootDiskLink is the disk resource an instance boots from.
func gcpInstanceBootDiskLink(inst ComputeInstance) string {
	for _, disk := range inst.Disks {
		if disk.Boot || len(inst.Disks) == 1 {
			return gcpComputeResourcePath(disk.Source)
		}
	}
	return ""
}

// gcpInstanceOfDisk is the instance whose boot disk is the named disk.
func gcpInstanceOfDisk(diskPath string) (ComputeInstance, bool) {
	if gcpInstances == nil {
		return ComputeInstance{}, false
	}
	for _, inst := range gcpInstances.List() {
		if gcpInstanceBootDiskLink(inst) == diskPath {
			return inst, true
		}
	}
	return ComputeInstance{}, false
}

// registerComputeImages mounts the image collection: the catalog read paths
// the generic registrar leaves to this file, and the insert and delete that
// touch engine images.
func registerComputeImages(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/global/images"
	notFound := func(w http.ResponseWriter, path string) {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "The resource '%s' was not found", path)
	}

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		items := gcpImageList(project)
		listed, ok := gcpApplyComputeListParams(w, r, items)
		if !ok {
			return
		}
		page, next, ok := paginateListCompute(w, r, listed, gcpAsc(gcpField("name")), gcpAsc(gcpField("selfLink")))
		if !ok {
			return
		}
		if page == nil {
			page = []map[string]any{}
		}
		resp := map[string]any{
			"kind":     "compute#imageList",
			"id":       gcpImageLink(project, ""),
			"items":    page,
			"selfLink": computeSelfLink(gcpImageLink(project, "")),
		}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET "+base+"/{image}", func(w http.ResponseWriter, r *http.Request) {
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "image")
		resource, ok := gcpImageResource(project, name)
		if !ok {
			notFound(w, gcpImageLink(project, name))
			return
		}
		sim.WriteJSON(w, http.StatusOK, resource)
	})

	// "images/family/{family}" and "images/{resource}/getIamPolicy" share a path
	// shape, so one handler serves both. Compute Engine's family template comes
	// first, so a family named "getIamPolicy" is a family lookup.
	srv.HandleFunc("GET "+base+"/{first}/{second}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		first, second := sim.PathParam(r, "first"), sim.PathParam(r, "second")
		if first == "family" {
			resource, ok := gcpImageFromFamily(project, second)
			if !ok {
				notFound(w, gcpImageLink(project, "family/"+second))
				return
			}
			sim.WriteJSON(w, http.StatusOK, resource)
			return
		}
		if second != "getIamPolicy" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no such image method %q", second)
			return
		}
		handleResourceIAM(w, r, gcpResourcePolicies,
			"compute/"+computeGlobalLink(project, "images", first), "getIamPolicy")
	})
	for verb, method := range map[string]string{"setIamPolicy": "POST", "testIamPermissions": "POST"} {
		verb := verb
		srv.HandleFunc(method+" "+base+"/{resource}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies,
				"compute/"+computeGlobalLink(sim.PathParam(r, "project"), "images", sim.PathParam(r, "resource")), verb)
		})
	}

	// images.insert creates an image from one source: rawDisk.source names
	// the OCI image an instance runs on, sourceImage another image, and
	// sourceDisk a stopped instance's boot disk, whose contents the image
	// captures. Compute Engine refuses the disk of a running instance unless
	// forceCreate is set.
	srv.HandleFunc("POST "+base, func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		name, _ := body["name"].(string)
		if computeRefuseResourceName(w, name) {
			return
		}
		key := gcpImageLink(project, name)
		if _, exists := gcpImageResource(project, name); computeConflict(w, exists, "images", name) {
			return
		}
		body["kind"] = "compute#image"
		body["id"] = computeNumericID()
		body["selfLink"] = computeSelfLink(key)
		body["creationTimestamp"] = time.Now().UTC().Format(time.RFC3339)
		body["labelFingerprint"] = computeFingerprint()
		body["status"] = "READY"
		if _, ok := body["sourceType"]; !ok {
			body["sourceType"] = "RAW"
		}
		if arch, _ := body["architecture"].(string); arch == "" {
			body["architecture"] = "X86_64"
		}
		raw, _ := body["rawDisk"].(map[string]any)
		rawSource, _ := raw["source"].(string)
		sourceDisk, _ := body["sourceDisk"].(string)
		sourceImage, _ := body["sourceImage"].(string)
		switch {
		case rawSource != "":
			if _, err := reference.ParseNormalizedNamed(rawSource); err != nil {
				computeFieldError(w, "invalid", fmt.Sprintf("Invalid value for field 'resource.rawDisk.source': '%s'. %v.", rawSource, err))
				return
			}
		case sourceImage != "":
			resolved, err := gcpResolveImage(project, sourceImage)
			if err != nil {
				computeFieldError(w, "invalid", fmt.Sprintf("Invalid value for field 'resource.sourceImage': '%s'. %s.", sourceImage, errComputeImageNotFound))
				return
			}
			body["sourceImage"] = computeSelfLink(resolved.SelfLink)
			if resolved.Image.Architecture == "linux/arm64" {
				body["architecture"] = "ARM64"
			}
		case sourceDisk != "":
			diskPath := gcpComputeReferencePath(sourceDisk)
			if !strings.HasPrefix(diskPath, "projects/") {
				diskPath = "projects/" + project + "/" + diskPath
			}
			inst, ok := gcpInstanceOfDisk(diskPath)
			if !ok {
				computeFieldError(w, "invalid", fmt.Sprintf("Invalid value for field 'resource.sourceDisk': '%s'. The referenced disk resource cannot be found.", sourceDisk))
				return
			}
			if inst.Status != ComputeInstanceTerminated && inst.Status != ComputeInstanceSuspended && r.URL.Query().Get("forceCreate") != "true" {
				computeFieldError(w, "resourceInUseByAnotherResource", fmt.Sprintf(
					"The disk resource '%s' is already being used by '%s'", computeSelfLink(diskPath), computeSelfLink(inst.SelfLink)))
				return
			}
			source, err := gcpInstanceBootImage(inst)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "capture disk %s: %v", diskPath, err)
				return
			}
			if err := gcpCaptureInstanceDisk(r.Context(), inst, gcpCapturedImageReference(key)); err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "capture disk %s: %v", diskPath, err)
				return
			}
			body["sourceDisk"] = computeSelfLink(diskPath)
			body["sourceDiskId"] = computeNumericID()
			body["architecture"] = "X86_64"
			if source.Image.Architecture == "linux/arm64" {
				body["architecture"] = "ARM64"
			}
			if _, ok := body["diskSizeGb"]; !ok {
				body["diskSizeGb"] = "10"
			}
		default:
			computeFieldError(w, "invalid", "Invalid value for field 'resource': ''. Must specify one of 'rawDisk.source', 'sourceDisk' or 'sourceImage'.")
			return
		}
		gcpComputeImages.Put(key, body)
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "insert"))
	})

	srv.HandleFunc("DELETE "+base+"/{image}", func(w http.ResponseWriter, r *http.Request) {
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "image")
		key := gcpImageLink(project, name)
		resource, ok := gcpComputeImages.Get(key)
		if !ok {
			notFound(w, key)
			return
		}
		gcpComputeImages.Delete(key)
		if sourceDisk, _ := resource["sourceDisk"].(string); sourceDisk != "" {
			gcpRemoveCapturedImage(r.Context(), gcpCapturedImageReference(key))
		}
		sim.WriteJSON(w, http.StatusOK, computeGlobalOp(project, key, "delete"))
	})
}

// gcpRemoveCapturedImage removes a captured image from the engine; an engine
// that never held it has nothing to remove.
func gcpRemoveCapturedImage(ctx context.Context, reference string) {
	cli := sim.DockerClient()
	if cli == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := cli.ImageRemove(ctx, reference, client.ImageRemoveOptions{Force: true}); err != nil && !cerrdefs.IsNotFound(err) {
		log.Printf("compute: remove the captured image %s: %v", reference, err)
	}
}
