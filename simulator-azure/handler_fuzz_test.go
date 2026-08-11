package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// FuzzTagsScopeRouting drives the Tags-default middleware with arbitrary URL
// paths. The middleware derives the scope by trimming a fixed marker suffix;
// it must never panic, even when the path contains multi-byte runes whose
// case-folding changes the byte length relative to the lowercased copy.
func FuzzTagsScopeRouting(f *testing.F) {
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		f.Fatalf("new azure sim server: %v", err)
	}
	registerTags(srv)

	seeds := []string{
		"/subscriptions/s1/providers/Microsoft.Resources/tags/default",
		"/SUBSCRIPTIONS/s1/PROVIDERS/MICROSOFT.RESOURCES/TAGS/DEFAULT",
		"/providers/Microsoft.Resources/tags/default",
		"İ/providers/Microsoft.Resources/tags/default",
		"K/providers/Microsoft.Resources/tags/default", // Kelvin sign -> 'k'
		"/tags/default",
		"/",
		"",
		"default",
		strings.Repeat("İ", 50) + "/providers/Microsoft.Resources/tags/default",
	}
	for _, s := range seeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, path string) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req := newRawRequest(http.MethodGet, "localhost", path)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req) // must not panic
	})
}

// FuzzStorageHostRouting drives the storage data-plane host-routing middleware
// with arbitrary Host headers and paths. Subdomain parsing (`{account}.blob.`,
// `{account}.{service}.localhost`) must never panic on a malformed host.
func FuzzStorageHostRouting(f *testing.F) {
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		f.Fatalf("new azure sim server: %v", err)
	}
	registerAzureFiles(srv)
	registerBlobDataPlane(srv)

	type hp struct{ host, path string }
	seeds := []hp{
		{"acct.blob.localhost:4568", "/container/blob"},
		{"acct.file.localhost", "/share/dir"},
		{".blob.localhost", "/"},
		{"acct.blob.", "/x"},
		{"localhost", "/"},
		{"", ""},
		{"a.b.c.localhost", "/p"},
		{"İ.blob.localhost", "/c/b"},
		{":::", "/"},
	}
	for _, s := range seeds {
		f.Add(s.host, s.path)
	}
	f.Fuzz(func(t *testing.T, host, path string) {
		if !strings.HasPrefix(path, "/") {
			path = "/" + path
		}
		req := newRawRequest(http.MethodGet, host, path)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req) // must not panic
	})
}

// newRawRequest builds an *http.Request whose URL.Path and Host are set to
// arbitrary (possibly control-character-bearing) strings WITHOUT going through
// URL parsing, so the sim handlers — not net/url — see the raw bytes. This is
// how a malicious client's already-decoded path reaches the mux.
func newRawRequest(method, host, path string) *http.Request {
	req := httptest.NewRequest(method, "http://placeholder/", nil)
	req.URL.Path = path
	req.URL.RawPath = ""
	req.Host = host
	req.URL.Host = host
	return req
}
