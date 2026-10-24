package main

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAppServiceForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://app.azurewebsites.net/", nil)
	r.RemoteAddr = "198.51.100.4:51234"
	r.TLS = &tls.ConnectionState{}
	h := http.Header{"X-Forwarded-For": {"203.0.113.7"}, "X-Forwarded-Proto": {"spoofed"}}
	appServiceForwardedHeaders(r, &Site{})(h)
	if got := h.Get("X-Forwarded-For"); got != "203.0.113.7, 198.51.100.4:51234" {
		t.Errorf("X-Forwarded-For = %q", got)
	}
	for _, name := range []string{"X-Forwarded-Proto", "X-AppService-Proto"} {
		if got := h.Get(name); got != "https" {
			t.Errorf("%s = %q, want https", name, got)
		}
	}

	r.TLS = nil
	h = http.Header{}
	appServiceForwardedHeaders(r, &Site{})(h)
	if got := h.Get("X-Forwarded-For"); got != "198.51.100.4:51234" {
		t.Errorf("X-Forwarded-For = %q", got)
	}
	for _, name := range []string{"X-Forwarded-Proto", "X-AppService-Proto"} {
		if got := h.Get(name); got != "http" {
			t.Errorf("%s = %q, want http", name, got)
		}
	}
}

func TestContainerAppsIngressForwardedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "https://app.example.azurecontainerapps.io/", nil)
	r.RemoteAddr = "198.51.100.4:51234"
	r.TLS = &tls.ConnectionState{}
	h := http.Header{"X-Forwarded-For": {"203.0.113.7"}, "X-Forwarded-Proto": {"spoofed"}}
	acaIngressForwardedHeaders(r, ContainerApp{})(h)
	if got := h.Get("X-Forwarded-For"); got != "203.0.113.7, 198.51.100.4" {
		t.Errorf("X-Forwarded-For = %q", got)
	}
	if got := h.Get("X-Forwarded-Proto"); got != "https" {
		t.Errorf("X-Forwarded-Proto = %q, want the client's protocol over the one it sent", got)
	}

	r.TLS = nil
	h = http.Header{}
	acaIngressForwardedHeaders(r, ContainerApp{})(h)
	if got := h.Get("X-Forwarded-For"); got != "198.51.100.4" {
		t.Errorf("X-Forwarded-For = %q", got)
	}
	if got := h.Get("X-Forwarded-Proto"); got != "http" {
		t.Errorf("X-Forwarded-Proto = %q", got)
	}
}
