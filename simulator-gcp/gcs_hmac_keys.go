package main

import (
	"encoding/base64"
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerGCSHmacKeys(srv *sim.Server) {
	srv.HandleFunc("GET /storage/v1/projects/{projectId}/serviceAccount", func(w http.ResponseWriter, r *http.Request) {
		project, ok := gcsResolveProject(w, sim.PathParam(r, "projectId"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":          "storage#serviceAccount",
			"email_address": gcsServiceAgentEmail(strings.TrimPrefix(project.Name, "projects/")),
		})
	})

	srv.HandleFunc("GET /storage/v1/projects/{projectId}/hmacKeys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "projectId")
		saFilter := r.URL.Query().Get("serviceAccountEmail")
		showDeleted := r.URL.Query().Get("showDeletedKeys") == "true"
		items := gcsHmacKeys.Filter(func(k GCSHmacKey) bool {
			if k.ProjectID != project {
				return false
			}
			if saFilter != "" && k.ServiceAccountEmail != saFilter {
				return false
			}
			if k.State == "DELETED" && !showDeleted {
				return false
			}
			return true
		})
		gcsWriteMapList(w, r, items, "storage#hmacKeysMetadata", func(v GCSHmacKey) string { return v.AccessID })
	})

	srv.HandleFunc("GET /storage/v1/projects/{projectId}/hmacKeys/{accessId}", func(w http.ResponseWriter, r *http.Request) {
		project, accessID := sim.PathParam(r, "projectId"), sim.PathParam(r, "accessId")
		k, ok := gcsHmacKeys.Get(project + "\x00" + accessID)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "HMAC key %q not found", accessID)
			return
		}
		sim.WriteJSON(w, http.StatusOK, gcsStructToMap(k))
	})

	srv.HandleFunc("POST /storage/v1/projects/{projectId}/hmacKeys", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "projectId")
		sa := r.URL.Query().Get("serviceAccountEmail")
		if sa == "" {
			GCPError(w, http.StatusBadRequest, "serviceAccountEmail is required", "INVALID_ARGUMENT")
			return
		}
		accessID := "GOOG" + strings.ToUpper(gcsRandHex(12))
		now := gcsTimestamp()
		k := GCSHmacKey{
			Kind:                "storage#hmacKeyMetadata",
			ID:                  project + "/" + accessID,
			AccessID:            accessID,
			ProjectID:           project,
			ServiceAccountEmail: sa,
			State:               "ACTIVE",
			TimeCreated:         now,
			Updated:             now,
			SelfLink:            gcpSelfLink(r, "/storage/v1/projects/"+project+"/hmacKeys/"+accessID),
			Etag:                "CAE=",
			secret:              base64.StdEncoding.EncodeToString([]byte(gcsRandHex(20))),
		}
		gcsHmacKeys.Put(project+"\x00"+accessID, k)
		// Create returns storage#hmacKey: { kind, metadata, secret }.
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind":     "storage#hmacKey",
			"metadata": gcsStructToMap(k),
			"secret":   k.secret,
		})
	})

	srv.HandleFunc("PUT /storage/v1/projects/{projectId}/hmacKeys/{accessId}", func(w http.ResponseWriter, r *http.Request) {
		project, accessID := sim.PathParam(r, "projectId"), sim.PathParam(r, "accessId")
		k, ok := gcsHmacKeys.Get(project + "\x00" + accessID)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "HMAC key %q not found", accessID)
			return
		}
		var in GCSHmacKey
		if err := sim.ReadJSON(r, &in); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if in.State != "" {
			k.State = in.State
		}
		k.Updated = gcsTimestamp()
		gcsHmacKeys.Put(project+"\x00"+accessID, k)
		sim.WriteJSON(w, http.StatusOK, gcsStructToMap(k))
	})

	srv.HandleFunc("DELETE /storage/v1/projects/{projectId}/hmacKeys/{accessId}", func(w http.ResponseWriter, r *http.Request) {
		project, accessID := sim.PathParam(r, "projectId"), sim.PathParam(r, "accessId")
		k, ok := gcsHmacKeys.Get(project + "\x00" + accessID)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "HMAC key %q not found", accessID)
			return
		}
		// Real GCS only allows deleting an INACTIVE key; it then transitions
		// to DELETED rather than vanishing.
		k.State = "DELETED"
		k.Updated = gcsTimestamp()
		gcsHmacKeys.Put(project+"\x00"+accessID, k)
		w.WriteHeader(http.StatusNoContent)
	})
}
