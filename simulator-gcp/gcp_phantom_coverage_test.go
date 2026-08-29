package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// A served count can hide an unserved method. The coverage probe asks whether
// a handler answered, and a multi-segment `{x...}` route answers for every path
// beneath it — including a sibling collection nobody implemented. Cloud
// Storage's five per-object ACL methods counted as covered for as long as the
// gate existed: `/o/{object}/acl/{entity}` reached objects.get, which replied
// `object "doc.txt/acl/user-a" not found`, a structured 404 from a handler that
// ran. Indistinguishable, to the probe, from a served read of an absent object.
//
// The mux knows what the probe cannot: http.ServeMux.Handler reports which
// pattern matched. A Discovery path's literal segments are structural — Google
// routes on them — so a matched pattern missing one of them did not route the
// method; something above it swallowed the tail.

var gcpPhantomLabel = regexp.MustCompile(`\{[^}]+\}`)

// gcpPathLiterals returns a template's literal segments: those carrying no
// label at all. `b/{bucket}/o/{object}/acl/{entity}` yields b, o, acl.
func gcpPathLiterals(template string) []string {
	template = strings.SplitN(template, ":", 2)[0]
	var out []string
	for _, seg := range strings.Split(strings.Trim(template, "/"), "/") {
		if seg == "" || gcpPhantomLabel.MatchString(seg) {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// gcpPatternHasLiteral reports whether a mux pattern routes on a segment.
func gcpPatternHasLiteral(pattern, literal string) bool {
	pattern = strings.SplitN(pattern, ":", 2)[0]
	if i := strings.Index(pattern, " "); i >= 0 {
		pattern = pattern[i+1:]
	}
	for _, seg := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if seg == literal {
			return true
		}
	}
	return false
}

// gcpFanInPatterns are the routes that own a whole subtree and dispatch inside
// the handler rather than through the mux, so a literal missing from the
// pattern is not a missing route.
//
// Each entry needs evidence that the handler reads the tail and rejects what it
// does not route — the coverage probe already reads such a rejection as
// unserved, so the method count stays honest. An entry that merely silences the
// gate reinstates the blind spot the gate exists to close.
var gcpFanInPatterns = map[string]string{
	// Cloud Spanner's paths all hang off one instance, and the collections
	// nest deeper than the mux can name without registering the whole
	// document. The handler routes on the tail and answers a method-not-found
	// for every tail it does not.
	"GET /spanner/v1/projects/{project}/instances/{rest...}":    "Cloud Spanner instance subtree",
	"POST /spanner/v1/projects/{project}/instances/{rest...}":   "Cloud Spanner instance subtree",
	"PATCH /spanner/v1/projects/{project}/instances/{rest...}":  "Cloud Spanner instance subtree",
	"DELETE /spanner/v1/projects/{project}/instances/{rest...}": "Cloud Spanner instance subtree",

	// The AIP-151 IAM verbs apply to any resource, so the route names the
	// resource generically and the handler reads the verb after the colon.
	"POST /v1/{resource...}": "generic setIamPolicy/getIamPolicy/testIamPermissions",

	// Colon-verb fan-in: one handler per resource dispatches every AIP-136
	// custom method on it and answers an unrecognised verb as unknown.
	"POST /v2/projects/{project}/locations/{location}/{functionsVerb}":                                                               "Cloud Run Functions custom methods",
	"POST /v1/projects/{project}/{databasesVerb}":                                                                                    "Firestore database custom methods",
	"GET /v1/projects/{project}/locations/{location}/{locationGetAction}":                                                            "per-location custom reads",
	"POST /v2/projects/{project}/instances/{instance}/{tablesColl}":                                                                  "Cloud Bigtable table custom methods",
	"POST /v2/projects/{project}/instances/{instance}/clusters/{cluster}/{backupsColl}":                                              "Cloud Bigtable backup custom methods",
	"POST /v1/projects/{project}/locations/{location}/keyRings/{keyRing}/cryptoKeys/{cryptoKey}/{cryptoKeyVersionsCollectionAction}": "Cloud KMS crypto-key-version custom methods",

	// objects.compose, objects.copy and objects.rewrite put their verb after
	// the object name, which may itself hold slashes, so the mux cannot name
	// it. The handler matches the suffix and 404s a tail it does not own.
	"POST /storage/v1/b/{bucket}/o/{destObject...}": "Cloud Storage compose/copyTo/rewriteTo",
}

// TestServiceConformance_GCPNoPhantomCoverage holds every method the coverage
// probe counts as served to a route that actually names it.
func TestServiceConformance_GCPNoPhantomCoverage(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	mux := srv.Mux()

	p := newGCPCoverageProbe(t)
	docs := loadDiscoveryDocs(t)

	var phantoms []string
	for _, d := range docs {
		prefix := gcpDocMountPrefix(d.File)
		for _, m := range d.Methods {
			literals := gcpPathLiterals(m.Path)
			if len(literals) == 0 {
				continue
			}
			// methodServed counts a method as served when ANY of its plausible
			// renderings is, so the pattern to judge is the one that answered
			// — not the first rendering, which may name a coordinate the route
			// does not accept and fall through to a catch-all.
			for _, uri := range gcpRenderMethodURIs(m) {
				if !p.probe(m.HTTPMethod, d.Host, prefix+uri).served {
					continue
				}
				req := httptest.NewRequest(m.HTTPMethod, prefix+uri, nil)
				req.Host = d.Host
				_, pattern := mux.Handler(req)
				if pattern == "" || gcpFanInPatterns[pattern] != "" {
					break
				}
				var missing []string
				for _, lit := range literals {
					if !gcpPatternHasLiteral(pattern, lit) {
						missing = append(missing, lit)
					}
				}
				if len(missing) > 0 {
					phantoms = append(phantoms, d.File+": "+m.HTTPMethod+" "+m.Path+
						"\n    matched "+pattern+
						"\n    routes on no segment named "+strings.Join(missing, ", "))
				}
				break
			}
		}
	}

	sort.Strings(phantoms)
	if len(phantoms) > 0 {
		t.Errorf("%d method(s) count as served but reach a route that does not name them:\n\n%s",
			len(phantoms), strings.Join(phantoms, "\n\n"))
	}
}

// TestServiceConformance_GCPPhantomSweepIsSound guards the detector: it must
// see a swallowed sibling and must not flag a route that names its method.
func TestServiceConformance_GCPPhantomSweepIsSound(t *testing.T) {
	const swallowing = "GET /storage/v1/b/{bucket}/o/{object...}"
	if gcpPatternHasLiteral(swallowing, "acl") {
		t.Error("a catch-all pattern reported as routing on the segment it swallows")
	}
	if !gcpPatternHasLiteral("GET /storage/v1/b/{bucket}/o/{object}/acl/{entity}", "acl") {
		t.Error("a pattern that names the segment reported as not routing on it")
	}

	literals := gcpPathLiterals("b/{bucket}/o/{object}/acl/{entity}")
	want := []string{"b", "o", "acl"}
	if strings.Join(literals, ",") != strings.Join(want, ",") {
		t.Errorf("literal segments = %v, want %v", literals, want)
	}
	// A colon verb rides the segment before it rather than being one, so the
	// version literal is the only one here.
	if got := gcpPathLiterals("v2/{+name}:setIamPolicy"); strings.Join(got, ",") != "v2" {
		t.Errorf("literal segments = %v, want [v2]", got)
	}

	// The mux the sweep reads must be the one that serves requests.
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "/v2/projects/p/locations/us-central1/jobs", nil)
	req.Host = "run.googleapis.com"
	if _, pattern := srv.Mux().Handler(req); pattern == "" {
		t.Error("the mux reports no pattern for a method the simulator serves")
	}
}
