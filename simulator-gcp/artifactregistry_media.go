package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerARArtifactMedia(srv *sim.Server, repos sim.Store[Repository]) {
	// :create rides the media /upload/v1 prefix and answers {operation:
	// Operation}; :import is a control-plane POST answering an Operation.
	artifactCreate := func(kind string) http.HandlerFunc {
		finish := func(name string, request, data []byte, contentType string) (any, error) {
			repo, ok := repos.Get(name)
			if !ok {
				return nil, apiRefuse(http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			}
			return arFinishArtifactUpload(repo, kind, request, data, contentType)
		}
		return func(w http.ResponseWriter, r *http.Request) {
			name, ok := arRepoExists(repos, w, r)
			if !ok {
				return
			}
			serveMediaUpload(w, r, name, kind+":create", kind == "genericArtifacts", finish)
		}
	}

	// The document gives each media method two paths — the /upload/v1 media
	// path that carries the bytes and the plain /v1 one — and the service
	// answers both.
	for _, kind := range []string{"aptArtifacts", "yumArtifacts", "googetArtifacts", "goModules", "genericArtifacts", "kfpArtifacts"} {
		create := artifactCreate(kind)
		srv.HandleFunc("POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/"+kind+":create", create)
		srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/"+kind+":create", create)
	}

	// Of the :create methods, only genericArtifacts.upload declares the
	// resumable protocol, whose chunks and cancellation address the session
	// URI on either media path.
	genericCreate := artifactCreate("genericArtifacts")

	srv.HandleFunc("PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create", genericCreate)

	srv.HandleFunc("DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create", genericCreate)

	srv.HandleFunc("POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create", genericCreate)

	srv.HandleFunc("PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create", genericCreate)

	srv.HandleFunc("DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/genericArtifacts:create", genericCreate)

	registerARArtifactImport := func(kind string) {
		srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/"+kind+":import", func(w http.ResponseWriter, r *http.Request) {
			name, ok := arRepoExists(repos, w, r)
			if !ok {
				return
			}
			repo, _ := repos.Get(name)
			arHandleArtifactImport(w, r, repo, kind)
		})
	}

	for _, kind := range []string{"aptArtifacts", "yumArtifacts", "googetArtifacts"} {
		registerARArtifactImport(kind)
	}
}
