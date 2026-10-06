package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const vendoredDiscoveryDir = "../specs/cloud-api/gcp"

// vendoredDiscoveryFiles lists the vendored Discovery documents by base name.
func vendoredDiscoveryFiles(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(vendoredDiscoveryDir, "*.discovery.json.gz"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no vendored Discovery documents: %v", err)
	}
	for i, f := range files {
		files[i] = filepath.Base(f)
	}
	return files
}

func gunzip(t *testing.T, compressed []byte) []byte {
	t.Helper()
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// Every embedded Discovery document is its vendored one, byte for byte, and
// every vendored document is embedded.
func TestDiscoveryDocumentsAreTheVendoredOnes(t *testing.T) {
	vendored := vendoredDiscoveryFiles(t)
	for _, name := range vendored {
		want, err := os.ReadFile(filepath.Join(vendoredDiscoveryDir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, err := discoveryDocuments.ReadFile("discovery/" + name)
		if err != nil {
			t.Fatalf("%s is vendored but not embedded; copy it into discovery/", name)
		}
		if !bytes.Equal(want, got) {
			t.Fatalf("discovery/%s differs from %s/%s; copy the vendored document over it", name, vendoredDiscoveryDir, name)
		}
	}
	embedded, err := discoveryDocuments.ReadDir("discovery")
	if err != nil {
		t.Fatal(err)
	}
	if len(embedded) != len(vendored) {
		t.Fatalf("%d documents embedded, %d vendored", len(embedded), len(vendored))
	}
}

// Each API serves its own Discovery document under its own host, its regional
// host and its mTLS host; another version, or a host that names no API the
// simulator implements, has none.
func TestDiscoveryDocumentsServedUnderTheirHosts(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	get := func(host, version string) (int, []byte) {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+"/$discovery/rest?version="+version, nil))
		return rec.Code, rec.Body.Bytes()
	}
	docs, err := gcpDiscoveryIndex()
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != len(vendoredDiscoveryFiles(t)) {
		t.Fatalf("indexed %d documents", len(docs))
	}
	for _, d := range docs {
		vendored, err := os.ReadFile(filepath.Join(vendoredDiscoveryDir, strings.TrimPrefix(d.file, "discovery/")))
		if err != nil {
			t.Fatal(err)
		}
		want := gunzip(t, vendored)
		for _, host := range []string{d.name + ".googleapis.com", "us-central1-" + d.name + ".googleapis.com", d.name + ".mtls.googleapis.com"} {
			code, body := get(host, d.version)
			if code != http.StatusOK || !bytes.Equal(body, want) {
				t.Fatalf("%s version %s answered %d with %d bytes, want the %d bytes of %s", host, d.version, code, len(body), len(want), d.file)
			}
		}
		if code, _ := get(d.name+".googleapis.com", "v0"); code != http.StatusNotFound {
			t.Fatalf("%s version v0 answered %d", d.name, code)
		}
	}
	if code, _ := get("www.googleapis.com", "v1"); code != http.StatusNotFound {
		t.Fatalf("www.googleapis.com answered %d", code)
	}
}

// A bare address:port names no API: a version one implemented API publishes
// is that API's, v2 is BigQuery's (the document the bq CLI asks its API root
// for), and v1, which many publish, is none.
func TestDiscoveryDocumentAtABareOrigin(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	for version, want := range map[string]string{
		"v2":      "bigquery-v2.discovery.json.gz",
		"v3":      "cloudresourcemanager-v3.discovery.json.gz",
		"v1b3":    "dataflow-v1b3.discovery.json.gz",
		"v1beta4": "sqladmin-v1beta4.discovery.json.gz",
		"v1":      "",
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://127.0.0.1:4568/$discovery/rest?version="+version, nil))
		if want == "" {
			if rec.Code != http.StatusNotFound {
				t.Fatalf("version %s at a bare origin answered %d", version, rec.Code)
			}
			continue
		}
		vendored, err := os.ReadFile(filepath.Join(vendoredDiscoveryDir, want))
		if err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), gunzip(t, vendored)) {
			t.Fatalf("version %s at a bare origin answered %d, want %s", version, rec.Code, want)
		}
	}
}

// The directory capture holds an entry for exactly the embedded documents.
func TestDiscoveryDirectoryCoversTheEmbeddedDocuments(t *testing.T) {
	docs, err := gcpDiscoveryIndex()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := gcpDiscoveryDirectoryCapture()
	if err != nil {
		t.Fatal(err)
	}
	embedded := map[string]bool{}
	for _, d := range docs {
		embedded[d.name+":"+d.version] = true
	}
	listed := map[string]bool{}
	for _, e := range dir.entries {
		if e.ID != e.Name+":"+e.Version {
			t.Fatalf("directory entry %q names %s %s", e.ID, e.Name, e.Version)
		}
		if !embedded[e.ID] {
			t.Fatalf("directory entry %s has no embedded document", e.ID)
		}
		listed[e.ID] = true
	}
	for id := range embedded {
		if !listed[id] {
			t.Fatalf("embedded document %s has no directory entry; recapture discovery_directory_vendored.json", id)
		}
	}
	for host, ids := range dir.CentralRest {
		for _, id := range ids {
			if !listed[id] {
				t.Fatalf("centralRest[%s] names %s, which the directory does not list", host, id)
			}
		}
	}
}

func serveDiscovery(t *testing.T, target string) (int, []byte) {
	t.Helper()
	srv := buildOperationsTestSimulator(t)
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec.Code, rec.Body.Bytes()
}

// discovery.apis.list answers the directory at www.googleapis.com, at
// discovery.googleapis.com and at a bare origin, filtered by name and by
// preferred; another API's host serves no directory.
func TestDiscoveryDirectoryList(t *testing.T) {
	ids := func(body []byte) []string {
		var list struct {
			Kind             string `json:"kind"`
			DiscoveryVersion string `json:"discoveryVersion"`
			Items            []struct {
				ID string `json:"id"`
			} `json:"items"`
		}
		if err := json.Unmarshal(body, &list); err != nil {
			t.Fatal(err)
		}
		if list.Kind != "discovery#directoryList" || list.DiscoveryVersion != "v1" {
			t.Fatalf("directory list %s", body)
		}
		var out []string
		for _, item := range list.Items {
			out = append(out, item.ID)
		}
		return out
	}
	for _, origin := range []string{"http://www.googleapis.com", "http://discovery.googleapis.com", "http://127.0.0.1:4567"} {
		code, body := serveDiscovery(t, origin+"/discovery/v1/apis?name=run")
		if code != http.StatusOK || !slices.Equal(ids(body), []string{"run:v1", "run:v2"}) {
			t.Fatalf("%s name=run answered %d %s", origin, code, body)
		}
	}
	code, body := serveDiscovery(t, "http://www.googleapis.com/discovery/v1/apis?name=run&preferred=true")
	if code != http.StatusOK || !slices.Equal(ids(body), []string{"run:v2"}) {
		t.Fatalf("name=run&preferred=true answered %d %s", code, body)
	}
	code, body = serveDiscovery(t, "http://www.googleapis.com/discovery/v1/apis?name=nope")
	if code != http.StatusOK || strings.Contains(string(body), `"items"`) {
		t.Fatalf("name=nope answered %d %s", code, body)
	}
	code, body = serveDiscovery(t, "http://www.googleapis.com/discovery/v1/apis?preferred=maybe")
	if code != http.StatusBadRequest || !strings.Contains(string(body), `Invalid value at 'preferred' (TYPE_BOOL), \"maybe\"`) ||
		!strings.Contains(string(body), "type.googleapis.com/google.rpc.BadRequest") {
		t.Fatalf("preferred=maybe answered %d %s", code, body)
	}
	if code, _ := serveDiscovery(t, "http://run.googleapis.com/discovery/v1/apis"); code != http.StatusNotFound {
		t.Fatalf("run.googleapis.com served the directory: %d", code)
	}
}

// discovery.apis.getRest serves a document only where the central path of
// that host serves it.
func TestDiscoveryDirectoryGetRest(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   string
	}{
		{"http://www.googleapis.com/discovery/v1/apis/compute/v1/rest", "compute-v1.discovery.json.gz"},
		{"http://127.0.0.1:4567/discovery/v1/apis/bigquery/v2/rest", "bigquery-v2.discovery.json.gz"},
		{"http://discovery.googleapis.com/discovery/v1/apis/run/v1/rest", "cloudrun-v1.discovery.json.gz"},
		{"http://discovery.googleapis.com/discovery/v1/apis/compute/v1/rest", ""},
		{"http://www.googleapis.com/discovery/v1/apis/run/v2/rest", ""},
		{"http://www.googleapis.com/discovery/v1/apis/nope/v1/rest", ""},
	} {
		code, body := serveDiscovery(t, tc.target)
		if tc.want == "" {
			if code != http.StatusNotFound || !strings.Contains(string(body), "Requested entity was not found.") {
				t.Fatalf("%s answered %d %s", tc.target, code, body)
			}
			continue
		}
		vendored, err := os.ReadFile(filepath.Join(vendoredDiscoveryDir, tc.want))
		if err != nil {
			t.Fatal(err)
		}
		if code != http.StatusOK || !bytes.Equal(body, gunzip(t, vendored)) {
			t.Fatalf("%s answered %d, want %s", tc.target, code, tc.want)
		}
	}
}

// A request with no version parameter gets the API's default version, and a
// version an API does not publish names the API and version in its 404.
func TestDiscoveryDocumentDefaultVersion(t *testing.T) {
	code, body := serveDiscovery(t, "http://run.googleapis.com/$discovery/rest")
	vendored, err := os.ReadFile(filepath.Join(vendoredDiscoveryDir, "cloudrun-v2.discovery.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK || !bytes.Equal(body, gunzip(t, vendored)) {
		t.Fatalf("run.googleapis.com without a version answered %d", code)
	}
	code, body = serveDiscovery(t, "http://run.googleapis.com/$discovery/rest?version=v9")
	if code != http.StatusNotFound || !strings.Contains(string(body), "Discovery document not found for API service: run.googleapis.com format: rest version: v9") {
		t.Fatalf("run v9 answered %d %s", code, body)
	}
}
