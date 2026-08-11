package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
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

	rules := gcpIngressPacketRules(target, ComputeNetworkInterface{Name: "nic0", Network: network})
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

func TestGCPComputeLoadBalancerDataPlaneProxiesHealthyInstanceGroupMember(t *testing.T) {
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
	defer func() {
		gcpHealthChecks = nil
		gcpBackendServices = nil
		gcpURLMaps = nil
		gcpTargetHTTPProxies = nil
		gcpForwardingRules = nil
		gcpInstanceGroups = nil
		gcpInstances = nil
	}()
	registerGCPComputeLoadBalancerDataPlane(srv)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		_, _ = w.Write([]byte("gcp-lb-target"))
	}))
	defer target.Close()
	targetURL, err := url.Parse(target.URL)
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
		Name:       "hc",
		SelfLink:   "projects/test-project/global/healthChecks/hc",
		Type:       "HTTP",
		TimeoutSec: 2,
		HttpHealthCheck: &ComputeHTTPHealthCheck{
			Port:        int64(port),
			RequestPath: "/healthz",
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

	req := httptest.NewRequest(http.MethodGet, "http://simulator/work", nil)
	req.Host = fr.IPAddress
	rr := httptest.NewRecorder()
	srv.Mux().ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("data-plane status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "gcp-lb-target" {
		t.Fatalf("data-plane body = %q", rr.Body.String())
	}
}
