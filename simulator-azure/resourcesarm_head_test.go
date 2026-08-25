package main

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The 204 mapping for Azure Resource Manager's existence checks must skip
// exactly the providers that serve HEAD themselves — no more, no fewer. Both
// halves matter: skipping one that does not serve HEAD would report a status
// the check never returns, and mapping one that does would overwrite a
// provider's own answer (API Management's entity-tag reads carry 200 and an
// ETag, and a client comparing tags would see a body-less 204 instead).
//
// The set is derived here from the vendored swaggers rather than trusted, so
// a re-vendor that gives another provider a HEAD operation fails until the
// list in resourcesarm.go agrees.
func TestAzureProviderOwnsHEADMatchesTheVendoredSwaggers(t *testing.T) {
	specs, err := filepath.Glob(filepath.Join("..", "specs", "cloud-api", "azure", "*.json.gz"))
	if err != nil || len(specs) == 0 {
		t.Fatalf("no vendored Azure specs found: %v", err)
	}
	declared := map[string]struct{}{}
	for _, path := range specs {
		for _, namespace := range headProviderNamespaces(t, path) {
			declared[namespace] = struct{}{}
		}
	}
	want := make([]string, 0, len(declared))
	for namespace := range declared {
		want = append(want, namespace)
	}
	sort.Strings(want)

	got := append([]string(nil), azureProviderOwnsHEADNamespaces...)
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the providers that declare HEAD operations are %v, the carve-out lists %v — "+
			"a provider serving HEAD must keep its own answer, and one that does not must get "+
			"the existence check's 204; update azureProviderOwnsHEADNamespaces",
			want, got)
	}
}

// headProviderNamespaces returns the provider namespaces one vendored
// swagger declares a HEAD operation for.
func headProviderNamespaces(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var document struct {
		Paths map[string]map[string]json.RawMessage `json:"paths"`
	}
	if err := json.NewDecoder(reader).Decode(&document); err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	var namespaces []string
	for route, verbs := range document.Paths {
		if _, hasHead := verbs["head"]; !hasHead {
			continue
		}
		_, after, found := strings.Cut(route, "/providers/")
		if !found {
			continue
		}
		namespace := strings.ToLower(strings.SplitN(after, "/", 2)[0])
		// The generic template's own placeholder is the existence check
		// itself, which is what the mapping serves rather than skips.
		if strings.HasPrefix(namespace, "{") {
			continue
		}
		namespaces = append(namespaces, namespace)
	}
	return namespaces
}
