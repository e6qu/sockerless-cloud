package main

import (
	"compress/gzip"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Every route the Google Cloud simulator serves must be a real Google API
// method: buildSimulator's route table is checked against the Discovery
// documents vendored under specs/cloud-api/gcp/, which
// scripts/fetch-gcp-discovery.sh refreshes.

// discoveryMethod is one REST method from a vendored Discovery document.
type discoveryMethod struct {
	HTTPMethod string
	Path       string // basePath-joined flatPath (preferred) or path
	// PathParams maps each path parameter to its Discovery "pattern" (empty when
	// unconstrained). A reserved-expansion parameter ({+name}) spans several
	// segments, and its pattern is the only description of that shape the
	// coverage probe can render a URI from.
	PathParams map[string]string
}

type discoveryDoc struct {
	File string
	// Host is the service's own API host from the document's rootUrl. The
	// simulator resolves services that share a URI path by the Host header, so a
	// probe sends the document's host.
	Host    string
	Methods []discoveryMethod
	// ResumableSessionPaths are the media paths of methods that declare the
	// resumable protocol, whose chunk PUTs and cancelling DELETE address the
	// session URI there; the protocol, not the document, describes both.
	ResumableSessionPaths []string
}

func loadDiscoveryDocs(t *testing.T) []*discoveryDoc {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "gcp", "*.discovery.json.gz"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no vendored Discovery documents found (glob err: %v) — run scripts/fetch-gcp-discovery.sh", err)
	}

	type rawParameter struct {
		Location string `json:"location"`
		Pattern  string `json:"pattern"`
	}
	type rawMethod struct {
		HTTPMethod              string                  `json:"httpMethod"`
		Path                    string                  `json:"path"`
		FlatPath                string                  `json:"flatPath"`
		Parameters              map[string]rawParameter `json:"parameters"`
		UseMediaDownloadService bool                    `json:"useMediaDownloadService"`
		MediaUpload             *struct {
			Protocols struct {
				Simple struct {
					Path string `json:"path"`
				} `json:"simple"`
				Resumable struct {
					Path string `json:"path"`
				} `json:"resumable"`
			} `json:"protocols"`
		} `json:"mediaUpload"`
	}
	type rawResource struct {
		Methods   map[string]rawMethod   `json:"methods"`
		Resources map[string]rawResource `json:"resources"`
	}

	var out []*discoveryDoc
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			t.Fatalf("open %s: %v", p, err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatalf("gunzip %s: %v", p, err)
		}
		var doc struct {
			BasePath  string                 `json:"basePath"`
			RootURL   string                 `json:"rootUrl"`
			Methods   map[string]rawMethod   `json:"methods"`
			Resources map[string]rawResource `json:"resources"`
		}
		err = json.NewDecoder(gz).Decode(&doc)
		_ = gz.Close()
		_ = f.Close()
		if err != nil {
			t.Fatalf("decode %s: %v", p, err)
		}

		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(doc.RootURL, "https://"), "http://"), "/")
		if i := strings.Index(host, "/"); i >= 0 {
			host = host[:i]
		}
		if host == "" {
			t.Fatalf("%s: document has no rootUrl — the service's API host is unknown", p)
		}
		d := &discoveryDoc{File: filepath.Base(p), Host: host}
		join := func(rel string) string {
			return "/" + strings.TrimPrefix(strings.TrimSuffix(doc.BasePath, "/")+"/"+strings.TrimPrefix(rel, "/"), "/")
		}
		addMethod := func(m rawMethod, parentShape string) {
			if m.HTTPMethod == "" {
				return
			}
			pathParams := map[string]string{}
			for name, param := range m.Parameters {
				if param.Location == "path" {
					pattern := param.Pattern
					// A reserved-expansion parent whose pattern is a bare character class permits
					// slashes but describes no segments; its siblings in the collection name the
					// same parent's segments, so the collection's shape stands in.
					if parentShape != "" && strings.Contains(m.Path, "{+"+name+"}") && gcpPatternIsShapeless(pattern) {
						pattern = parentShape
					}
					pathParams[name] = pattern
				}
			}
			// The flatPath and the {+param} path are equivalent descriptions of the URI;
			// index both.
			for _, rel := range []string{m.FlatPath, m.Path} {
				if rel == "" {
					continue
				}
				d.Methods = append(d.Methods, discoveryMethod{HTTPMethod: m.HTTPMethod, Path: join(rel), PathParams: pathParams})
				// Media-download variant: alt=media requests ride the
				// /download-prefixed service path.
				if m.UseMediaDownloadService {
					d.Methods = append(d.Methods, discoveryMethod{HTTPMethod: m.HTTPMethod, Path: "/download" + join(rel), PathParams: pathParams})
				}
			}
			// Each media-upload protocol has its own absolute path (/upload/... for
			// simple, /resumable/upload/... for resumable); a resumable session begun on
			// either addresses its session URI there.
			if m.MediaUpload != nil {
				simple, resumable := m.MediaUpload.Protocols.Simple.Path, m.MediaUpload.Protocols.Resumable.Path
				for _, path := range []string{simple, resumable} {
					if path == "" {
						continue
					}
					d.Methods = append(d.Methods, discoveryMethod{HTTPMethod: m.HTTPMethod, Path: path, PathParams: pathParams})
					if resumable != "" {
						d.ResumableSessionPaths = append(d.ResumableSessionPaths, path)
					}
				}
			}
		}
		var walk func(res rawResource)
		walk = func(res rawResource) {
			// Every reserved-expansion parent of one collection denotes the
			// same parent, so the most informative pattern any of its methods
			// declares is the shape for all of them.
			parentShape := ""
			for _, m := range res.Methods {
				for _, param := range m.Parameters {
					if param.Location == "path" && gcpPatternNamesSegments(param.Pattern) {
						parentShape = param.Pattern
					}
				}
			}
			for _, m := range res.Methods {
				addMethod(m, parentShape)
			}
			for _, sub := range res.Resources {
				walk(sub)
			}
		}
		for _, m := range doc.Methods {
			addMethod(m, "")
		}
		for _, res := range doc.Resources {
			walk(res)
		}
		if len(d.Methods) == 0 {
			t.Fatalf("%s: no REST methods found", p)
		}
		out = append(out, d)
	}
	return out
}

var gcpParamSegment = regexp.MustCompile(`\{[^}]+\}`)

// normalizeGCPPath collapses path parameters so mux patterns ({project},
// {object...}) compare equal to Discovery templates ({projectsId}, {+name}):
// greedy labels become {+}, plain labels {}, and a trailing slash is dropped.
func normalizeGCPPath(p string) string {
	p = gcpParamSegment.ReplaceAllStringFunc(p, func(s string) string {
		inner := s[1 : len(s)-1]
		if strings.HasPrefix(inner, "+") || strings.HasSuffix(inner, "...") {
			return "{+}"
		}
		return "{}"
	})
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

// gcpMountPrefixes maps the simulator's mount prefixes to the service they
// disambiguate. Google separates services by host; on one port Spanner's
// "/v1/projects/{p}/instances" would collide with Cloud SQL's. The prefix is
// stripped before matching, and the rest must be a real method path.
var gcpMountPrefixes = map[string]string{
	"/spanner": "spanner-v1.discovery.json.gz",
}

// allowedNonSpecGCPRoutes lists the registered patterns no Discovery document
// describes: documented Google wire surfaces and the simulator's own control
// surface. Each entry carries its justification.
var allowedNonSpecGCPRoutes = map[string]string{
	// The simulator's own channel from a Cloud NAT's egress sidecar, under
	// the token the simulator minted for it; no cloud client ever sees it.
	"POST /sockerless/sidecar/egress": "egress sidecar reports (simulator-internal, token-bound)",
	// Every Google API serves its Discovery document under its own host, and no
	// document describes that endpoint; client generators and the bq tool build
	// from it.
	"GET /$discovery/rest": "each implemented API's Discovery document, under that API's host",
	// The document declares uploadSource's media spelling as
	// "/upload/v2/{+parent}:uploadSource"; the colon sits on the parent's last
	// segment, where this route ends.
	"POST /upload/v2/projects/{project}/locations/{location}": "Cloud Run uploadSource, media-upload spelling",
	// Compute Engine's "images/family/{family}" and
	// "images/{resource}/getIamPolicy" are one shape to a path router; this route
	// serves both, resolving the literal family segment first.
	"GET /compute/v1/projects/{project}/global/images/{first}/{second}": "image family lookup and image IAM policy read, which a path router cannot separate",
	// The JSON API's batch endpoint, which Cloud Storage documents on its own
	// page (cloud.google.com/storage/docs/batch) and its Discovery document
	// names only as batchPath, not as a method.
	"POST /batch/storage/v1": "Cloud Storage JSON API batch endpoint (the document's batchPath)",
	// OAuth 2.0 token endpoints (oauth2.googleapis.com /token, and
	// www.googleapis.com/oauth2/v4/token), outside every service's document.
	"POST /token":           "OAuth2 token endpoint (oauth2.googleapis.com)",
	"POST /oauth2/v4/token": "legacy OAuth2 token endpoint",
	"POST /o/oauth2/token":  "original OAuth2 token endpoint (accounts.google.com/o/oauth2/token)",
	// Security Token Service token exchange (sts.googleapis.com /v1/token), for
	// Workforce Identity Federation.
	"POST /v1/token": "Security Token Service token exchange (sts.googleapis.com)",
	// Security Token Service introspection (sts.googleapis.com /v1/introspect,
	// RFC 7662), which gcloud resolves a federated workforce principal against
	// after `gcloud auth login --cred-file`.
	"POST /v1/introspect": "Security Token Service token introspection (sts.googleapis.com)",

	// The metadata server's version directory without its trailing slash, which
	// answers the 301 Google answers; the rest of the tree is under the
	// /computeMetadata/ prefix below.
	"GET /computeMetadata": "GCE metadata server version directory (documented Google surface; no Discovery document)",

	// OpenID Connect discovery and the JSON Web Key Set publishing the
	// simulator's access-token signing key, at the standard well-known paths.
	"GET /.well-known/openid-configuration": "OpenID Connect discovery for the simulator's access-token signing key",
	"GET /.well-known/jwks.json":            "JSON Web Key Set for the simulator's access-token signing key",

	// The Cloud Storage XML API and V4 signed-URL data plane, addressing the
	// object under the bucket path; the XML API docs describe it.
	"/{bucket}/{object...}": "GCS XML API / V4 signed-URL data plane",
	"GET /{path...}":        "GCS XML API bucket-host read path",
	"/{path...}":            "GCS XML API bucket-host data plane",

	// The Artifact Registry Docker/OCI data plane: the shared registry mounts one
	// subtree per method at /v2/ and routes repository, blob and manifest paths
	// itself, per the OCI distribution spec. Every other /v2/ route is a
	// Discovery method and is checked as one.
	"GET /v2/":    "OCI distribution data plane subtree (Artifact Registry)",
	"POST /v2/":   "OCI distribution data plane subtree (Artifact Registry)",
	"PUT /v2/":    "OCI distribution data plane subtree (Artifact Registry)",
	"PATCH /v2/":  "OCI distribution data plane subtree (Artifact Registry)",
	"DELETE /v2/": "OCI distribution data plane subtree (Artifact Registry)",

	// The Docker token service Artifact Registry names in every Bearer challenge's
	// realm (`Bearer realm="https://us-central1-docker.pkg.dev/v2/token"`), part
	// of the same OCI data plane. The non-GET verbs answer 405 as the live service
	// does, rather than falling through the /v2/ subtree to a 404.
	"GET /v2/token":    "Artifact Registry Docker token service (challenge realm)",
	"POST /v2/token":   "Artifact Registry Docker token service (405: GET-only)",
	"PUT /v2/token":    "Artifact Registry Docker token service (405: GET-only)",
	"PATCH /v2/token":  "Artifact Registry Docker token service (405: GET-only)",
	"DELETE /v2/token": "Artifact Registry Docker token service (405: GET-only)",
}

var allowedNonSpecGCPPrefixes = map[string]string{
	"/sim/v1/":          "simulator control + dashboard surface (sockerless-specific)",
	"/computeMetadata/": "GCE metadata server (documented Google surface; no Discovery document)",
	"/sockerless/":      "simulator-internal host-dispatch surface",
}

// specPath is one method URI from a Discovery document, pre-split into
// normalized segments ({} plain param, {+} greedy param, literal,
// optionally carrying a trailing :verb on the final segment).
type specPath struct {
	Method string
	Segs   []string
}

func flattenDocs(docs []*discoveryDoc) []specPath {
	var out []specPath
	for _, d := range docs {
		for _, m := range d.Methods {
			out = append(out, specPath{Method: m.HTTPMethod, Segs: splitNormSegs(m.Path)})
		}
	}
	return out
}

func splitNormSegs(p string) []string {
	p = strings.TrimSuffix(strings.TrimPrefix(normalizeGCPPath(p), "/"), "/")
	if p == "" {
		return nil
	}
	return strings.Split(p, "/")
}

func matchesAnySpec(specs []specPath, method, path string) bool {
	simSegs := splitNormSegs(path)
	for _, s := range specs {
		if s.Method == method && matchSegs(simSegs, s.Segs) {
			return true
		}
	}
	return false
}

// matchSegs reports whether a mux pattern spells a Discovery method URI:
//   - spec "{+}" consumes one or more simulator segments;
//   - spec "{}" consumes exactly one, and a simulator literal there ("global")
//     narrows the parameter;
//   - a spec literal requires the same literal, or, for a final ":verb" form
//     ("{}:addVersion"), a final simulator "{}" or "{+}", since ServeMux
//     cannot spell colon verbs and handlers fan in on a parameter;
//   - a trailing simulator "{+}" consumes any remaining spec segments.
func matchSegs(sim, spec []string) bool {
	if len(spec) == 0 {
		return len(sim) == 0
	}
	if len(sim) == 0 {
		return false
	}
	simSeg, specSeg := sim[0], spec[0]
	simLast, specLast := len(sim) == 1, len(spec) == 1

	if simSeg == "{+}" && simLast {
		return true // greedy fan-in: superset of whatever the spec continues with
	}
	if specSeg == "{+}" {
		if specLast {
			return true // spec reserved expansion swallows the rest
		}
		for i := 1; i <= len(sim); i++ {
			if matchSegs(sim[i:], spec[1:]) {
				return true
			}
		}
		return false
	}

	ok := false
	switch {
	case specSeg == "{}":
		ok = true // sim literal or param both fine
	case simSeg == specSeg:
		ok = true
	case simLast && specLast && strings.Contains(specSeg, ":") && (simSeg == "{}" || simSeg == "{+}"):
		// colon-verb fan-in: the sim parameter captures "name:verb"
		ok = true
	case simLast && specLast && strings.Contains(specSeg, ":") && strings.HasPrefix(specSeg, "{}") && strings.HasPrefix(simSeg, "{}"):
		ok = simSeg == specSeg
	}
	if !ok {
		return false
	}
	return matchSegs(sim[1:], spec[1:])
}

func TestRoutesExistInDiscoveryDocs(t *testing.T) {
	docs := loadDiscoveryDocs(t)
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	all := flattenDocs(docs)
	byFile := map[string][]specPath{}
	var sessionPaths []specPath
	for _, d := range docs {
		byFile[d.File] = flattenDocs([]*discoveryDoc{d})
		for _, p := range d.ResumableSessionPaths {
			sessionPaths = append(sessionPaths, specPath{Method: http.MethodPost, Segs: splitNormSegs(p)})
		}
	}

	var offenders []string
	for _, pattern := range srv.RoutePatterns() {
		if pattern == "GET /health" {
			continue // shared simulator health endpoint
		}
		if _, ok := allowedNonSpecGCPRoutes[pattern]; ok {
			continue
		}
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			// Method-less catch-all patterns are data-plane fan-ins;
			// only the explicitly allowlisted ones are acceptable.
			offenders = append(offenders, pattern+"  (method-less pattern not in allowlist)")
			continue
		}
		prefixAllowed := false
		for prefix := range allowedNonSpecGCPPrefixes {
			if strings.HasPrefix(path, prefix) {
				prefixAllowed = true
				break
			}
		}
		if prefixAllowed {
			continue
		}

		mounted := false
		for prefix, file := range gcpMountPrefixes {
			if strings.HasPrefix(path, prefix+"/") {
				mounted = true
				if !matchesAnySpec(byFile[file], method, strings.TrimPrefix(path, prefix)) {
					offenders = append(offenders, pattern+"  (mount "+prefix+": not a method of "+file+")")
				}
				break
			}
		}
		if mounted {
			continue
		}

		if (method == http.MethodPut || method == http.MethodDelete) && matchesAnySpec(sessionPaths, http.MethodPost, path) {
			continue
		}
		if !matchesAnySpec(all, method, path) {
			offenders = append(offenders, pattern+"  (normalized: "+method+" "+normalizeGCPPath(path)+")")
		}
	}

	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Errorf("%d registered route(s) do not exist in the vendored Discovery documents (invented path, wrong path shape, or a real-but-undescribed surface that needs a justified allowlist entry):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}

// TestVendoredDiscoveryDocsAreConsumed flags a vendored Discovery document no
// route references: a removed service, or the wrong document fetched.
func TestVendoredDiscoveryDocsAreConsumed(t *testing.T) {
	docs := loadDiscoveryDocs(t)
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)

	used := map[string]bool{}
	byFile := map[string][]specPath{}
	for _, d := range docs {
		byFile[d.File] = flattenDocs([]*discoveryDoc{d})
	}
	for _, pattern := range srv.RoutePatterns() {
		method, path, found := strings.Cut(pattern, " ")
		if !found {
			continue
		}
		for prefix := range gcpMountPrefixes {
			if strings.HasPrefix(path, prefix+"/") {
				path = strings.TrimPrefix(path, prefix)
			}
		}
		for _, d := range docs {
			if !used[d.File] && matchesAnySpec(byFile[d.File], method, path) {
				used[d.File] = true
			}
		}
	}

	var stale []string
	for _, d := range docs {
		if !used[d.File] {
			stale = append(stale, d.File)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("%d vendored Discovery document(s) are not referenced by any registered route (stale vendor or wrong version):\n  %s",
			len(stale), strings.Join(stale, "\n  "))
	}
}
