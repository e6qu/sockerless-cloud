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
	"net/url"
	"os"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	dockerclient "github.com/moby/moby/client"
)

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
	RegistryURI     string          `json:"registryUri,omitempty"` // external: canonical `<location>-<format>.pkg.dev/<project>/<repo>` URI; sim serves at the configured endpoint, not pkg.dev
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
	// ContentType is the media type the file was published with, which a
	// download answers with.
	ContentType string `json:"-"`
	// DockerRepo names the registry repository (PROJECT/REPOSITORY/IMAGE) whose
	// manifest or blob a Docker repository's file is; its bytes stay in the
	// registry rather than in arFileContents.
	DockerRepo string `json:"-"`
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

// The registry data plane and its auth hooks read repositories from here.
var arRepos sim.Store[Repository]

// The repository colon-verb fan-in lives beside the repository routes, while
// the version and tag stores are created with the package routes further down,
// so the custom methods that resolve an artifact read them from here.
var (
	arPackages sim.Store[ARPackage]
	arVersions sim.Store[ARVersion]
	arTags     sim.Store[ARTag]
	// arRegistry holds a Docker image's manifests and blobs, which
	// exportArtifact writes into Cloud Storage.
	arRegistry *sim.OCIRegistry
)

// remoteRepositoryConfigMembers is the RemoteRepositoryConfig member set from
// the artifactregistry-v1 Discovery document. Intake drops every other member,
// as Google Cloud's proto-JSON parsing does, so a read never echoes it.
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
	initARPrewarmStore(srv)
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
		Authorize: arAuthorizeV2,
		// Google Artifact Registry answers the authorized ping with status 200,
		// `content-length: 0`, an empty body, and — the part no client would
		// guess — `content-type: text/html; charset=UTF-8`. Captured from
		// `us-docker.pkg.dev/v2/` with a token from its own token service;
		// `gcr.io/v2/` answers identically.
		BaseResponse: func(w http.ResponseWriter) {
			w.Header().Set("Content-Type", "text/html; charset=UTF-8")
			w.Header().Set("Content-Length", "0")
			w.WriteHeader(http.StatusOK)
		},
		// Artifact Registry documents that it refuses Docker chunked uploads but not
		// how, so a second write into an upload session answers the Docker Registry
		// HTTP API v2 code for an unimplemented operation, `UNSUPPORTED`.
		RefuseChunkedUpload: func(w http.ResponseWriter) {
			w.Header().Set("Allow", "POST, PUT, GET, DELETE")
			sim.OCIError(w, "UNSUPPORTED",
				"Artifact Registry does not support chunked uploads; send the blob in a single monolithic upload",
				http.StatusMethodNotAllowed)
		},
		OnManifestPut: func(_, repo, ref, contentType string, data []byte) {
			registerDockerImageFromManifest(dockerImagesForHooks, repo, ref, contentType, data)
			if project, location, repoID, _, ok := artifactRegistryImageParts(repo); ok {
				arSyncDockerFiles(fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID))
			}
		},
		OnManifestDelete: func(_, repo, digest string) {
			project, location, repoID, imagePath, ok := artifactRegistryImageParts(repo)
			if !ok {
				return
			}
			repoName := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)
			arForgetVersion(dockerImagesForHooks, repoName, imagePath, digest)
			arSyncDockerFiles(repoName)
		},
		HydrateManifest: func(reg *sim.OCIRegistry, scope, repo, ref string) bool {
			if err := hydrateOCIImageFromLocalDocker(reg, scope, dockerImagesForHooks, repo, ref); err != nil {
				fmt.Fprintf(os.Stderr, "[sim-gcp-ar] local docker cache miss for %s:%s: %v\n", repo, ref, err)
				return false
			}
			return true
		},
	}
	arRegistry = reg

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
			GCPError(w, http.StatusBadRequest, "repositoryId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var repo Repository
		if err := sim.ReadJSON(r, &repo); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)
		if _, exists := repos.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "repository %q already exists", name)
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
		repo.RegistryURI = arRegistryURI(location, project, repoID, repo.Format)
		repo.RemoteRepositoryConfig = sanitizeRemoteRepositoryConfig(repo.RemoteRepositoryConfig)
		repo.CreateTime = now
		repo.UpdateTime = now

		repos.Put(name, repo)

		lro := artifactRegistryLRO(project, location, repo, "type.googleapis.com/google.devtools.artifactregistry.v1.Repository")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		repoID := sim.PathParam(r, "repo")

		if strings.Contains(r.URL.Path, "/dockerImages") {
			return
		}

		// getIamPolicy is a GET with the verb after the colon, which {repo} captures.
		if base, action, ok := strings.Cut(repoID, ":"); ok {
			resource := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, base)
			handleResourceIAM(w, r, gcpResourcePolicies, resource, action)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)
		repo, ok := repos.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, repo)
	})

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
		if arRepoVerbHandled(w, r, resource, action, repos, arVersions, arTags) {
			return
		}
		handleResourceIAM(w, r, gcpResourcePolicies, resource, action)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/repositories/", project, location)

		result := repos.Filter(func(repo Repository) bool {
			return strings.HasPrefix(repo.Name, prefix)
		})
		listed, listOK := gcpApplyListParams(w, r, result)
		if !listOK {
			return
		}
		result = listed
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result, gcpAsc(func(v Repository) string { return v.Name }))
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

	srv.HandleFunc("DELETE /v1/projects/{project}/locations/{location}/repositories/{repo}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		repoID := sim.PathParam(r, "repo")
		name := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)

		_, ok := repos.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return
		}
		repos.Delete(name)

		images := dockerImages.Filter(func(img DockerImage) bool {
			return strings.HasPrefix(img.Name, name+"/")
		})
		for _, img := range images {
			dockerImages.Delete(img.Name)
		}
		arDeleteRepositoryContents(name)

		lro := artifactRegistryLRO(project, location, nil, gcpEmptyType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		repoID := sim.PathParam(r, "repo")
		repoName := fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID)

		if _, ok := repos.Get(repoName); !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", repoName)
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

	registerARSubresources(srv, repos, dockerImages)

	reg.Register(srv)

	// The Docker token service the data plane's Bearer challenge names. Each verb
	// mounts on its own to keep a method-specific pattern under /v2/; GET also
	// serves HEAD.
	srv.HandleFunc("GET /v2/token", arTokenServiceHandler)
	srv.HandleFunc("POST /v2/token", arTokenServiceMethodNotAllowed)
	srv.HandleFunc("PUT /v2/token", arTokenServiceMethodNotAllowed)
	srv.HandleFunc("PATCH /v2/token", arTokenServiceMethodNotAllowed)
	srv.HandleFunc("DELETE /v2/token", arTokenServiceMethodNotAllowed)
}

// arOperationMetadataType is the metadata message Artifact Registry declares
// for its long-running operations; it carries no fields.
const arOperationMetadataType = "type.googleapis.com/google.devtools.artifactregistry.v1.OperationMetadata"

func artifactRegistryLRO(project, location string, resource any, typeName string) Operation {
	return newLRO(project, location, resource, typeName, gcpEmptyOperationMetadata(arOperationMetadataType))
}

// arArtifactOperations names, per artifact kind, the response and metadata
// messages its :create (upload) and :import methods declare.
var arArtifactOperations = map[string]struct {
	uploadResponse, uploadMetadata, importResponse, importMetadata string
}{
	"aptArtifacts":     {"UploadAptArtifactResponse", "UploadAptArtifactMetadata", "ImportAptArtifactsResponse", "ImportAptArtifactsMetadata"},
	"yumArtifacts":     {"UploadYumArtifactResponse", "UploadYumArtifactMetadata", "ImportYumArtifactsResponse", "ImportYumArtifactsMetadata"},
	"googetArtifacts":  {"UploadGoogetArtifactResponse", "UploadGoogetArtifactMetadata", "ImportGoogetArtifactsResponse", "ImportGoogetArtifactsMetadata"},
	"goModules":        {uploadResponse: "GoModule", uploadMetadata: "UploadGoModuleMetadata"},
	"genericArtifacts": {uploadResponse: "GenericArtifact", uploadMetadata: "UploadGenericArtifactMetadata"},
	"kfpArtifacts":     {uploadResponse: "KfpArtifact", uploadMetadata: "UploadKfpArtifactMetadata"},
}

// registerARSubresources answers every long-running mutation with a completed
// Operation, as Artifact Registry does.
func registerARSubresources(srv *sim.Server, repos sim.Store[Repository], dockerImages sim.Store[DockerImage]) {
	packages := sim.MakeStore[ARPackage](srv.DB(), "ar_packages")
	versions := sim.MakeStore[ARVersion](srv.DB(), "ar_versions")
	tags := sim.MakeStore[ARTag](srv.DB(), "ar_tags")
	arPackages, arVersions, arTags = packages, versions, tags
	files := sim.MakeStore[ARFile](srv.DB(), "ar_files")
	arFiles = files
	arFileContents = sim.MakeStore[arFileContent](srv.DB(), "ar_file_contents")
	rules := sim.MakeStore[ARRule](srv.DB(), "ar_rules")
	attachments := sim.MakeStore[ARAttachment](srv.DB(), "ar_attachments")
	projectSettings := sim.MakeStore[ARProjectSettings](srv.DB(), "ar_project_settings")
	vpcscConfigs := sim.MakeStore[ARVPCSCConfig](srv.DB(), "ar_vpcsc_configs")
	projectConfigs := sim.MakeStore[ARProjectConfig](srv.DB(), "ar_project_configs")

	registerARPackages(srv, repos, dockerImages, packages, tags, versions)
	registerARFiles(srv, repos, files)
	registerARRulesAndAttachments(srv, repos, attachments, rules)
	registerARArtifactMedia(srv, repos)

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/dockerImages/{image}", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		name := repo + "/dockerImages/" + sim.PathParam(r, "image")
		img, ok := dockerImages.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "docker image %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, img)
	})

	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := arRepoExists(repos, w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"mavenArtifacts": []any{}})
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/mavenArtifacts/{artifact}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := arRepoExists(repos, w, r); !ok {
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "maven artifact %q not found", sim.PathParam(r, "artifact"))
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := arRepoExists(repos, w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"npmPackages": []any{}})
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/npmPackages/{npmPackage}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := arRepoExists(repos, w, r); !ok {
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "npm package %q not found", sim.PathParam(r, "npmPackage"))
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := arRepoExists(repos, w, r); !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"pythonPackages": []any{}})
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/pythonPackages/{pythonPackage}", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := arRepoExists(repos, w, r); !ok {
			return
		}
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "python package %q not found", sim.PathParam(r, "pythonPackage"))
	})
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/repositories/{repo}/prewarmedArtifacts", func(w http.ResponseWriter, r *http.Request) {
		repo, ok := arRepoExists(repos, w, r)
		if !ok {
			return
		}
		items := arPrewarmedListing(repo)
		if items == nil {
			items = []ARPrewarmedArtifact{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"prewarmedArtifacts": items})
	})

	srv.HandleFunc("PATCH /v1/projects/{project}/locations/{location}/repositories/{repo}", func(w http.ResponseWriter, r *http.Request) {
		name := arRepoName(r)
		repo, ok := repos.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
			return
		}
		var patch Repository
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
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

// arRemoteUpstreamRef is the reference, as the local Docker daemon names it,
// of the image a remote repository proxies for imageName: its remote
// repository configuration names Docker Hub or a custom registry as the
// upstream.
func arRemoteUpstreamRef(imageName, reference string) (string, error) {
	project, location, repoID, imagePath, ok := artifactRegistryImageParts(imageName)
	if !ok {
		return "", fmt.Errorf("no repository holds %s", imageName)
	}
	repo, ok := arRepos.Get(fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID))
	if !ok || repo.Mode != "REMOTE_REPOSITORY" {
		return "", fmt.Errorf("repository %s is not a remote repository", repoID)
	}
	docker, _ := repo.RemoteRepositoryConfig["dockerRepository"].(map[string]any)
	if public, _ := docker["publicRepository"].(string); public == "DOCKER_HUB" {
		return strings.TrimPrefix(imagePath, "library/") + ":" + reference, nil
	}
	custom, _ := docker["customRepository"].(map[string]any)
	if uri, _ := custom["uri"].(string); uri != "" {
		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(uri, "https://"), "http://"), "/")
		return host + "/" + imagePath + ":" + reference, nil
	}
	return "", fmt.Errorf("remote repository %s names no Docker upstream", repoID)
}

// hydrateOCIImageFromLocalDocker is the AR pull-through cache: on a manifest
// miss in a remote repository it saves the upstream image from the local
// Docker daemon and populates the shared registry's blobs + manifest.
func hydrateOCIImageFromLocalDocker(reg *sim.OCIRegistry, scope string, dockerImages sim.Store[DockerImage], imageName, reference string) error {
	localRef, err := arRemoteUpstreamRef(imageName, reference)
	if err != nil {
		return err
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

	// Label the `docker save` config blob, byte-compatible with the OCI image
	// config, with the OCI config media type: docker build's FROM rejects a mixed
	// OCI-manifest / Docker-config image.
	configDigest := digestBytes(configData)
	reg.PutBlob(scope, imageName, configDigest, "application/vnd.oci.image.config.v1+json", configData)

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
		reg.PutBlob(scope, imageName, layerDigest, "application/vnd.oci.image.layer.v1.tar", layerData)
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
	reg.PutManifest(scope, imageName, reference, "application/vnd.oci.image.manifest.v1+json", ociManifest)
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
	arRecordPush(fmt.Sprintf("projects/%s/locations/%s/repositories/%s", project, location, repoID), imagePath, reference, manifestDigest, now)
}

// arRegistryURI is a repository's endpoint, LOCATION-FORMAT.pkg.dev/PROJECT/
// REPOSITORY with the format lowercased: the formula gcloud's
// AddRegistryBaseToRepositoryInfo applies to every format. A domain-scoped
// project ID's colon becomes a path separator.
func arRegistryURI(location, project, repoID, format string) string {
	return fmt.Sprintf("%s-%s.pkg.dev/%s/%s", location, strings.ToLower(format),
		strings.ReplaceAll(project, ":", "/"), repoID)
}

// arPackageName is the resource name of the package a request addresses. A
// package id escapes the slashes of a nested image name, and the router hands
// the path segment over unescaped.
func arPackageName(repo string, r *http.Request) string {
	return repo + "/packages/" + url.PathEscape(sim.PathParam(r, "pkg"))
}

// arRecordPush records the package, version and tag a Docker push creates: the
// image is the package, named with its slashes escaped, the manifest digest is
// its version, and a push by tag points that tag at the version.
func arRecordPush(repo, imagePath, reference, digest, now string) {
	pkg := repo + "/packages/" + url.PathEscape(imagePath)
	arPackages.Upsert(pkg, func(p *ARPackage) {
		if p.Name == "" {
			p.Name, p.CreateTime = pkg, now
		}
		p.UpdateTime = now
	})
	version := pkg + "/versions/" + digest
	arVersions.Upsert(version, func(v *ARVersion) {
		if v.Name == "" {
			v.Name, v.CreateTime = version, now
		}
		v.UpdateTime = now
	})
	if !strings.HasPrefix(reference, "sha256:") {
		tag := pkg + "/tags/" + reference
		arTags.Put(tag, ARTag{Name: tag, Version: version})
	}
}

// arForgetVersion drops what the control plane holds for one manifest of an
// image: its dockerImages row, its version and the tags that point at it.
func arForgetVersion(dockerImages sim.Store[DockerImage], repo, imagePath, digest string) {
	dockerImages.Delete(repo + "/dockerImages/" + imagePath + "@" + digest)
	version := repo + "/packages/" + url.PathEscape(imagePath) + "/versions/" + url.PathEscape(digest)
	arVersions.Delete(version)
	for _, tag := range arTags.Filter(func(t ARTag) bool { return t.Version == version }) {
		arTags.Delete(tag.Name)
	}
}

// arDeleteImageManifests deletes from the Docker data plane the manifests of
// an image that carry digest, or all of them when digest is empty, so an image
// deleted through the control plane no longer pulls.
func arDeleteImageManifests(repo, imagePath, digest string) {
	parts := strings.Split(repo, "/")
	if arRegistry == nil || len(parts) != 6 {
		return
	}
	path := parts[1] + "/" + parts[5] + "/" + imagePath
	for _, m := range arRegistry.Manifests.Filter(func(m sim.OCIManifest) bool {
		return m.Repo == path && (digest == "" || m.Digest == digest)
	}) {
		arRegistry.DeleteManifest(m.Scope, m.Repo, m.Ref)
	}
	arSyncDockerFiles(repo)
}

// arDeleteRepositoryContents deletes a deleted repository's packages,
// versions, tags and files and its images' manifests, so a repository created
// again under the name starts empty.
func arDeleteRepositoryContents(repo string) {
	prefix := repo + "/packages/"
	for _, p := range arPackages.Filter(func(p ARPackage) bool { return strings.HasPrefix(p.Name, prefix) }) {
		arPackages.Delete(p.Name)
	}
	for _, v := range arVersions.Filter(func(v ARVersion) bool { return strings.HasPrefix(v.Name, prefix) }) {
		arVersions.Delete(v.Name)
	}
	for _, t := range arTags.Filter(func(t ARTag) bool { return strings.HasPrefix(t.Name, prefix) }) {
		arTags.Delete(t.Name)
	}
	arDeleteFiles(func(f ARFile) bool { return strings.HasPrefix(f.Name, repo+"/files/") })
	parts := strings.Split(repo, "/")
	if arRegistry == nil || len(parts) != 6 {
		return
	}
	path := parts[1] + "/" + parts[5] + "/"
	for _, m := range arRegistry.Manifests.Filter(func(m sim.OCIManifest) bool { return strings.HasPrefix(m.Repo, path) }) {
		arRegistry.DeleteManifest(m.Scope, m.Repo, m.Ref)
	}
}

// artifactRegistryImageParts splits a Docker Registry v2 repository path,
// PROJECT/REPOSITORY/IMAGE, into the Artifact Registry resource it addresses.
// The real service takes the location from its regional endpoint host; the
// simulator serves every location at one coordinate, so it takes the location
// the control plane created the repository at, and a repository no location
// holds addresses nothing.
func artifactRegistryImageParts(imageName string) (project, location, repoID, imagePath string, ok bool) {
	parts := strings.SplitN(imageName, "/", 3)
	if len(parts) < 3 {
		return "", "", "", "", false
	}
	project, repoID, imagePath = parts[0], parts[1], parts[2]
	location, ok = artifactRegistryRepositoryLocation(project, repoID)
	if !ok {
		return "", "", "", "", false
	}
	return project, location, repoID, imagePath, true
}

// artifactRegistryRepositoryLocation reports the location a project's
// repository was created at, and whether any location holds one by that name.
func artifactRegistryRepositoryLocation(project, repoID string) (string, bool) {
	if arRepos == nil {
		return "", false
	}
	prefix := fmt.Sprintf("projects/%s/locations/", project)
	suffix := fmt.Sprintf("/repositories/%s", repoID)
	matches := arRepos.Filter(func(repo Repository) bool {
		return strings.HasPrefix(repo.Name, prefix) && strings.HasSuffix(repo.Name, suffix)
	})
	if len(matches) == 0 {
		return "", false
	}
	segments := strings.Split(matches[0].Name, "/")
	if len(segments) < 4 {
		return "", false
	}
	return segments[3], true
}

const (
	pkgType    = "type.googleapis.com/google.devtools.artifactregistry.v1.Package"
	verType    = "type.googleapis.com/google.devtools.artifactregistry.v1.Version"
	fileType   = "type.googleapis.com/google.devtools.artifactregistry.v1.File"
	attachType = "type.googleapis.com/google.devtools.artifactregistry.v1.Attachment"
)

func arRepoName(r *http.Request) string {
	return fmt.Sprintf("projects/%s/locations/%s/repositories/%s",
		sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "repo"))
}

// arRepoExists answers NOT_FOUND for a repository the request names and
// repos does not hold.
func arRepoExists(repos sim.Store[Repository], w http.ResponseWriter, r *http.Request) (string, bool) {
	name := arRepoName(r)
	if _, ok := repos.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", name)
		return "", false
	}
	return name, true
}
