package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

const arTestHost = "artifactregistry.googleapis.com"

func arTestSend(t *testing.T, srv *sim.Server, method, target string, headers map[string]string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	if !strings.HasPrefix(target, "http") {
		target = "http://" + arTestHost + target
	}
	req := httptest.NewRequest(method, target, bytes.NewReader(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// arTestStartSession begins a resumable session and returns its session URI.
func arTestStartSession(t *testing.T, srv *sim.Server, path, request, mediaType string) string {
	t.Helper()
	rec := arTestSend(t, srv, http.MethodPost, path+"?uploadType=resumable&alt=json",
		map[string]string{"Content-Type": "application/json", "X-Upload-Content-Type": mediaType}, []byte(request))
	if rec.Code != http.StatusOK {
		t.Fatalf("start session answered %d %s", rec.Code, rec.Body)
	}
	location := rec.Header().Get("Location")
	parsed, err := url.Parse(location)
	if err != nil || parsed.Path != path || parsed.Query().Get("upload_id") == "" || parsed.Query().Get("alt") != "json" {
		t.Fatalf("session URI %q", location)
	}
	return location
}

// The Go client's flow: the session begins on the /upload path and every chunk
// is a POST to the session URI with X-GUploader-No-308, so an incomplete
// upload answers 200 with X-Http-Status-Code-Override: 308.
func TestARResumableFileUploadGoClientFlow(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "resumable-files", "GENERIC")
	content := bytes.Repeat([]byte("0123456789abcdef"), 40)
	session := arTestStartSession(t, srv, "/upload/v1/"+repo+"/files:upload", `{"fileId":"big.bin"}`, "application/x-big")

	for off := 0; off < len(content); off += 256 {
		end := min(off+256, len(content))
		rec := arTestSend(t, srv, http.MethodPost, session, map[string]string{
			"Content-Range":      fmt.Sprintf("bytes %d-%d/*", off, end-1),
			"Content-Type":       "application/x-big",
			"X-GUploader-No-308": "yes",
		}, content[off:end])
		if rec.Code != http.StatusOK || rec.Header().Get("X-Http-Status-Code-Override") != "308" {
			t.Fatalf("chunk at %d answered %d %v %s", off, rec.Code, rec.Header(), rec.Body)
		}
		if got := rec.Header().Get("Range"); got != fmt.Sprintf("bytes=0-%d", end-1) {
			t.Fatalf("chunk at %d: Range %q", off, got)
		}
	}
	rec := arTestSend(t, srv, http.MethodPost, session, map[string]string{
		"Content-Range": fmt.Sprintf("bytes */%d", len(content)), "X-GUploader-No-308": "yes",
	}, nil)
	if rec.Code != http.StatusOK || rec.Header().Get("X-Http-Status-Code-Override") != "" {
		t.Fatalf("final request answered %d %v %s", rec.Code, rec.Header(), rec.Body)
	}
	var out struct {
		Operation struct {
			Done     bool           `json:"done"`
			Response map[string]any `json:"response"`
		} `json:"operation"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || !out.Operation.Done ||
		out.Operation.Response["name"] != repo+"/files/big.bin" || out.Operation.Response["sizeBytes"] != fmt.Sprint(len(content)) {
		t.Fatalf("final response %s", rec.Body)
	}
	if got := arTestDownload(t, srv, repo+"/files/big.bin"); !bytes.Equal(got, content) {
		t.Fatalf("downloaded %d bytes, want %d", len(got), len(content))
	}
	code, body := arTestCall(t, srv, http.MethodGet, "/download/v1/"+repo+"/files/big.bin:download?alt=media", "", nil)
	if code != http.StatusOK || len(body) != len(content) {
		t.Fatalf("download answered %d", code)
	}

	// A finished session answers its status query with the method's response.
	again := arTestSend(t, srv, http.MethodPut, session, map[string]string{"Content-Range": "bytes */*"}, nil)
	if again.Code != http.StatusOK || !bytes.Equal(again.Body.Bytes(), rec.Body.Bytes()) {
		t.Fatalf("status of a finished session answered %d %s", again.Code, again.Body)
	}
}

// apitools' flow, which gcloud drives: the session begins on the
// /resumable/upload path and the chunks are PUTs answered with a plain 308.
func TestARResumableGenericUploadApitoolsFlow(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "resumable-generic", "GENERIC")
	content := []byte(strings.Repeat("generic payload ", 64))
	total := len(content)
	path := "/resumable/upload/v1/" + repo + "/genericArtifacts:create"
	session := arTestStartSession(t, srv, path, `{"packageId":"tools","versionId":"2.0.0","filename":"tools.bin"}`, "application/octet-stream")

	status := arTestSend(t, srv, http.MethodPut, session, map[string]string{"Content-Range": "bytes */*"}, nil)
	if status.Code != http.StatusPermanentRedirect || status.Header().Get("Range") != "" {
		t.Fatalf("status before any bytes answered %d Range %q", status.Code, status.Header().Get("Range"))
	}
	put := func(first, last int) *httptest.ResponseRecorder {
		return arTestSend(t, srv, http.MethodPut, session, map[string]string{
			"Content-Range": fmt.Sprintf("bytes %d-%d/%d", first, last, total),
		}, content[first:last+1])
	}
	if rec := put(0, 299); rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Range") != "bytes=0-299" {
		t.Fatalf("first chunk answered %d Range %q", rec.Code, rec.Header().Get("Range"))
	}
	// A retried chunk that overlaps what arrived replaces it.
	if rec := put(200, 599); rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Range") != "bytes=0-599" {
		t.Fatalf("overlapping chunk answered %d Range %q", rec.Code, rec.Header().Get("Range"))
	}
	if rec := put(700, 799); rec.Code != http.StatusBadRequest {
		t.Fatalf("a chunk past the received bytes answered %d", rec.Code)
	}
	status = arTestSend(t, srv, http.MethodPut, session, map[string]string{"Content-Range": "bytes */*"}, nil)
	if status.Code != http.StatusPermanentRedirect || status.Header().Get("Range") != "bytes=0-599" {
		t.Fatalf("status answered %d Range %q", status.Code, status.Header().Get("Range"))
	}
	rec := put(600, total-1)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"`+repo+`/genericArtifacts/tools:2.0.0"`) {
		t.Fatalf("last chunk answered %d %s", rec.Code, rec.Body)
	}
	if got := arTestDownload(t, srv, repo+"/files/tools:2.0.0:tools.bin"); !bytes.Equal(got, content) {
		t.Fatalf("downloaded %q", got)
	}

	// A session whose publish fails reports the method's error.
	session = arTestStartSession(t, srv, path, `{"packageId":"tools","versionId":"2.0.0","filename":"tools.bin"}`, "application/octet-stream")
	if rec := put(0, total-1); rec.Code != http.StatusConflict {
		t.Fatalf("a duplicate file answered %d %s", rec.Code, rec.Body)
	}

	// Cancelling a session answers 499 and forgets it.
	session = arTestStartSession(t, srv, path, `{"packageId":"tools","versionId":"3.0.0","filename":"tools.bin"}`, "application/octet-stream")
	if rec := put(0, 99); rec.Code != http.StatusPermanentRedirect {
		t.Fatalf("chunk answered %d", rec.Code)
	}
	if rec := arTestSend(t, srv, http.MethodDelete, session, nil, nil); rec.Code != 499 {
		t.Fatalf("cancel answered %d", rec.Code)
	}
	if rec := put(100, 199); rec.Code != http.StatusNotFound {
		t.Fatalf("a chunk to a cancelled session answered %d", rec.Code)
	}

	// A session URI belongs to the method it was begun on.
	session = arTestStartSession(t, srv, path, `{}`, "text/plain")
	other := strings.Replace(session, "genericArtifacts:create", "files:upload", 1)
	if rec := arTestSend(t, srv, http.MethodPut, other, map[string]string{"Content-Range": "bytes 0-0/1"}, []byte("x")); rec.Code != http.StatusNotFound {
		t.Fatalf("a session addressed through another method answered %d", rec.Code)
	}
	if rec := arTestSend(t, srv, http.MethodPost, path, map[string]string{"Content-Type": "application/json"}, []byte(`{}`)); rec.Code != http.StatusBadRequest {
		t.Fatalf("the resumable path without uploadType=resumable answered %d", rec.Code)
	}
}

// Each media method that declares the resumable protocol takes a session on
// either media path, with its chunks PUT and its cancellation a DELETE on the
// session URI.
func TestARResumableSessionRoutes(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "resumable-routes", "GENERIC")
	for i, path := range []string{
		"/upload/v1/" + repo + "/files:upload",
		"/resumable/upload/v1/" + repo + "/files:upload",
		"/upload/v1/" + repo + "/genericArtifacts:create",
		"/resumable/upload/v1/" + repo + "/genericArtifacts:create",
	} {
		request := fmt.Sprintf(`{"fileId":"f%d","packageId":"p","versionId":"%d.0","filename":"f"}`, i, i)
		session := arTestStartSession(t, srv, path, request, "text/plain")
		rec := arTestSend(t, srv, http.MethodPut, session, map[string]string{"Content-Range": "bytes 0-4/5"}, []byte("hello"))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"done":true`) {
			t.Fatalf("%s: chunk answered %d %s", path, rec.Code, rec.Body)
		}
		session = arTestStartSession(t, srv, path, request, "text/plain")
		if rec := arTestSend(t, srv, http.MethodDelete, session, nil, nil); rec.Code != 499 {
			t.Fatalf("%s: cancel answered %d", path, rec.Code)
		}
	}
}

func TestARFileDeleteIsForGenericRepositories(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	generic := arTestRepository(t, srv, "del-generic", "GENERIC")
	if code, out := arTestUpload(t, srv, generic, "files", `{"fileId":"a.txt"}`, []byte("a"), "text/plain"); code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	gcpHostOK(t, srv, arTestHost, http.MethodDelete, "/v1/"+generic+"/files/a.txt", ``)
	if code, _ := gcpHostCall(t, srv, arTestHost, http.MethodGet, "/v1/"+generic+"/files/a.txt", ``); code != http.StatusNotFound {
		t.Fatalf("the deleted file answered %d", code)
	}

	python := arTestRepository(t, srv, "del-python", "PYTHON")
	if code, out := arTestUpload(t, srv, python, "files", `{"fileId":"b.txt"}`, []byte("b"), "text/plain"); code != http.StatusOK {
		t.Fatalf("upload answered %d %v", code, out)
	}
	code, out := gcpHostCall(t, srv, arTestHost, http.MethodDelete, "/v1/"+python+"/files/b.txt", ``)
	if errBody, _ := out["error"].(map[string]any); code != http.StatusBadRequest || errBody["status"] != "FAILED_PRECONDITION" {
		t.Fatalf("deleting from a Python repository answered %d %v", code, out)
	}
	gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/"+python+"/files/b.txt", ``)
}

// A Docker push records a File per manifest and per blob it references, named
// by digest; a layer two images share is one file that outlives either image.
func TestARDockerPushRecordsFiles(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	repo := arTestRepository(t, srv, "push-files", "DOCKER")
	config := []byte(`{"architecture":"amd64","os":"linux"}`)
	shared, own := []byte("shared layer"), []byte("second image's layer")
	configDigest, sharedDigest, ownDigest := digestBytes(config), digestBytes(shared), digestBytes(own)
	push := func(image string, layers ...[]byte) string {
		arRegistry.PutBlob("", "p/push-files/"+image, configDigest, "application/octet-stream", config)
		descriptors := []string{}
		for _, layer := range layers {
			arRegistry.PutBlob("", "p/push-files/"+image, digestBytes(layer), "application/octet-stream", layer)
			descriptors = append(descriptors, fmt.Sprintf(`{"mediaType":"application/vnd.oci.image.layer.v1.tar","digest":%q,"size":%d}`, digestBytes(layer), len(layer)))
		}
		manifest := []byte(fmt.Sprintf(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"mediaType":"application/vnd.oci.image.config.v1+json","digest":%q,"size":%d},"layers":[%s]}`,
			configDigest, len(config), strings.Join(descriptors, ",")))
		arRegistry.PutManifest("", "p/push-files/"+image, "v1", "application/vnd.oci.image.manifest.v1+json", manifest)
		return digestBytes(manifest)
	}
	first := push("first", shared)
	second := push("second", shared, own)
	firstVersion := repo + "/packages/first/versions/" + first
	secondVersion := repo + "/packages/second/versions/" + second

	listed := gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/"+repo+"/files", ``)
	if files, _ := listed["files"].([]any); len(files) != 5 {
		t.Fatalf("files = %v", files)
	}
	file := gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/"+repo+"/files/"+sharedDigest, ``)
	sum := sha256.Sum256(shared)
	hashes, _ := file["hashes"].([]any)
	if file["owner"] != firstVersion || file["sizeBytes"] != fmt.Sprint(len(shared)) || len(hashes) != 1 ||
		hashes[0].(map[string]any)["value"] != base64.StdEncoding.EncodeToString(sum[:]) {
		t.Fatalf("shared layer file = %v", file)
	}
	if file := gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/"+repo+"/files/"+second, ``); file["owner"] != secondVersion {
		t.Fatalf("manifest file = %v", file)
	}
	if got := arTestDownload(t, srv, repo+"/files/"+ownDigest); !bytes.Equal(got, own) {
		t.Fatalf("downloaded %q", got)
	}

	// Deleting the first image hands the shared layer to the image still using it.
	gcpHostOK(t, srv, arTestHost, http.MethodDelete, "/v1/"+repo+"/packages/first/versions/"+first+"?force=true", ``)
	if code, _ := gcpHostCall(t, srv, arTestHost, http.MethodGet, "/v1/"+repo+"/files/"+first, ``); code != http.StatusNotFound {
		t.Fatalf("the deleted manifest's file answered %d", code)
	}
	if file := gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/"+repo+"/files/"+sharedDigest, ``); file["owner"] != secondVersion {
		t.Fatalf("shared layer file after delete = %v", file)
	}
	if got := arTestDownload(t, srv, repo+"/files/"+sharedDigest); !bytes.Equal(got, shared) {
		t.Fatalf("downloaded %q", got)
	}
	if code, _ := gcpHostCall(t, srv, arTestHost, http.MethodDelete, "/v1/"+repo+"/files/"+ownDigest, ``); code != http.StatusBadRequest {
		t.Fatalf("files.delete in a Docker repository answered %d", code)
	}

	gcpHostOK(t, srv, arTestHost, http.MethodDelete, "/v1/"+repo+"/packages/second", ``)
	listed = gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/"+repo+"/files", ``)
	if files, _ := listed["files"].([]any); len(files) != 0 {
		t.Fatalf("files after deleting every image = %v", files)
	}
}

func TestARRepositoryRegistryURIFollowsFormat(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	for format, host := range map[string]string{
		"DOCKER": "docker", "MAVEN": "maven", "NPM": "npm", "PYTHON": "python",
		"APT": "apt", "YUM": "yum", "GO": "go", "GENERIC": "generic", "KFP": "kfp",
	} {
		repoID := "uri-" + strings.ToLower(format)
		arTestRepository(t, srv, repoID, format)
		repo := gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/projects/p/locations/us-central1/repositories/"+repoID, ``)
		if want := "us-central1-" + host + ".pkg.dev/p/" + repoID; repo["registryUri"] != want {
			t.Errorf("%s registryUri = %v, want %s", format, repo["registryUri"], want)
		}
	}
	gcpHostOK(t, srv, arTestHost, http.MethodPost,
		"/v1/projects/example.com:proj/locations/europe-west1/repositories?repositoryId=scoped", `{"format":"DOCKER"}`)
	repo := gcpHostOK(t, srv, arTestHost, http.MethodGet, "/v1/projects/example.com:proj/locations/europe-west1/repositories/scoped", ``)
	if repo["registryUri"] != "europe-west1-docker.pkg.dev/example.com/proj/scoped" {
		t.Fatalf("domain-scoped registryUri = %v", repo["registryUri"])
	}
}
