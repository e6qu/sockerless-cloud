//go:build realexec_host && linux

package realexec

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func requireNetworkHost(t *testing.T) {
	t.Helper()
	if err := DetectNetworkCapabilities().Require(); err != nil {
		t.Fatalf("this test needs a real network host: %v", err)
	}
}

func createTestNetwork(ctx context.Context, t *testing.T, prefix, cidr string) *Network {
	t.Helper()
	network, err := NewHost().CreateNetwork(ctx, NetworkSpec{
		NamespaceName: prefix + "nw",
		BridgeName:    prefix + "br",
		CIDR:          cidr,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := network.Close(context.Background()); err != nil {
			t.Errorf("network cleanup: %v", err)
		}
	})
	return network
}

func namespaceListed(ctx context.Context, t *testing.T, name string) bool {
	t.Helper()
	out, err := Runner{}.Output(ctx, "ip", "netns", "list")
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	for _, line := range strings.Split(out, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

// A process killed before its cleanup ran leaves an interface's namespace,
// with the guest end of its veth inside, under the name a restarted process
// derives again. Attaching reclaims the namespace instead of failing with
// "File exists", and the attached interface works.
func TestAttachNamespaceNICReclaimsOrphanedNamespace(t *testing.T) {
	requireNetworkHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	prefix := shortPrefix()
	network := createTestNetwork(ctx, t, prefix, "10.206.0.0/29")
	runner := Runner{}

	orphan, host, guest := prefix+"ns", prefix+"hv", prefix+"gv"
	if err := runner.Run(ctx, "ip", "netns", "add", orphan); err != nil {
		t.Fatalf("create orphaned namespace: %v", err)
	}
	t.Cleanup(func() { _ = runner.Run(context.Background(), "ip", "netns", "del", orphan) })
	if err := runner.Run(ctx, "ip", "link", "add", host, "type", "veth", "peer", "name", guest); err != nil {
		t.Fatalf("create orphaned veth: %v", err)
	}
	if err := runner.Run(ctx, "ip", "link", "set", guest, "netns", orphan); err != nil {
		t.Fatalf("move orphaned veth into its namespace: %v", err)
	}

	nic, err := network.AttachNamespaceNIC(ctx, NamespaceNICSpec{
		NamespaceName: orphan,
		HostVethName:  host,
		GuestVethName: guest,
	})
	if err != nil {
		t.Fatalf("attach over an orphaned namespace: %v", err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", orphan, "ping", "-c", "1", "-W", "1", network.Gateway.String()); err != nil {
		t.Fatalf("reclaimed namespace cannot reach the subnet gateway %s: %v", network.Gateway, err)
	}
	if err := nic.Close(ctx); err != nil {
		t.Fatalf("close reclaimed interface: %v", err)
	}
	if namespaceListed(ctx, t, orphan) {
		t.Fatalf("namespace %s outlived its interface", orphan)
	}
}

// A secondary address comes from the subnet's lease table, answers on the
// interface, and goes back to the subnet when it is removed or the interface
// closes.
func TestNamespaceNICSecondaryAddress(t *testing.T) {
	requireNetworkHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	prefix := shortPrefix()
	network := createTestNetwork(ctx, t, prefix, "10.207.0.0/28")
	runner := Runner{}

	attach := func(n string) *NamespaceNIC {
		t.Helper()
		nic, err := network.AttachNamespaceNIC(ctx, NamespaceNICSpec{
			NamespaceName: prefix + "n" + n,
			HostVethName:  prefix + "h" + n,
			GuestVethName: prefix + "g" + n,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = nic.Close(context.Background()) })
		return nic
	}
	first, second := attach("1"), attach("2")

	dynamic, err := first.AddAddress(ctx, nil)
	if err != nil {
		t.Fatalf("add a dynamic secondary address: %v", err)
	}
	if dynamic.String() != "10.207.0.4" {
		t.Fatalf("dynamic secondary = %s, want 10.207.0.4 after the primaries .2 and .3", dynamic)
	}
	static, err := first.AddAddress(ctx, net.ParseIP("10.207.0.9"))
	if err != nil {
		t.Fatalf("add a static secondary address: %v", err)
	}
	if again, err := first.AddAddress(ctx, static); err != nil || !again.Equal(static) {
		t.Fatalf("re-adding %s = %s, %v; want the address it holds", static, again, err)
	}
	if _, err := second.AddAddress(ctx, static); err == nil {
		t.Fatalf("a second interface was granted %s, which the first holds", static)
	}
	if _, err := first.AddAddress(ctx, first.PrivateIP); err == nil {
		t.Fatal("the primary address was granted again as a secondary")
	}
	if got := first.Addresses(); len(got) != 2 || !got[0].Equal(dynamic) || !got[1].Equal(static) {
		t.Fatalf("Addresses() = %v, want [%s %s]", got, dynamic, static)
	}

	out, err := runner.Output(ctx, "ip", "netns", "exec", first.NamespaceName, "ip", "-4", "-o", "addr", "show", "dev", first.GuestVethName)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{first.PrivateIP.String() + "/28", dynamic.String() + "/28", static.String() + "/28"} {
		if !strings.Contains(out, want) {
			t.Fatalf("interface addresses %q lack %s", out, want)
		}
	}
	for _, ip := range []net.IP{dynamic, static} {
		if err := runner.Run(ctx, "ip", "netns", "exec", second.NamespaceName, "ping", "-c", "1", "-W", "1", ip.String()); err != nil {
			t.Fatalf("second interface cannot reach secondary address %s: %v", ip, err)
		}
	}

	if err := first.RemoveAddress(ctx, static); err != nil {
		t.Fatalf("remove secondary address: %v", err)
	}
	if err := first.RemoveAddress(ctx, static); err == nil {
		t.Fatal("removing an address the interface no longer holds succeeded")
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", second.NamespaceName, "ping", "-c", "1", "-W", "1", static.String()); err == nil {
		t.Fatalf("removed address %s still answers", static)
	}
	if moved, err := second.AddAddress(ctx, static); err != nil || !moved.Equal(static) {
		t.Fatalf("the released %s was not leased again: %s, %v", static, moved, err)
	}

	if err := first.Close(ctx); err != nil {
		t.Fatalf("close the interface holding a secondary address: %v", err)
	}
	if again, err := second.AddAddress(ctx, dynamic); err != nil || !again.Equal(dynamic) {
		t.Fatalf("closing the interface did not release %s: %s, %v", dynamic, again, err)
	}
}

// A tap holds the leases of the machine behind it, so no other interface on
// the subnet is granted them, and closing the tap returns them.
func TestTapNICSecondaryAddressLease(t *testing.T) {
	requireNetworkHost(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	prefix := shortPrefix()
	network := createTestNetwork(ctx, t, prefix, "10.208.0.0/28")

	tap, err := network.defaultSubnet.AttachTapNIC(ctx, TapNICSpec{TapName: prefix + "tp"})
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := tap.AddAddress(net.ParseIP("10.208.0.7"))
	if err != nil {
		t.Fatalf("lease a secondary address to the tap: %v", err)
	}
	if _, err := network.defaultSubnet.ReserveAddress("other", secondary); err == nil {
		t.Fatalf("the subnet leased %s, which the tap holds", secondary)
	}
	if err := tap.Close(ctx); err != nil {
		t.Fatal(err)
	}
	ip, err := network.defaultSubnet.ReserveAddress("other", secondary)
	if err != nil {
		t.Fatalf("closing the tap did not release %s: %v", secondary, err)
	}
	network.defaultSubnet.ReleaseAddress(ip)
}
