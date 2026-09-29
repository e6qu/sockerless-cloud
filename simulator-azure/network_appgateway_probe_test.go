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

// A probe's minServers is the "minimum number of servers that are always
// marked healthy": with no member probed Up, a gateway whose probe sets it
// still routes to the pool's first member, and one whose probe leaves it at 0
// routes nowhere.
func TestApplicationGatewayMinServersKeepsAMemberInService(t *testing.T) {
	gw := ApplicationGateway{}
	gw.ID = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/applicationGateways/minservers"
	probeID := gw.ID + "/probes/p"
	gw.Properties.Probes = []ApplicationGatewayProbe{{applicationGatewayChild: applicationGatewayChild{ID: probeID, Name: "p"}}}
	pool := ApplicationGatewayBackendAddressPool{applicationGatewayChild: applicationGatewayChild{ID: gw.ID + "/backendAddressPools/pool"}}
	pool.Properties.BackendAddresses = []ApplicationGatewayBackendAddress{{IPAddress: "10.9.0.4"}, {IPAddress: "10.9.0.5"}}
	settings := ApplicationGatewayBackendHTTPSettings{applicationGatewayChild: applicationGatewayChild{ID: gw.ID + "/backendHttpSettingsCollection/s"}}
	settings.Properties.Probe = &SubResource{ID: probeID}

	if server, ok := applicationGatewayHealthyServer(gw, pool, settings); ok {
		t.Fatalf("minServers 0 routed to %s with no member probed Up", server.address)
	}
	gw.Properties.Probes[0].Properties.MinServers = 1
	server, ok := applicationGatewayHealthyServer(gw, pool, settings)
	if !ok || server.address != "10.9.0.4" {
		t.Fatalf("minServers 1 routed to %q (%v), want the first member 10.9.0.4", server.address, ok)
	}
}
