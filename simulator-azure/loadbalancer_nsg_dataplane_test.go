package main

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

func TestAzureNSGCompilerPreservesPriorityAndVNetDefault(t *testing.T) {
	// These are package-level stores shared with every other test in the
	// package. Restore what was there rather than clearing to nil: a nil store
	// makes any later test that reads one without first rebuilding the
	// simulator panic on a nil map, which turns this test into an ordering
	// hazard for tests it has nothing to do with.
	priorNSGs, priorSubnets, priorVnets := azureNSGs, azureSubnets, azureVnets
	azureNSGs = sim.MakeStore[NetworkSecurityGroup](nil, "test_nsgs")
	azureSubnets = sim.MakeStore[Subnet](nil, "test_subnets")
	azureVnets = sim.MakeStore[VirtualNetwork](nil, "test_vnets")
	defer func() {
		azureNSGs, azureSubnets, azureVnets = priorNSGs, priorSubnets, priorVnets
	}()

	vnetID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/virtualNetworks/vnet"
	subnetID := vnetID + "/subnets/default"
	nsgID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkSecurityGroups/web"
	azureVnets.Put(vnetID, VirtualNetwork{
		ID: vnetID,
		Properties: VNetProperties{
			AddressSpace: AddressSpace{AddressPrefixes: []string{"10.0.0.0/16"}},
		},
	})
	azureSubnets.Put(subnetID, Subnet{
		ID: subnetID,
		Properties: SubnetProperties{
			AddressPrefix:        "10.0.1.0/24",
			NetworkSecurityGroup: &NSGReference{ID: nsgID},
		},
	})
	azureNSGs.Put(nsgID, NetworkSecurityGroup{
		ID: nsgID,
		Properties: NSGProperties{SecurityRules: []SecurityRule{
			{
				Name: "allow-http",
				Properties: SecurityRuleProperties{
					Protocol:             "Tcp",
					SourceAddressPrefix:  "Internet",
					DestinationPortRange: "80",
					Access:               "Allow",
					Priority:             200,
					Direction:            "Inbound",
				},
			},
			{
				Name: "deny-ssh",
				Properties: SecurityRuleProperties{
					Protocol:             "Tcp",
					SourceAddressPrefix:  "*",
					DestinationPortRange: "22",
					Access:               "Deny",
					Priority:             100,
					Direction:            "Inbound",
				},
			},
		}},
	})
	nic := NetworkInterface{
		ID: "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic",
		Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{{
			Properties: NetworkInterfaceIPConfigurationProperties{Subnet: &SubResource{ID: subnetID}},
		}}},
	}

	stages, err := azureIngressPacketStages(nic)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 {
		t.Fatalf("NIC with one subnet NSG compiled %d stages, want 1", len(stages))
	}
	rules := stages[0]
	if len(rules) != 4 {
		t.Fatalf("compiled rules = %d, want 4: %+v", len(rules), rules)
	}
	if rules[0].Action != "drop" || rules[0].FromPort != 22 {
		t.Fatalf("first rule = %+v, want priority deny tcp/22", rules[0])
	}
	if rules[1].Action != "accept" || rules[1].FromPort != 80 || rules[1].SourceCIDR != "0.0.0.0/0" {
		t.Fatalf("second rule = %+v, want Internet allow tcp/80", rules[1])
	}
	if rules[2].Action != "accept" || rules[2].SourceCIDR != "10.0.0.0/16" {
		t.Fatalf("third rule = %+v, want default VNet allow", rules[2])
	}
	if rules[3].Action != "accept" || rules[3].SourceCIDR != "168.63.129.16/32" {
		t.Fatalf("fourth rule = %+v, want default AzureLoadBalancer allow", rules[3])
	}
}

func TestAzureLoadBalancerDataPlaneProxiesBackendPoolNIC(t *testing.T) {
	srv, err := sim.NewServer(sim.Config{Provider: "azure", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	// Restored rather than cleared, for the reason given above: a nil store
	// left behind panics whichever test runs next against it.
	priorPublicIPs, priorLBs, priorNICs := azurePublicIPs, azureLBs, azureNICs
	azurePublicIPs = sim.MakeStore[PublicIPAddress](nil, "test_public_ips")
	azureLBs = sim.MakeStore[LoadBalancer](nil, "test_lbs")
	azureNICs = sim.MakeStore[NetworkInterface](nil, "test_nics")
	defer func() {
		azurePublicIPs, azureLBs, azureNICs = priorPublicIPs, priorLBs, priorNICs
	}()
	registerAzureLoadBalancerDataPlane(srv)
	azureLoadBalancerHealth.Reset()
	t.Cleanup(azureLoadBalancerHealth.Reset)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/callback":
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "signed-in"})
			http.Redirect(w, r, "/home", http.StatusFound)
		case "/socket":
			if !lbplane.IsUpgradeRequest(r) {
				http.Error(w, "the upgrade headers did not reach the backend", http.StatusBadRequest)
				return
			}
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			defer conn.Close()
			_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n")
			_ = buf.Flush()
			line, err := buf.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = conn.Write([]byte("echo " + line))
		default:
			_, _ = w.Write([]byte("azure-lb-target host=" + r.Host))
		}
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

	lbID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/lb"
	pipID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/publicIPAddresses/pip"
	frontendID := lbID + "/frontendIPConfigurations/frontend"
	poolID := lbID + "/backendAddressPools/backend"
	probeID := lbID + "/probes/probe"
	ruleID := lbID + "/loadBalancingRules/http"
	azurePublicIPs.Put(pipID, PublicIPAddress{
		ID:         pipID,
		Properties: PublicIPAddressProperties{PublicIPAddress: "20.1.2.3"},
	})
	azureLBs.Put(lbID, LoadBalancer{
		ID: lbID,
		Properties: LoadBalancerProperties{
			FrontendIPConfigurations: []LoadBalancerChild{{
				ID:         frontendID,
				Name:       "frontend",
				Properties: map[string]any{"publicIPAddress": map[string]any{"id": pipID}},
			}},
			BackendAddressPools: []LoadBalancerChild{{ID: poolID, Name: "backend", Properties: map[string]any{}}},
			Probes: []LoadBalancerChild{{
				ID:   probeID,
				Name: "probe",
				Properties: map[string]any{
					"protocol": "Tcp",
					"port":     float64(port),
				},
			}},
			LoadBalancingRules: []LoadBalancerChild{{
				ID:   ruleID,
				Name: "http",
				Properties: map[string]any{
					"frontendPort":            float64(80),
					"backendPort":             float64(port),
					"frontendIPConfiguration": map[string]any{"id": frontendID},
					"backendAddressPool":      map[string]any{"id": poolID},
					"probe":                   map[string]any{"id": probeID},
				},
			}},
		},
	})
	azureNICs.Put("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/networkInterfaces/nic", NetworkInterface{
		Properties: NetworkInterfaceProperties{IPConfigurations: []NetworkInterfaceIPConfiguration{{
			Properties: NetworkInterfaceIPConfigurationProperties{
				PrivateIPAddress: host,
				LoadBalancerBackendAddressPools: []LoadBalancerChild{{
					ID: poolID,
				}},
			},
		}}},
	})

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "http://simulator"+path, nil)
		req.Host = "20.1.2.3"
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)
		return rr
	}
	// No probe has run yet, so no backend is in rotation.
	if rr := get("/work"); rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status before the first probe = %d, want 503", rr.Code)
	}
	azureSweepLoadBalancerProbes(context.Background(), time.Now())

	rr := get("/work")
	if rr.Code != http.StatusOK {
		t.Fatalf("data-plane status = %d, body = %q", rr.Code, rr.Body.String())
	}
	// A layer-4 load balancer hands the backend the client's own Host header.
	if strings.TrimSpace(rr.Body.String()) != "azure-lb-target host=20.1.2.3" {
		t.Fatalf("data-plane body = %q", rr.Body.String())
	}

	// A backend's redirect reaches the client with its cookie, unfollowed.
	rr = get("/callback")
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != "/home" ||
		!strings.Contains(rr.Header().Get("Set-Cookie"), "session=signed-in") {
		t.Fatalf("redirect relayed as %d %v", rr.Code, rr.Header())
	}

	// A WebSocket upgrade is tunnelled rather than answered and dropped.
	front := httptest.NewServer(srv)
	defer front.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(front.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := io.WriteString(conn, "GET /socket HTTP/1.1\r\nHost: 20.1.2.3\r\nConnection: Upgrade\r\nUpgrade: websocket\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade answered %d", resp.StatusCode)
	}
	if _, err := io.WriteString(conn, "hello\n"); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	reply, err := reader.ReadString('\n')
	if err != nil || reply != "echo hello\n" {
		t.Fatalf("tunnel replied %q, %v", reply, err)
	}
}

// An Http probe succeeds on 200 alone and does not follow a redirect, so a
// backend whose probe path redirects stays out of rotation.
func TestAzureLoadBalancerHTTPProbeMarksARedirectDown(t *testing.T) {
	priorLBs := azureLBs
	azureLBs = sim.MakeStore[LoadBalancer](nil, "test_lbs_probe")
	azureLoadBalancerHealth.Reset()
	t.Cleanup(func() { azureLBs = priorLBs; azureLoadBalancerHealth.Reset() })

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
	lbID := "/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/loadBalancers/probe-lb"
	poolID := lbID + "/backendAddressPools/backend"
	probeID := lbID + "/probes/http"
	lb := LoadBalancer{ID: lbID, Properties: LoadBalancerProperties{
		BackendAddressPools: []LoadBalancerChild{{ID: poolID, Name: "backend", Properties: map[string]any{
			"loadBalancerBackendAddresses": []any{map[string]any{"properties": map[string]any{"ipAddress": host}}},
		}}},
		Probes: []LoadBalancerChild{{ID: probeID, Name: "http", Properties: map[string]any{
			"protocol": "Http", "port": float64(port), "requestPath": "/health",
		}}},
		LoadBalancingRules: []LoadBalancerChild{{ID: lbID + "/loadBalancingRules/web", Name: "web", Properties: map[string]any{
			"frontendPort": float64(80), "backendPort": float64(port),
			"backendAddressPool": map[string]any{"id": poolID},
			"probe":              map[string]any{"id": probeID},
		}}},
	}}
	azureLBs.Put(lbID, lb)

	azureSweepLoadBalancerProbes(context.Background(), time.Now())
	if _, ok := azureHealthyLoadBalancerTarget(lb, lb.Properties.LoadBalancingRules[0]); ok {
		t.Fatal("a backend whose Http probe answered 302 is in rotation")
	}
	health, _ := azureLoadBalancerHealth.Health(azureLoadBalancerHealthKey(lb, lb.Properties.Probes[0],
		azureLBTarget{Address: net.JoinHostPort(host, portText), Port: port}))
	if health.LastStatus != http.StatusFound {
		t.Fatalf("probe read %d, want the 302 itself rather than the page it redirects to", health.LastStatus)
	}
}
