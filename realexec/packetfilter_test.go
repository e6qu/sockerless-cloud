package realexec

import (
	"encoding/binary"
	"testing"
)

// tcpFrame builds an Ethernet + IPv4 + TCP frame between the given endpoints,
// with the header fields a parser reads actually filled in — the filter is
// only meaningful if it is matching real headers.
func tcpFrame(srcIP, dstIP [4]byte, srcPort, dstPort uint16) []byte {
	return ipv4Frame(6, srcIP, dstIP, transportPorts(srcPort, dstPort))
}

func udpFrame(srcIP, dstIP [4]byte, srcPort, dstPort uint16) []byte {
	return ipv4Frame(17, srcIP, dstIP, transportPorts(srcPort, dstPort))
}

func transportPorts(srcPort, dstPort uint16) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint16(b[0:2], srcPort)
	binary.BigEndian.PutUint16(b[2:4], dstPort)
	return b
}

func ipv4Frame(protocol byte, srcIP, dstIP [4]byte, payload []byte) []byte {
	frame := make([]byte, ethernetHeaderLen+20+len(payload))
	binary.BigEndian.PutUint16(frame[12:14], etherTypeIPv4)
	ip := frame[ethernetHeaderLen:]
	ip[0] = 0x45
	binary.BigEndian.PutUint16(ip[2:4], uint16(20+len(payload)))
	ip[9] = protocol
	copy(ip[12:16], srcIP[:])
	copy(ip[16:20], dstIP[:])
	copy(ip[20:], payload)
	return frame
}

var (
	hostA = [4]byte{10, 0, 0, 1}
	hostB = [4]byte{10, 0, 1, 5}
	hostC = [4]byte{192, 168, 4, 4}
)

func TestNoFiltersCapturesEverything(t *testing.T) {
	if !MatchesAny(tcpFrame(hostA, hostB, 443, 51000), nil) {
		t.Error("a capture with no filters must keep every frame")
	}
}

func TestFilterMatchesTheFiveTuple(t *testing.T) {
	frame := tcpFrame(hostA, hostB, 443, 51000)
	for _, tc := range []struct {
		name  string
		f     CaptureFilter
		match bool
	}{
		{"protocol matches", CaptureFilter{Protocol: "TCP"}, true},
		{"protocol differs", CaptureFilter{Protocol: "UDP"}, false},
		{"protocol Any", CaptureFilter{Protocol: "Any"}, true},
		{"local address as source", CaptureFilter{LocalAddress: "10.0.0.1"}, true},
		{"local address as destination", CaptureFilter{LocalAddress: "10.0.1.5"}, true},
		{"address not in the frame", CaptureFilter{LocalAddress: "192.168.4.4"}, false},
		{"address by CIDR", CaptureFilter{LocalAddress: "10.0.0.0/16"}, true},
		{"address outside the CIDR", CaptureFilter{LocalAddress: "172.16.0.0/12"}, false},
		{"port as source", CaptureFilter{LocalPort: "443"}, true},
		{"port as destination", CaptureFilter{LocalPort: "51000"}, true},
		{"port not in the frame", CaptureFilter{LocalPort: "8080"}, false},
		{"port range covering it", CaptureFilter{LocalPort: "400-500"}, true},
		{"port range missing it", CaptureFilter{LocalPort: "1-100"}, false},
		{"semicolon list, one matching", CaptureFilter{LocalPort: "8080;443"}, true},
		{"every field together", CaptureFilter{
			Protocol: "TCP", LocalAddress: "10.0.0.1", LocalPort: "443",
			RemoteAddress: "10.0.1.5", RemotePort: "51000",
		}, true},
		{"one field wrong fails the whole filter", CaptureFilter{
			Protocol: "TCP", LocalAddress: "10.0.0.1", LocalPort: "443",
			RemoteAddress: "10.0.1.5", RemotePort: "9999",
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := MatchesAny(frame, []CaptureFilter{tc.f}); got != tc.match {
				t.Errorf("MatchesAny(%+v) = %v, want %v", tc.f, got, tc.match)
			}
		})
	}
}

func TestFiltersAreAlternatives(t *testing.T) {
	frame := tcpFrame(hostA, hostB, 443, 51000)
	filters := []CaptureFilter{
		{Protocol: "UDP"},
		{LocalPort: "443"},
	}
	if !MatchesAny(frame, filters) {
		t.Error("a frame matching any one filter is captured")
	}
	if MatchesAny(frame, []CaptureFilter{{Protocol: "UDP"}, {LocalPort: "9999"}}) {
		t.Error("a frame matching no filter is not captured")
	}
}

func TestUDPAndProtocolDiscrimination(t *testing.T) {
	udp := udpFrame(hostA, hostC, 53, 40000)
	if !MatchesAny(udp, []CaptureFilter{{Protocol: "UDP", LocalPort: "53"}}) {
		t.Error("UDP frame should match a UDP filter on its port")
	}
	if MatchesAny(udp, []CaptureFilter{{Protocol: "TCP"}}) {
		t.Error("UDP frame must not match a TCP filter")
	}
}

// A frame whose headers cannot be read carries no five-tuple, so it cannot be
// shown to satisfy a filter that narrows anything and is not captured. This is
// what a capture filtered to TCP does with the link's ARP traffic — recording
// it would report frames the operator excluded, which is how a real capture
// against this fixture caught the opposite rule being wrong.
//
// The exception is a filter that narrows nothing: it admits every frame,
// because there is no constraint for an unreadable frame to fail.
func TestUnparseableFramesMatchOnlyAnUnconstrainedFilter(t *testing.T) {
	arp := func() []byte {
		f := make([]byte, 42)
		binary.BigEndian.PutUint16(f[12:14], 0x0806)
		return f
	}()
	for _, tc := range []struct {
		name  string
		frame []byte
	}{
		{"shorter than an Ethernet header", []byte{1, 2, 3}},
		{"ARP, which carries no five-tuple", arp},
		{"unknown EtherType", func() []byte {
			f := make([]byte, 32)
			binary.BigEndian.PutUint16(f[12:14], 0x88cc) // LLDP
			return f
		}()},
		{"truncated IPv4 header", func() []byte {
			f := make([]byte, ethernetHeaderLen+10)
			binary.BigEndian.PutUint16(f[12:14], etherTypeIPv4)
			f[ethernetHeaderLen] = 0x45
			return f
		}()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if MatchesAny(tc.frame, []CaptureFilter{{Protocol: "TCP"}}) {
				t.Error("a frame with no readable five-tuple must not satisfy a filter that narrows one")
			}
			if !MatchesAny(tc.frame, []CaptureFilter{{Protocol: "Any"}}) {
				t.Error("a filter narrowing nothing admits every frame, readable or not")
			}
			if !MatchesAny(tc.frame, nil) {
				t.Error("an unfiltered capture records every frame")
			}
		})
	}
}

func TestVLANTaggedFramesAreParsed(t *testing.T) {
	inner := tcpFrame(hostA, hostB, 443, 51000)
	tagged := make([]byte, 0, len(inner)+4)
	tagged = append(tagged, inner[:12]...)
	tagged = append(tagged, 0x81, 0x00, 0x00, 0x64) // 802.1Q, VLAN 100
	tagged = append(tagged, inner[12:]...)
	if !MatchesAny(tagged, []CaptureFilter{{Protocol: "TCP", LocalPort: "443"}}) {
		t.Error("a VLAN-tagged frame must be parsed past the tag, not treated as unreadable")
	}
}

// A non-initial fragment carries no transport header, so its ports are unknown
// rather than zero — a port filter must not match it by accident.
func TestNonInitialFragmentHasNoPorts(t *testing.T) {
	frame := tcpFrame(hostA, hostB, 443, 51000)
	ip := frame[ethernetHeaderLen:]
	binary.BigEndian.PutUint16(ip[6:8], 185) // fragment offset, non-zero
	if MatchesAny(frame, []CaptureFilter{{LocalPort: "443"}}) {
		t.Error("a non-initial fragment has no readable ports and must not match a port filter")
	}
	if !MatchesAny(frame, []CaptureFilter{{Protocol: "TCP"}}) {
		t.Error("a fragment still matches on the fields it does carry")
	}
}
