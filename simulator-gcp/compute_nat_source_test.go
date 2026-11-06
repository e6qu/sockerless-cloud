package main

import (
	"net/netip"
	"testing"
)

// A call that reaches the simulator from a Cloud NAT's sidecar is the NAT's
// translated traffic: its audit record names the NAT's external address.
func TestAuditCallerOfACallThroughCloudNATIsTheNATAddress(t *testing.T) {
	const subnet = "projects/p/regions/r/subnetworks/nat-source-test"
	gcpNATEgressMu.Lock()
	gcpNATEgresses[subnet] = &gcpNATEgress{address: netip.MustParseAddr("10.86.0.254"), natIP: "34.1.2.3"}
	gcpNATEgressMu.Unlock()
	t.Cleanup(func() {
		gcpNATEgressMu.Lock()
		delete(gcpNATEgresses, subnet)
		gcpNATEgressMu.Unlock()
	})
	if got := auditCallerFromCredential("", "10.86.0.254:40000", "").ip; got != "34.1.2.3" {
		t.Fatalf("callerIp through the NAT = %q, want the NAT's address", got)
	}
	if got := auditCallerFromCredential("", "10.86.0.7:40000", "").ip; got != "10.86.0.7" {
		t.Fatalf("callerIp of a direct call = %q, want the caller's own address", got)
	}
}
