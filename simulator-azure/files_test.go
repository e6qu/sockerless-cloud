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
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// The Azure Files data plane and a Container Apps volume mount are two views of
// one share: `azurerm_container_app_environment_storage` names an account and a
// share, a job or app mounts it as Volume{StorageType: AzureFile}, and the
// simulator's Jobs / Apps executor bind-mounts FileShareHostDir(account, share)
// at the container's mount path. So a file written through the data plane must
// be readable inside the container, and a file the container writes must be
// readable through the data plane — one share, one set of bytes.
//
// These tests drive the data plane through the simulator's real handler chain
// at the `<account>.file.<host>` coordinate Azure publishes it on, then look at
// the directory the executor mounts.

func newFilesTestSim(t *testing.T) (*sim.Server, string) {
	t.Helper()
	dataDir := t.TempDir()
	t.Setenv("SIM_AZURE_FILES_DATA_DIR", dataDir)
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "azure", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("build simulator: %v", err)
	}
	// Long-running operations complete in a goroutine. One still running
	// when this test ends would read and write the stores while the next
	// test rebuilds them.
	t.Cleanup(AwaitAzureAsyncOperations)
	return srv, dataDir
}

// filesPlaneRequest issues one Files data-plane request at the
// `<account>.file.<host>` hostname the storage account's primaryEndpoints
// advertise.
func filesPlaneRequest(t *testing.T, srv *sim.Server, method, account, target string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body != nil {
		req = httptest.NewRequest(method, target, bytes.NewReader(body))
	} else {
		req = httptest.NewRequest(method, target, nil)
	}
	req.Host = account + ".file.localhost"
	req.Header.Set("x-ms-version", "2025-01-05")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	// The Files plane authorizes every request against the account's keys, so
	// this carries the credential a client carries.
	storageTestSignSharedKey(t, srv, req, account)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

func requireFilesStatus(t *testing.T, rec *httptest.ResponseRecorder, want int, what string) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("%s = %d, want %d (body %s)", what, rec.Code, want, rec.Body.String())
	}
}

// createFilesShareWithFile creates a share and writes payload to fileName
// through Create File + Upload Range, the pair the azfile SDK's Create +
// UploadBuffer issues.
func createFilesShareWithFile(t *testing.T, srv *sim.Server, account, share, fileName string, payload []byte) {
	t.Helper()
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"/"+fileName, nil,
		map[string]string{"x-ms-type": "file", "x-ms-content-length": fmt.Sprintf("%d", len(payload))}),
		http.StatusCreated, "CreateFile")
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"/"+fileName+"?comp=range", payload,
		map[string]string{"x-ms-write": "update", "x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)}),
		http.StatusCreated, "UploadRange")
}

// TestFilesDataPlaneWritesWhereContainerAppsMounts is the round trip that makes
// an Azure Files volume work: what a data-plane client uploads is what a
// container mounting the share reads, byte for byte, at the same relative path.
func TestFilesDataPlaneWritesWhereContainerAppsMounts(t *testing.T) {
	srv, dataDir := newFilesTestSim(t)
	const account, share, fileName = "mountacct", "mount-share", "workspace.txt"
	payload := []byte("bytes a mounting container must see")

	createFilesShareWithFile(t, srv, account, share, fileName, payload)

	// The bind the Container Apps executor builds for
	// Volume{StorageType: AzureFile} is FileShareHostDir(account, share) at the
	// container's mount path, so this is the file at <mountPath>/workspace.txt
	// inside the container.
	mounted := filepath.Join(FileShareHostDir(account, share), fileName)
	got, err := os.ReadFile(mounted)
	if err != nil {
		t.Fatalf("read the mounted share at %s: %v", mounted, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("mounted file = %q, want %q", got, payload)
	}

	// The mount path is under the configured share root, not somewhere else on
	// the host: the executor and the data plane agree on the layout.
	if want := filepath.Join(dataDir, account, share, fileName); mounted != want {
		t.Fatalf("mounted path = %s, want %s", mounted, want)
	}

	// A non-root workload must be able to write into the mounted share, the way
	// a real Azure Files SMB mount is writable by the mounting container.
	info, err := os.Stat(mounted)
	if err != nil {
		t.Fatalf("stat the mounted file: %v", err)
	}
	if info.Mode().Perm()&0o066 != 0o066 {
		t.Fatalf("mounted file mode = %v, want group and other write", info.Mode().Perm())
	}
}

// TestFilesDataPlaneReadsWhatAMountingContainerWrote is the other direction: a
// job writes its output into the mounted share and a data-plane client
// downloads it.
func TestFilesDataPlaneReadsWhatAMountingContainerWrote(t *testing.T) {
	srv, _ := newFilesTestSim(t)
	const account, share = "outputacct", "output-share"
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")

	// The container writes a result file into its mount.
	produced := []byte("job output written inside the container")
	if err := os.WriteFile(filepath.Join(FileShareHostDir(account, share), "result.txt"), produced, 0o666); err != nil {
		t.Fatalf("write into the mounted share: %v", err)
	}

	rec := filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"/result.txt", nil, nil)
	requireFilesStatus(t, rec, http.StatusOK, "DownloadFile")
	if got := rec.Body.String(); got != string(produced) {
		t.Fatalf("DownloadFile body = %q, want %q", got, produced)
	}
	if got, want := rec.Header().Get("Content-Length"), fmt.Sprintf("%d", len(produced)); got != want {
		t.Fatalf("Content-Length = %q, want %q", got, want)
	}

	rec = filesPlaneRequest(t, srv, http.MethodHead, account, "/"+share+"/result.txt", nil, nil)
	requireFilesStatus(t, rec, http.StatusOK, "GetFileProperties")

	rec = filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"?restype=directory&comp=list", nil, nil)
	requireFilesStatus(t, rec, http.StatusOK, "ListFilesAndDirectories")
	if !strings.Contains(rec.Body.String(), "result.txt") {
		t.Fatalf("directory listing = %s, want it to name result.txt", rec.Body.String())
	}

	// A directory the container created is listed as a directory, never as a
	// file with a size.
	if err := os.Mkdir(filepath.Join(FileShareHostDir(account, share), "logs"), 0o777); err != nil {
		t.Fatalf("mkdir inside the mounted share: %v", err)
	}
	rec = filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"?restype=directory&comp=list", nil, nil)
	requireFilesStatus(t, rec, http.StatusOK, "ListFilesAndDirectories")
	listing := rec.Body.String()
	directories, files, err := parseFilesListing(listing)
	if err != nil {
		t.Fatalf("parse directory listing %s: %v", listing, err)
	}
	if len(directories) != 1 || directories[0] != "logs" {
		t.Fatalf("directory listing = %s, want the logs directory listed as a Directory", listing)
	}
	if len(files) != 1 || files[0] != "result.txt" {
		t.Fatalf("directory listing = %s, want result.txt listed as a File", listing)
	}
}

// TestFilesDataPlaneShareLifecycleOwnsTheMountedDirectory: the share's contents
// live and die with the share, so a container mounting a re-created share never
// sees a previous share's files.
func TestFilesDataPlaneShareLifecycleOwnsTheMountedDirectory(t *testing.T) {
	srv, _ := newFilesTestSim(t)
	const account, share, fileName = "lifeacct", "life-share", "kept.txt"
	payload := []byte("first generation")

	createFilesShareWithFile(t, srv, account, share, fileName, payload)
	shareDir := FileShareHostDir(account, share)

	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodDelete, account, "/"+share+"?restype=share", nil, nil),
		http.StatusAccepted, "DeleteShare")
	if _, err := os.Stat(shareDir); !os.IsNotExist(err) {
		t.Fatalf("share directory after Delete Share: %v, want it gone", err)
	}

	// Re-creating the share yields an empty one even though the directory was
	// repopulated behind the simulator's back.
	if err := os.MkdirAll(shareDir, 0o777); err != nil {
		t.Fatalf("recreate share dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(shareDir, "stale.txt"), []byte("stale"), 0o666); err != nil {
		t.Fatalf("write stale file: %v", err)
	}
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare (again)")
	if _, err := os.Stat(filepath.Join(shareDir, "stale.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale file after re-creating the share: %v, want it gone", err)
	}

	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"/"+fileName, nil, nil),
		http.StatusNotFound, "DownloadFile from the re-created share")
}

// TestFilesDataPlaneRefusesNamesThatLeaveTheShare: the account comes from the
// Host header and the share and path from the URL, so a name that resolves
// outside the share's directory must be refused rather than followed.
func TestFilesDataPlaneRefusesNamesThatLeaveTheShare(t *testing.T) {
	srv, dataDir := newFilesTestSim(t)
	const account, share = "escapeacct", "escape-share"
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"?restype=share", nil, nil),
		http.StatusCreated, "CreateShare")

	escaped := filepath.Join(dataDir, "escaped.txt")
	rec := filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"/../../escaped.txt", nil,
		map[string]string{"x-ms-content-length": "5"})
	if rec.Code < 400 {
		t.Fatalf("Create File through a parent-directory path = %d, want a refusal", rec.Code)
	}
	if _, err := os.Stat(escaped); !os.IsNotExist(err) {
		t.Fatalf("a file was created outside the share at %s: %v", escaped, err)
	}

	// A file inside the share is untouched by the refused write.
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"/escaped.txt", nil, nil),
		http.StatusNotFound, "DownloadFile of the refused name")
}

// TestFilesDataPlaneProjectsARMShareOntoTheMountedDirectory: a share created
// through Microsoft.Storage — what `azurerm_storage_share` and the SDK's
// FileSharesClient create — is the same share the data plane writes to and a
// Container Apps volume mounts.
func TestFilesDataPlaneProjectsARMShareOntoTheMountedDirectory(t *testing.T) {
	srv, _ := newFilesTestSim(t)
	const (
		account = "armacct"
		share   = "arm-share"
		rg      = "arm-files-rg"
		sub     = "00000000-0000-0000-0000-000000000001"
	)
	armBase := "/subscriptions/" + sub + "/resourceGroups/" + rg +
		"/providers/Microsoft.Storage/storageAccounts/" + account

	// The ARM plane requires a valid bearer; mint one the way a client acquires
	// it from the token endpoint.
	now := time.Now()
	token, err := mintAzureSimJWT(simTenantID, "https://management.azure.com/", now, now.Add(time.Hour))
	if err != nil {
		t.Fatalf("mint ARM bearer: %v", err)
	}

	armRequest := func(method, target string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		var req *http.Request
		if body != nil {
			req = httptest.NewRequest(method, target, bytes.NewReader(body))
		} else {
			req = httptest.NewRequest(method, target, nil)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}

	rec := armRequest(http.MethodPut, armBase+"?api-version=2023-01-01",
		[]byte(`{"location":"eastus","sku":{"name":"Standard_LRS"},"kind":"StorageV2"}`))
	requireFilesStatus(t, rec, http.StatusOK, "ARM create storage account")

	rec = armRequest(http.MethodPut, armBase+"/fileServices/default/shares/"+share+"?api-version=2023-01-01",
		[]byte(`{"properties":{"shareQuota":1024}}`))
	requireFilesStatus(t, rec, http.StatusOK, "ARM create file share")

	payload := []byte("written to an ARM-created share")
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"/arm.txt", nil,
		map[string]string{"x-ms-content-length": fmt.Sprintf("%d", len(payload))}),
		http.StatusCreated, "CreateFile on the ARM-created share")
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/"+share+"/arm.txt?comp=range", payload,
		map[string]string{"x-ms-range": fmt.Sprintf("bytes=0-%d", len(payload)-1)}),
		http.StatusCreated, "UploadRange on the ARM-created share")

	mounted := filepath.Join(FileShareHostDir(account, share), "arm.txt")
	got, err2 := os.ReadFile(mounted)
	if err2 != nil {
		t.Fatalf("read the mounted ARM share at %s: %v", mounted, err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("mounted file = %q, want %q", got, payload)
	}

	// The quota the ARM create declared is the quota the data plane reports.
	rec = filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"?restype=share", nil, nil)
	requireFilesStatus(t, rec, http.StatusOK, "GetShareProperties")
	if got := rec.Header().Get("x-ms-share-quota"); got != "1024" {
		t.Fatalf("x-ms-share-quota = %q, want 1024", got)
	}

	// A data-plane share is the same resource read back through ARM.
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodPut, account, "/dp-share?restype=share", nil, nil),
		http.StatusCreated, "CreateShare on the data plane")
	rec = armRequest(http.MethodGet, armBase+"/fileServices/default/shares/dp-share?api-version=2023-01-01", nil)
	requireFilesStatus(t, rec, http.StatusOK, "ARM get the data-plane-created share")

	// Deleting through ARM takes the mounted contents with it.
	rec = armRequest(http.MethodDelete, armBase+"/fileServices/default/shares/"+share+"?api-version=2023-01-01", nil)
	requireFilesStatus(t, rec, http.StatusOK, "ARM delete file share")
	if _, err := os.Stat(mounted); !os.IsNotExist(err) {
		t.Fatalf("mounted file after ARM delete: %v, want it gone", err)
	}
	requireFilesStatus(t, filesPlaneRequest(t, srv, http.MethodGet, account, "/"+share+"?restype=share", nil, nil),
		http.StatusNotFound, "GetShareProperties after ARM delete")
}

// parseFilesListing decodes a List Directories and Files response into the
// directory and file names it enumerates, so a test asserts what the listing
// says rather than how the XML happens to be laid out.
func parseFilesListing(body string) (directories, files []string, err error) {
	var doc struct {
		Entries struct {
			Directories []struct {
				Name string `xml:"Name"`
			} `xml:"Directory"`
			Files []struct {
				Name string `xml:"Name"`
			} `xml:"File"`
		} `xml:"Entries"`
	}
	if err := xml.Unmarshal([]byte(body), &doc); err != nil {
		return nil, nil, err
	}
	for _, d := range doc.Entries.Directories {
		directories = append(directories, d.Name)
	}
	for _, f := range doc.Entries.Files {
		files = append(files, f.Name)
	}
	return directories, files, nil
}
