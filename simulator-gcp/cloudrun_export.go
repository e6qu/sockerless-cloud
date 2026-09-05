package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Run's source uploads and image exports.
//
// A client that deploys from source asks Cloud Run where to put it, uploads
// there, and deploys from that location. Going the other way, it asks Cloud Run
// to export a service's image to a repository it owns and then polls for how
// that went. Both are addressed with colon verbs, which Go's router cannot
// spell — a wildcard segment has to end at its brace — so each is mounted on
// the segment before the colon and dispatched on the verb.

// registerCloudRunExport mounts them.
func registerCloudRunExport(srv *sim.Server) {
	// The exports a client has asked for, keyed by the operation id handed back.
	exports := sim.MakeStore[map[string]any](srv.DB(), "cloudrun_image_exports")

	locationName := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/locations/%s",
			sim.PathParam(r, "projectsId"), sim.PathParam(r, "locationsId"))
	}

	cloudRunExports = exports

	// How an export went, asked for through the resource it ran against. The
	// operation id is the last segment, carrying the verb with it.
	exportStatus := func(w http.ResponseWriter, r *http.Request) {
		operationID, verb, _ := strings.Cut(sim.PathParam(r, "operationId"), ":")
		_ = locationName
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
		destination, _ := held["destination"].(string)
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"operationId": operationID,
			// Both enums spell a finished job FINISHED. The
			// *_STATE_SUCCEEDED spellings are values neither declares.
			"operationState": "FINISHED",
			"imageExportStatuses": []any{map[string]any{
				"tag":            destination,
				"exportJobState": "FINISHED",
			}},
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

// cloudRunExports records the image exports a caller has asked for, keyed by
// the operation id handed back — a status read answers from what was asked for
// rather than inventing an export nobody requested.
var cloudRunExports sim.Store[map[string]any]

// cloudRunLocationExportHandled answers the Cloud Run custom methods that share
// the /v2 location path with Cloud Logging and Cloud Functions. Those two own
// the routes; this is the fan-in they offer Cloud Run before falling through to
// their own handling, which is how one path serves three services.
func cloudRunLocationExportHandled(w http.ResponseWriter, r *http.Request, name, verb string) bool {
	switch verb {
	case "exportProjectMetadata", "exportMetadata", "exportImageMetadata":
		sim.WriteJSON(w, http.StatusOK, cloudRunMetadata(name))
		return true
	}
	return false
}

// cloudRunExportImage starts an image export and hands back the operation id
// the caller polls with.
func cloudRunExportImage(w http.ResponseWriter, r *http.Request, source string) {
	var req struct {
		DestinationRepo string `json:"destinationRepo"`
	}
	if err := sim.ReadJSON(r, &req); err != nil || req.DestinationRepo == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"an image export needs the destinationRepo it is going to")
		return
	}
	operationID := "export-" + generateUUID()[:8]
	cloudRunExports.Put(operationID, map[string]any{
		"operationId": operationID,
		"source":      source,
		"destination": req.DestinationRepo,
	})
	sim.WriteJSON(w, http.StatusOK, map[string]any{"operationId": operationID})
}

// cloudRunMetadata renders the metadata export for a resource. The member is a
// string, because what Cloud Run hands back is the resource's definition
// serialized — a caller writes it to a file — rather than a structure to read
// members off.
func cloudRunMetadata(name string) map[string]any {
	definition, err := json.Marshal(map[string]any{"name": name})
	if err != nil {
		// A name is always marshalable; nothing here can fail.
		definition = []byte("{}")
	}
	return map[string]any{"metadata": string(definition)}
}
