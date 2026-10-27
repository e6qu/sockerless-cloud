package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Tags-default middleware trims a fixed marker suffix and must not panic
// when case-folding changes a rune's byte length.
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

// Subdomain parsing (`{account}.blob.`, `{account}.{service}.localhost`) must
// not panic on a malformed host.
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

// newRawRequest sets URL.Path and Host without URL parsing, so the handlers see
// the raw, already-decoded bytes a malicious client's path reaches the mux as.
func newRawRequest(method, host, path string) *http.Request {
	req := httptest.NewRequest(method, "http://placeholder/", nil)
	req.URL.Path = path
	req.URL.RawPath = ""
	req.Host = host
	req.URL.Host = host
	return req
}
