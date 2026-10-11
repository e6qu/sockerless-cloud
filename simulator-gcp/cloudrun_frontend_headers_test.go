package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCloudRunRequestHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://svc-abcdefghij-us-central1.a.run.app/", nil)
	r.RemoteAddr = "198.51.100.4:51234"
	r.TLS = &tls.ConnectionState{}
	h := http.Header{
		"X-Forwarded-For":        {"203.0.113.7"},
		"X-Forwarded-Proto":      {"spoofed"},
		"X-Google-Internal-Flag": {"1"},
		"X-Googlebot":            {"kept"},
	}
	cloudRunRequestHeaders(r)(h)
	if got := h.Get("X-Forwarded-For"); got != "203.0.113.7,198.51.100.4" {
		t.Errorf("X-Forwarded-For = %q", got)
	}
	if got := h.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
	if _, ok := h["X-Google-Internal-Flag"]; ok {
		t.Error("an X-Google-* header the client sent reached the container")
	}
	if got := h.Get("X-Googlebot"); got != "kept" {
		t.Errorf("X-Googlebot = %q, want it untouched", got)
	}
	if got := h.Get("X-Cloud-Trace-Context"); got == "" {
		t.Error("no X-Cloud-Trace-Context added")
	}

	r.TLS = nil
	h = http.Header{"X-Cloud-Trace-Context": {"105445aa7843bc8bf206b12000100000/1;o=1"}}
	cloudRunRequestHeaders(r)(h)
	if got := h.Get("X-Forwarded-For"); got != "198.51.100.4" {
		t.Errorf("X-Forwarded-For = %q", got)
	}
	if got := h.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
	if got := h.Get("X-Cloud-Trace-Context"); got != "105445aa7843bc8bf206b12000100000/1;o=1" {
		t.Errorf("X-Cloud-Trace-Context = %q, want the client's", got)
	}
}
