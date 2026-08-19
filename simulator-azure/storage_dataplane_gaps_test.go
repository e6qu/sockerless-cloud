package main

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The Blob / Files / Queues data planes select an operation from the
// `restype` + `comp` query pair. For a value a dispatcher does not serve it
// must say so — never run whichever sibling handler happens to sit under the
// same method. The difference is not cosmetic: falling through means
// `PUT /{container}/{blob}?comp=tier` (Set Blob Tier) CREATES A BLOB and
// answers 201, so the caller is told a tier change succeeded that never
// happened, and a coverage measurement of the plane counts an operation the
// simulator has never implemented.
//
// Every gap case below therefore asserts two things: the declared gap in the
// response, and — the part that actually catches the bug — that the store is
// untouched. A test that only checked the status code would still pass if the
// handler answered 501 after creating the blob.

func buildStorageTestSim(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	return srv
}

// storagePlaneReq issues one request at a storage data-plane coordinate: the
// `<account>.<service>.<host>` hostname Azure publishes the plane on, which is
// how the simulator's host-addressed middleware reaches it.
func storagePlaneReq(t *testing.T, srv *sim.Server, method, account, service, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	var req *http.Request
	if reader != nil {
		req = httptest.NewRequest(method, target, reader)
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Host = account + "." + service + ".localhost"
	req.Header.Set("x-ms-version", "2025-01-05")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// assertStorageGap asserts the response is a declared gap: 501 in the storage
// XML error envelope, with the machine-readable code in x-ms-error-code where
// every Azure Storage SDK looks for it.
func assertStorageGap(t *testing.T, rec *httptest.ResponseRecorder, what string) {
	t.Helper()
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("%s: status = %d, want 501 — the dispatcher answered instead of declaring the gap: %s",
			what, rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("x-ms-error-code"); got != "NotImplemented" {
		t.Fatalf("%s: x-ms-error-code = %q, want NotImplemented", what, got)
	}
	var body struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: response is not a storage XML error: %v (%s)", what, err, rec.Body.String())
	}
	if body.Code != "NotImplemented" || !strings.Contains(body.Message, "not implemented by the simulator") {
		t.Fatalf("%s: error body = %+v, want a NotImplemented gap declaration", what, body)
	}
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s: status = %d, want %d: %s", what, rec.Code, want, rec.Body.String())
	}
}

// ── Blob ────────────────────────────────────────────────────────────

func TestBlobDataPlaneServedOperations(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, container, blob = "gapblobacct", "served-container", "served.txt"

	// Get Blob Service Properties. The azurerm provider polls this while waiting
	// for a storage account's data plane to come up, so a gap here fails an
	// apply rather than merely leaving an operation unimplemented.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/?restype=service&comp=properties", nil, nil),
		http.StatusOK, "GetBlobServiceProperties")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodHead, account, "blob", "/?restype=service&comp=properties", nil, nil),
		http.StatusOK, "GetBlobServiceProperties (HEAD)")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusCreated, "CreateContainer")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusOK, "GetContainerProperties")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodHead, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusOK, "GetContainerProperties (HEAD)")

	rec := storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/"+blob, []byte("v1"),
		map[string]string{"x-ms-blob-type": "BlockBlob"})
	assertStatus(t, rec, http.StatusCreated, "PutBlob")

	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/"+blob, nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlob")
	if got := rec.Body.String(); got != "v1" {
		t.Fatalf("GetBlob body = %q, want %q", got, "v1")
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodHead, account, "blob", "/"+container+"/"+blob, nil, nil),
		http.StatusOK, "GetBlobProperties")

	// A ranged Get Blob answers 206 with Content-Range. Clients that read a
	// blob in chunks — the az CLI's `storage blob download` among them — fail
	// outright when a ranged request is answered 200 with the whole blob.
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/"+blob, nil,
		map[string]string{"x-ms-range": "bytes=1-1"})
	assertStatus(t, rec, http.StatusPartialContent, "GetBlob (ranged)")
	if got := rec.Body.String(); got != "1" {
		t.Fatalf("ranged GetBlob body = %q, want %q", got, "1")
	}
	if got := rec.Header().Get("Content-Range"); got != "bytes 1-1/2" {
		t.Fatalf("Content-Range = %q, want %q", got, "bytes 1-1/2")
	}
	// A range running past the blob is clamped, exactly as Azure clamps it —
	// the az CLI asks for the whole 4 MiB first chunk of a two-byte blob.
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/"+blob, nil,
		map[string]string{"Range": "bytes=0-4194303"})
	assertStatus(t, rec, http.StatusPartialContent, "GetBlob (clamped range)")
	if got := rec.Header().Get("Content-Range"); got != "bytes 0-1/2" {
		t.Fatalf("clamped Content-Range = %q, want %q", got, "bytes 0-1/2")
	}
	if got := rec.Body.String(); got != "v1" {
		t.Fatalf("clamped ranged GetBlob body = %q, want %q", got, "v1")
	}
	// A start past the end is unsatisfiable.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/"+blob, nil,
		map[string]string{"x-ms-range": "bytes=9-9"}), http.StatusRequestedRangeNotSatisfiable, "GetBlob (out of range)")

	// Copy Blob: the bare PUT plus x-ms-copy-source, exactly as Azure spells it.
	rec = storagePlaneReq(t, srv, http.MethodPut, account, "blob", "/"+container+"/copy.txt", nil,
		map[string]string{"x-ms-copy-source": "http://" + account + ".blob.localhost/" + container + "/" + blob})
	assertStatus(t, rec, http.StatusAccepted, "CopyBlob")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"/copy.txt", nil, nil)
	if got := rec.Body.String(); got != "v1" {
		t.Fatalf("copied blob body = %q, want %q", got, "v1")
	}

	// Staged blocks then a commit, and the block list read back.
	blockID := "YmxvY2sx"
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"/blocks.txt?comp=block&blockid="+blockID, []byte("chunk"), nil),
		http.StatusCreated, "StageBlock")
	commit := []byte(`<?xml version="1.0" encoding="utf-8"?><BlockList><Latest>` + blockID + `</Latest></BlockList>`)
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "blob",
		"/"+container+"/blocks.txt?comp=blocklist", commit, nil),
		http.StatusCreated, "CommitBlockList")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob",
		"/"+container+"/blocks.txt?comp=blocklist&blocklisttype=all", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetBlockList")
	if !strings.Contains(rec.Body.String(), blockID) {
		t.Fatalf("GetBlockList body = %s, want it to name block %s", rec.Body.String(), blockID)
	}

	// Listing at both levels.
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/"+container+"?restype=container&comp=list", nil, nil)
	assertStatus(t, rec, http.StatusOK, "ListBlobs")
	if !strings.Contains(rec.Body.String(), blob) {
		t.Fatalf("ListBlobs body = %s, want it to name %s", rec.Body.String(), blob)
	}
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "blob", "/?comp=list", nil, nil)
	assertStatus(t, rec, http.StatusOK, "ListContainers")
	if !strings.Contains(rec.Body.String(), container) {
		t.Fatalf("ListContainers body = %s, want it to name %s", rec.Body.String(), container)
	}

	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "blob", "/"+container+"/"+blob, nil, nil),
		http.StatusAccepted, "DeleteBlob")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "blob", "/"+container+"?restype=container", nil, nil),
		http.StatusAccepted, "DeleteContainer")
}

// ── Files ───────────────────────────────────────────────────────────

func TestFilesDataPlaneServedOperations(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, share, file = "gapfileacct", "served-share", "served.txt"

	// Get File Service Properties, the Files sibling of the Blob operation the
	// azurerm provider polls.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "file", "/?restype=service&comp=properties", nil, nil),
		http.StatusOK, "GetFileServiceProperties")
	payload := []byte("hello files")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusOK, "GetShareProperties")

	accessPolicy := []byte(`<SignedIdentifiers><SignedIdentifier><Id>served-policy</Id><AccessPolicy>` +
		`<Start>2026-07-28T10:00:00Z</Start><Expiry>2026-07-29T10:00:00Z</Expiry>` +
		`<Permission>rwdl</Permission></AccessPolicy></SignedIdentifier></SignedIdentifiers>`)
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/"+share+"?restype=share&comp=acl", accessPolicy, nil), http.StatusOK, "SetShareACL")
	rec := storagePlaneReq(t, srv, http.MethodGet, account, "file",
		"/"+share+"?restype=share&comp=acl", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetShareACL")
	if !strings.Contains(rec.Body.String(), "<Id>served-policy</Id>") ||
		!strings.Contains(rec.Body.String(), "<Permission>rwdl</Permission>") {
		t.Fatalf("GetShareACL body = %s, want stored access policy", rec.Body.String())
	}

	// Create File allocates the declared size; Upload Range fills it.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file, nil,
		map[string]string{"x-ms-type": "file", "x-ms-content-length": fmt.Sprintf("%d", len(payload))}),
		http.StatusCreated, "CreateFile")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/"+file, nil, nil)
	assertStatus(t, rec, http.StatusOK, "DownloadFile (allocated)")
	if got := rec.Body.Bytes(); len(got) != len(payload) || strings.Trim(string(got), "\x00") != "" {
		t.Fatalf("freshly created file = %q, want %d zero bytes", got, len(payload))
	}
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", payload,
		map[string]string{"x-ms-write": "update", "x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)}),
		http.StatusCreated, "UploadRange")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/"+file, nil, nil)
	assertStatus(t, rec, http.StatusOK, "DownloadFile")
	if got := rec.Body.String(); got != string(payload) {
		t.Fatalf("DownloadFile body = %q, want %q", got, payload)
	}

	// A partial range write lands at its offset and leaves the rest intact.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", []byte("HELLO"),
		map[string]string{"x-ms-range": "bytes=0-4"}), http.StatusCreated, "UploadRange (partial)")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/"+file, nil, nil)
	if got, want := rec.Body.String(), "HELLO"+string(payload[5:]); got != want {
		t.Fatalf("DownloadFile body = %q, want %q", got, want)
	}
	// x-ms-write: clear zeroes the range instead of writing a body.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", nil,
		map[string]string{"x-ms-write": "clear", "x-ms-range": "bytes=0-4"}), http.StatusCreated, "UploadRange (clear)")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/"+file, nil, nil)
	if got, want := rec.Body.String(), "\x00\x00\x00\x00\x00"+string(payload[5:]); got != want {
		t.Fatalf("DownloadFile body = %q after clear, want %q", got, want)
	}
	// A range past the allocated size is rejected — Upload Range never grows a
	// file on Azure Files.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range",
		[]byte("xxx"), map[string]string{"x-ms-range": fmt.Sprintf("bytes=%d-%d", len(payload), len(payload)+2)}),
		http.StatusRequestedRangeNotSatisfiable, "UploadRange (out of range)")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodHead, account, "file", "/"+share+"/"+file, nil, nil),
		http.StatusOK, "GetFileProperties")

	// A ranged Download File answers 206 with Content-Range, like Get Blob.
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/"+file, nil,
		map[string]string{"x-ms-range": "bytes=5-9"})
	assertStatus(t, rec, http.StatusPartialContent, "DownloadFile (ranged)")
	if got := rec.Header().Get("Content-Range"); got != fmt.Sprintf("bytes 5-9/%d", len(payload)) {
		t.Fatalf("Content-Range = %q, want bytes 5-9/%d", got, len(payload))
	}
	if got, want := rec.Body.String(), string(payload[5:10]); got != want {
		t.Fatalf("ranged DownloadFile body = %q, want %q", got, want)
	}
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"?restype=directory&comp=list", nil, nil)
	assertStatus(t, rec, http.StatusOK, "ListFilesAndDirectories")
	if !strings.Contains(rec.Body.String(), file) {
		t.Fatalf("directory listing = %s, want it to name %s", rec.Body.String(), file)
	}
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/?comp=list", nil, nil)
	assertStatus(t, rec, http.StatusOK, "ListShares")
	if !strings.Contains(rec.Body.String(), share) {
		t.Fatalf("share listing = %s, want it to name %s", rec.Body.String(), share)
	}

	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"/"+file, nil, nil),
		http.StatusAccepted, "DeleteFile")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusAccepted, "DeleteShare")
}

func TestFilesDataPlaneUnservedCompDeclaresGap(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, share, file = "gapfileacct2", "gap-share", "gap.txt"
	payload := []byte("hello files")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file, nil,
		map[string]string{"x-ms-content-length": fmt.Sprintf("%d", len(payload))}), http.StatusCreated, "CreateFile")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", payload,
		map[string]string{"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)}), http.StatusCreated, "UploadRange")

	fileBody := func() string {
		rec := storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/"+file, nil, nil)
		assertStatus(t, rec, http.StatusOK, "DownloadFile")
		return rec.Body.String()
	}

	// A `comp` the Files data plane does not define must not fall through to
	// Create File, which would overwrite the file and report 201.
	for _, comp := range []string{"tier", "acl", "blocklist", "snapshot", "undelete"} {
		assertStorageGap(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
			"/"+share+"/"+file+"?comp="+comp, []byte("clobber"), nil), "PUT file ?comp="+comp)
		if got := fileBody(); got != string(payload) {
			t.Fatalf("file contents = %q after a refused ?comp=%s, want %q", got, comp, payload)
		}
	}
	// A `restype` naming no Files resource kind is refused the same way.
	for _, restype := range []string{"container", "junction"} {
		assertStorageGap(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
			"/"+share+"/"+file+"?restype="+restype, []byte("clobber"), nil), "PUT file ?restype="+restype)
		if got := fileBody(); got != string(payload) {
			t.Fatalf("file contents = %q after a refused ?restype=%s, want %q", got, restype, payload)
		}
	}
	// DELETE carries no `comp` on any Files operation, so one must not delete.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file",
		"/"+share+"/"+file+"?comp=lease", nil, nil), "DELETE file ?comp=lease")
	if got := fileBody(); got != string(payload) {
		t.Fatalf("file contents = %q after a refused DELETE ?comp=lease, want %q", got, payload)
	}
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file",
		"/"+share+"?restype=share&comp=lease", nil, nil), "DELETE share ?comp=lease")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusOK, "share survives a refused DELETE ?comp=lease")

	// A share operation addressed with a comp the service does not define must
	// not create the share it names.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/never-a-share?restype=share&comp=tier", nil, nil), "PUT share ?comp=tier")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "file", "/never-a-share?restype=share", nil, nil),
		http.StatusNotFound, "the refused ?comp=tier created no share")

	// The share root is addressed as a share or as a directory, never as a
	// file, so a bare PUT on it is not Create File.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share, nil,
		map[string]string{"x-ms-content-length": "5"}), "PUT share root as a file")
	// Nor is the share root a directory anyone creates.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/"+share+"?restype=directory", nil, nil), "PUT share root ?restype=directory")

	// The service level defines no listing under `restype=service`.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodGet, account, "file",
		"/?restype=service&comp=list", nil, nil), "GET service ?comp=list")
}

// TestFilesDataPlaneDirectoryAndShareOperationsAreServed drives the operations
// that make a nested Azure Files path usable — the ones an Azure Container Apps
// or Azure Functions workload's volume story depends on — end to end through the
// data plane, then looks at the directory a mounting workload sees.
func TestFilesDataPlaneNestedPathsAreReal(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, share = "nestedfileacct", "nested-share"
	payload := []byte("nested payload")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")

	// A directory is only created inside a directory that exists.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/"+share+"/a/b?restype=directory", nil, nil), http.StatusNotFound, "CreateDirectory without its parent")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/"+share+"/a?restype=directory", nil, map[string]string{"x-ms-meta-owner": "sockerless"}),
		http.StatusCreated, "CreateDirectory a")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/"+share+"/a/b?restype=directory", nil, nil), http.StatusCreated, "CreateDirectory a/b")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file",
		"/"+share+"/a?restype=directory", nil, nil), http.StatusConflict, "CreateDirectory on an existing name")

	rec := storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/a?restype=directory", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetDirectoryProperties")
	if got := rec.Header().Get("x-ms-meta-owner"); got != "sockerless" {
		t.Fatalf("x-ms-meta-owner = %q on Get Directory Properties, want %q", got, "sockerless")
	}

	// A file at depth lands in the real nested directory.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/a/b/deep.txt", nil,
		map[string]string{"x-ms-content-length": fmt.Sprintf("%d", len(payload))}), http.StatusCreated, "CreateFile at depth")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/a/b/deep.txt?comp=range", payload,
		map[string]string{"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)}), http.StatusCreated, "UploadRange at depth")
	onDisk := filepath.Join(FileShareHostDir(account, share), "a", "b", "deep.txt")
	if got, err := os.ReadFile(onDisk); err != nil || string(got) != string(payload) {
		t.Fatalf("file at %s = %q (err %v), want %q", onDisk, got, err, payload)
	}

	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/a/b?restype=directory&comp=list", nil, nil)
	assertStatus(t, rec, http.StatusOK, "ListFilesAndDirectories at depth")
	if !strings.Contains(rec.Body.String(), "deep.txt") {
		t.Fatalf("listing at depth = %s, want it to name deep.txt", rec.Body.String())
	}

	// Rename moves the entry in the share's directory tree.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/a/b/renamed.txt?comp=rename", nil,
		map[string]string{"x-ms-file-rename-source": "http://" + account + ".file.localhost/" + share + "/a/b/deep.txt"}),
		http.StatusOK, "RenameFile")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/a/b/deep.txt", nil, nil),
		http.StatusNotFound, "the renamed source is gone")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "file", "/"+share+"/a/b/renamed.txt", nil, nil)
	assertStatus(t, rec, http.StatusOK, "DownloadFile after rename")
	if rec.Body.String() != string(payload) {
		t.Fatalf("renamed file = %q, want %q", rec.Body.String(), payload)
	}

	// A directory holding anything cannot be deleted.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"/a/b?restype=directory", nil, nil),
		http.StatusConflict, "DeleteDirectory on a non-empty directory")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"/a/b/renamed.txt", nil, nil),
		http.StatusAccepted, "DeleteFile at depth")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"/a/b?restype=directory", nil, nil),
		http.StatusAccepted, "DeleteDirectory")
	if _, err := os.Stat(filepath.Join(FileShareHostDir(account, share), "a", "b")); !os.IsNotExist(err) {
		t.Fatalf("the deleted directory is still on disk: %v", err)
	}
}

// TestFilesDataPlaneLeaseGuardsWrites: a lease on a file or share is a lock, and
// a write without the matching lease id is refused with the service's own
// precondition failure.
func TestFilesDataPlaneLeaseGuardsWrites(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, share, file = "leasefileacct", "lease-share", "leased.txt"
	payload := []byte("leased payload")

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file, nil,
		map[string]string{"x-ms-content-length": fmt.Sprintf("%d", len(payload))}), http.StatusCreated, "CreateFile")

	rec := storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=lease", nil,
		map[string]string{"x-ms-lease-action": "acquire", "x-ms-proposed-lease-id": "11111111-2222-3333-4444-555555555555"})
	assertStatus(t, rec, http.StatusCreated, "AcquireFileLease")
	leaseID := rec.Header().Get("x-ms-lease-id")
	if leaseID != "11111111-2222-3333-4444-555555555555" {
		t.Fatalf("x-ms-lease-id = %q, want the proposed lease id", leaseID)
	}

	// A write with no lease id, and a write with the wrong one, are both
	// refused — and neither touches the file.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", payload,
		map[string]string{"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)}),
		http.StatusPreconditionFailed, "UploadRange without the lease id")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", payload,
		map[string]string{"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1),
			"x-ms-lease-id": "99999999-9999-9999-9999-999999999999"}),
		http.StatusPreconditionFailed, "UploadRange with the wrong lease id")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"/"+file, nil, nil),
		http.StatusPreconditionFailed, "DeleteFile without the lease id")

	// The holder of the lease writes.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=range", payload,
		map[string]string{"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1), "x-ms-lease-id": leaseID}),
		http.StatusCreated, "UploadRange with the lease id")

	// Releasing the lease reopens the file to everyone.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"/"+file+"?comp=lease", nil,
		map[string]string{"x-ms-lease-action": "release", "x-ms-lease-id": leaseID}),
		http.StatusOK, "ReleaseFileLease")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"/"+file, nil, nil),
		http.StatusAccepted, "DeleteFile after the lease was released")

	// The same lock on the share refuses Delete Share.
	rec = storagePlaneReq(t, srv, http.MethodPut, account, "file", "/"+share+"?restype=share&comp=lease", nil,
		map[string]string{"x-ms-lease-action": "acquire"})
	assertStatus(t, rec, http.StatusCreated, "AcquireShareLease")
	shareLease := rec.Header().Get("x-ms-lease-id")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"?restype=share", nil, nil),
		http.StatusPreconditionFailed, "DeleteShare without the share lease id")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "file", "/"+share+"?restype=share", nil,
		map[string]string{"x-ms-lease-id": shareLease}), http.StatusAccepted, "DeleteShare with the share lease id")
}

// ── Queues ──────────────────────────────────────────────────────────

func TestQueuesDataPlaneServedOperations(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, queue = "gapqueueacct", "served-queue"

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue, nil, nil),
		http.StatusCreated, "CreateQueue")

	// Set Queue Metadata replaces the metadata wholesale; Get Queue Metadata
	// reads back exactly what was set.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue+"?comp=metadata", nil,
		map[string]string{"x-ms-meta-owner": "sockerless"}), http.StatusNoContent, "SetQueueMetadata")
	rec := storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"?comp=metadata", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetQueueMetadata")
	if got := rec.Header().Get("x-ms-meta-owner"); got != "sockerless" {
		t.Fatalf("x-ms-meta-owner = %q after Set Queue Metadata, want %q", got, "sockerless")
	}

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPost, account, "queue", "/"+queue+"/messages",
		[]byte(`<QueueMessage><MessageText>aGVsbG8=</MessageText></QueueMessage>`), nil),
		http.StatusCreated, "PutMessage")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?peekonly=true", nil, nil)
	assertStatus(t, rec, http.StatusOK, "PeekMessages")
	if !strings.Contains(rec.Body.String(), "aGVsbG8=") {
		t.Fatalf("PeekMessages body = %s, want the enqueued message", rec.Body.String())
	}
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetMessages")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue", "/"+queue+"/messages", nil, nil),
		http.StatusNoContent, "ClearMessages")

	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/?comp=list", nil, nil)
	assertStatus(t, rec, http.StatusOK, "ListQueues")
	if !strings.Contains(rec.Body.String(), queue) {
		t.Fatalf("ListQueues body = %s, want it to name %s", rec.Body.String(), queue)
	}
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/?restype=service&comp=properties", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetServiceProperties")
	if !strings.Contains(rec.Body.String(), "<StorageServiceProperties>") {
		t.Fatalf("GetServiceProperties body = %s, want a StorageServiceProperties document", rec.Body.String())
	}

	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue", "/"+queue, nil, nil),
		http.StatusNoContent, "DeleteQueue")
}

func TestQueuesDataPlaneUnservedCompDeclaresGap(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, queue = "gapqueueacct2", "gap-queue"

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue, nil,
		map[string]string{"x-ms-meta-owner": "sockerless"}), http.StatusCreated, "CreateQueue")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPost, account, "queue", "/"+queue+"/messages",
		[]byte(`<QueueMessage><MessageText>aGVsbG8=</MessageText></QueueMessage>`), nil),
		http.StatusCreated, "PutMessage")

	// A `comp` the Queues data plane does not define must not fall through to
	// Create Queue, which for a name that does not exist would create it.
	for _, comp := range []string{"properties", "tier", "snapshot"} {
		assertStorageGap(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue",
			"/never-a-queue?comp="+comp, nil, nil), "PUT queue ?comp="+comp)
		assertStatus(t, storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/never-a-queue?comp=metadata", nil, nil),
			http.StatusNotFound, "the refused ?comp="+comp+" created no queue")
	}

	// A bare GET on the queue is not an operation Azure documents.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue, nil, nil),
		"GET queue (no comp)")

	// Delete Queue is the bare DELETE; with a comp it must not delete.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue", "/"+queue+"?comp=metadata", nil, nil),
		"DELETE queue ?comp=metadata")
	rec := storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"?comp=metadata", nil, nil)
	assertStatus(t, rec, http.StatusOK, "queue survives a refused DELETE ?comp=metadata")
	if got := rec.Header().Get("x-ms-meta-owner"); got != "sockerless" {
		t.Fatalf("queue metadata = %q after refused operations, want it unchanged", got)
	}

	// A comp on the messages collection names no documented operation, so it
	// must not enqueue, dequeue or clear.
	assertStorageGap(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue",
		"/"+queue+"/messages?comp=acl", nil, nil), "DELETE messages ?comp=acl")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?peekonly=true", nil, nil)
	if !strings.Contains(rec.Body.String(), "aGVsbG8=") {
		t.Fatalf("PeekMessages body = %s, want the message to survive a refused clear", rec.Body.String())
	}
}

// TestQueuesDataPlaneUpdateMessageExtendsInvisibility drives Update Message —
// the operation a long-running consumer calls to keep a message it is still
// working on invisible — through the pop-receipt check that makes it the
// dequeuer's alone.
func TestQueuesDataPlaneUpdateMessageExtendsInvisibility(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, queue = "updqueueacct", "upd-queue"

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue, nil, nil),
		http.StatusCreated, "CreateQueue")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPost, account, "queue", "/"+queue+"/messages",
		[]byte(`<QueueMessage><MessageText>aGVsbG8=</MessageText></QueueMessage>`), nil),
		http.StatusCreated, "PutMessage")

	// The dequeue takes a long invisibility window on purpose. What follows is
	// three more round trips before the pop receipt is used, and a one-second
	// window would let the message become visible again in between on a loaded
	// host — turning the receipt stale and failing the test for a reason it
	// does not test. The window's own expiry is not what is under test here.
	rec := storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages?visibilitytimeout=300", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetMessages")
	var dequeued struct {
		Messages []struct {
			MessageID  string `xml:"MessageId"`
			PopReceipt string `xml:"PopReceipt"`
		} `xml:"QueueMessage"`
	}
	if err := xml.Unmarshal(rec.Body.Bytes(), &dequeued); err != nil || len(dequeued.Messages) != 1 {
		t.Fatalf("GetMessages = %s (err %v), want one message", rec.Body.String(), err)
	}
	id, receipt := dequeued.Messages[0].MessageID, dequeued.Messages[0].PopReceipt

	// A stale pop receipt updates nothing.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue",
		"/"+queue+"/messages/"+id+"?popreceipt=not-the-receipt&visibilitytimeout=60", nil, nil),
		http.StatusBadRequest, "UpdateMessage with a stale pop receipt")

	// The dequeuer extends the invisibility and replaces the content; the
	// response supersedes the pop receipt it was given.
	rec = storagePlaneReq(t, srv, http.MethodPut, account, "queue",
		"/"+queue+"/messages/"+id+"?popreceipt="+receipt+"&visibilitytimeout=600",
		[]byte(`<QueueMessage><MessageText>b3RoZXI=</MessageText></QueueMessage>`), nil)
	assertStatus(t, rec, http.StatusNoContent, "UpdateMessage")
	updated := rec.Header().Get("x-ms-popreceipt")
	if updated == "" || updated == receipt {
		t.Fatalf("x-ms-popreceipt = %q, want a fresh receipt superseding %q", updated, receipt)
	}
	if rec.Header().Get("x-ms-time-next-visible") == "" {
		t.Fatalf("Update Message must report x-ms-time-next-visible")
	}

	// The extended invisibility holds: the message is not handed to another
	// consumer, and a peek does not see it either.
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"/messages", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetMessages during the extended invisibility")
	if strings.Contains(rec.Body.String(), "<MessageId>") {
		t.Fatalf("GetMessages = %s, want no message while the invisibility holds", rec.Body.String())
	}

	// The superseded receipt can no longer delete the message; the fresh one can.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue",
		"/"+queue+"/messages/"+id+"?popreceipt="+receipt, nil, nil),
		http.StatusBadRequest, "DeleteMessage with the superseded pop receipt")
	assertStatus(t, storagePlaneReq(t, srv, http.MethodDelete, account, "queue",
		"/"+queue+"/messages/"+id+"?popreceipt="+updated, nil, nil),
		http.StatusNoContent, "DeleteMessage with the fresh pop receipt")
}

// TestQueuesDataPlaneAccessPolicyAndServiceProperties drives the queue's stored
// access policies and the service-level configuration a write must actually
// change.
func TestQueuesDataPlaneAccessPolicyAndServiceProperties(t *testing.T) {
	srv := buildStorageTestSim(t)
	const account, queue = "aclqueueacct", "acl-queue"

	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue, nil, nil),
		http.StatusCreated, "CreateQueue")

	policy := []byte(`<SignedIdentifiers><SignedIdentifier><Id>consumer</Id><AccessPolicy>` +
		`<Start>2026-07-28T10:00:00Z</Start><Expiry>2026-07-29T10:00:00Z</Expiry>` +
		`<Permission>rap</Permission></AccessPolicy></SignedIdentifier></SignedIdentifiers>`)
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/"+queue+"?comp=acl", policy, nil),
		http.StatusNoContent, "SetQueueAccessPolicy")
	rec := storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/"+queue+"?comp=acl", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetQueueAccessPolicy")
	if !strings.Contains(rec.Body.String(), "<Id>consumer</Id>") ||
		!strings.Contains(rec.Body.String(), "<Permission>rap</Permission>") {
		t.Fatalf("GetQueueAccessPolicy body = %s, want the stored policy", rec.Body.String())
	}

	// Set Service Properties used to be answered by the GET handler, so a write
	// was reported as applied while nothing changed.
	assertStatus(t, storagePlaneReq(t, srv, http.MethodPut, account, "queue", "/?restype=service&comp=properties",
		[]byte(`<StorageServiceProperties><Logging><Version>1.0</Version><Delete>true</Delete>`+
			`<Read>true</Read><Write>true</Write><RetentionPolicy><Enabled>true</Enabled>`+
			`<Days>4</Days></RetentionPolicy></Logging></StorageServiceProperties>`), nil),
		http.StatusAccepted, "SetServiceProperties")
	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/?restype=service&comp=properties", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetServiceProperties")
	if !strings.Contains(rec.Body.String(), "<Days>4</Days>") {
		t.Fatalf("GetServiceProperties body = %s, want the properties Set Service Properties wrote", rec.Body.String())
	}

	rec = storagePlaneReq(t, srv, http.MethodGet, account, "queue", "/?restype=service&comp=stats", nil, nil)
	assertStatus(t, rec, http.StatusOK, "GetServiceStatistics")
	if !strings.Contains(rec.Body.String(), "<Status>live</Status>") {
		t.Fatalf("GetServiceStatistics body = %s, want a GeoReplication status", rec.Body.String())
	}
}
