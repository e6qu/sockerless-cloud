package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// These tests enforce the simulator's core fidelity invariant: every HTTP
// route the GCP simulator serves must be a REAL Google API method — the
// simulator must not invent paths under the Google API namespace. The
// registered route table (built in-process via buildSimulator) is
// validated against the official API Discovery documents vendored
// (gzipped, pinned by revision) under specs/cloud-api/gcp/. Refresh a
// document with scripts/fetch-gcp-discovery.sh.

// discoveryMethod is one REST method from a vendored Discovery document.
type discoveryMethod struct {
	HTTPMethod string
	Path       string // basePath-joined flatPath (preferred) or path
	// PathParams maps each path parameter of the method to the Discovery
	// document's "pattern" for it (empty when the document constrains none).
	// A reserved-expansion parameter ({+name}) stands for several URI
	// segments and the pattern is the document's only description of that
	// shape, so rendering a concrete request URI for the coverage probe
	// needs it.
	PathParams map[string]string
}

type discoveryDoc struct {
	File string
	// Host is the service's own API host from the document's rootUrl
	// (run.googleapis.com, redis.googleapis.com, …). Real Google gives each
	// service its own hostname and the simulator resolves the two services
	// that share a URI path by the Host header a real client sends, so a
	// probe must send the document's host to reach that document's service.
	Host    string
	Methods []discoveryMethod
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
		addMethod := func(m rawMethod) {
			if m.HTTPMethod == "" {
				return
			}
			pathParams := map[string]string{}
			for name, param := range m.Parameters {
				if param.Location == "path" {
					pathParams[name] = param.Pattern
				}
			}
			// Both the expanded flatPath and the {+param} template path
			// are real, equivalent descriptions of the method URI;
			// index both so either spelling matches.
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
			// Media-upload variant rides its own absolute path
			// (/upload/storage/v1/b/{bucket}/o).
			if m.MediaUpload != nil && m.MediaUpload.Protocols.Simple.Path != "" {
				d.Methods = append(d.Methods, discoveryMethod{HTTPMethod: m.HTTPMethod, Path: m.MediaUpload.Protocols.Simple.Path, PathParams: pathParams})
			}
		}
		var walk func(res rawResource)
		walk = func(res rawResource) {
			for _, m := range res.Methods {
				addMethod(m)
			}
			for _, sub := range res.Resources {
				walk(sub)
			}
		}
		for _, m := range doc.Methods {
			addMethod(m)
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

// normalizeGCPPath collapses path parameters so simulator mux patterns
// ({project}, {object...}) compare equal to Discovery templates
// ({projectsId}, {+name}) regardless of label naming. Greedy labels
// ({+x} / {x...}) normalize to {+}, plain labels to {}; a single
// trailing slash is dropped.
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

// gcpMountPrefixes maps simulator-local mount prefixes to the service
// they disambiguate. Real GCP separates services by hostname
// (spanner.googleapis.com vs sqladmin.googleapis.com); the collapsed
// single-port simulator cannot, and Spanner's "/v1/projects/{p}/instances"
// would otherwise collide with Cloud SQL's identical path. The prefix is
// stripped before Discovery matching; the remainder must still be a real
// method path of that service.
var gcpMountPrefixes = map[string]string{
	"/spanner": "spanner-v1.discovery.json.gz",
}

// allowedNonSpecGCPRoutes lists registered patterns that are real,
// documented Google wire surfaces NOT described by any Discovery
// document, plus the simulator's own control surface. Each entry MUST be
// justified — never a place to hide an invented Google path.
var allowedNonSpecGCPRoutes = map[string]string{
	// OAuth2 token endpoints — real Google auth surface
	// (oauth2.googleapis.com /token; legacy www.googleapis.com/oauth2/v4/token),
	// not part of any service Discovery document.
	"POST /token":           "OAuth2 token endpoint (oauth2.googleapis.com)",
	"POST /oauth2/v4/token": "legacy OAuth2 token endpoint",
	"POST /o/oauth2/token":  "original OAuth2 token endpoint (accounts.google.com/o/oauth2/token)",
	// Security Token Service token exchange (sts.googleapis.com /v1/token) —
	// the Workforce Identity Federation surface, described by the STS service's
	// own Discovery rather than any resource service's document.
	"POST /v1/token": "Security Token Service token exchange (sts.googleapis.com)",
	// Security Token Service token introspection (sts.googleapis.com
	// /v1/introspect, RFC 7662) — the endpoint gcloud resolves a federated
	// workforce principal against after `gcloud auth login --cred-file`; same
	// STS surface as /v1/token, outside any resource service's Discovery.
	"POST /v1/introspect": "Security Token Service token introspection (sts.googleapis.com)",

	// The metadata server's own version directory, addressed without its
	// trailing slash so the server answers the 301 real Google answers. The
	// rest of the tree sits under the /computeMetadata/ prefix allowlist
	// below; this one pattern is the prefix itself, minus the slash.
	"GET /computeMetadata": "GCE metadata server version directory (documented Google surface; no Discovery document)",

	// OpenID Connect discovery + JSON Web Key Set publishing the simulator's
	// access-token signing key — the well-known material a resource server uses
	// to verify tokens, served at the standard OIDC well-known paths rather than
	// any service Discovery document.
	"GET /.well-known/openid-configuration": "OpenID Connect discovery for the simulator's access-token signing key",
	"GET /.well-known/jwks.json":            "JSON Web Key Set for the simulator's access-token signing key",

	// GCS XML API / signed-URL data plane: requests address the object
	// directly under the bucket host/path. Real surface (XML API),
	// described by the XML API docs rather than the JSON Discovery doc.
	"/{bucket}/{object...}": "GCS XML API / V4 signed-URL data plane",
	"GET /{path...}":        "GCS XML API bucket-host read path",
	"/{path...}":            "GCS XML API bucket-host data plane",

	// Artifact Registry Docker/OCI registry data plane: the shared OCI
	// registry mounts one per-method subtree at /v2/ and dispatches the
	// repository/blob/manifest paths itself (the OCI distribution spec, not
	// Discovery). Only these five subtree mounts are outside Discovery —
	// every other /v2/… route the simulator serves (Cloud Run v2, Cloud
	// Functions v2, Cloud Logging v2, …) is a real Discovery method and is
	// checked as one.
	"GET /v2/":    "OCI distribution data plane subtree (Artifact Registry)",
	"POST /v2/":   "OCI distribution data plane subtree (Artifact Registry)",
	"PUT /v2/":    "OCI distribution data plane subtree (Artifact Registry)",
	"PATCH /v2/":  "OCI distribution data plane subtree (Artifact Registry)",
	"DELETE /v2/": "OCI distribution data plane subtree (Artifact Registry)",

	// GCS resumable upload protocol: Discovery describes only the
	// initiating POST (uploadType=resumable); the follow-up chunk PUTs
	// go to the returned session URI on the same /upload path. Real,
	// documented surface (resumable-uploads protocol).
	"PUT /upload/storage/v1/b/{bucket}/o": "GCS resumable upload session continuation",
}

var allowedNonSpecGCPPrefixes = map[string]string{
	"/sim/v1/":             "simulator control + dashboard surface (sockerless-specific)",
	"/computeMetadata/":    "GCE metadata server (documented Google surface; no Discovery document)",
	"/v2-functions-invoke": "deterministic Cloud Functions invoke host surface (run.app/cloudfunctions.net URL emulation)",
	"/v2-services-invoke":  "deterministic Cloud Run service invoke host surface (run.app URL emulation)",
	"/sockerless/":         "simulator-internal host-dispatch surface",
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

// matchSegs reports whether a simulator mux pattern is a valid spelling
// of a Discovery method URI. Rules:
//   - spec "{+}" (reserved expansion, e.g. {+name}) consumes one or more
//     simulator segments of any kind;
//   - spec "{}" consumes exactly one simulator segment of any kind — a
//     simulator literal there (e.g. hardcoded "global") narrows the spec
//     parameter, which is valid;
//   - a spec literal requires the identical simulator literal, or a
//     simulator "{}" / "{+}" in final position when the spec segment is a
//     ":verb" form ("{}:addVersion", "documents:runQuery") — Go's mux
//     cannot spell colon verbs as separate patterns, so handlers fan in
//     on a parameter that also captures the ":verb" suffix;
//   - a simulator trailing "{+}" consumes any remaining spec segments
//     (greedy fan-in accepts a superset of the spec path).
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
		// consume 1..n simulator segments
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

	all := flattenDocs(docs)
	byFile := map[string][]specPath{}
	for _, d := range docs {
		byFile[d.File] = flattenDocs([]*discoveryDoc{d})
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

// TestVendoredDiscoveryDocsAreConsumed flags vendored Discovery documents
// no registered route references — a stale vendor is either a removed
// service or a fetch of the wrong document/version.
func TestVendoredDiscoveryDocsAreConsumed(t *testing.T) {
	docs := loadDiscoveryDocs(t)
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}

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
