//go:build realexec_host && linux

package fabric

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net"
	"strings"
	"testing"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

func requireHost(t *testing.T) {
	t.Helper()
	if err := realexec.DetectNetworkCapabilities().Require(); err != nil {
		t.Fatalf("the fabric tests need a real network host: %v", err)
	}
}

func uniqueKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

func nsRuleset(ctx context.Context, t *testing.T, n *realexec.Network) string {
	t.Helper()
	out, err := realexec.Runner{}.Output(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "list", "ruleset")
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func namespaceExists(ctx context.Context, name string) bool {
	out, err := realexec.Runner{}.Output(ctx, "ip", "netns", "list")
	return err == nil && strings.Contains(out, name)
}

// A subnet leases only what its cloud leaves to tenants, and tearing its
// network down closes every interface on it and the namespace itself.
func TestFabricSubnetReservationAndTeardown(t *testing.T) {
	requireHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id := uniqueKey(t)
	var closedTaps []string
	f := New[string](Options{
		NetworkPrefix: "tfn",
		SubnetPrefix:  "tfs",
		Reserved:      realexec.HostReservation{First: 4, Last: 1},
		OnTapClosed:   func(tap *realexec.TapNIC) { closedTaps = append(closedTaps, tap.PrivateIP.String()) },
	})
	netKey, subKey := "vpc-"+id, "subnet-"+id
	t.Cleanup(func() { _ = f.TeardownNetwork(context.Background(), netKey, nil) })
	subnet, err := f.EnsureSubnet(ctx, netKey, subKey, "10.205.1.0/24", FirstHostGateway("10.205.1.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if again, err := f.EnsureSubnet(ctx, netKey, subKey, "10.205.1.0/24", nil); err != nil || again != subnet {
		t.Fatalf("second EnsureSubnet = %p, %v; want the realized subnet %p", again, err, subnet)
	}
	nic, err := f.AttachNamespaceNIC(ctx, subKey, "eni-"+id, realexec.NamespaceNICSpec{
		NamespaceName: LinuxName("ti", id),
		HostVethName:  LinuxName("th", id),
		GuestVethName: LinuxName("tg", id),
		MAC:           DeriveMAC("02:0a:ec", id),
	})
	if err != nil {
		t.Fatal(err)
	}
	if nic.PrivateIP.String() != "10.205.1.4" {
		t.Fatalf("first dynamic address = %s, want 10.205.1.4 past the reserved .0-.3", nic.PrivateIP)
	}
	if _, err := f.AttachNamespaceNIC(ctx, subKey, "eni-static-"+id, realexec.NamespaceNICSpec{
		NamespaceName: LinuxName("ti", "static"+id),
		HostVethName:  LinuxName("th", "static"+id),
		GuestVethName: LinuxName("tg", "static"+id),
		PrivateIP:     net.ParseIP("10.205.1.3"),
	}); err == nil {
		t.Fatal("a static lease of reserved 10.205.1.3 was granted")
	}
	tap, err := subnet.AttachTapNIC(ctx, realexec.TapNICSpec{TapName: LinuxName("tt", id)})
	if err != nil {
		t.Fatal(err)
	}
	_ = tap.Close(ctx)

	network := f.Network(netKey)
	if err := f.TeardownNetwork(ctx, netKey, nil); err != nil {
		t.Fatal(err)
	}
	if f.NIC("eni-"+id) != nil || f.Subnet(subKey) != nil || f.Network(netKey) != nil {
		t.Fatal("teardown left realized members registered")
	}
	if namespaceExists(ctx, network.NamespaceName) {
		t.Fatalf("namespace %s outlived its network's teardown", network.NamespaceName)
	}
	if namespaceExists(ctx, nic.NamespaceName) {
		t.Fatalf("interface namespace %s outlived its network's teardown", nic.NamespaceName)
	}
}

// A teardown waits for an attach that holds the network.
func TestFabricTeardownWaitsForHolders(t *testing.T) {
	requireHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id := uniqueKey(t)
	f := New[string](Options{NetworkPrefix: "tfn", SubnetPrefix: "tfs"})
	netKey := "net-" + id
	if _, err := f.EnsureNetwork(ctx, netKey); err != nil {
		t.Fatal(err)
	}
	release := f.HoldNetwork(netKey)
	done := make(chan error, 1)
	go func() { done <- f.TeardownNetwork(ctx, netKey, nil) }()
	select {
	case <-done:
		release()
		t.Fatal("teardown ran while an attach held the network")
	case <-time.After(300 * time.Millisecond):
	}
	if f.Network(netKey) == nil {
		t.Fatal("the network vanished under its holder")
	}
	release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Source NAT follows its owner: reconfiguring replaces the owner's tables,
// and releasing the owner removes them and returns its public address.
func TestFabricSNATOwnership(t *testing.T) {
	requireHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	id := uniqueKey(t)
	f := New[string](Options{NetworkPrefix: "tfn", SubnetPrefix: "tfs"})
	netKey := "net-" + id
	t.Cleanup(func() { _ = f.TeardownNetwork(context.Background(), netKey, nil) })
	network, err := f.EnsureNetwork(ctx, netKey)
	if err != nil {
		t.Fatal(err)
	}
	owner := "router-" + id + "/nat"
	ip, err := f.ReservePublicIP(owner, realexec.ReserveGCPPublicIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.ConfigureSNAT(ctx, netKey, owner, []string{"10.206.0.0/24", "10.206.1.0/24"}, ip); err != nil {
		t.Fatal(err)
	}
	ruleset := nsRuleset(ctx, t, network)
	if strings.Count(ruleset, " to "+ip.String()) != 2 {
		t.Fatalf("want two SNAT rules to %s:\n%s", ip, ruleset)
	}
	if err := f.ConfigureSNAT(ctx, netKey, owner, []string{"10.206.1.0/24"}, ip); err != nil {
		t.Fatal(err)
	}
	ruleset = nsRuleset(ctx, t, network)
	if strings.Contains(ruleset, "10.206.0.0/24") || !strings.Contains(ruleset, "10.206.1.0/24") {
		t.Fatalf("reconfiguring did not replace the owner's translation:\n%s", ruleset)
	}
	if err := f.ReleaseOwned(ctx, owner); err != nil {
		t.Fatal(err)
	}
	if ruleset = nsRuleset(ctx, t, network); strings.Contains(ruleset, "snat") {
		t.Fatalf("release left source NAT behind:\n%s", ruleset)
	}
	reused, err := realexec.ReserveGCPPublicIPv4("next-"+id, ip)
	if err != nil {
		t.Fatalf("the released address did not return to the pool: %v", err)
	}
	realexec.ReleasePublicIPv4(reused)
}
