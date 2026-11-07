package main

import (
	"encoding/binary"
	"net/netip"
	"testing"

	"github.com/e6qu/sockerless-cloud/sim/sidecar/gwlb"
)

// ipv4 is a minimal IPv4 packet carrying the first four bytes of a TCP or UDP
// header.
func ipv4(src, dst string, proto byte, sport, dport uint16) []byte {
	p := make([]byte, 24)
	p[0] = 0x45
	p[9] = proto
	s, d := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	binary.BigEndian.PutUint16(p[20:], sport)
	binary.BigEndian.PutUint16(p[22:], dport)
	return p
}

// A flow's key is the same in both directions, and each stickiness keys on
// the parts the Gateway Load Balancer documentation names: the 5-tuple, the
// addresses and protocol, or the addresses alone.
func TestGWLBFlowKeyFollowsTheStickiness(t *testing.T) {
	out := ipv4("10.0.1.5", "198.51.100.9", 6, 40000, 443)
	back := ipv4("198.51.100.9", "10.0.1.5", 6, 443, 40000)
	otherPort := ipv4("10.0.1.5", "198.51.100.9", 6, 40001, 443)
	udp := ipv4("10.0.1.5", "198.51.100.9", 17, 40000, 443)
	key := func(stickiness string, p []byte) string {
		t.Helper()
		k, ok := gwlbFlowKey(stickiness, p)
		if !ok {
			t.Fatalf("no key for an IPv4 packet")
		}
		return k
	}
	for _, s := range []string{gwlb.FiveTuple, gwlb.SourceIPDestIPProto, gwlb.SourceIPDestIP} {
		if key(s, out) != key(s, back) {
			t.Errorf("%s: a reply keys differently from its packet", s)
		}
	}
	if key(gwlb.FiveTuple, out) == key(gwlb.FiveTuple, otherPort) {
		t.Errorf("5_tuple: another source port keyed the same flow")
	}
	if key(gwlb.SourceIPDestIPProto, out) != key(gwlb.SourceIPDestIPProto, otherPort) {
		t.Errorf("source_ip_dest_ip_proto: another source port keyed another flow")
	}
	if key(gwlb.SourceIPDestIPProto, out) == key(gwlb.SourceIPDestIPProto, udp) {
		t.Errorf("source_ip_dest_ip_proto: another protocol keyed the same flow")
	}
	if key(gwlb.SourceIPDestIP, out) != key(gwlb.SourceIPDestIP, udp) {
		t.Errorf("source_ip_dest_ip: another protocol keyed another flow")
	}
	if _, ok := gwlbFlowKey(gwlb.FiveTuple, []byte{0x60, 0, 0}); ok {
		t.Errorf("a packet that is not IPv4 got a key")
	}
}

// The GENEVE the node sends carries the IPv4 protocol type and the three
// options of class 0x0108 the AWS tunnel handler requires, laid out as its
// parser reads them, and the node reads its own encapsulation back.
func TestGWLBEncapsulationCarriesTheGatewayLoadBalancerOptions(t *testing.T) {
	link := &gwlbLink{endpoint: "vpce-0a1b2c3d"}
	n := &gwlbNode{state: gwlb.State{Endpoints: []gwlb.Endpoint{{ID: "vpce-0a1b2c3d", Attachment: 0x1122334455667788}}}}
	flow := &gwlbFlow{cookie: 0xdeadbeef}
	packet := ipv4("10.0.1.5", "198.51.100.9", 6, 40000, 443)
	d := n.encapsulate(link, flow, packet)

	if d[0]&0xc0 != 0 || binary.BigEndian.Uint16(d[2:]) != 0x0800 {
		t.Fatalf("header %x: want version 0 and protocol type 0x0800", d[:8])
	}
	optionsLen := int(d[0]&0x3f) * 4
	want := []struct {
		typ   byte
		value []byte
	}{
		{1, binary.BigEndian.AppendUint64(nil, 0x0a1b2c3d)},
		{2, binary.BigEndian.AppendUint64(nil, 0x1122334455667788)},
		{3, binary.BigEndian.AppendUint32(nil, 0xdeadbeef)},
	}
	off := 8
	for _, w := range want {
		if binary.BigEndian.Uint16(d[off:]) != 0x0108 || d[off+2] != w.typ || int(d[off+3]&0x1f)*4 != len(w.value) {
			t.Fatalf("option at %d is %x: want class 0x0108 type %d of %d bytes", off, d[off:off+4], w.typ, len(w.value))
		}
		if got := d[off+4 : off+4+len(w.value)]; string(got) != string(w.value) {
			t.Errorf("option type %d carries %x, want %x", w.typ, got, w.value)
		}
		off += 4 + len(w.value)
	}
	if off != 8+optionsLen {
		t.Fatalf("options end at %d, the header says %d", off, 8+optionsLen)
	}

	endpoint, cookie, inner, ok := gwlbDecapsulate(d)
	if !ok || endpoint != 0x0a1b2c3d || cookie != 0xdeadbeef || string(inner) != string(packet) {
		t.Fatalf("decapsulated endpoint %x cookie %x ok %v, inner matches %v", endpoint, cookie, ok, string(inner) == string(packet))
	}
	if _, _, _, ok := gwlbDecapsulate(d[:6]); ok {
		t.Errorf("a datagram shorter than the header decapsulated")
	}
}

// New flows go to a healthy target by hash; with none healthy, a flow goes
// to a target picked at random; with no target, nowhere.
func TestGWLBPicksAHealthyTarget(t *testing.T) {
	n := &gwlbNode{state: gwlb.State{Targets: []gwlb.Target{{Address: "10.9.0.4"}, {Address: "10.9.0.5", Healthy: true}}}}
	for _, key := range []string{"a", "b", "c", "d"} {
		if got := n.pickTarget(key); got != "10.9.0.5" {
			t.Errorf("key %s went to %s, want the healthy 10.9.0.5", key, got)
		}
	}
	n.state.Targets[1].Healthy = false
	if got := n.pickTarget("a"); got != "10.9.0.4" && got != "10.9.0.5" {
		t.Errorf("with none healthy a flow went to %q", got)
	}
	n.state.Targets = nil
	if got := n.pickTarget("a"); got != "" {
		t.Errorf("with no target a flow went to %q", got)
	}
}
