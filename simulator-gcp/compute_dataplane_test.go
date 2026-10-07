package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func TestGCPFirewallCompilerPreservesPriorityDenyAndSourceTags(t *testing.T) {
	gcpFirewalls = sim.MakeStore[ComputeFirewall](nil, "test_firewalls")
	gcpInstances = sim.MakeStore[ComputeInstance](nil, "test_instances")
	defer func() {
		gcpFirewalls = nil
		gcpInstances = nil
	}()

	network := "projects/test-project/global/networks/test-net"
	source := ComputeInstance{
		Name:     "source",
		SelfLink: "projects/test-project/zones/us-central1-a/instances/source",
		Tags:     &ComputeInstanceTags{Items: []string{"runner"}},
		NetworkInterfaces: []ComputeNetworkInterface{{
			Name:      "nic0",
			Network:   network,
			NetworkIP: "10.42.0.2",
		}},
	}
	target := ComputeInstance{
		Name:     "target",
		SelfLink: "projects/test-project/zones/us-central1-a/instances/target",
		Tags:     &ComputeInstanceTags{Items: []string{"web"}},
	}
	gcpInstances.Put(source.SelfLink, source)
	gcpInstances.Put(target.SelfLink, target)
	gcpFirewalls.Put("projects/test-project/global/firewalls/deny-ssh", ComputeFirewall{
		Name:       "deny-ssh",
		SelfLink:   "projects/test-project/global/firewalls/deny-ssh",
		Network:    network,
		Direction:  "INGRESS",
		Priority:   500,
		TargetTags: []string{"web"},
		Denied: []ComputeFirewallAction{{
			IPProtocol: "tcp",
			Ports:      []string{"22"},
		}},
	})
	gcpFirewalls.Put("projects/test-project/global/firewalls/allow-runner", ComputeFirewall{
		Name:       "allow-runner",
		SelfLink:   "projects/test-project/global/firewalls/allow-runner",
		Network:    network,
		Direction:  "INGRESS",
		Priority:   1000,
		SourceTags: []string{"runner"},
		TargetTags: []string{"web"},
		Allowed: []ComputeFirewallAction{{
			IPProtocol: "tcp",
			Ports:      []string{"80-81"},
		}},
	})

	rules, err := gcpIngressPacketRules(target, ComputeNetworkInterface{Name: "nic0", Network: network})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 {
		t.Fatalf("compiled rules = %d, want 2: %+v", len(rules), rules)
	}
	if rules[0].Action != "drop" || rules[0].FromPort != 22 || rules[0].ToPort != 22 {
		t.Fatalf("first rule = %+v, want priority deny tcp/22", rules[0])
	}
	if rules[1].Action != "accept" || rules[1].SourceCIDR != "10.42.0.2/32" || rules[1].FromPort != 80 || rules[1].ToPort != 81 {
		t.Fatalf("second rule = %+v, want source-tag allow tcp/80-81", rules[1])
	}
}

// gcpLBFixture is one external Application Load Balancer chain — forwarding
// rule, target HTTP proxy, URL map, backend service, health check and an
// instance group whose one member is the httptest target.
type gcpLBFixture struct {
	srv *sim.Server
	fr  ComputeForwardingRule
	bs  ComputeBackendService
	hc  ComputeHealthCheck
}

func newGCPLBFixture(t *testing.T, target http.Handler, healthPath string) gcpLBFixture {
	t.Helper()
	srv, err := sim.NewServer(sim.Config{Provider: "gcp", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	gcpHealthChecks = sim.MakeStore[ComputeHealthCheck](nil, "test_hc")
	gcpBackendServices = sim.MakeStore[ComputeBackendService](nil, "test_bs")
	gcpURLMaps = sim.MakeStore[ComputeURLMap](nil, "test_um")
	gcpTargetHTTPProxies = sim.MakeStore[ComputeTargetHTTPProxy](nil, "test_proxy")
	gcpForwardingRules = sim.MakeStore[ComputeForwardingRule](nil, "test_fr")
	gcpInstanceGroups = sim.MakeStore[storedComputeInstanceGroup](nil, "test_ig")
	gcpInstances = sim.MakeStore[ComputeInstance](nil, "test_instances")
	gcpBackendHealth.Reset()
	t.Cleanup(func() {
		gcpHealthChecks = nil
		gcpBackendServices = nil
		gcpURLMaps = nil
		gcpTargetHTTPProxies = nil
		gcpForwardingRules = nil
		gcpInstanceGroups = nil
		gcpInstances = nil
		gcpBackendHealth.Reset()
	})
	registerGCPComputeLoadBalancerDataPlane(srv)

	backend := httptest.NewServer(target)
	t.Cleanup(backend.Close)
	targetURL, err := url.Parse(backend.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(targetURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	instance := ComputeInstance{
		Name:     "backend",
		SelfLink: "projects/test-project/zones/us-central1-a/instances/backend",
		NetworkInterfaces: []ComputeNetworkInterface{{
			Name:      "nic0",
			NetworkIP: host,
		}},
	}
	group := storedComputeInstanceGroup{
		ComputeInstanceGroup: ComputeInstanceGroup{
			Name:       "ig",
			SelfLink:   "projects/test-project/zones/us-central1-a/instanceGroups/ig",
			NamedPorts: []ComputeInstanceGroupNamedPort{{Name: "http", Port: int64(port)}},
		},
		Instances: []ComputeInstanceGroupInstance{{Instance: instance.SelfLink}},
	}
	hc := ComputeHealthCheck{
		Name:               "hc",
		SelfLink:           "projects/test-project/global/healthChecks/hc",
		Type:               "HTTP",
		CheckIntervalSec:   5,
		TimeoutSec:         2,
		HealthyThreshold:   2,
		UnhealthyThreshold: 2,
		HttpHealthCheck: &ComputeHTTPHealthCheck{
			PortSpecification: "USE_SERVING_PORT",
			RequestPath:       healthPath,
		},
	}
	bs := ComputeBackendService{
		Name:         "bs",
		SelfLink:     "projects/test-project/global/backendServices/bs",
		Protocol:     "HTTP",
		PortName:     "http",
		TimeoutSec:   2,
		HealthChecks: []string{hc.SelfLink},
		Backends:     []ComputeBackendServiceBackend{{Group: group.SelfLink}},
	}
	urlMap := ComputeURLMap{Name: "um", SelfLink: "projects/test-project/global/urlMaps/um", DefaultService: bs.SelfLink}
	proxy := ComputeTargetHTTPProxy{Name: "proxy", SelfLink: "projects/test-project/global/targetHttpProxies/proxy", UrlMap: urlMap.SelfLink}
	fr := ComputeForwardingRule{Name: "fr", SelfLink: "projects/test-project/global/forwardingRules/fr", IPAddress: "8.34.210.44", PortRange: "80", Target: proxy.SelfLink}

	gcpInstances.Put(instance.SelfLink, instance)
	gcpInstanceGroups.Put(group.SelfLink, group)
	gcpHealthChecks.Put(hc.SelfLink, hc)
	gcpBackendServices.Put(bs.SelfLink, bs)
	gcpURLMaps.Put(urlMap.SelfLink, urlMap)
	gcpTargetHTTPProxies.Put(proxy.SelfLink, proxy)
	gcpForwardingRules.Put(fr.SelfLink, fr)
	return gcpLBFixture{srv: srv, fr: fr, bs: bs, hc: hc}
}

// sweep runs the health checker n times, one checkIntervalSec apart.
func (f gcpLBFixture) sweep(start time.Time, n int) time.Time {
	for range n {
		gcpSweepBackendHealth(context.Background(), start)
		start = start.Add(time.Duration(f.hc.CheckIntervalSec) * time.Second)
	}
	return start
}

func (f gcpLBFixture) request(host, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "http://simulator"+path, nil)
	req.Host = host
	rr := httptest.NewRecorder()
	f.srv.Mux().ServeHTTP(rr, req)
	return rr
}

func (f gcpLBFixture) healthState(t *testing.T) string {
	t.Helper()
	health := gcpBackendServiceHealth(f.bs, "")
	if len(health) != 1 {
		t.Fatalf("health entries = %+v, want one", health)
	}
	state, _ := health[0]["healthState"].(string)
	return state
}

func TestGCPComputeLoadBalancerDataPlaneProxiesHealthyInstanceGroupMember(t *testing.T) {
	f := newGCPLBFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		_, _ = w.Write([]byte("gcp-lb-target host=" + r.Host))
	}), "/healthz")

	// The backend enters service only after healthyThreshold consecutive
	// successful probes, each checkIntervalSec apart.
	now := f.sweep(time.Now(), 1)
	if rr := f.request(f.fr.IPAddress, "/work"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status before healthyThreshold = %d, want 503", rr.Code)
	}
	if state := f.healthState(t); state != "UNHEALTHY" {
		t.Fatalf("healthState before healthyThreshold = %q", state)
	}
	f.sweep(now, 1)
	if state := f.healthState(t); state != "HEALTHY" {
		t.Fatalf("healthState after healthyThreshold = %q", state)
	}

	rr := f.request(f.fr.IPAddress, "/work")
	if rr.Code != http.StatusOK {
		t.Fatalf("data-plane status = %d, body = %q", rr.Code, rr.Body.String())
	}
	// The load balancer passes the client's Host header to the backend.
	if strings.TrimSpace(rr.Body.String()) != "gcp-lb-target host="+f.fr.IPAddress {
		t.Fatalf("data-plane body = %q", rr.Body.String())
	}
}

// A health check succeeds on HTTP 200 alone and never follows a redirect: a
// backend answering its health path with a 302 is unhealthy even though the
// page it redirects to answers 200.
func TestGCPHealthCheckJudgesARedirectAsUnhealthy(t *testing.T) {
	f := newGCPLBFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			http.Redirect(w, r, "/login", http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}), "/healthz")

	f.sweep(time.Now(), 3)
	if state := f.healthState(t); state != "UNHEALTHY" {
		t.Fatalf("healthState = %q, want UNHEALTHY", state)
	}
	if rr := f.request(f.fr.IPAddress, "/"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 with no healthy backend", rr.Code)
	}
}

// Two forwarding rules may share one address on different ports; the port the
// request arrived on picks between them.
func TestGCPForwardingRulesShareAnAddressAcrossPorts(t *testing.T) {
	f := newGCPLBFixture(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}), "/")
	other := ComputeForwardingRule{
		Name: "early", SelfLink: "projects/test-project/global/forwardingRules/early",
		IPAddress: f.fr.IPAddress, PortRange: "8080", Target: "projects/test-project/global/targetHttpProxies/absent",
	}
	// The 8080 rule lists first, so an address lookup that ignored the port
	// would pick it for a request on port 80.
	gcpForwardingRules.Put(other.SelfLink, other)
	f.sweep(time.Now(), 2)

	if rr := f.request(f.fr.IPAddress, "/"); rr.Code != http.StatusOK {
		t.Fatalf("port 80 status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if rr := f.request(f.fr.IPAddress+":8080", "/"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("port 8080 status = %d, want the 8080 rule's missing proxy to answer 503", rr.Code)
	}
	if rr := f.request(f.fr.IPAddress+":9090", "/"); rr.Code != http.StatusNotFound {
		t.Fatalf("port 9090 status = %d, want 404 for an address no rule serves on that port", rr.Code)
	}
}

// A backend that closes its connection in the middle of a chunked body has
// already had its status and headers relayed, so the load balancer ends the
// client's response where the backend stopped rather than appending an error
// to the body.
func TestGCPComputeLoadBalancerDataPlaneAbortsAResponseTheBackendCutsShort(t *testing.T) {
	f := newGCPLBFixture(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		conn, buf, err := http.NewResponseController(w).Hijack()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n7\r\npartial\r\n")
		_ = buf.Flush()
	}), "/healthz")
	f.sweep(time.Now(), 2)
	front := httptest.NewServer(f.srv.Mux())
	t.Cleanup(front.Close)

	req, err := http.NewRequest(http.MethodGet, front.URL+"/work", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = f.fr.IPAddress
	resp, err := front.Client().Do(req)
	if err != nil {
		t.Fatalf("request through the load balancer: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want the backend's 200", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read error = %v, want the response to end where the backend stopped", err)
	}
	if string(body) != "partial" {
		t.Fatalf("body = %q, want only what the backend sent", body)
	}
}
