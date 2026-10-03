package main

import (
	"bytes"
	"compress/gzip"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
)

// discoveryDocuments holds the Discovery document of every API the simulator
// implements, each byte-identical to its vendored copy in specs/cloud-api/gcp.
//
//go:embed discovery/*.discovery.json.gz
var discoveryDocuments embed.FS

// gcpDiscoveryDocument is one embedded Discovery document, keyed by the
// service label of its rootUrl's host and its version.
type gcpDiscoveryDocument struct {
	file    string
	name    string
	version string
	service string
}

// gcpServiceBigQuery is the service label of bigquery.googleapis.com.
const gcpServiceBigQuery = "bigquery"

var gcpDiscoveryIndex = sync.OnceValues(func() ([]gcpDiscoveryDocument, error) {
	files, err := fs.Glob(discoveryDocuments, "discovery/*.discovery.json.gz")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	docs := make([]gcpDiscoveryDocument, 0, len(files))
	for _, file := range files {
		body, err := readDiscoveryDocument(file)
		if err != nil {
			return nil, err
		}
		var head struct {
			Name    string `json:"name"`
			Version string `json:"version"`
			RootURL string `json:"rootUrl"`
		}
		if err := json.Unmarshal(body, &head); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		root, err := url.Parse(head.RootURL)
		if err != nil {
			return nil, fmt.Errorf("%s: rootUrl: %w", file, err)
		}
		service := gcpServiceFromHost(&http.Request{Host: root.Host})
		if service == "" || head.Version == "" {
			return nil, fmt.Errorf("%s names no Google API host and version (rootUrl %q, version %q)", file, head.RootURL, head.Version)
		}
		docs = append(docs, gcpDiscoveryDocument{file: file, name: head.Name, version: head.Version, service: service})
	}
	return docs, nil
})

func readDiscoveryDocument(file string) ([]byte, error) {
	compressed, err := discoveryDocuments.ReadFile(file)
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file, err)
	}
	return io.ReadAll(reader)
}

// gcpDiscoveryDocumentFor picks the document a request for version asks for.
// Under an API's own host it is that API's. A bare address:port names no API,
// so there it is the one API the simulator implements at that version, and
// for v2, which several publish, BigQuery's: the bq CLI fetches its document
// from whatever API root it is given.
func gcpDiscoveryDocumentFor(docs []gcpDiscoveryDocument, service, version string) (gcpDiscoveryDocument, bool) {
	var matches []gcpDiscoveryDocument
	for _, d := range docs {
		if d.version != version {
			continue
		}
		if service == "" && version == "v2" && d.service == gcpServiceBigQuery {
			return d, true
		}
		if service == "" || d.service == service {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		return gcpDiscoveryDocument{}, false
	}
	return matches[0], true
}

// registerDiscoveryService serves each implemented API's Discovery document
// where the API serves it: `GET /$discovery/rest?version=…` under the API's
// own host.
func registerDiscoveryService(srv *sim.Server) {
	srv.HandleFunc("GET /$discovery/rest", func(w http.ResponseWriter, r *http.Request) {
		docs, err := gcpDiscoveryIndex()
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery documents: %v", err)
			return
		}
		service, version := gcpServiceFromHost(r), r.URL.Query().Get("version")
		doc, ok := gcpDiscoveryDocumentFor(docs, service, version)
		if !ok {
			api := service
			if api == "" {
				api = strings.ToLower(r.Host)
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "no Discovery document for %s version %q", api, version)
			return
		}
		body, err := readDiscoveryDocument(doc.file)
		if err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery document: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=UTF-8")
		_, _ = w.Write(body)
	})
}
