package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// arFileContent holds a File's bytes apart from its record, so listing files
// never decodes their contents.
type arFileContent struct {
	Data []byte `json:"data"`
}

var (
	arFiles        sim.Store[ARFile]
	arFileContents sim.Store[arFileContent]
	// arPublishMu serializes the check-then-write of an upload or import, so
	// two publishes of one artifact cannot both find it absent.
	arPublishMu sync.Mutex
)

const arTypePrefix = "type.googleapis.com/google.devtools.artifactregistry.v1."

// arFileName is a File's resource name; the file ID's slashes are escaped.
func arFileName(repo, fileID string) string {
	return repo + "/files/" + url.PathEscape(fileID)
}

func arFileID(name string) string {
	escaped := name[strings.LastIndex(name, "/files/")+len("/files/"):]
	id, err := url.PathUnescape(escaped)
	if err != nil {
		return escaped
	}
	return id
}

// arHashes reports a file's SHA256; Hash.value is a bytes field, so it carries
// the digest itself in base64.
func arHashes(data []byte) []ARHash {
	sum := sha256.Sum256(data)
	return []ARHash{{Type: "SHA256", Value: base64.StdEncoding.EncodeToString(sum[:])}}
}

// arPutFile stores a file's bytes, then its record, so a record never names
// bytes that are not there.
func arPutFile(repo, fileID, owner, contentType string, data []byte) ARFile {
	now := nowTimestamp()
	name := arFileName(repo, fileID)
	arFileContents.Put(name, arFileContent{Data: data})
	file := ARFile{
		Name:        name,
		SizeBytes:   strconv.Itoa(len(data)),
		Hashes:      arHashes(data),
		CreateTime:  now,
		UpdateTime:  now,
		Owner:       owner,
		ContentType: contentType,
	}
	if existing, ok := arFiles.Get(name); ok {
		file.CreateTime = existing.CreateTime
		file.Annotations = existing.Annotations
	}
	arFiles.Put(name, file)
	return file
}

// arDeleteFiles deletes the files match accepts, record first, so no record
// outlives its bytes.
func arDeleteFiles(match func(ARFile) bool) {
	if arFiles == nil {
		return
	}
	for _, f := range arFiles.Filter(match) {
		arFiles.Delete(f.Name)
		arFileContents.Delete(f.Name)
	}
}

// arDeleteVersionFiles deletes the files a version, or every version of a
// package, owns: a version is deleted with all of its content.
func arDeleteVersionFiles(owner string) {
	arDeleteFiles(func(f ARFile) bool {
		return f.Owner == owner || strings.HasPrefix(f.Owner, owner+"/versions/")
	})
}

// arRecordVersion records the package and version a publish creates and
// reports whether the version is new.
func arRecordVersion(repo, packageID, versionID string) (ARVersion, bool) {
	now := nowTimestamp()
	pkg := repo + "/packages/" + url.PathEscape(packageID)
	arPackages.Upsert(pkg, func(p *ARPackage) {
		if p.Name == "" {
			p.Name, p.CreateTime = pkg, now
		}
		p.UpdateTime = now
	})
	name := pkg + "/versions/" + url.PathEscape(versionID)
	created := false
	arVersions.Upsert(name, func(v *ARVersion) {
		if v.Name == "" {
			v.Name, v.CreateTime, created = name, now, true
		}
		v.UpdateTime = now
	})
	version, _ := arVersions.Get(name)
	return version, created
}

// arDecodeUploadRequest parses an upload's request message; an absent one
// leaves every member unset.
func arDecodeUploadRequest(request []byte, into any) error {
	if len(bytes.TrimSpace(request)) == 0 {
		return nil
	}
	if err := json.Unmarshal(request, into); err != nil {
		return apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request message: %v", err)
	}
	return nil
}

// arArtifactFormats names the repository format each artifact kind publishes to.
var arArtifactFormats = map[string]string{
	"aptArtifacts":     "APT",
	"yumArtifacts":     "YUM",
	"googetArtifacts":  "GOOGET",
	"goModules":        "GO",
	"genericArtifacts": "GENERIC",
	"kfpArtifacts":     "KFP",
}

func arCheckFormat(repo Repository, kind string) error {
	if want := arArtifactFormats[kind]; repo.Format != want {
		return apiRefuse(http.StatusBadRequest, "FAILED_PRECONDITION",
			"repository %q has format %s; %s publish to %s repositories", repo.Name, repo.Format, kind, want)
	}
	return nil
}

type arUploadRequest struct {
	PackageID          string            `json:"packageId"`
	VersionID          string            `json:"versionId"`
	Filename           string            `json:"filename"`
	VersionAnnotations map[string]string `json:"versionAnnotations"`
	Tags               []string          `json:"tags"`
	Description        string            `json:"description"`
}

// arFinishArtifactUpload completes a :create media method. Each stores the
// uploaded bytes as a File and records the Package and Version the artifact
// names, which is what the method's description says it creates.
func arFinishArtifactUpload(repo Repository, kind string, request, data []byte, contentType string) (any, error) {
	var req arUploadRequest
	if err := arDecodeUploadRequest(request, &req); err != nil {
		return nil, err
	}
	if err := arCheckFormat(repo, kind); err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "the upload carries no artifact content")
	}
	arPublishMu.Lock()
	var response any
	var err error
	switch kind {
	case "genericArtifacts":
		response, err = arPublishGeneric(repo.Name, req, data, contentType)
	case "goModules":
		response, err = arPublishGoModule(repo.Name, data, contentType)
	case "kfpArtifacts":
		response, err = arPublishKfp(repo.Name, req, data, contentType)
	default:
		var artifact map[string]any
		artifact, err = arPublishPackage(repo.Name, kind, data, contentType)
		published := []map[string]any{}
		if artifact != nil {
			published = append(published, artifact)
		}
		response = map[string]any{kind: published}
	}
	arPublishMu.Unlock()
	if err != nil {
		return nil, err
	}
	project, location, _ := arRepoParts(repo.Name)
	ops := arArtifactOperations[kind]
	lro := newLRO(project, location, response, arTypePrefix+ops.uploadResponse, gcpEmptyOperationMetadata(arTypePrefix+ops.uploadMetadata))
	return map[string]any{"operation": lro}, nil
}

// The ID rules UploadGenericArtifactRequest states for each member.
var (
	arGenericPackageID = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?$`)
	arGenericVersionID = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.+~:-]*[a-z0-9])?$`)
	arGenericFilename  = regexp.MustCompile(`^[A-Za-z0-9._~@-]+$`)
)

// arPublishGeneric creates the GenericArtifact {parent}/genericArtifacts/
// package_id:version_id and the File {parent}/files/package_id:version_id:
// filename, as UploadGenericArtifactRequest names them. A file that already
// exists is ALREADY_EXISTS.
func arPublishGeneric(repo string, req arUploadRequest, data []byte, contentType string) (any, error) {
	switch {
	case !arGenericPackageID.MatchString(req.PackageID) || len(req.PackageID) > 256:
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "packageId %q is not a valid package ID", req.PackageID)
	case !arGenericVersionID.MatchString(req.VersionID) || len(req.VersionID) > 128:
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "versionId %q is not a valid version ID", req.VersionID)
	case req.VersionID == "latest":
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "a version called latest is not allowed")
	case !arGenericFilename.MatchString(req.Filename):
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "filename %q is not a valid file name", req.Filename)
	}
	fileID := req.PackageID + ":" + req.VersionID + ":" + req.Filename
	if _, exists := arFiles.Get(arFileName(repo, fileID)); exists {
		return nil, apiRefuse(http.StatusConflict, "ALREADY_EXISTS", "file %q already exists", arFileName(repo, fileID))
	}
	versionName := repo + "/packages/" + url.PathEscape(req.PackageID) + "/versions/" + url.PathEscape(req.VersionID)
	if _, exists := arVersions.Get(versionName); exists && len(req.VersionAnnotations) > 0 {
		return nil, apiRefuse(http.StatusBadRequest, "FAILED_PRECONDITION",
			"version %q already exists, so versionAnnotations cannot be applied", versionName)
	}
	version, created := arRecordVersion(repo, req.PackageID, req.VersionID)
	if created && len(req.VersionAnnotations) > 0 {
		version.Annotations = req.VersionAnnotations
		arVersions.Put(version.Name, version)
	}
	arPutFile(repo, fileID, version.Name, contentType, data)
	return map[string]any{
		"name":       repo + "/genericArtifacts/" + req.PackageID + ":" + req.VersionID,
		"version":    req.VersionID,
		"createTime": version.CreateTime,
		"updateTime": version.UpdateTime,
	}, nil
}

// arPublishGoModule stores a module zip and its go.mod under the paths the
// module proxy protocol serves them at, module/@v/version.zip and .mod.
func arPublishGoModule(repo string, data []byte, contentType string) (any, error) {
	module, err := arParseGoModuleZip(data)
	if err != nil {
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
	}
	versionName := repo + "/packages/" + url.PathEscape(module.Path) + "/versions/" + url.PathEscape(module.Version)
	if _, exists := arVersions.Get(versionName); exists {
		return nil, apiRefuse(http.StatusConflict, "ALREADY_EXISTS", "module %s@%s already exists", module.Path, module.Version)
	}
	version, _ := arRecordVersion(repo, module.Path, module.Version)
	base := arGoEscapePath(module.Path) + "/@v/" + module.Version
	arPutFile(repo, base+".zip", version.Name, contentType, data)
	arPutFile(repo, base+".mod", version.Name, "", module.GoMod)
	return map[string]any{
		"name":       repo + "/goModules/" + url.PathEscape(module.Path) + ":" + module.Version,
		"version":    module.Version,
		"createTime": version.CreateTime,
		"updateTime": version.UpdateTime,
	}, nil
}

// arPublishKfp stores a pipeline template under the version its sha256 digest
// names, the name KfpArtifact derives from; an upload that conflicts with an
// existing one overwrites it, as the method's description says.
func arPublishKfp(repo string, req arUploadRequest, data []byte, contentType string) (any, error) {
	pipeline, err := arParseKfp(data)
	if err != nil {
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
	}
	version, _ := arRecordVersion(repo, pipeline.Name, pipeline.Digest)
	if req.Description != "" {
		version.Description = req.Description
		arVersions.Put(version.Name, version)
	}
	arPutFile(repo, pipeline.Digest, version.Name, contentType, data)
	pkg := repo + "/packages/" + url.PathEscape(pipeline.Name)
	for _, tag := range req.Tags {
		name := pkg + "/tags/" + tag
		arTags.Put(name, ARTag{Name: name, Version: version.Name})
	}
	return map[string]any{
		"name":    repo + "/kfpArtifacts/" + pipeline.Digest,
		"version": version.Name,
	}, nil
}

var arPackageParsers = map[string]func([]byte) (arPackageArtifact, error){
	"aptArtifacts":    arParseDeb,
	"yumArtifacts":    arParseRPM,
	"googetArtifacts": arParseGoo,
}

// arPublishPackage stores an Apt, Yum or GooGet package under the package,
// version and architecture its own metadata declares. An artifact that
// conflicts with an existing file is ignored, as the upload and import
// methods' descriptions say, and so is not reported as published.
func arPublishPackage(repo, kind string, data []byte, contentType string) (map[string]any, error) {
	parsed, err := arPackageParsers[kind](data)
	if err != nil {
		return nil, apiRefuse(http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
	}
	if _, exists := arFiles.Get(arFileName(repo, parsed.FileName)); exists {
		return nil, nil
	}
	version, _ := arRecordVersion(repo, parsed.PackageName, parsed.Version)
	arPutFile(repo, parsed.FileName, version.Name, contentType, data)
	artifact := map[string]any{
		"name":         repo + "/" + kind + "/" + parsed.PackageName + ":" + parsed.Version + ":" + parsed.Architecture,
		"packageName":  parsed.PackageName,
		"architecture": parsed.Architecture,
	}
	if parsed.PackageType != "" {
		artifact["packageType"] = parsed.PackageType
	}
	if parsed.ControlFile != nil {
		artifact["controlFile"] = base64.StdEncoding.EncodeToString(parsed.ControlFile)
	}
	return artifact, nil
}

// arHandleArtifactImport serves the :import methods: each reads the objects
// the request's Cloud Storage URIs name and publishes them as an upload does.
// A URI that names nothing, or an object that is not a package, is reported in
// the response's errors rather than failing the whole import.
func arHandleArtifactImport(w http.ResponseWriter, r *http.Request, repo Repository, kind string) {
	var req struct {
		GcsSource *struct {
			URIs         []string `json:"uris"`
			UseWildcards bool     `json:"useWildcards"`
		} `json:"gcsSource"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	if err := arCheckFormat(repo, kind); err != nil {
		writeAPIError(w, err)
		return
	}
	if req.GcsSource == nil || len(req.GcsSource.URIs) == 0 {
		GCPError(w, http.StatusBadRequest, "gcsSource.uris names no Cloud Storage object to import", "INVALID_ARGUMENT")
		return
	}
	published := []map[string]any{}
	failures := []map[string]any{}
	fail := func(uri string, code int, err error) {
		failures = append(failures, map[string]any{
			"gcsSource": map[string]any{"uris": []string{uri}, "useWildcards": req.GcsSource.UseWildcards},
			"error":     map[string]any{"code": code, "message": err.Error()},
		})
	}
	arPublishMu.Lock()
	for _, uri := range req.GcsSource.URIs {
		objects, code, err := arGCSSourceObjects(uri, req.GcsSource.UseWildcards)
		if err != nil {
			fail(uri, code, err)
			continue
		}
		for _, obj := range objects {
			objectURI := "gs://" + obj.Bucket + "/" + obj.Name
			data, err := gcsObjectBytes(obj)
			if err != nil {
				fail(objectURI, 13, err) // INTERNAL
				continue
			}
			artifact, err := arPublishPackage(repo.Name, kind, data, obj.ContentType)
			if err != nil {
				fail(objectURI, 3, err) // INVALID_ARGUMENT
				continue
			}
			if artifact != nil {
				published = append(published, artifact)
			}
		}
	}
	arPublishMu.Unlock()
	project, location, _ := arRepoParts(repo.Name)
	ops := arArtifactOperations[kind]
	response := map[string]any{kind: published, "errors": failures}
	sim.WriteJSON(w, http.StatusOK, newLRO(project, location, response,
		arTypePrefix+ops.importResponse, gcpEmptyOperationMetadata(arTypePrefix+ops.importMetadata)))
}

// arGCSSourceObjects resolves a gs://bucket/object URI to the live objects it
// names; with wildcards, the object part is a pattern matched against every
// object name in the bucket. A failure carries its google.rpc.Code:
// INVALID_ARGUMENT (3) for a malformed URI, NOT_FOUND (5) for one naming
// nothing.
func arGCSSourceObjects(uri string, wildcards bool) ([]GCSObject, int, error) {
	const invalidArgument, notFound = 3, 5
	rest, ok := strings.CutPrefix(uri, "gs://")
	bucket, object, found := strings.Cut(rest, "/")
	if !ok || !found || bucket == "" || object == "" {
		return nil, invalidArgument, fmt.Errorf("%q is not a Cloud Storage object URI", uri)
	}
	if _, exists := gcsBuckets.Get(bucket); !exists {
		return nil, notFound, fmt.Errorf("bucket %q not found", bucket)
	}
	if !wildcards {
		obj, exists := gcsObjects.Get(bucket + "/" + object)
		if !exists {
			return nil, notFound, fmt.Errorf("object %q not found", uri)
		}
		return []GCSObject{obj}, 0, nil
	}
	if _, err := path.Match(object, ""); err != nil {
		return nil, invalidArgument, fmt.Errorf("%q is not a valid wildcard: %v", object, err)
	}
	var matched []GCSObject
	for _, entry := range gcsObjects.ListPrefix(bucket + "/") {
		if ok, _ := path.Match(object, entry.Item.Name); ok && entry.Item.Bucket == bucket {
			matched = append(matched, entry.Item)
		}
	}
	if len(matched) == 0 {
		return nil, notFound, fmt.Errorf("no object matches %q", uri)
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].Name < matched[j].Name })
	return matched, 0, nil
}

// arExportFile is one file of an exported artifact.
type arExportFile struct {
	id, name, contentType string
	data                  []byte
}

// arVersionFiles returns the files an artifact version consists of: the Files
// it owns, or, for a Docker image, whose layers other images may share and so
// own, its manifest and every manifest and blob that manifest references, each
// named by its digest.
func arVersionFiles(repo string, version ARVersion) ([]arExportFile, error) {
	var files []arExportFile
	if repository, _ := arRepos.Get(repo); repository.Format != "DOCKER" {
		owned := arFiles.Filter(func(f ARFile) bool { return f.Owner == version.Name })
		sort.Slice(owned, func(i, j int) bool { return owned[i].Name < owned[j].Name })
		for _, f := range owned {
			data, err := arFileBytes(f)
			if err != nil {
				return nil, err
			}
			files = append(files, arExportFile{id: arFileID(f.Name), name: f.Name, contentType: f.ContentType, data: data})
		}
		if len(files) > 0 {
			return files, nil
		}
	}
	pkgID, digest, ok := strings.Cut(strings.TrimPrefix(version.Name, repo+"/packages/"), "/versions/")
	if !ok || arRegistry == nil {
		return nil, fmt.Errorf("version %q has no files", version.Name)
	}
	imagePath, err := url.PathUnescape(pkgID)
	if err != nil {
		return nil, fmt.Errorf("version %q has no files", version.Name)
	}
	project, _, repoID := arRepoParts(repo)
	dockerRepo := project + "/" + repoID + "/" + imagePath
	seen := map[string]bool{}
	var walk func(digest, contentType string, manifest bool) error
	walk = func(digest, contentType string, manifest bool) error {
		if seen[digest] {
			return nil
		}
		seen[digest] = true
		var data []byte
		if manifest {
			stored := arRegistry.Manifests.Filter(func(m sim.OCIManifest) bool { return m.Repo == dockerRepo && m.Digest == digest })
			if len(stored) == 0 {
				return fmt.Errorf("manifest %s of %s is not in the registry", digest, dockerRepo)
			}
			data, contentType = stored[0].Data, stored[0].ContentType
		} else {
			blob, found := arRegistry.GetBlob("", dockerRepo, digest)
			if !found {
				return fmt.Errorf("blob %s of %s is not in the registry", digest, dockerRepo)
			}
			data = blob
		}
		files = append(files, arExportFile{id: digest, name: arFileName(repo, digest), contentType: contentType, data: data})
		if !manifest {
			return nil
		}
		var doc struct {
			Config    *arDescriptor  `json:"config"`
			Layers    []arDescriptor `json:"layers"`
			Manifests []arDescriptor `json:"manifests"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("manifest %s of %s: %w", digest, dockerRepo, err)
		}
		if doc.Config != nil {
			if err := walk(doc.Config.Digest, doc.Config.MediaType, false); err != nil {
				return err
			}
		}
		for _, layer := range doc.Layers {
			if err := walk(layer.Digest, layer.MediaType, false); err != nil {
				return err
			}
		}
		for _, child := range doc.Manifests {
			if err := walk(child.Digest, child.MediaType, true); err != nil {
				return err
			}
		}
		return nil
	}
	if err := walk(digest, "", true); err != nil {
		return nil, err
	}
	return files, nil
}

type arDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
}

// arHandleExportArtifact writes every file of the selected version into the
// Cloud Storage path the request names: gcsPath is the bucket and, optionally,
// a directory, and each file lands at that directory joined with its file ID,
// overwriting an object already there.
func arHandleExportArtifact(w http.ResponseWriter, r *http.Request, repo string) {
	var req struct {
		SourceVersion string `json:"sourceVersion"`
		SourceTag     string `json:"sourceTag"`
		GcsPath       string `json:"gcsPath"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	name, _, err := arArtifactSelector(repo, req.SourceVersion, req.SourceTag)
	if err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
		return
	}
	version, ok := arResolveVersion(name, arVersions, arTags)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "artifact %q not found", name)
		return
	}
	bucket, directory, _ := strings.Cut(req.GcsPath, "/")
	if bucket == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "gcsPath %q must start with a bucket name", req.GcsPath)
		return
	}
	if _, exists := gcsBuckets.Get(bucket); !exists {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "bucket %q not found", bucket)
		return
	}
	files, err := arVersionFiles(repo, version)
	if err != nil {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%v", err)
		return
	}
	directory = strings.Trim(directory, "/")
	exported := make([]map[string]any, 0, len(files))
	for _, f := range files {
		object := path.Join(directory, f.id)
		if _, err := persistGCSObjectBytes(bucket, object, f.data, GCSObject{ContentType: f.contentType}, gcsPreconditions{}); err != nil {
			writeGCSPersistError(w, "export artifact", err)
			return
		}
		exported = append(exported, map[string]any{
			"name":          f.name,
			"gcsObjectPath": bucket + "/" + object,
			"hashes":        arHashes(f.data),
		})
	}
	project, location, _ := arRepoParts(repo)
	sim.WriteJSON(w, http.StatusOK, newLRO(project, location, map[string]any{"exportedVersion": version},
		arTypePrefix+"ExportArtifactResponse",
		gcpFixedOperationMetadata(map[string]any{
			"@type":         arTypePrefix + "ExportArtifactMetadata",
			"exportedFiles": exported,
		})))
}

// arServeFileDownload answers files.download: with alt=media the file's bytes,
// otherwise the DownloadFileResponse message, which declares no members.
func arServeFileDownload(w http.ResponseWriter, r *http.Request, name string) {
	file, ok := arFiles.Get(name)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "file %q not found", name)
		return
	}
	if r.URL.Query().Get("alt") != "media" {
		sim.WriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	data, err := arFileBytes(file)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "%v", err)
		return
	}
	contentType := file.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// arFileBytes reads a file's content: from the registry for a Docker
// repository's manifest or blob, from arFileContents for any other.
func arFileBytes(f ARFile) ([]byte, error) {
	if f.DockerRepo == "" {
		content, ok := arFileContents.Get(f.Name)
		if !ok {
			return nil, fmt.Errorf("file %q has no stored content", f.Name)
		}
		return content.Data, nil
	}
	digest := arFileID(f.Name)
	if arRegistry != nil {
		manifests := arRegistry.Manifests.Filter(func(m sim.OCIManifest) bool { return m.Repo == f.DockerRepo && m.Digest == digest })
		if len(manifests) > 0 {
			return manifests[0].Data, nil
		}
		if data, ok := arRegistry.GetBlob("", f.DockerRepo, digest); ok {
			return data, nil
		}
	}
	return nil, fmt.Errorf("file %q is not in the registry", f.Name)
}

// arDigestHashes reports a digest-named file's SHA256, which is the digest
// itself.
func arDigestHashes(digest string) []ARHash {
	sum, err := hex.DecodeString(strings.TrimPrefix(digest, "sha256:"))
	if err != nil || !strings.HasPrefix(digest, "sha256:") {
		return nil
	}
	return []ARHash{{Type: "SHA256", Value: base64.StdEncoding.EncodeToString(sum)}}
}

// arDockerFilesMu serializes reconciling a Docker repository's files, which a
// push, a registry DELETE and a control-plane delete all do.
var arDockerFilesMu sync.Mutex

// arSyncDockerFiles makes a Docker repository's Files the manifests its
// registry holds and the config and layer blobs they reference, each named by
// its digest. A blob several images share is one file, owned by the version
// of the image that pushed it first, and stays while any manifest still
// references it.
func arSyncDockerFiles(repo string) {
	if arRegistry == nil || arFiles == nil {
		return
	}
	project, _, repoID := arRepoParts(repo)
	if project == "" {
		return
	}
	arDockerFilesMu.Lock()
	defer arDockerFilesMu.Unlock()
	prefix := project + "/" + repoID + "/"
	manifests := arRegistry.Manifests.Filter(func(m sim.OCIManifest) bool {
		return strings.HasPrefix(m.Repo, prefix) && m.Ref == m.Digest
	})
	sort.Slice(manifests, func(i, j int) bool {
		if !manifests[i].Pushed.Equal(manifests[j].Pushed) {
			return manifests[i].Pushed.Before(manifests[j].Pushed)
		}
		return manifests[i].Repo+manifests[i].Digest < manifests[j].Repo+manifests[j].Digest
	})
	type dockerFile struct {
		size        int
		contentType string
		owners      []string
		dockerRepos []string
	}
	want := map[string]*dockerFile{}
	add := func(digest string, size int, contentType, owner, dockerRepo string) {
		f := want[digest]
		if f == nil {
			f = &dockerFile{size: size, contentType: contentType}
			want[digest] = f
		}
		f.owners = append(f.owners, owner)
		f.dockerRepos = append(f.dockerRepos, dockerRepo)
	}
	for _, m := range manifests {
		imagePath := strings.TrimPrefix(m.Repo, prefix)
		version := repo + "/packages/" + url.PathEscape(imagePath) + "/versions/" + m.Digest
		add(m.Digest, len(m.Data), m.ContentType, version, m.Repo)
		var doc struct {
			Config *arDescriptor  `json:"config"`
			Layers []arDescriptor `json:"layers"`
		}
		if err := json.Unmarshal(m.Data, &doc); err != nil {
			continue
		}
		blobs := doc.Layers
		if doc.Config != nil {
			blobs = append([]arDescriptor{*doc.Config}, blobs...)
		}
		for _, d := range blobs {
			if data, ok := arRegistry.GetBlob("", m.Repo, d.Digest); ok {
				add(d.Digest, len(data), d.MediaType, version, m.Repo)
			}
		}
	}
	now := nowTimestamp()
	for digest, f := range want {
		name := arFileName(repo, digest)
		existing, exists := arFiles.Get(name)
		if exists && existing.DockerRepo == "" {
			continue
		}
		owner, dockerRepo := f.owners[0], f.dockerRepos[0]
		if exists {
			for i, candidate := range f.owners {
				if candidate == existing.Owner {
					owner, dockerRepo = candidate, f.dockerRepos[i]
					break
				}
			}
			if existing.Owner == owner && existing.DockerRepo == dockerRepo {
				continue
			}
		}
		file := ARFile{
			Name:        name,
			SizeBytes:   strconv.Itoa(f.size),
			Hashes:      arDigestHashes(digest),
			CreateTime:  now,
			UpdateTime:  now,
			Owner:       owner,
			ContentType: f.contentType,
			DockerRepo:  dockerRepo,
		}
		if exists {
			file.CreateTime, file.Annotations = existing.CreateTime, existing.Annotations
		}
		arFiles.Put(name, file)
	}
	for _, f := range arFiles.Filter(func(f ARFile) bool {
		return f.DockerRepo != "" && strings.HasPrefix(f.Name, repo+"/files/")
	}) {
		if _, keep := want[arFileID(f.Name)]; !keep {
			arFiles.Delete(f.Name)
		}
	}
}
