// Package filter reads Azure Network Watcher packet capture filters and
// matches captured frames against them: a filter is a 5-tuple of protocol,
// local and remote address, and local and remote port, each field naming one
// entry ("10.0.0.4"), a range ("10.0.0.1-10.0.0.9") or several entries
// ("10.0.0.4;10.0.0.5;"). A frame is captured when it matches any filter.
package filter

import (
	"encoding/binary"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
)

// Spec is a PacketCaptureFilter as the API spells it.
type Spec struct {
	Protocol        string `json:"protocol,omitempty"`
	LocalIPAddress  string `json:"localIPAddress,omitempty"`
	LocalPort       string `json:"localPort,omitempty"`
	RemoteIPAddress string `json:"remoteIPAddress,omitempty"`
	RemotePort      string `json:"remotePort,omitempty"`
}

// Filter is a parsed Spec.
type Filter struct {
	protocol    uint8
	local       addrSet
	remote      addrSet
	localPorts  portSet
	remotePorts portSet
}

type addrSet struct {
	ranges [][2]netip.Addr
}

type portSet struct {
	ranges [][2]uint16
}

const (
	protoTCP = 6
	protoUDP = 17
)

// Parse reads every filter of a capture; an empty list captures every frame.
func Parse(specs []Spec) ([]Filter, error) {
	out := make([]Filter, 0, len(specs))
	for i, s := range specs {
		f, err := parse(s)
		if err != nil {
			return nil, fmt.Errorf("filters[%d]: %w", i, err)
		}
		out = append(out, f)
	}
	return out, nil
}

func parse(s Spec) (Filter, error) {
	var f Filter
	switch strings.ToUpper(s.Protocol) {
	case "", "ANY":
	case "TCP":
		f.protocol = protoTCP
	case "UDP":
		f.protocol = protoUDP
	default:
		return f, fmt.Errorf("protocol %q is not TCP, UDP or Any", s.Protocol)
	}
	var err error
	if f.local, err = parseAddrs(s.LocalIPAddress); err != nil {
		return f, fmt.Errorf("localIPAddress: %w", err)
	}
	if f.remote, err = parseAddrs(s.RemoteIPAddress); err != nil {
		return f, fmt.Errorf("remoteIPAddress: %w", err)
	}
	if f.localPorts, err = parsePorts(s.LocalPort); err != nil {
		return f, fmt.Errorf("localPort: %w", err)
	}
	if f.remotePorts, err = parsePorts(s.RemotePort); err != nil {
		return f, fmt.Errorf("remotePort: %w", err)
	}
	return f, nil
}

// entries splits a field into its entries and reports whether it names a
// range. The API supports neither several ranges nor ranges mixed with
// entries.
func entries(field string) ([]string, bool, error) {
	field = strings.TrimSpace(field)
	if field == "" {
		return nil, false, nil
	}
	if strings.Contains(field, "-") {
		if strings.Contains(field, ";") {
			return nil, false, fmt.Errorf("%q mixes a range with several entries, which is not supported", field)
		}
		parts := strings.Split(field, "-")
		if len(parts) != 2 {
			return nil, false, fmt.Errorf("%q names more than one range, which is not supported", field)
		}
		return []string{strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])}, true, nil
	}
	var out []string
	for _, e := range strings.Split(field, ";") {
		if e = strings.TrimSpace(e); e != "" {
			out = append(out, e)
		}
	}
	return out, false, nil
}

func parseAddrs(field string) (addrSet, error) {
	list, isRange, err := entries(field)
	if err != nil {
		return addrSet{}, err
	}
	var set addrSet
	addrs := make([]netip.Addr, 0, len(list))
	for _, e := range list {
		a, err := netip.ParseAddr(e)
		if err != nil {
			return addrSet{}, fmt.Errorf("%q is not an IP address", e)
		}
		addrs = append(addrs, a.Unmap())
	}
	if isRange {
		if addrs[0].BitLen() != addrs[1].BitLen() || addrs[1].Less(addrs[0]) {
			return addrSet{}, fmt.Errorf("%q is not an ascending range", field)
		}
		set.ranges = append(set.ranges, [2]netip.Addr{addrs[0], addrs[1]})
		return set, nil
	}
	for _, a := range addrs {
		set.ranges = append(set.ranges, [2]netip.Addr{a, a})
	}
	return set, nil
}

func parsePorts(field string) (portSet, error) {
	list, isRange, err := entries(field)
	if err != nil {
		return portSet{}, err
	}
	var set portSet
	ports := make([]uint16, 0, len(list))
	for _, e := range list {
		p, err := strconv.ParseUint(e, 10, 16)
		if err != nil {
			return portSet{}, fmt.Errorf("%q is not a port", e)
		}
		ports = append(ports, uint16(p))
	}
	if isRange {
		if ports[1] < ports[0] {
			return portSet{}, fmt.Errorf("%q is not an ascending range", field)
		}
		set.ranges = append(set.ranges, [2]uint16{ports[0], ports[1]})
		return set, nil
	}
	for _, p := range ports {
		set.ranges = append(set.ranges, [2]uint16{p, p})
	}
	return set, nil
}

func (s addrSet) any() bool { return len(s.ranges) == 0 }

func (s addrSet) has(a netip.Addr) bool {
	if s.any() {
		return true
	}
	for _, r := range s.ranges {
		if r[0].BitLen() == a.BitLen() && !a.Less(r[0]) && !r[1].Less(a) {
			return true
		}
	}
	return false
}

func (s portSet) any() bool { return len(s.ranges) == 0 }

func (s portSet) has(p uint16) bool {
	if s.any() {
		return true
	}
	for _, r := range s.ranges {
		if p >= r[0] && p <= r[1] {
			return true
		}
	}
	return false
}

// Packet is what a filter reads from an Ethernet frame.
type Packet struct {
	IP       bool
	Protocol uint8
	Src, Dst netip.Addr
	// Ports holds the source and destination ports of a TCP or UDP segment.
	Ports            bool
	SrcPort, DstPort uint16
}

// Decode reads the network and transport headers of an Ethernet frame,
// through one 802.1Q tag.
func Decode(frame []byte) Packet {
	var p Packet
	if len(frame) < 14 {
		return p
	}
	etherType := binary.BigEndian.Uint16(frame[12:14])
	payload := frame[14:]
	if etherType == 0x8100 && len(payload) >= 4 {
		etherType = binary.BigEndian.Uint16(payload[2:4])
		payload = payload[4:]
	}
	var transport []byte
	switch etherType {
	case 0x0800:
		if len(payload) < 20 {
			return p
		}
		ihl := int(payload[0]&0x0f) * 4
		if ihl < 20 || len(payload) < ihl {
			return p
		}
		p.IP = true
		p.Protocol = payload[9]
		p.Src = netip.AddrFrom4([4]byte(payload[12:16]))
		p.Dst = netip.AddrFrom4([4]byte(payload[16:20]))
		// A fragment after the first carries no transport header.
		if binary.BigEndian.Uint16(payload[6:8])&0x1fff == 0 {
			transport = payload[ihl:]
		}
	case 0x86dd:
		if len(payload) < 40 {
			return p
		}
		p.IP = true
		p.Protocol = payload[6]
		p.Src = netip.AddrFrom16([16]byte(payload[8:24]))
		p.Dst = netip.AddrFrom16([16]byte(payload[24:40]))
		transport = payload[40:]
	default:
		return p
	}
	if (p.Protocol == protoTCP || p.Protocol == protoUDP) && len(transport) >= 4 {
		p.Ports = true
		p.SrcPort = binary.BigEndian.Uint16(transport[0:2])
		p.DstPort = binary.BigEndian.Uint16(transport[2:4])
	}
	return p
}

// Captures reports whether filters take a frame; outgoing says the machine
// sent it, so its source is the local side.
func Captures(filters []Filter, frame []byte, outgoing bool) bool {
	if len(filters) == 0 {
		return true
	}
	p := Decode(frame)
	for _, f := range filters {
		if f.matches(p, outgoing) {
			return true
		}
	}
	return false
}

func (f Filter) matches(p Packet, outgoing bool) bool {
	needsIP := f.protocol != 0 || !f.local.any() || !f.remote.any()
	needsPorts := !f.localPorts.any() || !f.remotePorts.any()
	if !needsIP && !needsPorts {
		return true
	}
	if !p.IP {
		return false
	}
	if f.protocol != 0 && p.Protocol != f.protocol {
		return false
	}
	local, remote := p.Dst, p.Src
	localPort, remotePort := p.DstPort, p.SrcPort
	if outgoing {
		local, remote = p.Src, p.Dst
		localPort, remotePort = p.SrcPort, p.DstPort
	}
	if !f.local.has(local) || !f.remote.has(remote) {
		return false
	}
	if needsPorts {
		if !p.Ports {
			return false
		}
		if !f.localPorts.has(localPort) || !f.remotePorts.has(remotePort) {
			return false
		}
	}
	return true
}
