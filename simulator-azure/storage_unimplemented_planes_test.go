package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// A storage account has six data planes, and its ARM response advertises all
// six endpoints because real Azure serves all six. The simulator serves four:
// Blob, Files, Queues and Tables. Static website (`{account}.web.…`) and Azure
// Data Lake Storage Gen2 (`{account}.dfs.…`) are declared gaps, and a request
// that arrives at either endpoint has to say so — a bare 200 with an empty body
// tells a caller that an operation it never performed succeeded, and it tells a
// coverage measurement that the plane is implemented.

// storagePlaneHostRequest issues one request at an arbitrary storage data-plane
// hostname, which is how the host-addressed storage middleware is reached.
func storagePlaneHostRequest(t *testing.T, srv *sim.Server, method, host, target string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Host = host
	req.Header.Set("x-ms-version", "2025-01-05")
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func assertUnimplementedStoragePlane(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("%s = %d, want 501 (body %s)", what, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-ms-error-code"); got != "NotImplemented" {
		t.Fatalf("%s x-ms-error-code = %q, want NotImplemented", what, got)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/xml" {
		t.Fatalf("%s Content-Type = %q, want application/xml", what, got)
	}
	if body := rec.Body.String(); !strings.Contains(body, "<Code>NotImplemented</Code>") {
		t.Fatalf("%s body = %s, want an Azure Storage error envelope", what, body)
	}
}

func TestStorageStaticWebsitePlaneDeclaresGap(t *testing.T) {
	srv, dataDir := newFilesTestSim(t)
	const account = "webacct"

	for _, tc := range []struct {
		method, host, target string
		body                 []byte
	}{
		{http.MethodGet, account + ".web.localhost", "/", nil},
		{http.MethodGet, account + ".web.localhost", "/index.html", nil},
		{http.MethodGet, account + ".web.shim.localhost", "/index.html", nil},
		{http.MethodPut, account + ".web.localhost", "/$web/index.html", []byte("<html></html>")},
		{http.MethodGet, account + ".web.localhost", "/?restype=service&comp=properties", nil},
	} {
		assertUnimplementedStoragePlane(t, storagePlaneHostRequest(t, srv, tc.method, tc.host, tc.target, tc.body),
			tc.method+" "+tc.host+tc.target)
	}

	// A write refused at the static-website endpoint changes nothing: the share
	// spelling it shares with the Files plane creates no share, and nothing
	// lands on disk.
	assertUnimplementedStoragePlane(t, storagePlaneHostRequest(t, srv, http.MethodPut,
		account+".web.localhost", "/never-a-share?restype=share", nil), "PUT ?restype=share at the web endpoint")
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodGet, account, "/never-a-share?restype=share", nil, nil),
		http.StatusNotFound, "GetShareProperties after a refused static-website write")
	if entries, err := os.ReadDir(dataDir); err != nil || len(entries) != 0 {
		t.Fatalf("share root after refused static-website writes = %v (err %v), want it empty", entries, err)
	}
}

func TestStorageDataLakeGen2PlaneDeclaresGap(t *testing.T) {
	srv, dataDir := newFilesTestSim(t)
	const account = "dfsacct"

	for _, tc := range []struct {
		method, host, target string
		body                 []byte
	}{
		{http.MethodGet, account + ".dfs.localhost", "/?resource=account", nil},
		{http.MethodPut, account + ".dfs.localhost", "/myfs?resource=filesystem", nil},
		{http.MethodPut, account + ".dfs.shim.localhost", "/myfs/dir/file.txt?resource=file", nil},
		{http.MethodPatch, account + ".dfs.localhost", "/myfs/dir/file.txt?action=append&position=0", []byte("data")},
		{http.MethodDelete, account + ".dfs.localhost", "/myfs?resource=filesystem", nil},
	} {
		assertUnimplementedStoragePlane(t, storagePlaneHostRequest(t, srv, tc.method, tc.host, tc.target, tc.body),
			tc.method+" "+tc.host+tc.target)
	}

	// The Data Lake Storage Gen2 plane addresses the same containers the Blob
	// plane does; a refused write there must not create one.
	assertUnimplementedStoragePlane(t, storagePlaneHostRequest(t, srv, http.MethodPut,
		account+".dfs.localhost", "/never-a-filesystem?restype=container", nil), "PUT ?restype=container at the dfs endpoint")
	rec := storagePlaneHostRequest(t, srv, http.MethodGet, account+".blob.localhost", "/never-a-filesystem?restype=container", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("GetContainerProperties after a refused Data Lake Storage Gen2 write = %d, want 404 (body %s)",
			rec.Code, rec.Body.String())
	}
	if entries, err := os.ReadDir(dataDir); err != nil || len(entries) != 0 {
		t.Fatalf("share root after refused Data Lake Storage Gen2 writes = %v (err %v), want it empty", entries, err)
	}
}

// TestStorageAccountAdvertisesOnlyPlanesThatAnswer keeps the advertised
// endpoints and the served planes in one story: every endpoint the ARM storage
// account publishes resolves to a handler, and the two the simulator does not
// implement answer with the declared gap instead of a success.
func TestStorageAccountAdvertisesOnlyPlanesThatAnswer(t *testing.T) {
	srv, _ := newFilesTestSim(t)
	const account = "endpointacct"

	served := map[string]int{
		"blob":  http.StatusNotFound, // GET of a container that does not exist
		"file":  http.StatusNotFound,
		"queue": http.StatusNotFound,
	}
	for service, want := range served {
		rec := storagePlaneHostRequest(t, srv, http.MethodGet, account+"."+service+".localhost",
			"/absent?restype="+map[string]string{"blob": "container", "file": "share", "queue": "queue"}[service], nil)
		if service == "queue" {
			// Queues address the queue itself with comp=metadata.
			rec = storagePlaneHostRequest(t, srv, http.MethodGet, account+".queue.localhost", "/absent?comp=metadata", nil)
		}
		if rec.Code != want {
			t.Fatalf("%s plane answered %d for an absent resource, want %d (body %s)",
				service, rec.Code, want, rec.Body.String())
		}
	}

	assertUnimplementedStoragePlane(t, storagePlaneHostRequest(t, srv, http.MethodGet,
		account+".web.localhost", "/", nil), "static website endpoint")
	assertUnimplementedStoragePlane(t, storagePlaneHostRequest(t, srv, http.MethodGet,
		account+".dfs.localhost", "/", nil), "Data Lake Storage Gen2 endpoint")
}

// TestFilesHostRootIsolation guards the assumption the tests above rely on:
// SIM_AZURE_FILES_DATA_DIR moves the whole share root, so a share's directory
// is always <root>/<account>/<share>.
func TestFilesHostRootIsolation(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SIM_AZURE_FILES_DATA_DIR", root)
	if got, want := FileShareHostDir("acct", "share"), filepath.Join(root, "acct", "share"); got != want {
		t.Fatalf("FileShareHostDir = %s, want %s", got, want)
	}
	if _, ok := fileShareHostPath("acct", "share", "../escape"); ok {
		t.Fatal("fileShareHostPath accepted a parent-directory path")
	}
	if _, ok := fileShareHostPath("acct", "..", "file.txt"); ok {
		t.Fatal("fileShareHostPath accepted a parent-directory share name")
	}
	if got, ok := fileShareHostPath("acct", "share", "dir/file.txt"); !ok ||
		got != filepath.Join(root, "acct", "share", "dir", "file.txt") {
		t.Fatalf("fileShareHostPath = %s (ok %v), want the nested path inside the share", got, ok)
	}
}
