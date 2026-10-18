package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
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
	pool, err := ipam.NewIPAMWithReserved("10.128.0.0/24", net.ParseIP("10.128.0.1"), gcpSubnetReservation)
	if err != nil {
		t.Fatal(err)
	}
	if first, err := pool.Reserve("vm", nil); err != nil || first.String() != "10.128.0.2" {
		t.Fatalf("first lease = %s, %v; want 10.128.0.2", first, err)
	}
	if _, err := pool.Reserve("vm", net.ParseIP("10.128.0.254")); err == nil {
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

func useLedgerStores(t *testing.T) {
	t.Helper()
	gcpSubnetworks = sim.MakeStore[ComputeSubnetwork](nil, "test_subnetworks")
	gcpInstances = sim.MakeStore[ComputeInstance](nil, "test_instances")
	t.Cleanup(func() {
		gcpSubnetworks = nil
		gcpInstances = nil
		gcpSubnetIPAMs.Lock()
		gcpSubnetIPAMs.pools = map[string]*ipam.IPAM{}
		gcpSubnetIPAMs.Unlock()
	})
}

func ledgerInstance(name, subnet, address string) ComputeInstance {
	return ComputeInstance{
		Name:     name,
		SelfLink: "projects/p/zones/us-central1-a/instances/" + name,
		NetworkInterfaces: []ComputeNetworkInterface{{
			Name: "nic0", Network: "projects/p/global/networks/n", Subnetwork: subnet, NetworkIP: address,
		}},
	}
}

// The stored instances are the ledger: an address a stored interface holds is
// leased before the first new lease, a requested address another interface
// holds is refused the way Compute Engine words it, the reserved addresses are
// never handed out, and a released address is leased again.
func TestGCPInstanceAddressLeasesComeFromTheSubnetworkLedger(t *testing.T) {
	useLedgerStores(t)
	subnet := "projects/p/regions/us-central1/subnetworks/s"
	gcpSubnetworks.Put(subnet, ComputeSubnetwork{SelfLink: subnet, Network: "projects/p/global/networks/n", IpCidrRange: "10.9.0.0/24"})
	held := ledgerInstance("held", subnet, "10.9.0.2")
	gcpInstances.Put(held.SelfLink, held)

	next := ledgerInstance("next", subnet, "")
	if err := gcpLeaseInstanceAddress(&next, 0); err != nil {
		t.Fatal(err)
	}
	if got := next.NetworkInterfaces[0].NetworkIP; got != "10.9.0.3" {
		t.Fatalf("first new lease = %s, want 10.9.0.3 after the stored 10.9.0.2", got)
	}

	dup := ledgerInstance("dup", subnet, "10.9.0.2")
	err := gcpLeaseInstanceAddress(&dup, 0)
	var refusal computeFieldRefusal
	if !errors.As(err, &refusal) || refusal.reason != "invalid" ||
		refusal.message != "Invalid value for field 'resource.networkInterfaces[0].networkIP': '10.9.0.2'. IP address '10.9.0.2' is already in use." {
		t.Fatalf("leasing a held address = %v, want Compute Engine's in-use refusal", err)
	}
	for _, reserved := range []string{"10.9.0.0", "10.9.0.1", "10.9.0.254", "10.9.0.255"} {
		inst := ledgerInstance("reserved", subnet, reserved)
		if err := gcpLeaseInstanceAddress(&inst, 0); !errors.As(err, &refusal) || refusal.reason != "invalid" {
			t.Fatalf("leasing reserved %s = %v, want an invalid-field refusal", reserved, err)
		}
	}

	gcpInstances.Delete(held.SelfLink)
	gcpReleaseInstanceAddresses(held)
	again := ledgerInstance("again", subnet, "10.9.0.2")
	if err := gcpLeaseInstanceAddress(&again, 0); err != nil {
		t.Fatalf("leasing a released address: %v", err)
	}

	// A forgotten ledger rebuilds from the store, which no longer holds the
	// instances that never reached it.
	gcpForgetSubnetworkIPAM(subnet)
	rebuilt := ledgerInstance("rebuilt", subnet, "10.9.0.3")
	if err := gcpLeaseInstanceAddress(&rebuilt, 0); err != nil {
		t.Fatalf("leasing after the rebuild: %v", err)
	}
}

// A subnetwork whose tenant addresses are all leased refuses the next lease
// with Compute Engine's IP_SPACE_EXHAUSTED reason.
func TestGCPInstanceAddressLeaseReportsAnExhaustedSubnetwork(t *testing.T) {
	useLedgerStores(t)
	subnet := "projects/p/regions/us-central1/subnetworks/tiny"
	gcpSubnetworks.Put(subnet, ComputeSubnetwork{SelfLink: subnet, Network: "projects/p/global/networks/n", IpCidrRange: "10.9.1.0/29"})
	for i, want := range []string{"10.9.1.2", "10.9.1.3", "10.9.1.4", "10.9.1.5"} {
		inst := ledgerInstance("vm", subnet, "")
		inst.SelfLink += string(rune('a' + i))
		if err := gcpLeaseInstanceAddress(&inst, 0); err != nil || inst.NetworkInterfaces[0].NetworkIP != want {
			t.Fatalf("lease %d = %s, %v; want %s", i, inst.NetworkInterfaces[0].NetworkIP, err, want)
		}
	}
	inst := ledgerInstance("one-too-many", subnet, "")
	err := gcpLeaseInstanceAddress(&inst, 0)
	var refusal computeFieldRefusal
	if !errors.As(err, &refusal) || refusal.reason != "IP_SPACE_EXHAUSTED" || refusal.message != "IP space of '"+subnet+"' is exhausted." {
		t.Fatalf("fifth lease of a /29 = %v, want IP_SPACE_EXHAUSTED", err)
	}
}

// instances.insert leases the interface its networkIP from the subnetwork,
// refuses an address another instance holds with a 400 the client branches
// on, and returns the address when the instance is deleted.
func TestComputeInstanceInsertLeasesAndReleasesItsAddress(t *testing.T) {
	srv := powerSimulator(t)
	installNoMachine(t)
	const project, region = powerProject, "us-central1"
	post := func(path, body string) *httptest.ResponseRecorder {
		return powerRequest(t, srv, http.MethodPost, path, body)
	}
	if rec := post("/compute/v1/projects/"+project+"/global/networks", `{"name":"lease-net"}`); rec.Code != http.StatusOK {
		t.Fatalf("networks.insert: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/compute/v1/projects/"+project+"/regions/"+region+"/subnetworks",
		`{"name":"lease-subnet","network":"projects/`+project+`/global/networks/lease-net","ipCidrRange":"10.77.0.0/24"}`); rec.Code != http.StatusOK {
		t.Fatalf("subnetworks.insert: %d %s", rec.Code, rec.Body.String())
	}
	t.Cleanup(func() {
		powerRequest(t, srv, http.MethodDelete, "/compute/v1/projects/"+project+"/regions/"+region+"/subnetworks/lease-subnet", "")
		powerRequest(t, srv, http.MethodDelete, "/compute/v1/projects/"+project+"/global/networks/lease-net", "")
	})
	instanceBody := func(name, address string) string {
		nic := `{"subnetwork":"regions/` + region + `/subnetworks/lease-subnet"`
		if address != "" {
			nic += `,"networkIP":"` + address + `"`
		}
		return `{"name":"` + name + `","networkInterfaces":[` + nic + `}]}`
	}
	insert := func(name, address string) *httptest.ResponseRecorder {
		return post("/compute/v1/projects/"+project+"/zones/"+powerZone+"/instances", instanceBody(name, address))
	}
	settle := func(rec *httptest.ResponseRecorder) {
		t.Helper()
		op := decodePowerOperation(t, rec)
		bg.Await()
		requirePowerOperationDone(t, srv, op.Name)
	}
	settle(insert("fixed", "10.77.0.5"))
	t.Cleanup(func() { gcpInstances.Delete(computeInstanceSelfLink(project, powerZone, "fixed")) })

	rec := insert("twin", "10.77.0.5")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("inserting a held address: %d %s, want 400", rec.Code, rec.Body.String())
	}
	var refused struct {
		Error struct {
			Errors []struct{ Reason, Message string } `json:"errors"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &refused); err != nil {
		t.Fatal(err)
	}
	if len(refused.Error.Errors) != 1 || refused.Error.Errors[0].Reason != "invalid" ||
		!strings.Contains(refused.Error.Errors[0].Message, "IP address '10.77.0.5' is already in use") {
		t.Fatalf("refusal = %s, want errors[0] invalid naming the address in use", rec.Body.String())
	}

	settle(insert("auto", ""))
	t.Cleanup(func() { gcpInstances.Delete(computeInstanceSelfLink(project, powerZone, "auto")) })
	auto, _ := gcpInstances.Get(computeInstanceSelfLink(project, powerZone, "auto"))
	if got := auto.NetworkInterfaces[0].NetworkIP; got != "10.77.0.2" {
		t.Fatalf("automatic lease = %s, want 10.77.0.2, the first tenant address", got)
	}

	settle(powerRequest(t, srv, http.MethodDelete, powerInstancePath("fixed", ""), ""))
	settle(insert("fixed-again", "10.77.0.5"))
	t.Cleanup(func() { gcpInstances.Delete(computeInstanceSelfLink(project, powerZone, "fixed-again")) })
}

// A subnetwork is one engine network over its own range, with its gateway
// where Compute Engine puts it, named from the subnetwork's self link within
// the engine's bounds and carrying the self link on its label.
func TestGCPSubnetworkNetworkSpansItsRange(t *testing.T) {
	subnet := ComputeSubnetwork{
		SelfLink:       "projects/unit-test/regions/us-central1/subnetworks/engine",
		Network:        "projects/unit-test/global/networks/n",
		IpCidrRange:    "10.213.7.0/24",
		GatewayAddress: gcpSubnetGateway("10.213.7.0/24"),
	}
	spec, err := gcpSubnetworkNetwork(subnet)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Name != "gcp-subnetwork-"+hex.EncodeToString(sha256sum(subnet.SelfLink)[:8]) || len(spec.Name) > 63 {
		t.Fatalf("engine network name = %q", spec.Name)
	}
	if spec.CIDR.String() != "10.213.7.0/24" || spec.Gateway.String() != "10.213.7.1" {
		t.Fatalf("engine network = %+v, want 10.213.7.0/24 with gateway 10.213.7.1", spec)
	}
	if got := spec.Labels[gcpSubnetworkNetworkLabel]; got != subnet.SelfLink {
		t.Fatalf("label %s = %q, want the subnetwork's self link", gcpSubnetworkNetworkLabel, got)
	}
	other := subnet
	other.SelfLink = "projects/unit-test/regions/us-central1/subnetworks/other"
	if same, _ := gcpSubnetworkNetwork(other); same.Name == spec.Name {
		t.Fatalf("two subnetworks share the engine network %q", spec.Name)
	}
	subnet.IpCidrRange = "not-a-range"
	if _, err := gcpSubnetworkNetwork(subnet); err == nil {
		t.Fatal("a subnetwork without an IPv4 range got an engine network")
	}
}

func sha256sum(s string) []byte {
	sum := sha256.Sum256([]byte(s))
	return sum[:]
}
