package main

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An Application Gateway probe grades the status the server itself returned:
// the default criterion (200-399) accepts a redirect as it stands, and a match
// naming 200 alone rejects it, rather than either following it to the page it
// names.
func TestApplicationGatewayProbeGradesARedirectByItsOwnCode(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			http.Redirect(w, r, "/login", http.StatusFound)
		}
	}))
	defer backend.Close()
	host, portText, err := net.SplitHostPort(strings.TrimPrefix(backend.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	server := applicationGatewayServer{address: host}
	spec := applicationGatewayResolvedProbe{
		protocol: "Http", path: "/health", port: int32(port), timeout: 5 * time.Second,
		interval: 30 * time.Second, unhealthyThreshold: 3,
	}

	status, err := applicationGatewayRunProbe(context.Background(), spec, server)
	health, log := applicationGatewayProbeVerdict(spec, server, status, err)
	if health != "Up" || !strings.Contains(log, "Received status code 302") {
		t.Fatalf("default criterion: %s %q", health, log)
	}

	spec.statusCodes = []string{"200"}
	status, err = applicationGatewayRunProbe(context.Background(), spec, server)
	health, log = applicationGatewayProbeVerdict(spec, server, status, err)
	if health != "Down" || !strings.Contains(log, "Received status code 302") {
		t.Fatalf("200-only criterion: %s %q", health, log)
	}
}
