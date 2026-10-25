package main

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerARPackages mounts a repository's packages, versions and tags.
func registerARPackages(srv *sim.Server, repos sim.Store[Repository], dockerImages sim.Store[DockerImage], packages sim.Store[ARPackage], tags sim.Store[ARTag], versions sim.Store[ARVersion]) {
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		prefix := repo + "/packages/"
		gcpWriteList(w, r, packages, prefix, "packages", func(v ARPackage) string { return v.Name })
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r)
		pkg, ok := packages.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "package %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, pkg)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r)
		pkg, ok := packages.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "package %q not found", name)
			return
		}
		var patch ARPackage
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Annotations != nil {
			pkg.Annotations = patch.Annotations
		}
		pkg.UpdateTime = nowTimestamp()
		packages.Put(name, pkg)
		sim.WriteJSON(w, http.StatusOK, pkg)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r)
		_, ok = packages.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "package %q not found", name)
			return
		}
		packages.Delete(name)
		arDeleteVersionFiles(name)
		for _, v := range versions.Filter(func(v ARVersion) bool { return strings.HasPrefix(v.Name, name+"/versions/") }) {
			versions.Delete(v.Name)
		}
		for _, t := range tags.Filter(func(t ARTag) bool { return strings.HasPrefix(t.Name, name+"/tags/") }) {
			tags.Delete(t.Name)
		}
		imagePath := sim.PathParam(r, "pkg")
		for _, img := range dockerImages.Filter(func(img DockerImage) bool {
			return strings.HasPrefix(img.Name, repo+"/dockerImages/"+imagePath+"@")
		}) {
			dockerImages.Delete(img.Name)
		}
		arDeleteImageManifests(repo, imagePath, "")
		lro := artifactRegistryLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, gcpEmptyType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		prefix := arPackageName(repo, r) + "/versions/"
		gcpWriteList(w, r, versions, prefix, "versions", func(v ARVersion) string { return v.Name })
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r) + "/versions/" + url.PathEscape(sim.PathParam(r, "version"))
		v, ok := versions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "version %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r) + "/versions/" + url.PathEscape(sim.PathParam(r, "version"))
		v, ok := versions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "version %q not found", name)
			return
		}
		var patch ARVersion
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Annotations != nil {
			v.Annotations = patch.Annotations
		}
		if patch.Description != "" {
			v.Description = patch.Description
		}
		v.UpdateTime = nowTimestamp()
		versions.Put(name, v)
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r) + "/versions/" + url.PathEscape(sim.PathParam(r, "version"))
		_, ok = versions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "version %q not found", name)
			return
		}
		tagged := tags.Filter(func(t ARTag) bool { return t.Version == name })
		if len(tagged) > 0 && !gcpQueryBool(r, "force") {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"version %q is tagged; delete it with force to delete its tags too", name)
			return
		}
		imagePath, digest := sim.PathParam(r, "pkg"), sim.PathParam(r, "version")
		arDeleteVersionFiles(name)
		arForgetVersion(dockerImages, repo, imagePath, digest)
		arDeleteImageManifests(repo, imagePath, digest)
		lro := artifactRegistryLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, gcpEmptyType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		var req struct {
			Names        []string `json:"names"`
			ValidateOnly bool     `json:"validateOnly"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if len(req.Names) == 0 {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "names is required")
			return
		}
		parent := arPackageName(repo, r) + "/versions/"
		for _, n := range req.Names {
			if !strings.HasPrefix(n, parent) {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "version %q is not in package %q", n, strings.TrimSuffix(parent, "/versions/"))
				return
			}
		}
		failed := []string{}
		for _, n := range req.Names {
			if _, exists := versions.Get(n); !exists {
				failed = append(failed, n)
				continue
			}
			if !req.ValidateOnly {
				digest, err := url.PathUnescape(strings.TrimPrefix(n, parent))
				if err != nil {
					failed = append(failed, n)
					continue
				}
				arDeleteVersionFiles(n)
				arForgetVersion(dockerImages, repo, sim.PathParam(r, "pkg"), digest)
				arDeleteImageManifests(repo, sim.PathParam(r, "pkg"), digest)
			}
		}
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, gcpEmptyType,
			gcpFixedOperationMetadata(map[string]any{
				"@type":          "type.googleapis.com/google.devtools.artifactregistry.v1.BatchDeleteVersionsMetadata",
				"failedVersions": failed,
			}))
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		prefix := arPackageName(repo, r) + "/tags/"
		gcpWriteList(w, r, tags, prefix, "tags", func(v ARTag) string { return v.Name })
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r) + "/tags/" + sim.PathParam(r, "tag")
		t, ok := tags.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		tagID := r.URL.Query().Get("tagId")
		if tagID == "" {
			GCPError(w, http.StatusBadRequest, "tagId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var t ARTag
		if err := sim.ReadJSON(r, &t); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		t.Name = arPackageName(repo, r) + "/tags/" + tagID
		tags.Put(t.Name, t)
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r) + "/tags/" + sim.PathParam(r, "tag")
		t, ok := tags.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag %q not found", name)
			return
		}
		var patch ARTag
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Version != "" {
			t.Version = patch.Version
		}
		tags.Put(name, t)
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arPackageName(repo, r) + "/tags/" + sim.PathParam(r, "tag")
		if _, ok := tags.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag %q not found", name)
			return
		}
		tags.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})
}
