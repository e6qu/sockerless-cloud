package main

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A project's enrolment in Compute Engine's preview features. The simulator
// does not vendor Google's feature catalogue, so the output-only description
// stays absent; `activationStatus` and the rollout operation are the caller's,
// written by update and returned by reads. The list holds the features this
// project has set, and any other feature is not found.

var gcpComputePreviewFeatures sim.Store[map[string]any]

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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
		// A request writes only these two members; the rest is Google's account of
		// the feature.
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
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
		// PreviewFeatureList declares no kind.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"id":       "projects/" + sim.PathParam(r, "project") + "/global/previewFeatures",
			"selfLink": computeSelfLink(prefix),
			"items":    items,
		})
	})
}
