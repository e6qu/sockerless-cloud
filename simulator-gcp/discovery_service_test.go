package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
		for _, host := range []string{d.service + ".googleapis.com", "us-central1-" + d.service + ".googleapis.com", d.service + ".mtls.googleapis.com"} {
			code, body := get(host, d.version)
			if code != http.StatusOK || !bytes.Equal(body, want) {
				t.Fatalf("%s version %s answered %d with %d bytes, want the %d bytes of %s", host, d.version, code, len(body), len(want), d.file)
			}
		}
		if code, _ := get(d.service+".googleapis.com", "v0"); code != http.StatusNotFound {
			t.Fatalf("%s version v0 answered %d", d.service, code)
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
