package main

import (
	"net/http"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// registerARFiles mounts a repository's files, their download and their upload.
func registerARFiles(srv *sim.Server, repos sim.Store[Repository], files sim.Store[ARFile]) {
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		prefix := repo + "/files/"
		gcpWriteList(w, r, files, prefix, "files", func(v ARFile) string { return v.Name })
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		fileID := sim.PathParam(r, "file")
		name := arFileName(repo, fileID)
		// files.download shares this path shape as {file}:download. A file ID
		// may itself hold colons, so a name that is a file is a get.
		if _, isFile := files.Get(name); !isFile {
			if base, found := strings.CutSuffix(fileID, ":download"); found {
				arServeFileDownload(w, r, arFileName(repo, base))
				return
			}
		}
		f, ok := files.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arFileName(repo, sim.PathParam(r, "file"))
		f, ok := files.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		var patch ARFile
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Annotations != nil {
			f.Annotations = patch.Annotations
		}
		f.UpdateTime = nowTimestamp()
		files.Put(name, f)
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := arFileName(repo, sim.PathParam(r, "file"))
		if _, ok := files.Get(name); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		// "It is only allowed on generic repositories," files.delete says.
		if repository, _ := repos.Get(repo); repository.Format != "GENERIC" {
			GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"repository %q has format %s; files can be deleted only from generic repositories", repo, repository.Format)
			return
		}
		arDeleteFiles(func(f ARFile) bool { return f.Name == name })
		lro := artifactRegistryLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, gcpEmptyType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// files.download is a media download: the Discovery document serves it
	// from the /download/v1 prefix.
	srv.HandleFunc("GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		fileID, found := strings.CutSuffix(sim.PathParam(r, "fileAction"), ":download")
		if !found {
			http.NotFound(w, r)
			return
		}
		arServeFileDownload(w, r, arFileName(repo, fileID))
	})

	// File upload. Registered on the plain /v1 path as well as the media
	// /upload/v1 one, because the document declares both and the service
	// answers both, and on the /resumable/upload/v1 path its resumable protocol
	// names. A file with no fileId is named by the sha256 digest of its
	// content, as UploadFileRequest says.
	finishFileUpload := func(repo string, request, data []byte, contentType string) (any, error) {
		var req struct {
			FileID   string `json:"fileId"`
			FileType string `json:"fileType"`
		}
		if err := arDecodeUploadRequest(request, &req); err != nil {
			return nil, err
		}
		if data == nil {
			return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "the upload carries no file content")
		}
		fileID := req.FileID
		if fileID == "" {
			fileID = digestBytes(data)
		}
		f := arPutFile(repo, fileID, "", contentType, data)
		project, location, _ := arRepoParts(repo)
		return map[string]any{"operation": artifactRegistryLRO(project, location, f, fileType)}, nil
	}

	uploadFile := func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		serveMediaUpload(w, r, repo, "files:upload", true, finishFileUpload)
	}

	srv.HandleFunc("POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)

	srv.HandleFunc("PUT /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)

	srv.HandleFunc("DELETE /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)

	srv.HandleFunc("POST /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)

	srv.HandleFunc("PUT /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)

	srv.HandleFunc("DELETE /resumable/upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", uploadFile)
}
