package main

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"io"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// bigqueryDiscoveryDocument is the BigQuery API v2 Discovery document,
// byte-identical to specs/cloud-api/gcp/bigquery-v2.discovery.json.gz.
//
//go:embed discovery/bigquery-v2.discovery.json.gz
var bigqueryDiscoveryDocument []byte

// gcpServiceBigQuery is the service label of bigquery.googleapis.com.
const gcpServiceBigQuery = "bigquery"

// registerDiscoveryService serves BigQuery's Discovery document where the API
// serves it, `GET /$discovery/rest?version=v2`. The bq CLI fetches it from any
// API root that is not Google's default and builds its client from it. A Host
// naming another Google API has no document here; a bare address:port names no
// API, and BigQuery's is the one document the simulator serves.
func registerDiscoveryService(srv *sim.Server) {
	srv.HandleFunc("GET /$discovery/rest", func(w http.ResponseWriter, r *http.Request) {
		service, version := gcpServiceFromHost(r), r.URL.Query().Get("version")
		if (service != "" && service != gcpServiceBigQuery) || version != "v2" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no Discovery document for API %q version %q", service, version)
			return
		}
		reader, err := gzip.NewReader(bytes.NewReader(bigqueryDiscoveryDocument))
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery document: %v", err)
			return
		}
		doc, err := io.ReadAll(reader)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery document: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = w.Write(doc)
	})
}
