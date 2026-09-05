package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

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

	rules, filtered := azureIngressPacketRules(nic)
	if !filtered {
		t.Fatalf("NIC with subnet NSG was not filtered")
	}
	if len(rules) != 3 {
		t.Fatalf("compiled rules = %d, want 3: %+v", len(rules), rules)
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

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("azure-lb-target"))
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

	req := httptest.NewRequest(http.MethodGet, "http://simulator/work", nil)
	req.Host = "20.1.2.3"
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("data-plane status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "azure-lb-target" {
		t.Fatalf("data-plane body = %q", rr.Body.String())
	}
}
