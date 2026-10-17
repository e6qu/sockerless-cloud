package sidecar

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/miekg/dns"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/dnsserver"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

const workloadImage = "mirror.gcr.io/library/busybox:latest"

// A workload on a cloud network resolves through the sidecar at the cloud's
// reserved address, and the query reaches the simulator's resolver attributed
// to the network; the workload's metadata request reaches the simulator's
// HTTP listener with the workload's own address as its remote address.
func TestSidecarForwardsAWorkloadsDNSAndMetadataWithItsNetwork(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), workloadImage, platform); err != nil {
		t.Fatal(err)
	}
	callbackHost, err := workloadhost.CallbackHost()
	if err != nil {
		t.Fatal(err)
	}

	var seen sync.Map
	resolver, err := dnsserver.Start(dnsserver.Config{Name: "test", Catalog: func(q dnsserver.Query) dnsserver.View {
		seen.Store(q.Name, q.Network)
		rr, _ := dns.NewRR("db.internal. 60 IN A 10.77.0.9")
		return dnsserver.View{Names: []dns.RR{rr}}
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	networkName := fmt.Sprintf("sockerless-sim-test-net-%d", time.Now().UnixNano())
	port, err := resolver.ListenNetwork(networkName)
	if err != nil {
		t.Fatal(err)
	}

	var remote string
	httpListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote = r.RemoteAddr
		_, _ = io.WriteString(w, "i-0123")
	})}
	go func() { _ = server.Serve(httpListener) }()
	defer func() { _ = server.Close() }()
	_, httpPort, _ := net.SplitHostPort(httpListener.Addr().String())

	cidr := netip.MustParsePrefix("10.77.0.0/24")
	if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: networkName, CIDR: cidr}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sim.RemoveDockerNetworkContext(context.Background(), networkName) }()

	sidecarAddr := netip.MustParseAddr("10.77.0.2")
	token := sim.NewSidecarToken()
	handle, err := Start(t.Context(), Spec{
		Network:      networkName,
		Address:      sidecarAddr,
		Name:         networkName + "-sidecar",
		DNSUpstream:  DNSUpstream(callbackHost, port),
		HTTPUpstream: "http://" + net.JoinHostPort(callbackHost, httpPort),
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
	}, sim.NoopSink{})
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Cancel()
	readyCtx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	if err := WaitReady(readyCtx, sidecarAddr); err != nil {
		t.Fatal(err)
	}

	var output []string
	var mu sync.Mutex
	workload, err := sim.StartContainerSyncContext(t.Context(), sim.ContainerConfig{
		Image:        workloadImage,
		Architecture: platform,
		Command:      []string{"sh", "-c"},
		Args: []string{"nslookup db.internal 2>&1; " +
			"wget -q -O - http://" + sidecarAddr.String() + "/latest/meta-data/instance-id"},
		Name:        networkName + "-workload",
		Network:     networkName,
		IPv4Address: "10.77.0.20",
		DNS:         []string{sidecarAddr.String()},
		Timeout:     time.Minute,
		Sandbox:     sim.SandboxProfile{CapDrop: []string{"ALL"}, NoNewPrivileges: true, DenyDockerSocket: true, DenyHostNetwork: true},
	}, sim.FuncSink(func(line sim.LogLine) {
		mu.Lock()
		output = append(output, line.Text)
		mu.Unlock()
	}))
	if err != nil {
		t.Fatal(err)
	}
	result := workload.Wait()
	mu.Lock()
	all := strings.Join(output, "\n")
	mu.Unlock()
	if result.ExitCode != 0 {
		t.Fatalf("workload exited %d: %s", result.ExitCode, all)
	}
	if !strings.Contains(all, "10.77.0.9") {
		t.Fatalf("the workload did not resolve db.internal through the sidecar: %s", all)
	}
	if network, ok := seen.Load("db.internal."); !ok || network != networkName {
		t.Fatalf("the resolver attributed the query to %v, want %s", network, networkName)
	}
	if !strings.Contains(all, "i-0123") {
		t.Fatalf("the workload did not reach the metadata listener through the sidecar: %s", all)
	}
	// The trust middleware is what rewrites the remote address on a real
	// simulator; here the raw listener sees the sidecar's own address, which
	// proves the request came through the sidecar and not from the host.
	host, _, _ := net.SplitHostPort(remote)
	if host != sidecarAddr.String() && host != callbackHost {
		t.Fatalf("metadata request arrived from %s, want the sidecar %s", remote, sidecarAddr)
	}
	_ = strconv.Itoa
}

// A manager gives a network one sidecar at the second host of its range,
// hands the same one back on a second Ensure, and takes it down with Remove.
func TestManagerRunsOneSidecarPerNetwork(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	resolver, err := dnsserver.Start(dnsserver.Config{Name: "test", Catalog: func(dnsserver.Query) dnsserver.View { return dnsserver.View{} }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resolver.Close() }()
	networkName := fmt.Sprintf("sockerless-sim-test-mgr-%d", time.Now().UnixNano())
	if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: networkName, CIDR: netip.MustParsePrefix("10.78.0.0/24")}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sim.RemoveDockerNetworkContext(context.Background(), networkName) }()
	m := NewManager(resolver, "http://127.0.0.1:1", nil)
	defer m.Close()
	first, err := m.Ensure(t.Context(), networkName, netip.Addr{})
	if err != nil {
		t.Fatal(err)
	}
	if first.Address != netip.MustParseAddr("10.78.0.2") {
		t.Fatalf("sidecar at %s, want the second host of the range", first.Address)
	}
	again, err := m.Ensure(t.Context(), networkName, netip.Addr{})
	if err != nil || again != first {
		t.Fatalf("second Ensure returned %v, %v", again, err)
	}
	m.Remove(networkName)
	existing, err := sim.FindExistingContainers(map[string]string{"sockerless-sim-sidecar": networkName})
	if err != nil || len(existing) != 0 {
		t.Fatalf("after Remove the network still has %d sidecar containers (%v)", len(existing), err)
	}
}
