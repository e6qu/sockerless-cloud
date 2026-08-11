//go:build linux

package realexec

import (
	"os/exec"
	"testing"
	"time"
)

// These prove mirroring really forwards traffic. A capture is self-evidently
// testable — the file either holds frames or it does not — but a mirroring
// session's whole claim is that a copy arrives SOMEWHERE ELSE, so the
// assertions are made at the collector: traffic is generated on the mirrored
// link and observed arriving on a completely separate link, which can only
// happen if the frames were really forwarded there.

// collectorNamespace builds a second two-namespace link, unrelated to the
// mirrored one, and returns the collector's namespace and interface plus the
// far side a mirrored frame arrives at. Injecting a frame out of the collector
// interface delivers it to that far side, which is where the collector workload
// sits — a Firecracker guest behind its TAP, or the peer of a veth pair.
func collectorNamespace(t *testing.T) (namespace, iface, observeNamespace, observeIface string) {
	t.Helper()
	suffix := randomSuffix()
	namespace = "col-" + suffix
	observeNamespace = "obs-" + suffix
	iface = "col0"
	observeIface = "col1"

	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("ip", "netns", "add", namespace)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", namespace).Run() })
	run("ip", "netns", "add", observeNamespace)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", observeNamespace).Run() })

	run("ip", "netns", "exec", namespace, "ip", "link", "add", iface, "type", "veth", "peer", "name", observeIface)
	run("ip", "netns", "exec", namespace, "ip", "link", "set", observeIface, "netns", observeNamespace)
	// The collector link carries no addresses of its own. Mirrored frames keep
	// the mirrored link's addressing, so an addressed collector link would add
	// the kernel's own IPv6 autoconfiguration and ARP chatter to the very
	// interface the assertions read — noise indistinguishable from a delivery.
	run("ip", "netns", "exec", namespace, "ip", "link", "set", iface, "up")
	run("ip", "netns", "exec", observeNamespace, "ip", "link", "set", observeIface, "up")
	return namespace, iface, observeNamespace, observeIface
}

// mirroredSubnet is the mirrored link's addressing. Frames carrying it cannot
// arise on the collector link by any means other than being mirrored there,
// which is what makes an observation at the collector conclusive.
const mirroredSubnet = "10.77.0.0/24"

// generateMirroredTraffic sends real packets across the mirrored link.
func generateMirroredTraffic(t *testing.T, namespace string) {
	t.Helper()
	if out, err := exec.Command("ip", "netns", "exec", namespace,
		"ping", "-c", "5", "-i", "0.2", "-W", "1", peerAddress).CombinedOutput(); err != nil {
		t.Fatalf("generate traffic: %v\n%s", err, out)
	}
}

func awaitPackets(t *testing.T, count func() uint64, within time.Duration) uint64 {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if n := count(); n > 0 {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
	return count()
}

func TestMirrorDeliversTrafficToTheCollector(t *testing.T) {
	requireCaptureHost(t)
	sourceNS, sourceIface := captureNamespace(t)
	collectorNS, collectorIface, observeNS, observeIface := collectorNamespace(t)

	// Watch the far side of the collector link — the frames observed here
	// arrived only because the mirror forwarded them across.
	observed, err := StartCapture(CaptureSpec{
		NamespaceName: observeNS,
		InterfaceName: observeIface,
		Filters:       []CaptureFilter{{LocalAddress: mirroredSubnet}},
	})
	if err != nil {
		t.Fatalf("StartCapture on the collector: %v", err)
	}
	t.Cleanup(func() { _ = observed.Stop() })

	mirror, err := StartMirror(MirrorSpec{
		SourceNamespace: sourceNS,
		SourceInterface: sourceIface,
		Collectors:      []MirrorTarget{{NamespaceName: collectorNS, InterfaceName: collectorIface}},
	})
	if err != nil {
		t.Fatalf("StartMirror: %v", err)
	}
	t.Cleanup(func() { _ = mirror.Stop() })
	if !mirror.Status().Running {
		t.Fatal("a started mirror reports Running")
	}

	generateMirroredTraffic(t, sourceNS)

	mirrored := awaitPackets(t, func() uint64 { return mirror.Status().Packets }, 5*time.Second)
	if mirrored == 0 {
		t.Fatal("the mirror forwarded no packets while real traffic crossed the mirrored interface")
	}
	arrived := awaitPackets(t, func() uint64 { return observed.Status().Packets }, 5*time.Second)
	if arrived == 0 {
		t.Fatal("no mirrored packets arrived at the collector — the session counted " +
			"forwarded frames that never reached the collector's link")
	}

	if err := mirror.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if mirror.Status().Running {
		t.Error("a stopped mirror does not report Running")
	}
}

// A stopped mirror really stops forwarding: traffic generated after the stop
// does not reach the collector.
func TestMirrorStopsForwarding(t *testing.T) {
	requireCaptureHost(t)
	sourceNS, sourceIface := captureNamespace(t)
	collectorNS, collectorIface, observeNS, observeIface := collectorNamespace(t)

	mirror, err := StartMirror(MirrorSpec{
		SourceNamespace: sourceNS,
		SourceInterface: sourceIface,
		Collectors:      []MirrorTarget{{NamespaceName: collectorNS, InterfaceName: collectorIface}},
	})
	if err != nil {
		t.Fatalf("StartMirror: %v", err)
	}
	generateMirroredTraffic(t, sourceNS)
	if awaitPackets(t, func() uint64 { return mirror.Status().Packets }, 5*time.Second) == 0 {
		t.Fatal("the mirror forwarded nothing before being stopped, so stopping proves nothing")
	}
	if err := mirror.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	// Watch the collector only from here on, so anything seen arrived after
	// the mirror stopped.
	observed, err := StartCapture(CaptureSpec{
		NamespaceName: observeNS,
		InterfaceName: observeIface,
		Filters:       []CaptureFilter{{LocalAddress: mirroredSubnet}},
	})
	if err != nil {
		t.Fatalf("StartCapture on the collector: %v", err)
	}
	t.Cleanup(func() { _ = observed.Stop() })

	generateMirroredTraffic(t, sourceNS)
	time.Sleep(time.Second)
	if packets := observed.Status().Packets; packets != 0 {
		t.Fatalf("%d packets reached the collector after the mirror stopped", packets)
	}
}

// The policy's filter decides what is forwarded, so traffic the filter excludes
// does not reach the collector at all.
func TestMirrorHonoursItsFilter(t *testing.T) {
	requireCaptureHost(t)
	sourceNS, sourceIface := captureNamespace(t)
	collectorNS, collectorIface, observeNS, observeIface := collectorNamespace(t)

	observed, err := StartCapture(CaptureSpec{
		NamespaceName: observeNS,
		InterfaceName: observeIface,
		Filters:       []CaptureFilter{{LocalAddress: mirroredSubnet}},
	})
	if err != nil {
		t.Fatalf("StartCapture on the collector: %v", err)
	}
	t.Cleanup(func() { _ = observed.Stop() })

	// Mirror TCP only; the traffic generated below is ICMP.
	mirror, err := StartMirror(MirrorSpec{
		SourceNamespace: sourceNS,
		SourceInterface: sourceIface,
		Collectors:      []MirrorTarget{{NamespaceName: collectorNS, InterfaceName: collectorIface}},
		Filters:         []CaptureFilter{{Protocol: "TCP"}},
	})
	if err != nil {
		t.Fatalf("StartMirror: %v", err)
	}
	t.Cleanup(func() { _ = mirror.Stop() })

	generateMirroredTraffic(t, sourceNS)
	time.Sleep(time.Second)

	if packets := mirror.Status().Packets; packets != 0 {
		t.Errorf("the mirror forwarded %d packets under a TCP-only filter while only ICMP crossed the link", packets)
	}
	if packets := observed.Status().Packets; packets != 0 {
		t.Errorf("%d filtered-out packets reached the collector", packets)
	}
}

// A session with nowhere to deliver is refused rather than started: it would
// read frames, drop them, and report itself healthy.
func TestMirrorRefusesASessionWithNoCollector(t *testing.T) {
	requireCaptureHost(t)
	sourceNS, sourceIface := captureNamespace(t)

	if _, err := StartMirror(MirrorSpec{
		SourceNamespace: sourceNS,
		SourceInterface: sourceIface,
	}); err == nil {
		t.Fatal("a mirroring session with no collector must be refused")
	}
}
