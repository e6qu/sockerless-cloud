package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// The embedded BigQuery Discovery document is the vendored one, byte for byte.
func TestBigQueryDiscoveryDocumentIsTheVendoredOne(t *testing.T) {
	vendored, err := os.ReadFile("../specs/cloud-api/gcp/bigquery-v2.discovery.json.gz")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(vendored, bigqueryDiscoveryDocument) {
		t.Fatal("discovery/bigquery-v2.discovery.json.gz differs from specs/cloud-api/gcp/bigquery-v2.discovery.json.gz; copy the vendored document over it")
	}
}

// BigQuery serves its Discovery document at its own host and at a bare
// origin; another API's host has none here.
func TestBigQueryDiscoveryDocumentServed(t *testing.T) {
	srv := buildOperationsTestSimulator(t)
	for host, want := range map[string]int{
		"bigquery.googleapis.com": http.StatusOK,
		"127.0.0.1:4568":          http.StatusOK,
		"run.googleapis.com":      http.StatusNotFound,
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "http://"+host+"/$discovery/rest?version=v2", nil))
		if rec.Code != want {
			t.Fatalf("%s answered %d, want %d", host, rec.Code, want)
		}
		if want != http.StatusOK {
			continue
		}
		var doc struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil || doc.Name != "bigquery" || doc.Version != "v2" {
			t.Fatalf("%s served %q %q: %v", host, doc.Name, doc.Version, err)
		}
	}
}
