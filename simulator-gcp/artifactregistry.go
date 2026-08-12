package main

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
	dockerclient "github.com/moby/moby/client"
)

// Artifact Registry types

// Repository represents an Artifact Registry repository.
type Repository struct {
	Name                   string            `json:"name"`
	Format                 string            `json:"format"`
	Mode                   string            `json:"mode,omitempty"`
	Description            string            `json:"description,omitempty"`
	Labels                 map[string]string `json:"labels,omitempty"`
	KmsKeyName             string            `json:"kmsKeyName,omitempty"`
	CleanupPolicyDryRun    *bool             `json:"cleanupPolicyDryRun,omitempty"`
	RemoteRepositoryConfig map[string]any    `json:"remoteRepositoryConfig,omitempty"`
	// Nested writable configs the sim persists verbatim so the
	// terraform-provider-google read path round-trips without drift.
	CleanupPolicies json.RawMessage `json:"cleanupPolicies,omitempty"`
	DockerConfig    json.RawMessage `json:"dockerConfig,omitempty"`
	RegistryURI     string          `json:"registryUri,omitempty"` // external: canonical `<location>-docker.pkg.dev/<project>/<repo>` URI; sim serves OCI at the configured endpoint, not pkg.dev
	CreateTime      string          `json:"createTime"`
	UpdateTime      string          `json:"updateTime"`
}

// DockerImage represents a Docker image in Artifact Registry.
type DockerImage struct {
	Name       string   `json:"name"`
	URI        string   `json:"uri"` // external: canonical `<location>-docker.pkg.dev/<project>/<repo>@<digest>` URI; sim serves OCI at the configured endpoint
	Tags       []string `json:"tags,omitempty"`
	UploadTime string   `json:"uploadTime"`
	MediaType  string   `json:"mediaType,omitempty"`
	BuildTime  string   `json:"buildTime,omitempty"`
}

// ARPackage mirrors the artifactregistry-v1 Package schema. Packages are named
// collections of versions within a repository.
type ARPackage struct {
	Name        string            `json:"name"`
	DisplayName string            `json:"displayName,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ARVersion mirrors the artifactregistry-v1 Version schema.
type ARVersion struct {
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ARTag mirrors the artifactregistry-v1 Tag schema. A tag is an alternative
// name pointing at a version within a package.
type ARTag struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// ARFile mirrors the artifactregistry-v1 File schema
// (GoogleDevtoolsArtifactregistryV1File).
type ARFile struct {
	Name        string            `json:"name"`
	SizeBytes   string            `json:"sizeBytes,omitempty"`
	Hashes      []ARHash          `json:"hashes,omitempty"`
	CreateTime  string            `json:"createTime,omitempty"`
	UpdateTime  string            `json:"updateTime,omitempty"`
	Owner       string            `json:"owner,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
}

// ARHash mirrors the artifactregistry-v1 Hash schema.
type ARHash struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
}

// ARRule mirrors the artifactregistry-v1 Rule schema
// (GoogleDevtoolsArtifactregistryV1Rule).
type ARRule struct {
	Name      string          `json:"name"`
	Action    string          `json:"action,omitempty"`
	Operation string          `json:"operation,omitempty"`
	Condition json.RawMessage `json:"condition,omitempty"`
	PackageID string          `json:"packageId,omitempty"`
}

// ARAttachment mirrors the artifactregistry-v1 Attachment schema.
type ARAttachment struct {
	Name                string            `json:"name"`
	Target              string            `json:"target,omitempty"`
	Type                string            `json:"type,omitempty"`
	AttachmentNamespace string            `json:"attachmentNamespace,omitempty"`
	Annotations         map[string]string `json:"annotations,omitempty"`
	CreateTime          string            `json:"createTime,omitempty"`
	UpdateTime          string            `json:"updateTime,omitempty"`
	Files               []string          `json:"files,omitempty"`
	OCIVersionName      string            `json:"ociVersionName,omitempty"`
}

// ARProjectSettings mirrors the artifactregistry-v1 ProjectSettings schema.
type ARProjectSettings struct {
	Name                   string `json:"name"`
	LegacyRedirectionState string `json:"legacyRedirectionState,omitempty"`
	PullPercent            int    `json:"pullPercent,omitempty"`
}

// ARVPCSCConfig mirrors the artifactregistry-v1 VPCSCConfig schema.
type ARVPCSCConfig struct {
	Name        string `json:"name"`
	VPCSCPolicy string `json:"vpcscPolicy,omitempty"`
}

// ARProjectConfig mirrors the artifactregistry-v1 ProjectConfig schema.
type ARProjectConfig struct {
	Name               string          `json:"name"`
	PlatformLogsConfig json.RawMessage `json:"platformLogsConfig,omitempty"`
}

// Package-level store for dashboard access.
var arRepos sim.Store[Repository]

// remoteRepositoryConfigMembers is the RemoteRepositoryConfig member set
// from the artifactregistry-v1 Discovery document. The sim stores the
// config as a raw map for verbatim round-trips, so request fields the
// schema doesn't define (real GCP's proto-JSON parsing discards them and
// never echoes them back) are dropped at intake.
var remoteRepositoryConfigMembers = map[string]bool{
	"aptRepository":             true,
	"commonRepository":          true,
	"description":               true,
	"disableUpstreamValidation": true,
	"dockerRepository":          true,
	"mavenRepository":           true,
	"npmRepository":             true,
	"pythonRepository":          true,
	"upstreamCredentials":       true,
	"yumRepository":             true,
}

// sanitizeRemoteRepositoryConfig keeps only schema-defined members,
// mirroring real GCP's treatment of unknown request fields.
func sanitizeRemoteRepositoryConfig(cfg map[string]any) map[string]any {
	if cfg == nil {
		return nil
	}
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		if remoteRepositoryConfigMembers[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func registerArtifactRegistry(srv *sim.Server) {
	repos := sim.MakeStore[Repository](srv.DB(), "ar_repos")
	arRepos = repos
	dockerImages := sim.MakeStore[DockerImage](srv.DB(), "ar_docker_images")

	// OCI Distribution data plane (shared registry library). Cloud-specifics:
	// AR serves its control-plane API under /v2/projects/ (SkipPath), registers
	// a DockerImage row on manifest push (OnManifestPut), and hydrates docker-hub
	// remote repos from the local Docker daemon on a pull miss (HydrateManifest).
	dockerImagesForHooks := dockerImages
	reg := &sim.OCIRegistry{
		Manifests: sim.MakeStore[sim.OCIManifest](srv.DB(), "ar_manifests"),
		Blobs:     sim.MakeStore[sim.OCIBlob](srv.DB(), "ar_blobs"),
		Uploads:   sim.MakeStore[sim.OCIUpload](srv.DB(), "ar_uploads"),
		SkipPath:  func(path string) bool { return strings.HasPrefix(path, "/v2/projects/") },
		OnManifestPut: func(repo, ref, contentType string, data []byte) {
			registerDockerImageFromManifest(dockerImagesForHooks, repo, ref, contentType, data)
		},
		HydrateManifest: func(reg *sim.OCIRegistry, repo, ref string) bool {
			if err := hydrateOCIImageFromLocalDocker(reg, dockerImagesForHooks, repo, ref); err != nil {
				fmt.Fprintf(os.Stderr, "[sim-gcp-ar] local docker cache miss for %s:%s: %v\n", repo, ref, err)
				return false
			}
			return true
		},
	}

	// Create repository
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		// The terraform google provider sends repository_id (snake_case),
		// while the SDK sends repositoryId (camelCase). Accept both.
		repoID := r.URL.Query().Get("repositoryId")
		if repoID == "" {
			repoID = r.URL.Query().Get("repository_id")
		}
		if repoID == "" {
			sim.GCPError(w, http.StatusBadRequest, "repositoryId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var repo Repository
		if err := sim.ReadJSON(r, &repo); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)
		if _, exists := repos.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "repository %q already exists", name)
			return
		}

		now := nowTimestamp()
		repo.Name = name
		if repo.Format == "" {
			repo.Format = "DOCKER"
		}
		if repo.Mode == "" {
			repo.Mode = "STANDARD_REPOSITORY"
		}
		repo.RegistryURI = fmt.Sprintf("%s-docker.pkg.dev/%s/%s", location, project, repoID)
		repo.RemoteRepositoryConfig = sanitizeRemoteRepositoryConfig(repo.RemoteRepositoryConfig)
		repo.CreateTime = now
		repo.UpdateTime = now

		repos.Put(name, repo)

		lro := newLRO(project, location, repo, "type.googleapis.com/google.devtools.artifactregistry.v1.Repository")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// Get repository (also handles :getIamPolicy/:setIamPolicy suffixes from terraform)
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		repoID := sim.PathParam(r, "repo")

		// Don't match if path continues with /dockerImages
		if strings.Contains(r.URL.Path, "/dockerImages") {
			return
		}

		// Handle IAM operations — terraform google provider uses GET for these
		if base, action, ok := strings.Cut(repoID, ":"); ok {
			resource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, base)
			handleResourceIAM(w, r, gcpResourcePolicies, resource, action)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)
		repo, ok := repos.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, repo)
	})

	// Artifact registry repository IAM (POST variant)
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repoAction}", func(w http.ResponseWriter, r *http.Request) {
		repoAction := sim.PathParam(r, "repoAction")
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")

		repo, action, ok := strings.Cut(repoAction, ":")
		if !ok {
			http.NotFound(w, r)
			return
		}

		resource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repo)
		handleResourceIAM(w, r, gcpResourcePolicies, resource, action)
	})

	// List repositories
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/repositories/", project, location)

		result := repos.Filter(func(repo Repository) bool {
			return strings.HasPrefix(repo.Name, prefix)
		})
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []Repository{}
		}
		resp := map[string]any{"repositories": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Delete repository
	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		repoID := sim.PathParam(r, "repo")
		name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)

		repo, ok := repos.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return
		}
		repos.Delete(name)

		// Clean up docker images for this repo
		images := dockerImages.Filter(func(img DockerImage) bool {
			return strings.HasPrefix(img.Name, name+"/")
		})
		for _, img := range images {
			dockerImages.Delete(img.Name)
		}

		lro := newLRO(project, location, repo, "type.googleapis.com/google.devtools.artifactregistry.v1.Repository")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// List docker images
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		repoID := sim.PathParam(r, "repo")
		repoName := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)

		if _, ok := repos.Get(repoName); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", repoName)
			return
		}

		prefix := repoName + "/dockerImages/"
		result := dockerImages.Filter(func(img DockerImage) bool {
			return strings.HasPrefix(img.Name, prefix)
		})
		if result == nil {
			result = []DockerImage{}
		}

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"dockerImages": result,
		})
	})

	// Packages / versions / tags / files / rules / attachments and the
	// project-scoped singleton configs (the Artifact Registry JSON admin API
	// beyond repository CRUD + the OCI data plane).
	registerARSubresources(srv, repos, dockerImages)

	// OCI Distribution data plane — mounted from the shared registry library.
	reg.Register(srv)
}

// registerARSubresources mounts the package/version/tag/file/rule/attachment
// CRUD surface plus the project-scoped projectSettings / vpcscConfig /
// projectConfig singletons. Long-running mutations (delete package/version,
// batchDelete, import, upload, attachment create/delete) return a completed
// Operation exactly as real Artifact Registry does.
func registerARSubresources(srv *sim.Server, repos sim.Store[Repository], dockerImages sim.Store[DockerImage]) {
	const (
		pkgType    = "type.googleapis.com/google.devtools.artifactregistry.v1.Package"
		verType    = "type.googleapis.com/google.devtools.artifactregistry.v1.Version"
		fileType   = "type.googleapis.com/google.devtools.artifactregistry.v1.File"
		attachType = "type.googleapis.com/google.devtools.artifactregistry.v1.Attachment"
		importType = "type.googleapis.com/google.devtools.artifactregistry.v1.ImportArtifactsResponse"
	)
	packages := sim.MakeStore[ARPackage](srv.DB(), "ar_packages")
	versions := sim.MakeStore[ARVersion](srv.DB(), "ar_versions")
	tags := sim.MakeStore[ARTag](srv.DB(), "ar_tags")
	files := sim.MakeStore[ARFile](srv.DB(), "ar_files")
	rules := sim.MakeStore[ARRule](srv.DB(), "ar_rules")
	attachments := sim.MakeStore[ARAttachment](srv.DB(), "ar_attachments")
	projectSettings := sim.MakeStore[ARProjectSettings](srv.DB(), "ar_project_settings")
	vpcscConfigs := sim.MakeStore[ARVPCSCConfig](srv.DB(), "ar_vpcsc_configs")
	projectConfigs := sim.MakeStore[ARProjectConfig](srv.DB(), "ar_project_configs")

	repoName := func(r *http.Request) string {
		return fmt.Sprintf("projects/%s/locations/%s/repositories/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "repo"))
	}
	repoExists := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		name := repoName(r)
		if _, ok := repos.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return "", false
		}
		return name, true
	}

	// ---- Packages ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		prefix := repo + "/packages/"
		result := packages.Filter(func(p ARPackage) bool { return strings.HasPrefix(p.Name, prefix) })
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []ARPackage{}
		}
		resp := map[string]any{"packages": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg")
		pkg, ok := packages.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "package %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, pkg)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg")
		pkg, ok := packages.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "package %q not found", name)
			return
		}
		var patch ARPackage
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg")
		pkg, ok := packages.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "package %q not found", name)
			return
		}
		packages.Delete(name)
		// Cascade-delete the package's versions and tags.
		for _, v := range versions.Filter(func(v ARVersion) bool { return strings.HasPrefix(v.Name, name+"/versions/") }) {
			versions.Delete(v.Name)
		}
		for _, t := range tags.Filter(func(t ARTag) bool { return strings.HasPrefix(t.Name, name+"/tags/") }) {
			tags.Delete(t.Name)
		}
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), pkg, pkgType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// ---- Versions ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		prefix := repo + "/packages/" + sim.PathParam(r, "pkg") + "/versions/"
		result := versions.Filter(func(v ARVersion) bool { return strings.HasPrefix(v.Name, prefix) })
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []ARVersion{}
		}
		resp := map[string]any{"versions": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg") + "/versions/" + sim.PathParam(r, "version")
		v, ok := versions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "version %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, v)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions/{version}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg") + "/versions/" + sim.PathParam(r, "version")
		v, ok := versions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "version %q not found", name)
			return
		}
		var patch ARVersion
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg") + "/versions/" + sim.PathParam(r, "version")
		v, ok := versions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "version %q not found", name)
			return
		}
		versions.Delete(name)
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), v, verType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/versions:batchDelete", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		var req struct {
			Names        []string `json:"names"`
			ValidateOnly bool     `json:"validateOnly"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !req.ValidateOnly {
			for _, n := range req.Names {
				versions.Delete(n)
			}
		}
		_ = repo
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, verType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// ---- Tags ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		prefix := repo + "/packages/" + sim.PathParam(r, "pkg") + "/tags/"
		result := tags.Filter(func(t ARTag) bool { return strings.HasPrefix(t.Name, prefix) })
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []ARTag{}
		}
		resp := map[string]any{"tags": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg") + "/tags/" + sim.PathParam(r, "tag")
		t, ok := tags.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		tagID := r.URL.Query().Get("tagId")
		if tagID == "" {
			sim.GCPError(w, http.StatusBadRequest, "tagId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var t ARTag
		if err := sim.ReadJSON(r, &t); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		t.Name = repo + "/packages/" + sim.PathParam(r, "pkg") + "/tags/" + tagID
		tags.Put(t.Name, t)
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg") + "/tags/" + sim.PathParam(r, "tag")
		t, ok := tags.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag %q not found", name)
			return
		}
		var patch ARTag
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Version != "" {
			t.Version = patch.Version
		}
		tags.Put(name, t)
		sim.WriteJSON(w, http.StatusOK, t)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/packages/{pkg}/tags/{tag}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/packages/" + sim.PathParam(r, "pkg") + "/tags/" + sim.PathParam(r, "tag")
		if _, ok := tags.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "tag %q not found", name)
			return
		}
		tags.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// ---- Files ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		prefix := repo + "/files/"
		result := files.Filter(func(f ARFile) bool { return strings.HasPrefix(f.Name, prefix) })
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []ARFile{}
		}
		resp := map[string]any{"files": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		fileID := sim.PathParam(r, "file")
		// The download sub-resource (.../files/{file}:download) shares this
		// path shape; route it to a DownloadFileResponse.
		if base, action, ok := strings.Cut(fileID, ":"); ok && action == "download" {
			name := repo + "/files/" + base
			if _, ok := files.Get(name); !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{})
			return
		}
		name := repo + "/files/" + fileID
		f, ok := files.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, f)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/files/{file}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/files/" + sim.PathParam(r, "file")
		f, ok := files.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		var patch ARFile
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/files/" + sim.PathParam(r, "file")
		f, ok := files.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		files.Delete(name)
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), f, fileType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// File download (media: rides the /download/v1 prefix; alt=media returns
	// the file bytes, otherwise a DownloadFileResponse).
	srv.HandleFunc("GET /download/v1/projects/{project}/locations/{location}/repositories/{repo}/files/{fileAction}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		fileID, action, ok := strings.Cut(sim.PathParam(r, "fileAction"), ":")
		if !ok || action != "download" {
			http.NotFound(w, r)
			return
		}
		name := repo + "/files/" + fileID
		if _, ok := files.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// File upload (media: rides the /upload/v1 prefix).
	srv.HandleFunc("POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/files:upload", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "read upload body: %v", err)
			return
		}
		fileID := digestBytes(body)
		f := ARFile{
			Name:       repo + "/files/" + fileID,
			SizeBytes:  fmt.Sprintf("%d", len(body)),
			Hashes:     []ARHash{{Type: "SHA256", Value: fileID}},
			CreateTime: nowTimestamp(),
			UpdateTime: nowTimestamp(),
		}
		files.Put(f.Name, f)
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), f, fileType)
		sim.WriteJSON(w, http.StatusOK, map[string]any{"operation": lro})
	})

	// ---- Rules ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		prefix := repo + "/rules/"
		result := rules.Filter(func(ru ARRule) bool { return strings.HasPrefix(ru.Name, prefix) })
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []ARRule{}
		}
		resp := map[string]any{"rules": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/rules/" + sim.PathParam(r, "rule")
		ru, ok := rules.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, ru)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/rules", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		ruleID := r.URL.Query().Get("ruleId")
		if ruleID == "" {
			sim.GCPError(w, http.StatusBadRequest, "ruleId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var ru ARRule
		if err := sim.ReadJSON(r, &ru); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ru.Name = repo + "/rules/" + ruleID
		rules.Put(ru.Name, ru)
		sim.WriteJSON(w, http.StatusOK, ru)
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/rules/" + sim.PathParam(r, "rule")
		ru, ok := rules.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", name)
			return
		}
		var patch ARRule
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Action != "" {
			ru.Action = patch.Action
		}
		if patch.Operation != "" {
			ru.Operation = patch.Operation
		}
		if patch.Condition != nil {
			ru.Condition = patch.Condition
		}
		if patch.PackageID != "" {
			ru.PackageID = patch.PackageID
		}
		rules.Put(name, ru)
		sim.WriteJSON(w, http.StatusOK, ru)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/rules/{rule}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/rules/" + sim.PathParam(r, "rule")
		if _, ok := rules.Get(name); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rule %q not found", name)
			return
		}
		rules.Delete(name)
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
	})

	// ---- Attachments ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		prefix := repo + "/attachments/"
		result := attachments.Filter(func(a ARAttachment) bool { return strings.HasPrefix(a.Name, prefix) })
		result = gcpApplyListParams(result, r)
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		if page == nil {
			page = []ARAttachment{}
		}
		resp := map[string]any{"attachments": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/attachments/" + sim.PathParam(r, "attachment")
		a, ok := attachments.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "attachment %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, a)
	})

	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		attachmentID := r.URL.Query().Get("attachmentId")
		if attachmentID == "" {
			sim.GCPError(w, http.StatusBadRequest, "attachmentId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var a ARAttachment
		if err := sim.ReadJSON(r, &a); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		a.Name = repo + "/attachments/" + attachmentID
		a.CreateTime = nowTimestamp()
		a.UpdateTime = nowTimestamp()
		attachments.Put(a.Name, a)
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), a, attachType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}/attachments/{attachment}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/attachments/" + sim.PathParam(r, "attachment")
		a, ok := attachments.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "attachment %q not found", name)
			return
		}
		attachments.Delete(name)
		lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), a, attachType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// ---- Artifact :create / :import (apt/yum/googet/go/npm/python/kfp/generic) ----
	//
	// :create rides the media /upload/v1 prefix and returns
	// {operation: Operation}; :import is a control-plane POST returning an
	// Operation directly. The artifact bytes themselves land in the OCI /v2/
	// data plane or via the typed package managers; here the admin API records
	// the long-running import/create operation faithfully.
	registerARArtifactCreate := func(kind string) {
		srv.HandleFunc("POST /upload/v1/projects/{project}/locations/{location}/repositories/{repo}/"+kind+":create", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := repoExists(w, r); !ok {
				return
			}
			lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, importType)
			sim.WriteJSON(w, http.StatusOK, map[string]any{"operation": lro})
		})
	}
	for _, kind := range []string{"aptArtifacts", "yumArtifacts", "googetArtifacts", "goModules", "genericArtifacts", "kfpArtifacts"} {
		registerARArtifactCreate(kind)
	}

	registerARArtifactImport := func(kind string) {
		srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/repositories/{repo}/"+kind+":import", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := repoExists(w, r); !ok {
				return
			}
			lro := newLRO(sim.PathParam(r, "project"), sim.PathParam(r, "location"), nil, importType)
			sim.WriteJSON(w, http.StatusOK, lro)
		})
	}
	for _, kind := range []string{"aptArtifacts", "yumArtifacts", "googetArtifacts"} {
		registerARArtifactImport(kind)
	}

	// ---- Typed artifact listings (maven/npm/python) + dockerImage get ----

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := repoExists(w, r)
		if !ok {
			return
		}
		name := repo + "/dockerImages/" + sim.PathParam(r, "image")
		img, ok := dockerImages.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "docker image %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, img)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"mavenArtifacts": []any{}})
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "maven artifact %q not found", sim.PathParam(r, "artifact"))
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"npmPackages": []any{}})
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "npm package %q not found", sim.PathParam(r, "npmPackage"))
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"pythonPackages": []any{}})
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "python package %q not found", sim.PathParam(r, "pythonPackage"))
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := repoExists(w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"prewarmedArtifacts": []any{}})
	})

	// ---- Repository PATCH ----

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}", func(w http.ResponseWriter, r *http.Request) {
		name := repoName(r)
		repo, ok := repos.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return
		}
		var patch Repository
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.Description != "" {
			repo.Description = patch.Description
		}
		if patch.Labels != nil {
			repo.Labels = patch.Labels
		}
		if patch.CleanupPolicies != nil {
			repo.CleanupPolicies = patch.CleanupPolicies
		}
		if patch.CleanupPolicyDryRun != nil {
			repo.CleanupPolicyDryRun = patch.CleanupPolicyDryRun
		}
		if patch.DockerConfig != nil {
			repo.DockerConfig = patch.DockerConfig
		}
		repo.UpdateTime = nowTimestamp()
		repos.Put(name, repo)
		sim.WriteJSON(w, http.StatusOK, repo)
	})

	// ---- Project-scoped singletons: projectSettings / vpcscConfig / projectConfig ----

	srv.HandleFunc("GET /v1/projects/{project}/projectSettings", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("projects/%s/projectSettings", sim.PathParam(r, "project"))
		ps, ok := projectSettings.Get(name)
		if !ok {
			ps = ARProjectSettings{Name: name, LegacyRedirectionState: "REDIRECTION_STATE_UNSPECIFIED"}
			projectSettings.Put(name, ps)
		}
		sim.WriteJSON(w, http.StatusOK, ps)
	})
	srv.HandleFunc("PATCH /v1/projects/{project}/projectSettings", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("projects/%s/projectSettings", sim.PathParam(r, "project"))
		ps, ok := projectSettings.Get(name)
		if !ok {
			ps = ARProjectSettings{Name: name}
		}
		var patch ARProjectSettings
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.LegacyRedirectionState != "" {
			ps.LegacyRedirectionState = patch.LegacyRedirectionState
		}
		if patch.PullPercent != 0 {
			ps.PullPercent = patch.PullPercent
		}
		ps.Name = name
		projectSettings.Put(name, ps)
		sim.WriteJSON(w, http.StatusOK, ps)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/vpcscConfig", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("projects/%s/locations/%s/vpcscConfig", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
		cfg, ok := vpcscConfigs.Get(name)
		if !ok {
			cfg = ARVPCSCConfig{Name: name, VPCSCPolicy: "VPCSC_POLICY_UNSPECIFIED"}
			vpcscConfigs.Put(name, cfg)
		}
		sim.WriteJSON(w, http.StatusOK, cfg)
	})
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/vpcscConfig", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("projects/%s/locations/%s/vpcscConfig", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
		cfg, ok := vpcscConfigs.Get(name)
		if !ok {
			cfg = ARVPCSCConfig{Name: name}
		}
		var patch ARVPCSCConfig
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.VPCSCPolicy != "" {
			cfg.VPCSCPolicy = patch.VPCSCPolicy
		}
		cfg.Name = name
		vpcscConfigs.Put(name, cfg)
		sim.WriteJSON(w, http.StatusOK, cfg)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/projectConfig", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("projects/%s/locations/%s/projectConfig", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
		cfg, ok := projectConfigs.Get(name)
		if !ok {
			cfg = ARProjectConfig{Name: name}
			projectConfigs.Put(name, cfg)
		}
		sim.WriteJSON(w, http.StatusOK, cfg)
	})
	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/projectConfig", func(w http.ResponseWriter, r *http.Request) {
		name := fmt.Sprintf("projects/%s/locations/%s/projectConfig", sim.PathParam(r, "project"), sim.PathParam(r, "location"))
		cfg, ok := projectConfigs.Get(name)
		if !ok {
			cfg = ARProjectConfig{Name: name}
		}
		var patch ARProjectConfig
		if err := sim.ReadJSON(r, &patch); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if patch.PlatformLogsConfig != nil {
			cfg.PlatformLogsConfig = patch.PlatformLogsConfig
		}
		cfg.Name = name
		projectConfigs.Put(name, cfg)
		sim.WriteJSON(w, http.StatusOK, cfg)
	})
}

// hydrateOCIImageFromLocalDocker is the AR pull-through cache: on a manifest
// miss for a docker-hub remote repo it saves the image from the local Docker
// daemon and populates the shared registry's blobs + manifest.
func hydrateOCIImageFromLocalDocker(reg *sim.OCIRegistry, dockerImages sim.Store[DockerImage], imageName, reference string) error {
	// Map the AR remote-repo path back to the local Docker daemon ref it
	// proxies, mirroring the backend's image rewrite (gcp-common
	// image_resolve.go): `docker-hub` proxies Docker Hub, `gitlab-registry`
	// proxies registry.gitlab.com (the gitlab-runner-helper image).
	var localRef string
	switch {
	case strings.Contains(imageName, "/docker-hub/"):
		idx := strings.Index(imageName, "/docker-hub/")
		localRef = strings.TrimPrefix(imageName[idx+len("/docker-hub/"):], "library/") + ":" + reference
	case strings.Contains(imageName, "/gitlab-registry/"):
		idx := strings.Index(imageName, "/gitlab-registry/")
		localRef = "registry.gitlab.com/" + imageName[idx+len("/gitlab-registry/"):] + ":" + reference
	default:
		return fmt.Errorf("repository is not a docker-hub or gitlab-registry remote repository")
	}
	ctx := context.Background()
	cli, err := dockerclient.New(dockerclient.FromEnv)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	defer func() { _ = cli.Close() }()

	rc, err := cli.ImageSave(ctx, []string{localRef})
	if err != nil {
		return fmt.Errorf("docker image save %q: %w", localRef, err)
	}
	defer rc.Close()

	manifestData, files, err := readDockerImageSave(rc)
	if err != nil {
		return err
	}
	var saved []struct {
		Config   string   `json:"Config"`
		RepoTags []string `json:"RepoTags"`
		Layers   []string `json:"Layers"`
	}
	if err := json.Unmarshal(manifestData, &saved); err != nil {
		return fmt.Errorf("decode docker save manifest: %w", err)
	}
	if len(saved) == 0 {
		return fmt.Errorf("docker save manifest is empty")
	}
	image := saved[0]
	configData, ok := files[image.Config]
	if !ok {
		return fmt.Errorf("docker save config %q missing", image.Config)
	}

	// Serve a fully OCI manifest (OCI manifest + OCI config + OCI tar
	// layers). The `docker save` config blob is byte-compatible with the
	// OCI image-config schema, so it is labelled with the OCI config media
	// type rather than the Docker v2s2 type — a mixed OCI-manifest /
	// Docker-config image is rejected by docker build's FROM parsing
	// ("invalid mixed OCI image with Docker v2s2 config").
	configDigest := digestBytes(configData)
	reg.PutBlob(imageName, configDigest, "application/vnd.oci.image.config.v1+json", configData)

	type descriptor struct {
		MediaType string `json:"mediaType"`
		Size      int64  `json:"size"`
		Digest    string `json:"digest"`
	}
	layerDescriptors := make([]descriptor, 0, len(image.Layers))
	for _, layerPath := range image.Layers {
		layerData, ok := files[layerPath]
		if !ok {
			return fmt.Errorf("docker save layer %q missing", layerPath)
		}
		layerDigest := digestBytes(layerData)
		reg.PutBlob(imageName, layerDigest, "application/vnd.oci.image.layer.v1.tar", layerData)
		layerDescriptors = append(layerDescriptors, descriptor{
			MediaType: "application/vnd.oci.image.layer.v1.tar",
			Size:      int64(len(layerData)),
			Digest:    layerDigest,
		})
	}

	manifest := map[string]any{
		"schemaVersion": 2,
		"mediaType":     "application/vnd.oci.image.manifest.v1+json",
		"config": descriptor{
			MediaType: "application/vnd.oci.image.config.v1+json",
			Size:      int64(len(configData)),
			Digest:    configDigest,
		},
		"layers": layerDescriptors,
	}
	ociManifest, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode OCI manifest: %w", err)
	}
	reg.PutManifest(imageName, reference, "application/vnd.oci.image.manifest.v1+json", ociManifest)
	registerDockerImageFromManifest(dockerImages, imageName, reference, "application/vnd.oci.image.manifest.v1+json", ociManifest)
	return nil
}

func readDockerImageSave(r io.Reader) ([]byte, map[string][]byte, error) {
	tr := tar.NewReader(r)
	files := make(map[string][]byte)
	var manifest []byte
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, nil, fmt.Errorf("read docker save tar: %w", err)
		}
		if hdr.Typeflag == tar.TypeDir {
			continue
		}
		var buf bytes.Buffer
		if _, err := io.Copy(&buf, tr); err != nil {
			return nil, nil, fmt.Errorf("read docker save entry %q: %w", hdr.Name, err)
		}
		data := buf.Bytes()
		if hdr.Name == "manifest.json" {
			manifest = data
		}
		files[hdr.Name] = data
	}
	if len(manifest) == 0 {
		return nil, nil, fmt.Errorf("docker save manifest.json missing")
	}
	return manifest, files, nil
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum)
}

func registerDockerImageFromManifest(dockerImages sim.Store[DockerImage], imageName, reference, contentType string, data []byte) {
	var manifest struct {
		MediaType string `json:"mediaType"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		fmt.Fprintf(os.Stderr, "[sim-gcp-ar] mediaType extraction from manifest failed (image=%s ref=%s): %v\n",
			imageName, reference, err)
		manifest.MediaType = contentType
	}

	project, location, repoID, imagePath, ok := artifactRegistryImageParts(imageName)
	if !ok {
		fmt.Fprintf(os.Stderr, "[sim-gcp-ar] docker image registration skipped for malformed Artifact Registry image name %q\n", imageName)
		return
	}
	manifestDigest := digestBytes(data)
	now := nowTimestamp()
	imgName := fmt.Sprintf("projects/%s/locations/%s/repositories/%s/dockerImages/%s@%s", project, location, repoID, imagePath, manifestDigest)
	tags := []string{}
	if !strings.HasPrefix(reference, "sha256:") {
		tags = append(tags, reference)
	}

	img := DockerImage{
		Name:       imgName,
		URI:        fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s@%s", location, project, repoID, imagePath, manifestDigest),
		Tags:       tags,
		UploadTime: now,
		MediaType:  contentType,
	}
	dockerImages.Put(imgName, img)
}

func artifactRegistryImageParts(imageName string) (project, location, repoID, imagePath string, ok bool) {
	location = "us-central1"
	parts := strings.SplitN(imageName, "/", 3)
	if len(parts) < 3 {
		return "", "", "", "", false
	}
	project, repoID, imagePath = parts[0], parts[1], parts[2]
	if arRepos != nil {
		prefix := fmt.Sprintf("projects/%s/locations/", project)
		suffix := fmt.Sprintf("/repositories/%s", repoID)
		matches := arRepos.Filter(func(repo Repository) bool {
			return strings.HasPrefix(repo.Name, prefix) && strings.HasSuffix(repo.Name, suffix)
		})
		if len(matches) > 0 {
			segments := strings.Split(matches[0].Name, "/")
			if len(segments) >= 4 {
				location = segments[3]
			}
		}
	}
	return project, location, repoID, imagePath, true
}
