package main

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// ARPrewarmedArtifact mirrors the artifactregistry-v1 PrewarmedArtifact schema.
type ARPrewarmedArtifact struct {
	URI            string `json:"uri"`
	Location       string `json:"location,omitempty"`
	ExpirationTime string `json:"expirationTime,omitempty"`

	Repo string `json:"-"`
}

var arPrewarmed sim.Store[ARPrewarmedArtifact]

func initARPrewarmStore(srv *sim.Server) {
	arPrewarmed = sim.MakeStore[ARPrewarmedArtifact](srv.DB(), "ar_prewarmed_artifacts")
}

// What the service caches an artifact for when a request names no retention.
const arDefaultPrewarmRetentionDays = 3

func arPrewarmKey(repo, streamLocation, uri string) string {
	return repo + "\x00" + streamLocation + "\x00" + uri
}

// arArtifactSelector resolves the version-or-tag oneof a prewarm, check, remove
// or export request carries. Both members are full resource names under the
// repository, and the registry URI the response reports is built from the
// package and the version digest or tag.
func arArtifactSelector(repo, version, tag string) (resourceName, uri string, err error) {
	switch {
	case version != "" && tag != "":
		return "", "", fmt.Errorf("version and tag are mutually exclusive")
	case version == "" && tag == "":
		return "", "", fmt.Errorf("one of version or tag is required")
	}
	resourceName = version
	separator := "@"
	if version == "" {
		resourceName, separator = tag, ":"
	}
	if !strings.HasPrefix(resourceName, repo+"/packages/") {
		return "", "", fmt.Errorf("%q does not name an artifact in %q", resourceName, repo)
	}
	rest := strings.TrimPrefix(resourceName, repo+"/packages/")
	pkg, ref, found := strings.Cut(rest, "/versions/")
	if !found {
		if pkg, ref, found = strings.Cut(rest, "/tags/"); !found {
			return "", "", fmt.Errorf("%q names neither a version nor a tag", resourceName)
		}
	}
	project, location, repoID := arRepoParts(repo)
	return resourceName, fmt.Sprintf("%s-docker.pkg.dev/%s/%s/%s%s%s",
		location, project, repoID, pkg, separator, ref), nil
}

// arRepoParts splits projects/{p}/locations/{l}/repositories/{r}.
func arRepoParts(repo string) (project, location, repoID string) {
	parts := strings.Split(repo, "/")
	if len(parts) != 6 {
		return "", "", ""
	}
	return parts[1], parts[3], parts[5]
}

// arRepoVerbHandled serves the four custom methods that ride the repository
// segment. It shares the repository colon-verb fan-in with the IAM triple, so
// it reports whether it took the verb and leaves the rest to that path.
func arRepoVerbHandled(w http.ResponseWriter, r *http.Request, repo, verb string,
	repos sim.Store[Repository], versions sim.Store[ARVersion], tags sim.Store[ARTag]) bool {
	switch verb {
	case "prewarmArtifact", "checkPrewarmedArtifact", "removePrewarmedArtifact", "exportArtifact":
	default:
		return false
	}
	if _, exists := repos.Get(repo); !exists {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "repository %q not found", repo)
		return true
	}
	switch verb {
	case "prewarmArtifact":
		arHandlePrewarm(w, r, repo, versions, tags)
	case "checkPrewarmedArtifact":
		arHandleCheckPrewarmed(w, r, repo)
	case "removePrewarmedArtifact":
		arHandleRemovePrewarmed(w, r, repo)
	case "exportArtifact":
		arHandleExportArtifact(w, r, repo, versions, tags)
	}
	return true
}

func arHandlePrewarm(w http.ResponseWriter, r *http.Request, repo string, versions sim.Store[ARVersion], tags sim.Store[ARTag]) {
	var req struct {
		StreamLocation string `json:"streamLocation"`
		Version        string `json:"version"`
		Tag            string `json:"tag"`
		Platform       string `json:"platform"`
		RetentionDays  string `json:"retentionDays"`
		Force          bool   `json:"force"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	name, uri, err := arArtifactSelector(repo, req.Version, req.Tag)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	if !arArtifactExists(name, versions, tags) {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "artifact %q not found", name)
		return
	}
	// streamLocation is optional: unset caches the artifact where it already
	// lives, which is the repository's own location.
	_, repoLocation, _ := arRepoParts(repo)
	streamLocation := req.StreamLocation
	if streamLocation == "" {
		streamLocation = repoLocation
	}
	key := arPrewarmKey(repo, streamLocation, uri)
	if _, exists := arPrewarmed.Get(key); exists && !req.Force {
		sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
			"artifact %q is already prewarmed in %q", uri, streamLocation)
		return
	}
	retention := arDefaultPrewarmRetentionDays
	if req.RetentionDays != "" {
		days, convErr := strconv.Atoi(req.RetentionDays)
		if convErr != nil || days <= 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"retentionDays %q is not a positive integer", req.RetentionDays)
			return
		}
		retention = days
	}
	artifact := ARPrewarmedArtifact{
		URI:            uri,
		Location:       streamLocation,
		ExpirationTime: time.Now().UTC().AddDate(0, 0, retention).Format(time.RFC3339),
		Repo:           repo,
	}
	arPrewarmed.Put(key, artifact)
	sim.WriteJSON(w, http.StatusOK, newLROFromResource(repo, map[string]any{"prewarmedArtifact": artifact},
		"type.googleapis.com/google.devtools.artifactregistry.v1.PrewarmArtifactResponse"))
}

func arHandleCheckPrewarmed(w http.ResponseWriter, r *http.Request, repo string) {
	artifact, ok := arPrewarmedFromRequest(w, r, repo)
	if !ok {
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"prewarmedArtifact": artifact})
}

func arHandleRemovePrewarmed(w http.ResponseWriter, r *http.Request, repo string) {
	artifact, ok := arPrewarmedFromRequest(w, r, repo)
	if !ok {
		return
	}
	arPrewarmed.Delete(arPrewarmKey(repo, artifact.Location, artifact.URI))
	sim.WriteJSON(w, http.StatusOK, map[string]any{"prewarmedArtifact": artifact})
}

// arPrewarmedFromRequest resolves the record a check or remove request names.
func arPrewarmedFromRequest(w http.ResponseWriter, r *http.Request, repo string) (ARPrewarmedArtifact, bool) {
	var req struct {
		StreamLocation string `json:"streamLocation"`
		Version        string `json:"version"`
		Tag            string `json:"tag"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return ARPrewarmedArtifact{}, false
	}
	_, uri, err := arArtifactSelector(repo, req.Version, req.Tag)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return ARPrewarmedArtifact{}, false
	}
	_, repoLocation, _ := arRepoParts(repo)
	streamLocation := req.StreamLocation
	if streamLocation == "" {
		streamLocation = repoLocation
	}
	artifact, ok := arPrewarmed.Get(arPrewarmKey(repo, streamLocation, uri))
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"artifact %q is not prewarmed in %q", uri, streamLocation)
		return ARPrewarmedArtifact{}, false
	}
	return artifact, true
}

// arHandleExportArtifact copies the artifact's blob into Cloud Storage, which
// this simulator also serves. The bytes come from the OCI blob the digest
// names; an artifact whose blob is absent is NOT_FOUND rather than an export
// of invented content.
func arHandleExportArtifact(w http.ResponseWriter, r *http.Request, repo string, versions sim.Store[ARVersion], tags sim.Store[ARTag]) {
	var req struct {
		SourceVersion string `json:"sourceVersion"`
		SourceTag     string `json:"sourceTag"`
		GcsPath       string `json:"gcsPath"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	name, _, err := arArtifactSelector(repo, req.SourceVersion, req.SourceTag)
	if err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	version, ok := arResolveVersion(name, versions, tags)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "artifact %q not found", name)
		return
	}
	// gcsPath starts with the bucket name and may carry a directory, per the
	// member's own description; the object keeps the artifact's digest.
	bucket, prefix, _ := strings.Cut(strings.TrimPrefix(req.GcsPath, "gs://"), "/")
	if bucket == "" {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"gcsPath %q must start with a bucket name", req.GcsPath)
		return
	}
	if _, exists := gcsBuckets.Get(bucket); !exists {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
		return
	}
	digest := version.Name[strings.LastIndex(version.Name, "/")+1:]
	_, repoLocation, repoID := arRepoParts(repo)
	blob, ok := arRegistry.Blobs.Get(repoID + "@" + digest)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
			"no stored blob for %q, so there is nothing to export", digest)
		return
	}
	object := strings.TrimSuffix(prefix, "/")
	if object != "" {
		object += "/"
	}
	object += digest
	if _, err := persistGCSObject(gcsObjects, bucket, object, blob.Data, GCSObject{
		ContentType: blob.ContentType,
		Metadata:    map[string]string{"artifactregistry-source": version.Name, "artifactregistry-location": repoLocation},
	}); err != nil {
		writeGCSPersistError(w, "export artifact", err)
		return
	}
	sim.WriteJSON(w, http.StatusOK, newLROFromResource(repo, map[string]any{"exportedVersion": version},
		"type.googleapis.com/google.devtools.artifactregistry.v1.ExportArtifactResponse"))
}

// arResolveVersion follows a tag to its version, or reads the version directly.
func arResolveVersion(name string, versions sim.Store[ARVersion], tags sim.Store[ARTag]) (ARVersion, bool) {
	if version, ok := versions.Get(name); ok {
		return version, true
	}
	if tag, ok := tags.Get(name); ok {
		return versions.Get(tag.Version)
	}
	return ARVersion{}, false
}

// arArtifactExists accepts a tag whose version row has not been written: a tag
// is an artifact selector in its own right.
func arArtifactExists(name string, versions sim.Store[ARVersion], tags sim.Store[ARTag]) bool {
	if _, ok := versions.Get(name); ok {
		return true
	}
	_, ok := tags.Get(name)
	return ok
}

// arPrewarmedListing returns a repository's records, uri order.
func arPrewarmedListing(repo string) []ARPrewarmedArtifact {
	items := arPrewarmed.Filter(func(a ARPrewarmedArtifact) bool { return a.Repo == repo })
	sort.Slice(items, func(i, j int) bool {
		if items[i].URI != items[j].URI {
			return items[i].URI < items[j].URI
		}
		return items[i].Location < items[j].Location
	})
	return items
}
