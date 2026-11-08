package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A client deploying from source asks Cloud Run where to upload it; going the
// other way, it asks Cloud Run to export a revision's or execution's image and
// polls the export's status. ServeMux cannot spell a colon verb after a
// wildcard, so each mounts on the segment before the colon and dispatches on
// the verb.

func registerCloudRunExport(srv *sim.Server) {
	// The exports a client has asked for, keyed by the operation id handed back.
	exports := sim.MakeStore[map[string]any](srv.DB(), "cloudrun_image_exports")

	cloudRunExports = exports

	// The export status hangs off the resource it ran against; the last segment
	// is the operation id with the verb attached.
	exportStatus := func(w http.ResponseWriter, r *http.Request) {
		operationID, verb, _ := strings.Cut(sim.PathParam(r, "operationId"), ":")
		if verb != "exportStatus" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no Cloud Run method named %q", verb)
			return
		}
		held, ok := exports.Get(operationID)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"no image export with operation id %q", operationID)
			return
		}
		// The export runs within the request that starts it, so its status is
		// final when first read.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"operationId":         operationID,
			"operationState":      "FINISHED",
			"imageExportStatuses": []any{held["status"]},
		})
	}
	srv.HandleFunc("GET /v2/projects/{projectsId}/locations/{locationsId}/services/{servicesId}/revisions/{revisionsId}/{operationId}", exportStatus)
	srv.HandleFunc("GET /v2/projects/{projectsId}/locations/{locationsId}/jobs/{jobsId}/executions/{executionsId}/{operationId}", exportStatus)

	// uploadSource and the resource-level metadata exports are colon verbs on
	// segments no other service registers a bare route for, so Cloud Run owns
	// these two patterns outright.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}",
		func(w http.ResponseWriter, r *http.Request) {
			_, verb, _ := strings.Cut(sim.PathParam(r, "location"), ":")
			if !cloudRunLocationVerbHandled(w, r, "", verb) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no Cloud Run location method named %q", verb)
			}
		})
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/{resourceId}",
		func(w http.ResponseWriter, r *http.Request) {
			resource, verb, _ := strings.Cut(sim.PathParam(r, "resourceId"), ":")
			name := fmt.Sprintf("projects/%s/locations/%s/%s",
				sim.PathParam(r, "project"), sim.PathParam(r, "location"), resource)
			if !cloudRunLocationExportHandled(w, r, name, verb) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no Cloud Run method named %q", verb)
			}
		})

	// The media-upload spelling of uploadSource rides its own absolute path,
	// which is where a client sends the archive itself. The verb it answers is
	// the one already served on the API path.
	srv.HandleFunc("POST /upload/v2/projects/{project}/locations/{location}",
		func(w http.ResponseWriter, r *http.Request) {
			_, verb, _ := strings.Cut(sim.PathParam(r, "location"), ":")
			if !cloudRunLocationVerbHandled(w, r, "", verb) {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"no Cloud Run upload method named %q", verb)
			}
		})
}

// cloudRunExports records the image exports callers asked for, keyed by
// operation id, so a status read answers only for a requested export.
var cloudRunExports sim.Store[map[string]any]

// cloudRunLocationExportHandled serves the Cloud Run custom methods on the /v2
// location path, which Cloud Logging and Cloud Functions own and offer to Cloud
// Run first.
func cloudRunLocationExportHandled(w http.ResponseWriter, r *http.Request, name, verb string) bool {
	switch verb {
	case "exportProjectMetadata", "exportMetadata", "exportImageMetadata":
		sim.WriteJSON(w, http.StatusOK, cloudRunMetadata(name))
		return true
	}
	return false
}

// cloudRunExportImage copies the image the revision, execution or service
// runs into the Artifact Registry repository the request names, and records
// the outcome under the operation id the caller polls with.
func cloudRunExportImage(w http.ResponseWriter, r *http.Request, source string) {
	var req struct {
		DestinationRepo string `json:"destinationRepo"`
	}
	if err := sim.ReadJSON(r, &req); err != nil || req.DestinationRepo == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"an image export needs the destinationRepo it is going to")
		return
	}
	image, ok := cloudRunSourceImage(source)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s has no container image to export", source)
		return
	}
	operationID := "export-" + sim.NewUUID()[:8]
	status := map[string]any{"tag": req.DestinationRepo, "exportJobState": "FINISHED"}
	digest, err := cloudRunCopyImage(r.Context(), image, req.DestinationRepo)
	if err != nil {
		status["status"] = map[string]any{"code": 9, "message": err.Error()}
	} else {
		status["exportedImageDigest"] = digest
		status["status"] = map[string]any{"code": 0}
	}
	cloudRunExports.Put(operationID, map[string]any{
		"operationId": operationID,
		"source":      source,
		"image":       image,
		"destination": req.DestinationRepo,
		"status":      status,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operationId": operationID})
}

// cloudRunSourceImage is the first container image of a revision, of an
// execution's task template, or of a service's latest ready revision.
func cloudRunSourceImage(name string) (string, bool) {
	first := func(containers []Container) (string, bool) {
		if len(containers) == 0 || containers[0].Image == "" {
			return "", false
		}
		return containers[0].Image, true
	}
	switch {
	case strings.Contains(name, "/revisions/"):
		if rev, ok := crv2Revisions.Get(name); ok {
			return first(rev.Containers)
		}
	case strings.Contains(name, "/executions/"):
		if exec, ok := crjExecutions.Get(name); ok && exec.Template != nil {
			return first(exec.Template.Containers)
		}
	case strings.Contains(name, "/services/"):
		if svc, ok := crv2Services.Get(name); ok {
			if rev, ok := crv2Revisions.Get(svc.LatestReadyRevision); ok {
				return first(rev.Containers)
			}
			if svc.Template != nil {
				return first(svc.Template.Containers)
			}
		}
	}
	return "", false
}

// cloudRunCopyImage reads image — from this simulator's Artifact Registry
// when it names a LOCATION-docker.pkg.dev host, otherwise over the Registry v2
// API — and stores it in the destination repository, which must exist.
func cloudRunCopyImage(ctx context.Context, image, destination string) (string, error) {
	destHost, destPath, _ := strings.Cut(destination, "/")
	if !strings.HasSuffix(destHost, "-docker.pkg.dev") {
		return "", fmt.Errorf("the destination %s is not an Artifact Registry Docker repository", destination)
	}
	destRepo, destTag := cloudRunSplitImage(destPath)
	if _, _, _, _, ok := artifactRegistryImageParts(destRepo); !ok {
		return "", fmt.Errorf("the Artifact Registry repository of %s does not exist", destination)
	}
	host, path, _ := strings.Cut(image, "/")
	repo, ref := cloudRunSplitImage(path)
	var pull sim.OCIPull
	var err error
	if strings.HasSuffix(host, "-docker.pkg.dev") {
		pull, err = arRegistry.ExportImage("", repo, ref, 0)
	} else {
		if host == "docker.io" || host == "index.docker.io" {
			host = "registry-1.docker.io"
		}
		pullCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		pull, err = sim.PullOCIImage(pullCtx, &http.Client{Timeout: 10 * time.Minute}, "https://"+host, repo, ref, "", "", 0)
	}
	if err != nil {
		return "", fmt.Errorf("could not read %s: %w", image, err)
	}
	var tags []string
	if destTag != "" && !strings.HasPrefix(destTag, "sha256:") {
		tags = []string{destTag}
	}
	arRegistry.ImportImage("", destRepo, pull, tags)
	return pull.Root.Digest, nil
}

// cloudRunSplitImage splits PATH:TAG or PATH@DIGEST; a bare path is latest.
func cloudRunSplitImage(path string) (repo, ref string) {
	if at := strings.Index(path, "@"); at >= 0 {
		return path[:at], path[at+1:]
	}
	if colon := strings.LastIndex(path, ":"); colon >= 0 && !strings.Contains(path[colon:], "/") {
		return path[:colon], path[colon+1:]
	}
	return path, "latest"
}

// cloudRunMetadata renders a resource's metadata export: its definition
// serialized as a string, which a caller writes to a file.
func cloudRunMetadata(name string) map[string]any {
	definition, err := json.Marshal(map[string]any{"name": name})
	if err != nil {
		// A name is always marshalable; nothing here can fail.
		definition = []byte("{}")
	}
	return map[string]any{"metadata": string(definition)}
}
