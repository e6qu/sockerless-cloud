package main

import (
	"sort"
	"strings"
	"testing"
)

// A served count can hide an unserved operation: a route owning a subtree
// answers for every path beneath it, including a sibling collection nobody
// implemented, with a structured 404 or empty list the probe cannot tell from
// a served read of an absent resource.
//
// The mux reports which pattern matched. A Swagger path's literal segments are
// structural, so a matched pattern missing one did not route the operation.
// Unlike the Google Cloud detector (simulator-gcp/gcp_phantom_coverage_test.go),
// literals match case-insensitively: Azure Resource Manager paths are, and the
// simulator registers both `serverfarms` and `serverFarms`.

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

// azurePhantomFanIn are routes that own a subtree and dispatch inside the
// handler, so a literal missing from the pattern is not a missing route. Each
// entry names what dispatches.
var azurePhantomFanIn = map[string]string{
	// Key Vault, Cosmos DB Table and Service Bus serve their own path space under
	// their own host: the Host header selects the service, and one route accepts
	// the whole path.
	"GET /{resourceId}":    "data-plane fan-in: the Host header selects the service and the handler dispatches on it",
	"PUT /{resourceId}":    "data-plane fan-in: the Host header selects the service and the handler dispatches on it",
	"PATCH /{resourceId}":  "data-plane fan-in: the Host header selects the service and the handler dispatches on it",
	"DELETE /{resourceId}": "data-plane fan-in: the Host header selects the service and the handler dispatches on it",

	// A repository name carries slashes, so `/v2/{name}/manifests/{reference}`
	// cannot be a mux pattern.
	"GET /v2/":    "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",
	"POST /v2/":   "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",
	"PUT /v2/":    "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",
	"DELETE /v2/": "OCI distribution: a repository name carries slashes, so the mux cannot name the segments after it",

	"GET /acr/v1/{path...}":    "Azure Container Registry data plane: the repository name carries slashes",
	"PATCH /acr/v1/{path...}":  "Azure Container Registry data plane: the repository name carries slashes",
	"DELETE /acr/v1/{path...}": "Azure Container Registry data plane: the repository name carries slashes",

	// Middleware ahead of the mux answers Azure RBAC reads: the azurerm provider
	// reads role definitions at every scope, and Go's mux admits no
	// variable-length wildcard mid-pattern. The pattern is the one the mux would
	// have reached.
	"GET /subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/{resourceProviderNamespace}/{parentResourcePath}/{resourceType}/{resourceName}": "Azure RBAC scope-generic reads are served by middleware ahead of the mux",
}

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
			// The probe counts an operation served when any candidate is, so judge the
			// pattern that answered, not the first candidate.
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

// TestServiceConformance_AzurePhantomSweepIsSound: the detector must see a
// swallowed sibling and must not flag a route naming its operation in other
// casing.
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
