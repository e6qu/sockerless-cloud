package sidecar

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

// A workload routed through an egress sidecar reaches the outside, and the
// outside sees the gateway's address and not the workload's; the simulator
// gets every connection reported under the sidecar's token, with what it
// carried.
func TestEgressSidecarCarriesARoutedWorkloadsConnectionsFromItsAddress(t *testing.T) {
	sim.InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := sim.PullImage(t.Context(), workloadImage, platform); err != nil {
		t.Fatal(err)
	}
	callbackHost, err := workloadhost.CallbackHost()
	if err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var httpRemote, udpRemote string
	httpListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		httpRemote = r.RemoteAddr
		mu.Unlock()
		_, _ = io.WriteString(w, "hello from outside")
	})}
	go func() { _ = server.Serve(httpListener) }()
	defer func() { _ = server.Close() }()
	_, httpPort, _ := net.SplitHostPort(httpListener.Addr().String())

	// The echo answers from the address it was reached at: a gateway admits
	// a datagram's reply from the destination it went to and nothing else.
	echo, err := net.ListenPacket("udp4", net.JoinHostPort(callbackHost, "0"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = echo.Close() }()
	go func() {
		buf := make([]byte, 1024)
		for {
			n, from, err := echo.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			udpRemote = from.String()
			mu.Unlock()
			_, _ = echo.WriteTo([]byte("echo "+string(buf[:n])), from)
		}
	}()
	_, udpPort, _ := net.SplitHostPort(echo.LocalAddr().String())

	networkName := fmt.Sprintf("sockerless-sim-test-egress-%d", time.Now().UnixNano())
	token := sim.NewSidecarToken()
	sim.RegisterSidecarToken(token, networkName)
	defer sim.UnregisterSidecarToken(token)
	reports := make(chan egress.Report, 64)
	reportListener, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	reporter := &http.Server{Handler: sim.SidecarTrust(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != egress.ReportPath {
			http.NotFound(w, r)
			return
		}
		EgressReports(func(network string, report egress.Report) {
			if network == networkName {
				reports <- report
			}
		})(w, r)
	}))}
	go func() { _ = reporter.Serve(reportListener) }()
	defer func() { _ = reporter.Close() }()
	_, reportPort, _ := net.SplitHostPort(reportListener.Addr().String())

	cidr := netip.MustParsePrefix("10.76.0.0/24")
	if _, err := sim.EnsureCloudNetwork(t.Context(), sim.CloudNetwork{Name: networkName, CIDR: cidr}); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = sim.RemoveDockerNetworkContext(context.Background(), networkName) }()
	gatewayAddr := netip.MustParseAddr("10.76.0.3")
	handle, err := StartEgress(t.Context(), EgressSpec{
		Network:      networkName,
		Address:      gatewayAddr,
		Name:         networkName + "-nat",
		Gateway:      "nat-test",
		Sources:      []netip.Prefix{cidr},
		Local:        []netip.Prefix{cidr},
		TCPIdle:      350 * time.Second,
		UDPIdle:      30 * time.Second,
		HTTPUpstream: "http://" + net.JoinHostPort(callbackHost, reportPort),
		Token:        token,
		ExtraHosts:   workloadhost.ExtraHosts(),
	}, sim.FuncSink(func(line sim.LogLine) { t.Logf("sidecar: %s", line.Text) }))
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Cancel()

	var output []string
	workload, err := sim.StartContainerSyncContext(t.Context(), sim.ContainerConfig{
		Image:        workloadImage,
		Architecture: platform,
		Command:      []string{"sh", "-c"},
		Args: []string{"ip route replace default via " + gatewayAddr.String() + " && " +
			"wget -q -O - http://" + net.JoinHostPort(callbackHost, httpPort) + "/ && echo && " +
			"echo ping | nc -u -w 2 " + callbackHost + " " + udpPort},
		Name:        networkName + "-workload",
		Network:     networkName,
		IPv4Address: "10.76.0.20",
		Timeout:     time.Minute,
		// The proof's workload routes itself through the gateway, which a
		// cloud does for its guests from outside them.
		Sandbox: sim.SandboxProfile{CapDrop: []string{"ALL"}, CapAdd: []string{"NET_ADMIN"}, NoNewPrivileges: true, DenyDockerSocket: true, DenyHostNetwork: true},
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
	tcpRemote, _, _ := net.SplitHostPort(httpRemote)
	udpFrom, _, _ := net.SplitHostPort(udpRemote)
	mu.Unlock()
	if result.ExitCode != 0 || !strings.Contains(all, "hello from outside") {
		t.Fatalf("the workload did not reach the outside through the gateway (exit %d): %s", result.ExitCode, all)
	}
	if !strings.Contains(all, "echo ping") {
		t.Fatalf("the workload's datagram was not answered through the gateway: %s", all)
	}
	for what, remote := range map[string]string{"connection": tcpRemote, "datagram": udpFrom} {
		if remote == "10.76.0.20" || (remote != gatewayAddr.String() && remote != callbackHost) {
			t.Fatalf("the %s arrived from %s, want the gateway %s", what, remote, gatewayAddr)
		}
	}

	seen := map[string]egress.Event{}
	deadline := time.After(30 * time.Second)
	for len(seen) < 5 {
		select {
		case report := <-reports:
			if report.Gateway != "nat-test" {
				t.Fatalf("a report named gateway %q", report.Gateway)
			}
			for _, ev := range report.Events {
				if ev.Kind != egress.Active {
					seen[ev.Protocol+":"+string(ev.Kind)] = ev
				}
			}
		case <-deadline:
			t.Fatalf("the sidecar reported %v and not every connection event", seen)
		}
	}
	tcpClosed, ok := seen["tcp:"+string(egress.Closed)]
	if !ok || tcpClosed.Source != "10.76.0.20" || tcpClosed.Destination != callbackHost || fmt.Sprint(tcpClosed.DestinationPort) != httpPort ||
		tcpClosed.BytesOut == 0 || tcpClosed.BytesIn == 0 || tcpClosed.Reason != egress.ReasonClosed {
		t.Fatalf("the closed connection was reported as %+v", tcpClosed)
	}
	if established := seen["tcp:"+string(egress.Established)]; established.ViaPort == 0 {
		t.Fatalf("the established connection names no port of the gateway's own: %+v", established)
	}
	for _, kind := range []egress.Kind{egress.Opened, egress.Established} {
		if _, ok := seen["tcp:"+string(kind)]; !ok {
			t.Fatalf("no %s event for the connection: %v", kind, seen)
		}
		if _, ok := seen["udp:"+string(kind)]; !ok {
			t.Fatalf("no %s event for the datagram flow: %v", kind, seen)
		}
	}
	// A gateway stopped with its grace reports what its open connections
	// carried before it goes.
	sim.StopContainer(handle.ContainerID, 10*time.Second)
	<-handle.Exited()
	udpClosed := false
	flush := time.After(10 * time.Second)
	for !udpClosed {
		select {
		case report := <-reports:
			for _, ev := range report.Events {
				if ev.Protocol == "udp" && ev.Kind == egress.Closed && ev.BytesIn > 0 && ev.BytesOut > 0 {
					udpClosed = true
				}
			}
		case <-flush:
			t.Fatal("the sidecar did not report the datagram flow's end with what it carried when it stopped")
		}
	}
	_ = json.Marshal
}
