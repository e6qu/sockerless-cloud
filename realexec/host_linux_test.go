//go:build realexec_host && linux

package realexec

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestExternalNamespaceNICRoundTrip plumbs a veth into an EXISTING netns (a
// stand-in for a Docker container, made with `unshare -n`), verifying the guest
// gets eth0 at the leased IP and can reach the subnet gateway — the ECS netns
// path.
func TestExternalNamespaceNICRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if _, err := exec.LookPath("nsenter"); err != nil {
		t.Fatalf("nsenter (util-linux) is required for the external-namespace NIC round-trip: %v", err)
	}

	prefix := shortPrefix()
	host := NewHost()
	network, err := host.CreateNetwork(ctx, NetworkSpec{
		NamespaceName: prefix + "nw",
		BridgeName:    prefix + "br",
		CIDR:          "10.204.0.0/29",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = network.Close(context.Background()) }()

	// A process in its own (existing) network namespace — like a container.
	holder := exec.Command("unshare", "-n", "sleep", "30")
	if err := holder.Start(); err != nil {
		t.Fatalf("unshare -n: %v", err)
	}
	defer func() { _ = holder.Process.Kill(); _ = holder.Wait() }()
	pid := holder.Process.Pid
	waitProcessNetnsChanged(t, pid)

	nic, err := network.AttachExternalNamespaceNIC(ctx, ExternalNamespaceNICSpec{
		PID:           pid,
		HostVethName:  prefix + "eh",
		GuestVethName: prefix + "eg",
		GuestIfName:   "eth0",
		MAC:           "02:00:5e:20:00:01",
		PrivateIP:     net.ParseIP("10.204.0.4"),
	})
	if err != nil {
		t.Fatalf("AttachExternalNamespaceNIC: %v", err)
	}
	defer func() { _ = nic.Close(context.Background()) }()

	pidStr := fmt.Sprintf("%d", pid)
	runner := Runner{}
	out, err := runner.Output(ctx, "nsenter", "-t", pidStr, "-n", "--", "ip", "-4", "-o", "addr", "show", "eth0")
	if err != nil {
		t.Fatalf("inspect guest eth0: %v", err)
	}
	if !strings.Contains(out, "10.204.0.4") {
		t.Fatalf("guest eth0 missing leased IP 10.204.0.4: %q", out)
	}
	if err := runner.Run(ctx, "nsenter", "-t", pidStr, "-n", "--", "ping", "-c", "1", "-W", "2", network.Gateway.String()); err != nil {
		t.Fatalf("guest cannot reach subnet gateway %s: %v", network.Gateway, err)
	}
}

func waitProcessNetnsChanged(t *testing.T, pid int) {
	t.Helper()
	self, err := os.Readlink("/proc/self/ns/net")
	if err != nil {
		t.Fatalf("read self netns: %v", err)
	}
	target := fmt.Sprintf("/proc/%d/ns/net", pid)
	deadline := time.Now().Add(5 * time.Second)
	for {
		current, err := os.Readlink(target)
		if err == nil && current != self {
			return
		}
		if time.Now().After(deadline) {
			if err != nil {
				t.Fatalf("wait for process netns %s: %v", target, err)
			}
			t.Fatalf("process %d did not enter a separate netns", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestHostCapabilitiesForRealExecution(t *testing.T) {
	report := DetectCapabilities("firecracker", "jailer", "ip", "nft")
	if err := report.Require(); err != nil {
		t.Fatal(err)
	}
}

func TestNetworkNamespaceNICRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	prefix := shortPrefix()
	host := NewHost()
	network, err := host.CreateNetwork(ctx, NetworkSpec{
		NamespaceName: prefix + "nw",
		BridgeName:    prefix + "br",
		CIDR:          "10.203.0.0/29",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := network.Close(context.Background()); err != nil {
			t.Fatalf("network cleanup: %v", err)
		}
	}()

	first, err := network.AttachNamespaceNIC(ctx, NamespaceNICSpec{
		NamespaceName: prefix + "n1",
		HostVethName:  prefix + "h1",
		GuestVethName: prefix + "g1",
		MAC:           "02:00:5e:10:00:01",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := first.Close(context.Background()); err != nil {
			t.Fatalf("first NIC cleanup: %v", err)
		}
	}()

	second, err := network.AttachNamespaceNIC(ctx, NamespaceNICSpec{
		NamespaceName: prefix + "n2",
		HostVethName:  prefix + "h2",
		GuestVethName: prefix + "g2",
		MAC:           "02:00:5e:10:00:02",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(context.Background()); err != nil {
			t.Fatalf("second NIC cleanup: %v", err)
		}
	}()

	if first.PrivateIP.Equal(second.PrivateIP) {
		t.Fatalf("duplicate private IP lease: %s", first.PrivateIP)
	}
	if first.PrivateIP.String() != "10.203.0.2" || second.PrivateIP.String() != "10.203.0.3" {
		t.Fatalf("leases = %s, %s; want 10.203.0.2, 10.203.0.3", first.PrivateIP, second.PrivateIP)
	}

	runner := Runner{}
	if _, err := runner.Output(ctx, "ip", "netns", "exec", network.NamespaceName, "ip", "link", "show", network.BridgeName); err != nil {
		t.Fatalf("network bridge %s is not inside namespace %s: %v", network.BridgeName, network.NamespaceName, err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", first.NamespaceName, "ping", "-c", "1", "-W", "1", network.Gateway.String()); err != nil {
		t.Fatalf("first namespace cannot reach bridge gateway %s: %v", network.Gateway, err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", second.NamespaceName, "ping", "-c", "1", "-W", "1", first.PrivateIP.String()); err != nil {
		t.Fatalf("second namespace cannot reach first NIC %s over bridge: %v", first.PrivateIP, err)
	}
	if err := first.ConfigureIngressFilter(ctx, nil); err != nil {
		t.Fatalf("configure deny-all ingress filter: %v", err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", second.NamespaceName, "ping", "-c", "1", "-W", "1", first.PrivateIP.String()); err == nil {
		t.Fatalf("second namespace reached first NIC despite deny-all ingress filter")
	}
	if err := first.ConfigureIngressFilter(ctx, []PacketRule{{Protocol: "icmp", SourceCIDR: "10.203.0.0/29"}}); err != nil {
		t.Fatalf("configure allow-icmp ingress filter: %v", err)
	}
	// A security group must allow ARP independently of its IP permissions.
	// Flush the neighbor learned before the filter existed so this probe proves
	// a newly attached peer can resolve the destination NIC through the filter.
	if err := runner.Run(ctx, "ip", "netns", "exec", second.NamespaceName, "ip", "neigh", "flush", first.PrivateIP.String()); err != nil {
		t.Fatalf("flush cached neighbor for filtered NIC: %v", err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", second.NamespaceName, "ping", "-c", "1", "-W", "1", first.PrivateIP.String()); err != nil {
		t.Fatalf("second namespace cannot reach first NIC after allow-icmp ingress filter: %v", err)
	}

	otherSubnet, err := network.CreateSubnet(ctx, SubnetSpec{
		Name:       "other",
		BridgeName: prefix + "b2",
		CIDR:       "10.203.1.0/29",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := otherSubnet.Close(context.Background()); err != nil {
			t.Fatalf("other subnet cleanup: %v", err)
		}
	}()
	third, err := otherSubnet.AttachNamespaceNIC(ctx, NamespaceNICSpec{
		NamespaceName: prefix + "n3",
		HostVethName:  prefix + "h3",
		GuestVethName: prefix + "g3",
		MAC:           "02:00:5e:10:00:03",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := third.Close(context.Background()); err != nil {
			t.Fatalf("third NIC cleanup: %v", err)
		}
	}()
	if third.PrivateIP.String() != "10.203.1.2" {
		t.Fatalf("third lease = %s; want 10.203.1.2", third.PrivateIP)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", third.NamespaceName, "ping", "-c", "1", "-W", "1", otherSubnet.Gateway.String()); err != nil {
		t.Fatalf("third namespace cannot reach subnet gateway %s: %v", otherSubnet.Gateway, err)
	}

	publicIP, err := ReservePublicIPv4("host-test", nil)
	if err != nil {
		t.Fatalf("reserve public IPv4: %v", err)
	}
	defer ReleasePublicIPv4(publicIP)
	if err := network.ConfigureSNAT(ctx, "10.203.0.0/29", publicIP, prefix+"sn"); err != nil {
		t.Fatalf("configure SNAT: %v", err)
	}
	egress, err := network.EnsureEgress(ctx)
	if err != nil {
		t.Fatalf("ensure egress: %v", err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", first.NamespaceName, "ping", "-c", "1", "-W", "1", egress.HostIP.String()); err != nil {
		t.Fatalf("first namespace cannot reach egress host peer %s through routed fabric: %v", egress.HostIP, err)
	}
	metadataListener, err := net.Listen("tcp", net.JoinHostPort(egress.HostIP.String(), "0"))
	if err != nil {
		t.Fatalf("listen on egress host peer for metadata probe: %v", err)
	}
	defer metadataListener.Close()
	metadataRemote := make(chan string, 1)
	metadataServer := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		metadataRemote <- host
		_, _ = w.Write([]byte("METADATA_OK\n"))
	})}
	defer metadataServer.Close()
	go func() { _ = metadataServer.Serve(metadataListener) }()
	metadataPort := metadataListener.Addr().(*net.TCPAddr).Port
	if err := network.ConfigureMetadataDNAT(ctx, metadataPort, prefix+"md"); err != nil {
		t.Fatalf("configure metadata DNAT: %v", err)
	}
	out, err := runner.Output(ctx, "ip", "netns", "exec", first.NamespaceName, "curl", "-fsS", "--max-time", "2", "http://"+MetadataIPv4+"/metadata-probe")
	if err != nil {
		t.Fatalf("first namespace cannot reach provider metadata address: %v", err)
	}
	if strings.TrimSpace(string(out)) != "METADATA_OK" {
		t.Fatalf("metadata probe response = %q", out)
	}
	select {
	case remote := <-metadataRemote:
		if remote != first.PrivateIP.String() {
			t.Fatalf("metadata server saw remote %s, want guest private IP %s", remote, first.PrivateIP)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("metadata server did not receive probe")
	}
	if err := network.RemoveAddressDNAT(ctx, prefix+"md"); err != nil {
		t.Fatalf("remove metadata DNAT: %v", err)
	}
	if _, err := runner.Output(ctx, "ip", "netns", "exec", first.NamespaceName,
		"curl", "-fsS", "--max-time", "1", "http://"+MetadataIPv4+"/metadata-probe"); err == nil {
		t.Fatal("provider metadata address remained reachable after its DNAT table was removed")
	}

	table := prefix + "tbl"
	if err := runner.Run(ctx, "nft", "add", "table", "inet", table); err != nil {
		t.Fatalf("create nft cleanup target: %v", err)
	}
	var cleanup CleanupStack
	cleanup.Add(func(cleanupCtx context.Context) error {
		return runner.Run(cleanupCtx, "nft", "delete", "table", "inet", table)
	})
	if err := cleanup.Close(ctx); err != nil {
		t.Fatalf("cleanup nft table: %v", err)
	}
	if _, err := runner.Output(ctx, "nft", "list", "table", "inet", table); err == nil {
		t.Fatalf("nft table %s still exists after cleanup", table)
	}
}

func shortPrefix() string {
	var b [3]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("sx%06x", os.Getpid())[:8]
	}
	return "sx" + hex.EncodeToString(b[:])
}

func TestCloseRemovesHostArtifacts(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := shortPrefix()
	host := NewHost()
	network, err := host.CreateNetwork(ctx, NetworkSpec{
		NamespaceName: prefix + "nw",
		BridgeName:    prefix + "br",
		CIDR:          "10.204.0.0/29",
	})
	if err != nil {
		t.Fatal(err)
	}
	nic, err := network.AttachNamespaceNIC(ctx, NamespaceNICSpec{
		NamespaceName: prefix + "ns",
		HostVethName:  prefix + "hv",
		GuestVethName: prefix + "gv",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := nic.Close(ctx); err != nil {
		t.Fatalf("close nic: %v", err)
	}
	if err := network.Close(ctx); err != nil {
		t.Fatalf("close network: %v", err)
	}

	runner := Runner{}
	out, err := runner.Output(ctx, "ip", "netns", "list")
	if err != nil {
		t.Fatalf("list namespaces: %v", err)
	}
	if strings.Contains(out, prefix+"ns") {
		t.Fatalf("namespace %sns still exists after cleanup: %s", prefix, out)
	}
	if strings.Contains(out, prefix+"nw") {
		t.Fatalf("network namespace %snw still exists after cleanup: %s", prefix, out)
	}
}

// TestSubnetResolverDNATServesTheSubnetBridge drives the path an Amazon ECS
// task takes: a VPC network, a subnet inside it, and a workload attached to
// that subnet. A network carries no bridge of its own — each of its subnets has
// one — so a resolver configured against the network's bridge name lands on
// `dev ""` and every task attach fails with "Cannot find device". Exercising
// only the network-level call hid that completely.
func TestSubnetResolverDNATServesTheSubnetBridge(t *testing.T) {
	report := DetectNetworkCapabilities()
	if err := report.Require(); err != nil {
		t.Skipf("host cannot create network namespaces: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	prefix := shortPrefix()
	runner := Runner{}
	host := NewHost()
	// A VPC network, created the way the Amazon EC2 slice creates one: no
	// bridge of its own, subnets underneath it.
	network, err := host.CreateNetwork(ctx, NetworkSpec{
		NamespaceName: prefix + "vn",
		BridgeName:    prefix + "vb",
		CIDR:          "10.212.0.0/16",
	})
	if err != nil {
		t.Fatalf("create the VPC network: %v", err)
	}
	defer func() { _ = network.Close(context.Background()) }()

	subnet, err := network.CreateSubnet(ctx, SubnetSpec{
		Name:       prefix + "sn",
		BridgeName: prefix + "sb",
		CIDR:       "10.212.1.0/24",
	})
	if err != nil {
		t.Fatalf("create the subnet: %v", err)
	}

	workload, err := subnet.AttachNamespaceNIC(ctx, NamespaceNICSpec{
		NamespaceName: prefix + "s1",
		HostVethName:  prefix + "sh",
		GuestVethName: prefix + "sg",
		MAC:           "02:00:5e:12:00:01",
	})
	if err != nil {
		t.Fatalf("attach the workload to the subnet: %v", err)
	}

	udpConn, err := net.ListenPacket("udp", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen udp: %v", err)
	}
	defer udpConn.Close()
	resolverPort := udpConn.LocalAddr().(*net.UDPAddr).Port
	seen := make(chan struct{}, 1)
	go func() {
		buf := make([]byte, 512)
		if _, _, err := udpConn.ReadFrom(buf); err == nil {
			seen <- struct{}{}
		}
	}()

	if err := subnet.ConfigureResolverDNAT(ctx, resolverPort, prefix+"dns"); err != nil {
		t.Fatalf("configure the subnet resolver: %v", err)
	}
	if err := runner.Run(ctx, "ip", "netns", "exec", workload.NamespaceName, "bash", "-c",
		fmt.Sprintf("exec 3<>/dev/udp/%s/53 && printf probe >&3", VPCResolverIPv4)); err != nil {
		t.Fatalf("query the VPC resolver from the workload: %v", err)
	}
	select {
	case <-seen:
	case <-time.After(10 * time.Second):
		t.Fatal("the host resolver never saw the workload's query")
	}
}

// The resolver is served on a link-local address, which is never on a
// workload's own subnet — so a query for it is routed to the namespace rather
// than depending on an address-resolution answer for an address inside the
// subnet. That difference is what makes it work for a task whose subnet
// contains the VPC's base address.
func TestVPCResolverIsLinkLocal(t *testing.T) {
	ip := net.ParseIP(VPCResolverIPv4)
	if ip == nil || !ip.IsLinkLocalUnicast() {
		t.Fatalf("the VPC resolver address %q is not link-local", VPCResolverIPv4)
	}
}
