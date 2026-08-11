package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The metadata server advertises its contents through directory listings, and a
// client walks those listings to find its credentials. A listing that names a
// key no handler serves would send every such client to a 404, so these tests
// walk the tree the listings are rendered from and GET every node it names
// against the simulator main() serves.

// metadataTreeServer builds the simulator the way main() does and returns a
// handler plus a helper that performs a metadata read against it.
func metadataTreeServer(t *testing.T) func(path string) *httptest.ResponseRecorder {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	srv.WrapHandler(bearerAuthMiddleware(srv))
	return func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Metadata-Flavor", "Google")
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
}

// TestMetadataDirectoryListingsResolve walks every directory the metadata tree
// describes and GETs each child it lists. A listed directory must answer with
// its own listing and a listed leaf must answer with its value — no entry may
// be advertised without a handler behind it.
func TestMetadataDirectoryListingsResolve(t *testing.T) {
	get := metadataTreeServer(t)

	// walk visits one directory: it reads the served listing, checks it names
	// exactly the tree's children, and recurses into each entry.
	var walk func(dirPath string, children []gceMetadataEntry)
	walk = func(dirPath string, children []gceMetadataEntry) {
		rec := get(dirPath)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200 (body %q)", dirPath, rec.Code, rec.Body.String())
		}
		if flavor := rec.Header().Get("Metadata-Flavor"); flavor != "Google" {
			t.Errorf("GET %s returned Metadata-Flavor %q, want Google", dirPath, flavor)
		}
		listed := strings.Fields(rec.Body.String())
		if len(listed) != len(children) {
			t.Fatalf("GET %s listed %v, want %d entries from the tree", dirPath, listed, len(children))
		}
		for i, child := range children {
			if listed[i] != child.listingName() {
				t.Errorf("GET %s entry %d = %q, want %q", dirPath, i, listed[i], child.listingName())
			}
			childPath := dirPath + child.name
			if child.isDir() {
				req := httptest.NewRequest(http.MethodGet, childPath, nil)
				walk(childPath+"/", child.children(req))
				continue
			}
			// A leaf the listing names must be served. `identity` is the one
			// leaf that requires a parameter — real Google rejects it without
			// an audience — so it is asked for the way a client asks.
			if child.name == "identity" {
				childPath += "?audience=https://sockerless.test"
			}
			if rec := get(childPath); rec.Code != http.StatusOK {
				t.Errorf("listing %s advertises %q but GET %s = %d (body %q)",
					dirPath, child.name, childPath, rec.Code, rec.Body.String())
			}
		}
	}

	walk("/computeMetadata/v1/", gceMetadataV1Children(httptest.NewRequest(http.MethodGet, "/", nil)))
}

// TestMetadataRootResidencyProbe proves the probe every Google auth library
// runs before it resolves Application Default Credentials: a GET of the
// server's root carrying `Metadata-Flavor: Google`, answered with that header
// and the root listing. A request to `/` without the header is not a metadata
// read and must reach whatever else owns the origin's root — the console —
// untouched.
func TestMetadataRootResidencyProbe(t *testing.T) {
	get := metadataTreeServer(t)

	rec := get("/")
	if rec.Code != http.StatusOK {
		t.Fatalf("residency probe = %d, want 200 (body %q)", rec.Code, rec.Body.String())
	}
	if flavor := rec.Header().Get("Metadata-Flavor"); flavor != "Google" {
		t.Errorf("residency probe returned Metadata-Flavor %q, want Google — a client rejects any other answer", flavor)
	}
	if got := strings.Fields(rec.Body.String()); len(got) != 1 || got[0] != "computeMetadata/" {
		t.Errorf("root listing = %v, want [computeMetadata/]", got)
	}

	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	srv.WrapHandler(bearerAuthMiddleware(srv))
	plain := httptest.NewRecorder()
	srv.ServeHTTP(plain, httptest.NewRequest(http.MethodGet, "/", nil))
	if plain.Header().Get("Metadata-Flavor") != "" {
		t.Errorf("GET / without the header must not be answered by the metadata server, got %v", plain.Header())
	}
}

// TestMetadataDirectoryRedirectsToTrailingSlash proves a directory addressed
// without its trailing slash answers with the 301 real Google answers, and that
// the redirect preserves the query so a redirect-following client keeps its
// recursive request.
func TestMetadataDirectoryRedirectsToTrailingSlash(t *testing.T) {
	get := metadataTreeServer(t)

	rec := get("/computeMetadata/v1/instance/service-accounts")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET without trailing slash = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/computeMetadata/v1/instance/service-accounts/" {
		t.Errorf("Location = %q, want the trailing-slash form", got)
	}

	rec = get("/computeMetadata/v1/project?recursive=true")
	if rec.Code != http.StatusMovedPermanently {
		t.Fatalf("GET without trailing slash = %d, want 301", rec.Code)
	}
	if got := rec.Header().Get("Location"); got != "/computeMetadata/v1/project/?recursive=true" {
		t.Errorf("Location = %q, want the query preserved", got)
	}
}

// TestMetadataRecursiveOmitsMintedLeaves proves recursive output carries the
// stored keys with the camelCase spelling real Google uses, and omits `token`
// and `identity` — the two leaves the metadata server mints per request rather
// than stores.
func TestMetadataRecursiveOmitsMintedLeaves(t *testing.T) {
	get := metadataTreeServer(t)

	rec := get("/computeMetadata/v1/instance/service-accounts/default/?recursive=true")
	if rec.Code != http.StatusOK {
		t.Fatalf("recursive read = %d (body %q)", rec.Code, rec.Body.String())
	}
	var account map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &account); err != nil {
		t.Fatalf("recursive read is not JSON: %v (body %q)", err, rec.Body.String())
	}
	for _, want := range []string{"aliases", "email", "scopes"} {
		if _, ok := account[want]; !ok {
			t.Errorf("recursive account output is missing %q: %v", want, account)
		}
	}
	for _, minted := range []string{"token", "identity"} {
		if _, ok := account[minted]; ok {
			t.Errorf("recursive account output must omit the minted %q leaf: %v", minted, account)
		}
	}

	rec = get("/computeMetadata/v1/project/?recursive=true")
	var project map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatalf("recursive project read is not JSON: %v (body %q)", err, rec.Body.String())
	}
	// Real Google spells the kebab-case keys camelCase in recursive output and
	// carries the project number as a JSON number.
	if _, ok := project["numericProjectId"]; !ok {
		t.Errorf("recursive project output must spell numeric-project-id as numericProjectId: %v", project)
	}
	if _, ok := project["projectId"]; !ok {
		t.Errorf("recursive project output must spell project-id as projectId: %v", project)
	}
	if _, ok := project["numericProjectId"].(float64); !ok {
		t.Errorf("numericProjectId must be a JSON number, got %T", project["numericProjectId"])
	}
}

// TestMetadataDirectoryRequiresFlavorHeader proves the directory endpoints are
// gated by the same `Metadata-Flavor: Google` header real Google requires on
// every metadata read.
func TestMetadataDirectoryRequiresFlavorHeader(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	srv.WrapHandler(bearerAuthMiddleware(srv))

	for _, path := range []string{
		"/computeMetadata/v1/",
		"/computeMetadata/v1/instance/service-accounts/",
		"/computeMetadata/v1/instance/service-accounts/default/",
		"/computeMetadata/v1/universe/universe-domain",
	} {
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s without Metadata-Flavor = %d, want 403", path, rec.Code)
		}
	}
}
