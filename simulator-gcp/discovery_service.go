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
	"slices"
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

// gcpDiscoveryDocument is one embedded Discovery document, keyed by its API
// name, which is also the service label of the host that serves it.
type gcpDiscoveryDocument struct {
	file    string
	name    string
	version string
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
		}
		if err := json.Unmarshal(body, &head); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if head.Name == "" || head.Version == "" {
			return nil, fmt.Errorf("%s names no API and version (name %q, version %q)", file, head.Name, head.Version)
		}
		docs = append(docs, gcpDiscoveryDocument{file: file, name: head.Name, version: head.Version})
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

// discoveryDirectoryJSON is a capture of the Discovery service's directory: the
// entries for the documents the simulator embeds, which hosts serve each
// through the central getRest path, and each API's default version.
//
//go:embed discovery_directory_vendored.json
var discoveryDirectoryJSON []byte

type gcpDiscoveryDirectory struct {
	Items          []json.RawMessage   `json:"items"`
	CentralRest    map[string][]string `json:"centralRest"`
	DefaultVersion map[string]*string  `json:"defaultVersion"`

	entries []gcpDirectoryEntry
}

type gcpDirectoryEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	Preferred bool   `json:"preferred"`
}

var gcpDiscoveryDirectoryCapture = sync.OnceValues(func() (*gcpDiscoveryDirectory, error) {
	var dir gcpDiscoveryDirectory
	if err := json.Unmarshal(discoveryDirectoryJSON, &dir); err != nil {
		return nil, fmt.Errorf("discovery_directory_vendored.json: %w", err)
	}
	for _, item := range dir.Items {
		var e gcpDirectoryEntry
		if err := json.Unmarshal(item, &e); err != nil {
			return nil, fmt.Errorf("discovery_directory_vendored.json: %w", err)
		}
		dir.entries = append(dir.entries, e)
	}
	return &dir, nil
})

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
		if service == "" && version == "v2" && d.name == gcpServiceBigQuery {
			return d, true
		}
		if service == "" || d.name == service {
			matches = append(matches, d)
		}
	}
	if len(matches) != 1 {
		return gcpDiscoveryDocument{}, false
	}
	return matches[0], true
}

// writeDiscoveryError writes the error body the Discovery service answers,
// which carries no details list.
func writeDiscoveryError(w http.ResponseWriter, code int, status, message string, details ...any) {
	body := map[string]any{"code": code, "message": message, "status": status}
	if len(details) > 0 {
		body["details"] = details
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": body})
}

func writeDiscoveryDocument(w http.ResponseWriter, doc gcpDiscoveryDocument) {
	body, err := readDiscoveryDocument(doc.file)
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery document: %v", err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_, _ = w.Write(body)
}

// discoveryCentralHost names the host whose central directory a request
// reaches: www.googleapis.com and discovery.googleapis.com serve it, and a bare
// address:port is the www.googleapis.com root the Discovery document names.
// Any other API host serves no directory.
func discoveryCentralHost(r *http.Request) (string, bool) {
	switch gcpServiceFromHost(r) {
	case "", "www":
		return "www.googleapis.com", true
	case "discovery":
		return "discovery.googleapis.com", true
	}
	return "", false
}

// registerDiscoveryService serves each implemented API's Discovery document
// where the API serves it, `GET /$discovery/rest?version=…` under the API's
// own host, and the Discovery service's directory: discovery.apis.list at
// `GET /discovery/v1/apis` and discovery.apis.getRest at
// `GET /discovery/v1/apis/{api}/{version}/rest`.
func registerDiscoveryService(srv *sim.Server) {
	srv.HandleFunc("GET /$discovery/rest", handleDiscoveryPerHost)
	srv.HandleFunc("GET /discovery/v1/apis", handleDiscoveryAPIsList)
	srv.HandleFunc("GET /discovery/v1/apis/{api}/{version}/rest", handleDiscoveryAPIsGetRest)
}

func handleDiscoveryPerHost(w http.ResponseWriter, r *http.Request) {
	docs, err := gcpDiscoveryIndex()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery documents: %v", err)
		return
	}
	dir, err := gcpDiscoveryDirectoryCapture()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery directory: %v", err)
		return
	}
	service := gcpServiceFromHost(r)
	version, versioned := r.URL.Query()["version"]
	v := ""
	if versioned {
		v = version[0]
	} else if def := dir.DefaultVersion[service]; def != nil {
		v = *def
	}
	doc, ok := gcpDiscoveryDocumentFor(docs, service, v)
	if !ok {
		api := strings.ToLower(r.Host)
		if service != "" {
			api = service + ".googleapis.com"
		}
		writeDiscoveryError(w, http.StatusNotFound, "NOT_FOUND",
			fmt.Sprintf("Discovery document not found for API service: %s format: rest version: %s", api, v))
		return
	}
	writeDiscoveryDocument(w, doc)
}

func handleDiscoveryAPIsList(w http.ResponseWriter, r *http.Request) {
	if _, ok := discoveryCentralHost(r); !ok {
		http.NotFound(w, r)
		return
	}
	dir, err := gcpDiscoveryDirectoryCapture()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery directory: %v", err)
		return
	}
	q := r.URL.Query()
	preferredOnly := false
	if raw, ok := q["preferred"]; ok {
		switch raw[0] {
		case "true":
			preferredOnly = true
		case "false":
		default:
			msg := fmt.Sprintf("Invalid value at 'preferred' (TYPE_BOOL), %q", raw[0])
			writeDiscoveryError(w, http.StatusBadRequest, "INVALID_ARGUMENT", msg, map[string]any{
				"@type":           "type.googleapis.com/google.rpc.BadRequest",
				"fieldViolations": []any{map[string]any{"field": "preferred", "description": msg}},
			})
			return
		}
	}
	name := q.Get("name")
	var items []json.RawMessage
	for i, e := range dir.entries {
		if (name != "" && e.Name != name) || (preferredOnly && !e.Preferred) {
			continue
		}
		items = append(items, dir.Items[i])
	}
	type directoryList struct {
		Kind             string            `json:"kind"`
		DiscoveryVersion string            `json:"discoveryVersion"`
		Items            []json.RawMessage `json:"items,omitempty"`
	}
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_ = json.NewEncoder(w).Encode(directoryList{Kind: "discovery#directoryList", DiscoveryVersion: "v1", Items: items})
}

func handleDiscoveryAPIsGetRest(w http.ResponseWriter, r *http.Request) {
	host, ok := discoveryCentralHost(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	dir, err := gcpDiscoveryDirectoryCapture()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery directory: %v", err)
		return
	}
	docs, err := gcpDiscoveryIndex()
	if err != nil {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "read the Discovery documents: %v", err)
		return
	}
	api, version := r.PathValue("api"), r.PathValue("version")
	if slices.Contains(dir.CentralRest[host], api+":"+version) {
		for _, d := range docs {
			if d.name == api && d.version == version {
				writeDiscoveryDocument(w, d)
				return
			}
		}
	}
	writeDiscoveryError(w, http.StatusNotFound, "NOT_FOUND", "Requested entity was not found.")
}
