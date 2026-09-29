package main

import (
	"net"
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	"github.com/e6qu/sockerless-cloud/sim"
)

// Compute Engine resolves a deny and an allow firewall rule of the same
// priority in favour of the deny, whatever the rules are named.
func TestGCPFirewallDenyWinsAtEqualPriority(t *testing.T) {
	gcpFirewalls = sim.MakeStore[ComputeFirewall](nil, "test_firewalls")
	gcpInstances = sim.MakeStore[ComputeInstance](nil, "test_instances")
	defer func() {
		gcpFirewalls = nil
		gcpInstances = nil
	}()
	network := "projects/test-project/global/networks/test-net"
	target := ComputeInstance{Name: "target", SelfLink: "projects/test-project/zones/us-central1-a/instances/target"}
	gcpFirewalls.Put("a-allow", ComputeFirewall{
		Name: "a-allow", SelfLink: "projects/test-project/global/firewalls/a-allow", Network: network,
		Direction: "INGRESS", Priority: 1000, SourceRanges: []string{"0.0.0.0/0"},
		Allowed: []ComputeFirewallAction{{IPProtocol: "tcp", Ports: []string{"22"}}},
	})
	gcpFirewalls.Put("b-deny", ComputeFirewall{
		Name: "b-deny", SelfLink: "projects/test-project/global/firewalls/b-deny", Network: network,
		Direction: "INGRESS", Priority: 1000, SourceRanges: []string{"0.0.0.0/0"},
		Denied: []ComputeFirewallAction{{IPProtocol: "tcp", Ports: []string{"22"}}},
	})
	rules, err := gcpIngressPacketRules(target, ComputeNetworkInterface{Name: "nic0", Network: network})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || rules[0].Action != "drop" || rules[1].Action != "accept" {
		t.Fatalf("rules = %+v, want the deny first", rules)
	}
}

func TestGCPFirewallRejectsAnUnparseablePort(t *testing.T) {
	msg := gcpInvalidFirewallPort(ComputeFirewall{Allowed: []ComputeFirewallAction{{IPProtocol: "tcp", Ports: []string{"22", "ssh"}}}})
	if !strings.Contains(msg, "resource.allowed[0].ports[1]") || !strings.Contains(msg, "'ssh'") {
		t.Fatalf("message = %q, want it to name resource.allowed[0].ports[1] and 'ssh'", msg)
	}
	if msg := gcpInvalidFirewallPort(ComputeFirewall{Denied: []ComputeFirewallAction{{IPProtocol: "tcp", Ports: []string{"80-81", "443"}}}}); msg != "" {
		t.Fatalf("valid ports rejected: %s", msg)
	}
}

// The gateway of a subnetwork is the address after its network address, and
// the first address an instance leases is the one after that.
func TestGCPSubnetworkAddressing(t *testing.T) {
	if gw := gcpSubnetGateway("10.128.0.0/20"); gw != "10.128.0.1" {
		t.Fatalf("gateway = %s, want 10.128.0.1", gw)
	}
	if gw := gcpSubnetGateway("not-a-cidr"); gw != "" {
		t.Fatalf("gateway of an invalid range = %q, want empty", gw)
	}
	ipam, err := realexec.NewIPAMWithReserved("10.128.0.0/24", net.ParseIP("10.128.0.1"), gcpSubnetReservation)
	if err != nil {
		t.Fatal(err)
	}
	if first, err := ipam.Reserve("vm", nil); err != nil || first.String() != "10.128.0.2" {
		t.Fatalf("first lease = %s, %v; want 10.128.0.2", first, err)
	}
	if _, err := ipam.Reserve("vm", net.ParseIP("10.128.0.254")); err == nil {
		t.Fatal("the second-to-last address, which Google Cloud reserves, was leased")
	}
}

// A subnetwork naming its network by full URL joins the namespace of the
// network named by relative path.
func TestGCPNetworkKeyIgnoresReferenceSpelling(t *testing.T) {
	self := "projects/p/regions/us-central1/subnetworks/s"
	want := "projects/p/global/networks/n"
	for _, ref := range []string{
		"https://www.googleapis.com/compute/v1/projects/p/global/networks/n",
		"projects/p/global/networks/n",
		"global/networks/n",
	} {
		if got := gcpNetworkKey(self, ref); got != want {
			t.Errorf("gcpNetworkKey(%q) = %q, want %q", ref, got, want)
		}
	}
}
