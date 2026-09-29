//go:build realexec_host && linux

package realexec

import (
	"context"
	"net"
	"testing"
	"time"
)

// ingressPair builds a network holding two namespace NICs, the second of which
// probes the first.
func ingressPair(ctx context.Context, t *testing.T, cidr string) (target, probe *NamespaceNIC) {
	t.Helper()
	prefix := shortPrefix()
	network, err := NewHost().CreateNetwork(ctx, NetworkSpec{NamespaceName: prefix + "nw", BridgeName: prefix + "br", CIDR: cidr})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = network.Close(context.Background()) })
	target, err = network.AttachNamespaceNIC(ctx, NamespaceNICSpec{NamespaceName: prefix + "n1", HostVethName: prefix + "h1", GuestVethName: prefix + "g1", MAC: "02:00:5e:10:01:01"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = target.Close(context.Background()) })
	probe, err = network.AttachNamespaceNIC(ctx, NamespaceNICSpec{NamespaceName: prefix + "n2", HostVethName: prefix + "h2", GuestVethName: prefix + "g2", MAC: "02:00:5e:10:01:02"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = probe.Close(context.Background()) })
	return target, probe
}

func pingReaches(ctx context.Context, from *NamespaceNIC, to net.IP) bool {
	runner := Runner{}
	_ = runner.Run(ctx, "ip", "netns", "exec", from.NamespaceName, "ip", "neigh", "flush", to.String())
	return runner.Run(ctx, "ip", "netns", "exec", from.NamespaceName, "ping", "-c", "1", "-W", "1", to.String()) == nil
}

// A filter cleared and then applied again with the same rules is installed
// again: the memo of the committed program must not outlive the table.
func TestIngressFilterReappliesAfterClear(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	target, probe := ingressPair(ctx, t, "10.204.0.0/29")
	onlyTCP1 := []PacketRule{{Protocol: "tcp", SourceCIDR: "0.0.0.0/0", FromPort: 1, ToPort: 1}}
	if err := target.ConfigureIngressFilter(ctx, onlyTCP1); err != nil {
		t.Fatal(err)
	}
	if pingReaches(ctx, probe, target.PrivateIP) {
		t.Fatal("ping crossed a filter that allows only tcp/1")
	}
	if err := target.ClearIngressFilter(ctx); err != nil {
		t.Fatal(err)
	}
	if err := target.ClearIngressFilter(ctx); err != nil {
		t.Fatalf("clearing an unfiltered interface: %v", err)
	}
	if !pingReaches(ctx, probe, target.PrivateIP) {
		t.Fatal("ping blocked after the filter was cleared")
	}
	if err := target.ConfigureIngressFilter(ctx, onlyTCP1); err != nil {
		t.Fatal(err)
	}
	if pingReaches(ctx, probe, target.PrivateIP) {
		t.Fatal("the same filter applied after a clear was not installed again")
	}
}

// A staged filter delivers a packet only when every stage accepts it.
func TestStagedIngressFilterNeedsEveryStageToAllow(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	target, probe := ingressPair(ctx, t, "10.204.1.0/29")
	allowICMP := []PacketRule{{Protocol: "icmp", SourceCIDR: "10.204.1.0/29"}}
	denyICMP := []PacketRule{{Protocol: "icmp", SourceCIDR: "10.204.1.0/29", Action: "drop"}}

	if err := target.ConfigureStagedIngressFilter(ctx, [][]PacketRule{allowICMP, allowICMP}); err != nil {
		t.Fatal(err)
	}
	if !pingReaches(ctx, probe, target.PrivateIP) {
		t.Fatal("ping blocked although both stages allow it")
	}
	if err := target.ConfigureStagedIngressFilter(ctx, [][]PacketRule{allowICMP, denyICMP}); err != nil {
		t.Fatal(err)
	}
	if pingReaches(ctx, probe, target.PrivateIP) {
		t.Fatal("ping delivered although the second stage denies it")
	}
	if err := target.ConfigureStagedIngressFilter(ctx, [][]PacketRule{allowICMP, nil}); err != nil {
		t.Fatal(err)
	}
	if pingReaches(ctx, probe, target.PrivateIP) {
		t.Fatal("ping delivered although the second stage allows nothing")
	}
}
