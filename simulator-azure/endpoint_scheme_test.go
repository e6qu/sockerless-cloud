package main

import (
	"crypto/tls"
	"net/http/httptest"
	"testing"
)

// Behind a TLS-terminating gateway the request arrives over plaintext, so
// trusting r.TLS alone would advertise http:// URLs to clients that reach the
// deployment only over https://.
func TestAzureRequestSchemeHonorsForwardedProto(t *testing.T) {
	for _, tc := range []struct {
		name      string
		forwarded string
		tls       bool
		want      string
	}{
		{name: "plain request", want: "http"},
		{name: "direct TLS", tls: true, want: "https"},
		{name: "behind TLS gateway", forwarded: "https", want: "https"},
		{name: "forwarded case-insensitive", forwarded: "HTTPS", want: "https"},
		{name: "proxy chain keeps the client hop", forwarded: "https, http", want: "https"},
		{name: "gateway reports plaintext", forwarded: "http", tls: true, want: "http"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			if tc.forwarded != "" {
				r.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			if tc.tls {
				r.TLS = &tls.ConnectionState{HandshakeComplete: true}
			}
			if got := azureRequestScheme(r); got != tc.want {
				t.Fatalf("azureRequestScheme = %q, want %q", got, tc.want)
			}
		})
	}
}
