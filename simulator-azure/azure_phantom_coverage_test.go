package main

import (
	"sort"
	"strings"
	"testing"
)

// A served count can hide an unserved operation. The coverage probe asks
// whether a handler answered, and a route owning a subtree answers for every
// path beneath it — including a sibling collection nobody implemented. The
// answer such a route gives is a structured 404 or an empty list from a
// handler that ran, which is indistinguishable, to the probe, from a served
// read of an absent resource.
//
// The mux knows what the probe cannot: it reports which pattern matched. A
// Swagger path's literal segments are structural — Azure Resource Manager
// routes on them — so a matched pattern missing one of them did not route the
// operation; something above it swallowed the tail.
//
// This is the Azure counterpart of the Google Cloud detector in
// simulator-gcp/gcp_phantom_coverage_test.go. It differs in one way that
// matters: Azure Resource Manager paths are case-insensitive and the
// simulator registers both spellings a client may send (`serverfarms` and
// `serverFarms`), so a literal matches its segment without regard to case.

// azurePatternHasLiteral reports whether a mux pattern routes on a segment.
func azurePatternHasLiteral(pattern, literal string) bool {
	if i := strings.Index(pattern, " "); i >= 0 {
		pattern = pattern[i+1:]
	}
	for _, seg := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if strings.EqualFold(seg, literal) {
			return true
		}
	}
	return false
}

// azureSpecLiterals returns a Swagger path's literal segments: those carrying
// no `{parameter}` at all.
func azureSpecLiterals(segs []string) []string {
	var out []string
	for _, seg := range segs {
		if seg == "" || azureIsWildcard(seg) {
			continue
		}
		out = append(out, seg)
	}
	return out
}

// azurePhantomFanIn are the routes that own a whole subtree and dispatch
// inside the handler rather than through the mux, so a literal missing from
// the pattern is not a missing route. Each entry states what does the
// dispatching, because that is what makes the omission legitimate.
var azurePhantomFanIn = map[string]string{
	// The data-plane fan-in. Key Vault, Cosmos DB Table and Service Bus each
	// serve their own path space under their own host, so the coordinate that
	// selects the service is the Host header and the path belongs to the
	// service's protocol — `/keys`, `/Tables('{table}')`, `/$namespaceinfo`.
	// One route accepts the whole path and the handler dispatches on the host
	// it arrived under.
	"GET /{resourceId}":    "data-plane fan-in: the Host header selects the service and the handler dispatches on it",
	"PUT /{resourceId}":    "data-plane fan-in: the Host header selects the service and the handler dispatches on it",
	"PATCH /{resourceId}":  "data-plane fan-in: the Host header selects the service and the handler dispatches on it",
	"DELETE /{resourceId}": "data-plane fan-in: the Host header selects the service and the handler dispatches on it",

	// The OCI distribution API a container registry speaks. A repository name
	// carries slashes, so `/v2/{name}/manifests/{reference}` cannot be a mux
	// pattern at all — the handler splits the path itself.
	"GET /v2/":    "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",
	"POST /v2/":   "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",
	"PUT /v2/":    "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",
	"DELETE /v2/": "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",

	// Azure Container Registry's own data plane, for the same reason.
	"GET /acr/v1/{path...}":    "Azure Container Registry data plane: the repository name carries slashes",
	"PATCH /acr/v1/{path...}":  "Azure Container Registry data plane: the repository name carries slashes",
	"DELETE /acr/v1/{path...}": "Azure Container Registry data plane: the repository name carries slashes",

	// Azure RBAC reads are answered by middleware that runs before the mux,
	// because the azurerm provider looks role definitions up at every scope
	// and Go's mux admits no variable-length wildcard in the middle of a
	// pattern. The pattern reported here is the one the mux would have
	// reached, not the code that answered.
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}": "Azure RBAC scope-generic reads are served by middleware ahead of the mux",
}

// TestServiceConformance_AzureNoPhantomCoverage holds every operation the
// coverage probe counts as served to a route that actually names it.
func TestServiceConformance_AzureNoPhantomCoverage(t *testing.T) {
	_, byFile := loadSwaggerPaths(t)
	p := newAzureProber(t)
	routes := azureSimRoutes(p.srv)

	var phantoms []string
	files := make([]string, 0, len(byFile))
	for file := range byFile {
		files = append(files, file)
	}
	sort.Strings(files)

	for _, file := range files {
		host := azureProbeHost(file)
		for _, sp := range byFile[file] {
			literals := azureSpecLiterals(sp.Raw)
			if len(literals) == 0 {
				continue
			}
			// The probe counts an operation as served when ANY of its
			// candidate paths is, so the pattern to judge is the one that
			// answered — not the first candidate, which may name a coordinate
			// the route does not accept and fall through to a subtree owner.
			for _, path := range azureProbeCandidates(routes, sp) {
				res := p.serve(sp.Method, path, sp.Query, azureProbeAPIVersion(sp), host)
				if !res.served {
					continue
				}
				if res.pattern == "" || azurePhantomFanIn[res.pattern] != "" {
					break
				}
				var missing []string
				for _, lit := range literals {
					if !azurePatternHasLiteral(res.pattern, lit) {
						missing = append(missing, lit)
					}
				}
				if len(missing) > 0 {
					phantoms = append(phantoms, strings.TrimSuffix(file, ".swagger.json.gz")+": "+
						sp.Method+" /"+strings.Join(sp.Raw, "/")+
						"\n    matched "+res.pattern+
						"\n    routes on no segment named "+strings.Join(missing, ", "))
				}
				break
			}
		}
	}

	sort.Strings(phantoms)
	if len(phantoms) > 0 {
		t.Errorf("%d operation(s) count as served but reach a route that does not name them:\n\n%s",
			len(phantoms), strings.Join(phantoms, "\n\n"))
	}
}

// TestServiceConformance_AzurePhantomSweepIsSound guards the detector: it must
// see a swallowed sibling, and it must not flag a route that names its
// operation — including one whose casing differs from the specification's,
// which is the shape Azure Resource Manager's case-insensitive paths take.
func TestServiceConformance_AzurePhantomSweepIsSound(t *testing.T) {
	const owner = "GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.Web/sites/{name}"
	if azurePatternHasLiteral(owner, "config") {
		t.Error("a subtree-owning pattern reported as routing on a segment it swallows")
	}
	if !azurePatternHasLiteral(owner, "sites") {
		t.Error("a pattern reported as not routing on a segment it names")
	}
	if !azurePatternHasLiteral(
		"GET /subscriptions/{s}/providers/Microsoft.Web/serverFarms", "serverfarms") {
		t.Error("a literal failed to match the casing the simulator registers it under")
	}
	if got := azureSpecLiterals([]string{"subscriptions", "{subscriptionId}", "providers", "Microsoft.Web", "sites"}); len(got) != 4 {
		t.Errorf("literal segments = %v, want the four that carry no parameter", got)
	}
}
