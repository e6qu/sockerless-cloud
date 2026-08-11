//go:build linux

package realexec

import (
	"os/exec"
	"testing"
	"time"
)

// These prove the capture records REAL traffic: the test creates a network
// namespace with a veth pair, sends actual packets across it, and asserts the
// capture file holds those packets. Nothing is simulated — if the packet socket
// or the namespace entry is broken, no frames arrive and the assertions fail.
//
// Capturing needs CAP_NET_RAW and namespace manipulation, so these run on a
// capable Linux host (CI, and the repository's real-network container harness)
// and are skipped nowhere else — the file itself is Linux-only, and the
// capability check below fails loudly rather than skipping when the host is
// Linux but not privileged.

func requireCaptureHost(t *testing.T) {
	t.Helper()
	// Capture needs less than the network fabric does — a namespace to enter
	// and a raw socket, not nftables or sysctl — so it asks for exactly that.
	if err := DetectCaptureCapabilities().Require(); err != nil {
		t.Fatalf("traffic capture tests need a privileged Linux host: %v", err)
	}
}

// peerAddress is the far end of the capture fixture's link — what the tests
// send traffic to so that it crosses the interface being captured.
const peerAddress = "10.77.0.2"

// captureNamespace builds a two-namespace link and returns the namespace and
// interface to capture on.
//
// The two ends have to live in SEPARATE namespaces. With both ends of a veth
// pair in one namespace on one subnet, Linux answers the ARP for the peer
// locally and short-circuits the traffic, so nothing crosses the wire and a
// capture correctly records nothing — the fixture would be testing itself
// rather than the capture.
func captureNamespace(t *testing.T) (namespace, iface string) {
	t.Helper()
	suffix := randomSuffix()
	namespace = "cap-" + suffix
	peerNamespace := "peer-" + suffix
	iface = "cap0"
	peerIface := "cap1"

	run := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	run("ip", "netns", "add", namespace)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", namespace).Run() })
	run("ip", "netns", "add", peerNamespace)
	t.Cleanup(func() { _ = exec.Command("ip", "netns", "del", peerNamespace).Run() })

	// Create the pair in the capture namespace, then move the far end across.
	run("ip", "netns", "exec", namespace, "ip", "link", "add", iface, "type", "veth", "peer", "name", peerIface)
	run("ip", "netns", "exec", namespace, "ip", "link", "set", peerIface, "netns", peerNamespace)

	run("ip", "netns", "exec", namespace, "ip", "addr", "add", "10.77.0.1/24", "dev", iface)
	run("ip", "netns", "exec", namespace, "ip", "link", "set", iface, "up")
	run("ip", "netns", "exec", namespace, "ip", "link", "set", "lo", "up")
	run("ip", "netns", "exec", peerNamespace, "ip", "addr", "add", peerAddress+"/24", "dev", peerIface)
	run("ip", "netns", "exec", peerNamespace, "ip", "link", "set", peerIface, "up")
	run("ip", "netns", "exec", peerNamespace, "ip", "link", "set", "lo", "up")
	return namespace, iface
}

func TestCaptureRecordsRealTraffic(t *testing.T) {
	requireCaptureHost(t)
	namespace, iface := captureNamespace(t)

	capture, err := StartCapture(CaptureSpec{
		NamespaceName: namespace,
		InterfaceName: iface,
	})
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	t.Cleanup(func() { _ = capture.Stop() })

	if status := capture.Status(); !status.Running {
		t.Fatal("a started capture reports Running")
	}

	// Real traffic across the pair the capture is watching.
	if out, err := exec.Command("ip", "netns", "exec", namespace,
		"ping", "-c", "3", "-i", "0.2", "-W", "1", peerAddress).CombinedOutput(); err != nil {
		t.Fatalf("generate traffic: %v\n%s", err, out)
	}

	deadline := time.Now().Add(5 * time.Second)
	var packets uint64
	for time.Now().Before(deadline) {
		if packets = capture.Status().Packets; packets > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if packets == 0 {
		t.Fatal("capture recorded no packets while real traffic crossed the interface")
	}

	if err := capture.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	status := capture.Status()
	if status.Running {
		t.Error("a stopped capture does not report Running")
	}
	if status.StopReason != CaptureStopRequested {
		t.Errorf("stop reason = %q, want %q", status.StopReason, CaptureStopRequested)
	}

	// The artifact is a real capture file with the frames in it.
	file := capture.Bytes()
	if len(file) <= pcapFileHeaderLen {
		t.Fatalf("capture file is %d bytes, holding no frames", len(file))
	}
	assertReadableCapture(t, file, int(packets))
}

func TestCaptureHonoursItsFilters(t *testing.T) {
	requireCaptureHost(t)
	namespace, iface := captureNamespace(t)

	// A filter naming a protocol the traffic will not use: ICMP echoes must
	// not be recorded by a capture that asked only for TCP.
	capture, err := StartCapture(CaptureSpec{
		NamespaceName: namespace,
		InterfaceName: iface,
		Filters:       []CaptureFilter{{Protocol: "TCP"}},
	})
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	t.Cleanup(func() { _ = capture.Stop() })

	if out, err := exec.Command("ip", "netns", "exec", namespace,
		"ping", "-c", "3", "-i", "0.2", "-W", "1", peerAddress).CombinedOutput(); err != nil {
		t.Fatalf("generate traffic: %v\n%s", err, out)
	}
	time.Sleep(time.Second)
	if got := capture.Status().Packets; got != 0 {
		t.Errorf("capture filtered to TCP recorded %d ICMP packets", got)
	}
}

func TestCaptureStopsAtItsTimeLimit(t *testing.T) {
	requireCaptureHost(t)
	namespace, iface := captureNamespace(t)

	capture, err := StartCapture(CaptureSpec{
		NamespaceName: namespace,
		InterfaceName: iface,
		TimeLimit:     500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	t.Cleanup(func() { _ = capture.Stop() })

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !capture.Status().Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	status := capture.Status()
	if status.Running {
		t.Fatal("capture did not stop at its time limit")
	}
	if status.StopReason != CaptureStopTimeExceeded {
		t.Errorf("stop reason = %q, want %q", status.StopReason, CaptureStopTimeExceeded)
	}
}

// A per-packet limit truncates what is STORED while the frame's real length on
// the wire survives into the record. Sizing the read buffer to the limit
// instead would let the kernel truncate the frame, and the capture would claim
// a cut-short frame really was that short — losing exactly the fact the format
// preserves the original length to express.
func TestCaptureTruncatesButKeepsTheWireLength(t *testing.T) {
	requireCaptureHost(t)
	namespace, iface := captureNamespace(t)

	const perPacket = 40
	capture, err := StartCapture(CaptureSpec{
		NamespaceName:  namespace,
		InterfaceName:  iface,
		BytesPerPacket: perPacket,
	})
	if err != nil {
		t.Fatalf("StartCapture: %v", err)
	}
	t.Cleanup(func() { _ = capture.Stop() })

	// 200-byte payloads, so every frame on the wire is far longer than the
	// per-packet limit.
	if out, err := exec.Command("ip", "netns", "exec", namespace,
		"ping", "-c", "3", "-i", "0.2", "-W", "1", "-s", "200", peerAddress).CombinedOutput(); err != nil {
		t.Fatalf("generate traffic: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if capture.Status().Packets > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := capture.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	file := capture.Bytes()
	offset := pcapFileHeaderLen
	records := 0
	for offset+pcapRecordHeaderLen <= len(file) {
		captured := int(leUint32(file[offset+8 : offset+12]))
		original := int(leUint32(file[offset+12 : offset+16]))
		if captured != perPacket {
			t.Errorf("record %d stored %d bytes, want the %d-byte per-packet limit", records, captured, perPacket)
		}
		if original <= perPacket {
			t.Errorf("record %d reports a wire length of %d, which cannot be right for a 200-byte ping —"+
				" the frame was truncated before its real length was recorded", records, original)
		}
		offset += pcapRecordHeaderLen + captured
		records++
	}
	if records == 0 {
		t.Fatal("capture recorded no frames")
	}
}

func TestCaptureFailsOnAnInterfaceThatDoesNotExist(t *testing.T) {
	requireCaptureHost(t)
	namespace, _ := captureNamespace(t)
	if _, err := StartCapture(CaptureSpec{
		NamespaceName: namespace,
		InterfaceName: "definitely-not-here",
	}); err == nil {
		t.Fatal("capturing a nonexistent interface must fail rather than produce an empty capture")
	}
}

// assertReadableCapture walks the capture file's records the way a reader does,
// proving the frames are laid out to the format rather than merely present.
func assertReadableCapture(t *testing.T, file []byte, wantAtLeast int) {
	t.Helper()
	if len(file) < pcapFileHeaderLen {
		t.Fatalf("capture file is shorter than its header")
	}
	offset := pcapFileHeaderLen
	records := 0
	for offset+pcapRecordHeaderLen <= len(file) {
		captured := int(leUint32(file[offset+8 : offset+12]))
		original := int(leUint32(file[offset+12 : offset+16]))
		if original < captured {
			t.Fatalf("record %d claims %d captured bytes of a %d byte frame", records, captured, original)
		}
		offset += pcapRecordHeaderLen + captured
		records++
	}
	if offset != len(file) {
		t.Errorf("capture file has %d trailing bytes — the records do not tile it", len(file)-offset)
	}
	if records < wantAtLeast {
		t.Errorf("capture file holds %d records, want at least %d", records, wantAtLeast)
	}
}

func leUint32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}

// randomSuffix keeps namespace names unique across tests in one run, and
// across runs that overlap on a shared host. A Linux network namespace name is
// limited in length, so this stays short.
func randomSuffix() string {
	const digits = "0123456789abcdefghijklmnopqrstuvwxyz"
	n := time.Now().UnixNano()
	out := make([]byte, 0, 8)
	for range 8 {
		out = append(out, digits[n%int64(len(digits))])
		n /= int64(len(digits))
	}
	return string(out)
}
