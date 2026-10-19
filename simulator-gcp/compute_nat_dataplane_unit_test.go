package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
)

// A Cloud NAT's sidecar reports with the token the simulator minted for it
// and no Google credential: the bearer check lets the report through to the
// sidecar's own trust, which accepts the minted token and refuses anything
// else.
func TestCloudNATSidecarReportsPassTheBearerCheck(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	srv.WrapHandler(bearerAuthMiddleware(srv))
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, "nat-report-net")
	t.Cleanup(func() { sim.UnregisterSidecarToken(token) })

	report := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, egress.ReportPath, strings.NewReader(`{"gateway":"g","events":[]}`))
		if token != "" {
			req.Header.Set("X-Sockerless-Sidecar-Token", token)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	if rec := report(token); rec.Code != http.StatusNoContent {
		t.Fatalf("a report under the minted token = %d %s, want 204", rec.Code, rec.Body.String())
	}
	if rec := report(""); rec.Code != http.StatusUnauthorized || strings.Contains(rec.Body.String(), "UNAUTHENTICATED") {
		t.Fatalf("a report with no token = %d %s, want the sidecar trust's 401", rec.Code, rec.Body.String())
	}
}

// A Cloud NAT with logging on writes one nat_flows entry per established
// connection under the nat_gateway resource, naming the instance that opened
// it.
func TestCloudNATWritesAFlowEntryPerConnection(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := buildSimulator(sim.Config{Provider: "gcp", ListenAddr: ":0", LogLevel: "error"})
	if err != nil {
		t.Fatalf("buildSimulator: %v", err)
	}
	t.Cleanup(srv.StopBackground)
	gcpTestCreateProject(t, srv, gcpTestProjectID)
	project := gcpTestProjectID
	subnet := ComputeSubnetwork{
		Name:        "flow-subnet",
		SelfLink:    "projects/" + project + "/regions/us-central1/subnetworks/flow-subnet",
		Network:     "projects/" + project + "/global/networks/flow-net",
		Region:      "projects/" + project + "/regions/us-central1",
		IpCidrRange: "10.71.0.0/24",
	}
	gcpSubnetworks.Put(subnet.SelfLink, subnet)
	inst := ComputeInstance{
		Name:     "flow-vm",
		SelfLink: "projects/" + project + "/zones/us-central1-a/instances/flow-vm",
		Zone:     "projects/" + project + "/zones/us-central1-a",
		NetworkInterfaces: []ComputeNetworkInterface{{
			Name: "nic0", Network: subnet.Network, Subnetwork: subnet.SelfLink, NetworkIP: "10.71.0.2",
		}},
	}
	gcpInstances.Put(inst.SelfLink, inst)
	gcpNATEgressMu.Lock()
	gcpNATEgresses[subnet.SelfLink] = &gcpNATEgress{
		network: "flow-engine-net", router: "projects/" + project + "/regions/us-central1/routers/flow-router",
		nat: "flow-nat", routerID: "42", subnet: subnet,
		logging: &ComputeRouterNATLogConfig{Enable: true, Filter: "ALL"},
	}
	gcpNATEgressMu.Unlock()
	t.Cleanup(func() {
		gcpNATEgressMu.Lock()
		delete(gcpNATEgresses, subnet.SelfLink)
		gcpNATEgressMu.Unlock()
	})

	gcpTakeNATEgressReport("flow-engine-net", egress.Report{Gateway: subnet.SelfLink, Events: []egress.Event{
		{Kind: egress.Opened, Protocol: "tcp", Source: "10.71.0.2", SourcePort: 40000, Destination: "192.0.2.10", DestinationPort: 80, At: time.Now()},
		{Kind: egress.Established, Protocol: "tcp", Source: "10.71.0.2", SourcePort: 40000, Destination: "192.0.2.10", DestinationPort: 80, ViaPort: 51000, At: time.Now()},
	}})

	out := gcpHostOK(t, srv, "logging.googleapis.com", http.MethodPost, "/v2/entries:list",
		`{"resourceNames":["projects/`+project+`"],"filter":"resource.type=\"nat_gateway\""}`)
	entries, _ := out["entries"].([]any)
	if len(entries) != 1 {
		t.Fatalf("nat_gateway entries = %v, want one", out)
	}
	entry, _ := entries[0].(map[string]any)
	payload, _ := entry["jsonPayload"].(map[string]any)
	connection, _ := payload["connection"].(map[string]any)
	endpoint, _ := payload["endpoint"].(map[string]any)
	resource, _ := entry["resource"].(map[string]any)
	labels, _ := resource["labels"].(map[string]any)
	if entry["logName"] != "projects/"+project+"/logs/compute.googleapis.com%2Fnat_flows" ||
		labels["gateway_name"] != "flow-nat" || connection["src_ip"] != "10.71.0.2" ||
		endpoint["vm_name"] != "flow-vm" || payload["allocation_status"] != "OK" {
		t.Fatalf("the flow entry is %v", entry)
	}
}
