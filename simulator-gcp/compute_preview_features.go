package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// A project's enrolment in Compute Engine's preview features.
//
// Which features exist is Google's to say, and this simulator does not vendor
// that catalogue — so a feature's description, which the document marks output
// only, is left out rather than invented. What a project has done about a
// feature is a different thing and is the caller's own: `activationStatus`
// says whether the project turned it on, and the rollout operation says how it
// was rolled out. Both are written by the update, and both are what the read
// hands back.
//
// A feature this project has said nothing about is not found, which is what
// every other collection here answers for a resource it does not hold. It is
// also why the listing is the features this project has spoken for rather than
// every feature Google offers: the simulator can report what it holds, and
// reporting a catalogue it does not have would be inventing one.

var gcpComputePreviewFeatures sim.Store[map[string]any]

// registerComputePreviewFeatures mounts the update, the read and the listing.
func registerComputePreviewFeatures(srv *sim.Server) {
	gcpComputePreviewFeatures = sim.MakeStore[map[string]any](srv.DB(), "compute_preview_features")

	const base = "/compute/v1/projects/{project}/global/previewFeatures"
	key := func(r *http.Request) string {
		return "projects/" + sim.PathParam(r, "project") +
			"/global/previewFeatures/" + sim.PathParam(r, "previewFeature")
	}

	srv.HandleFunc("PATCH "+base+"/{previewFeature}", func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := sim.ReadJSON(r, &request); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := sim.PathParam(r, "previewFeature")
		held, existing := gcpComputePreviewFeatures.Get(key(r))
		if !existing {
			held = map[string]any{
				"kind":              "compute#previewFeature",
				"name":              name,
				"selfLink":          computeSelfLink(key(r)),
				"creationTimestamp": time.Now().UTC().Format(time.RFC3339),
			}
		}
		// Only the two the caller owns. The rest of the resource is Google's
		// account of the feature, and a request cannot write it.
		for _, member := range []string{"activationStatus", "rolloutOperation"} {
			if value, given := request[member]; given {
				held[member] = value
			}
		}
		gcpComputePreviewFeatures.Put(key(r), held)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(sim.PathParam(r, "project"),
			"global", computeSelfLink(key(r)), "update"))
	})

	srv.HandleFunc("GET "+base+"/{previewFeature}", func(w http.ResponseWriter, r *http.Request) {
		held, ok := gcpComputePreviewFeatures.Get(key(r))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"preview feature %q not found", sim.PathParam(r, "previewFeature"))
			return
		}
		sim.WriteJSON(w, http.StatusOK, held)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		prefix := "projects/" + sim.PathParam(r, "project") + "/global/previewFeatures/"
		held := gcpComputePreviewFeatures.Filter(func(feature map[string]any) bool {
			link, _ := feature["selfLink"].(string)
			return strings.Contains(link, prefix)
		})
		sort.Slice(held, func(i, j int) bool {
			left, _ := held[i]["name"].(string)
			right, _ := held[j]["name"].(string)
			return left < right
		})
		items := []any{}
		for _, feature := range held {
			items = append(items, feature)
		}
		// No kind: PreviewFeatureList is one of the few Compute Engine list
		// shapes the document declares without one.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":       "projects/" + sim.PathParam(r, "project") + "/global/previewFeatures",
			"selfLink": computeSelfLink(prefix),
			"items":    items,
		})
	})
}
