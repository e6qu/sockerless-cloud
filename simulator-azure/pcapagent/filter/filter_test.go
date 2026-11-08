package filter

import (
	"encoding/binary"
	"net/netip"
	"testing"
)

// udpFrame builds an Ethernet frame carrying an IPv4 UDP datagram.
func udpFrame(src, dst string, srcPort, dstPort uint16) []byte {
	frame := make([]byte, 14+20+8)
	binary.BigEndian.PutUint16(frame[12:], 0x0800)
	ip := frame[14:]
	ip[0] = 0x45
	ip[9] = protoUDP
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(ip[12:16], s[:])
	copy(ip[16:20], d[:])
	binary.BigEndian.PutUint16(ip[20:], srcPort)
	binary.BigEndian.PutUint16(ip[22:], dstPort)
	return frame
}

func TestCapturesMatchesTheFiveTupleFromTheMachinesSide(t *testing.T) {
	filters, err := Parse([]Spec{{Protocol: "UDP", LocalIPAddress: "10.0.0.4", RemotePort: "53"}})
	if err != nil {
		t.Fatal(err)
	}
	query := udpFrame("10.0.0.4", "168.63.129.16", 40000, 53)
	answer := udpFrame("168.63.129.16", "10.0.0.4", 53, 40000)
	if !Captures(filters, query, true) {
		t.Error("an outgoing query from the local address to remote port 53 must be captured")
	}
	if !Captures(filters, answer, false) {
		t.Error("the incoming answer is the same conversation seen from the machine and must be captured")
	}
	if Captures(filters, answer, true) {
		t.Error("read as outgoing, the answer's local side is 168.63.129.16, which the filter excludes")
	}
}

func TestParseReadsEntriesRangesAndLists(t *testing.T) {
	filters, err := Parse([]Spec{{LocalIPAddress: "10.0.0.1-10.0.0.9", LocalPort: "80;443;"}})
	if err != nil {
		t.Fatal(err)
	}
	if !Captures(filters, udpFrame("1.2.3.4", "10.0.0.5", 1, 443), false) {
		t.Error("10.0.0.5:443 falls in the range and the list")
	}
	if Captures(filters, udpFrame("1.2.3.4", "10.0.0.10", 1, 443), false) {
		t.Error("10.0.0.10 is past the range")
	}
	if Captures(filters, udpFrame("1.2.3.4", "10.0.0.5", 1, 8080), false) {
		t.Error("8080 is not in the list")
	}
	for _, bad := range []Spec{
		{LocalIPAddress: "10.0.0.1-10.0.0.9;10.0.0.20"},
		{LocalPort: "1-2-3"},
		{RemotePort: "90-80"},
		{Protocol: "ICMP"},
		{RemoteIPAddress: "not-an-address"},
	} {
		if _, err := Parse([]Spec{bad}); err == nil {
			t.Errorf("%+v must be refused", bad)
		}
	}
}

func TestNoFilterCapturesEveryFrame(t *testing.T) {
	if !Captures(nil, []byte{0x01}, false) {
		t.Error("without filters every frame is captured")
	}
	filters, _ := Parse([]Spec{{Protocol: "Any"}})
	if !Captures(filters, make([]byte, 60), false) {
		t.Error("an Any filter with no addresses or ports captures a non-IP frame")
	}
}
