package main

import (
	"net/netip"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
)

// What a machine sends through a NAT gateway reaches the simulator from the
// gateway's sidecar; the Activity Log names the gateway's public IP as the
// client address.
func TestNATGatewayPublicIPIsTheClientOfItsTraffic(t *testing.T) {
	priorGateways, priorIPs := azureNatGateways, azurePublicIPs
	azureNatGateways = sim.MakeStore[NatGateway](nil, "test_nat_gateways")
	azurePublicIPs = sim.MakeStore[PublicIPAddress](nil, "test_public_ips")
	t.Cleanup(func() { azureNatGateways, azurePublicIPs = priorGateways, priorIPs })

	const gatewayID = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/natGateways/nat-source-test"
	const pipID = "/subscriptions/s/resourceGroups/rg/providers/Microsoft.Network/publicIPAddresses/nat-source-pip"
	var pip PublicIPAddress
	pip.Properties.PublicIPAddress = "20.1.2.3"
	azurePublicIPs.Put(pipID, pip)
	var gw NatGateway
	gw.ID = gatewayID
	gw.Properties.PublicIPAddresses = []SubResource{{ID: pipID}}
	azureNatGateways.Put(gatewayID, gw)
	azureNATEgressMu.Lock()
	azureNATEgresses["nat-source-test"] = &azureNATEgress{gatewayID: gatewayID, address: netip.MustParseAddr("10.87.0.254")}
	azureNATEgressMu.Unlock()
	t.Cleanup(func() {
		azureNATEgressMu.Lock()
		delete(azureNATEgresses, "nat-source-test")
		azureNATEgressMu.Unlock()
	})

	if got, ok := azureNATPublicAddress("10.87.0.254"); !ok || got != "20.1.2.3" {
		t.Fatalf("the gateway's sidecar maps to %q (%v), want its public IP", got, ok)
	}
	if got, ok := azureNATPublicAddress("10.87.0.7"); ok {
		t.Fatalf("a machine's own address maps to %q", got)
	}
}
