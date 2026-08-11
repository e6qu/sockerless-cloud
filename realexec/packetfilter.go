package realexec

import (
	"encoding/binary"
	"net"
	"strconv"
	"strings"
)

// A capture filter selects frames by the five-tuple, which means the frames
// have to be parsed rather than passed through: the cloud's filter fields name
// addresses, ports and a protocol, so honouring them requires reading the
// Ethernet, IP and transport headers off each frame. Everything here is
// platform-independent and unit-tested; the Linux-only socket that produces the
// frames lives in capture_linux.go.

// CaptureFilter selects frames by the five-tuple. Every field is optional; an
// empty field matches anything. Addresses accept a single address or a CIDR
// block, and ports accept a single port or an inclusive "from-to" range —
// the forms Azure Network Watcher's PacketCaptureFilter documents.
//
// "Local" is the captured interface's own side of the conversation and
// "remote" is the other end, matched in both directions: a filter naming a
// local port matches a frame whose source port is it AND a frame whose
// destination port is it, because both belong to the same conversation as seen
// from this interface.
type CaptureFilter struct {
	Protocol      string
	LocalAddress  string
	LocalPort     string
	RemoteAddress string
	RemotePort    string
}

// frameTuple is what a parsed frame contributes to filter matching. Ports are
// zero for a protocol that has none.
type frameTuple struct {
	protocol            string
	source, destination net.IP
	sourcePort          uint16
	destinationPort     uint16
	parsed              bool
}

const (
	ethernetHeaderLen = 14
	etherTypeIPv4     = 0x0800
	etherTypeIPv6     = 0x86DD
	etherTypeVLAN     = 0x8100
)

// parseFrameTuple reads the five-tuple out of an Ethernet frame. A frame whose
// headers it cannot read reports parsed=false, which callers treat as "cannot
// be excluded by a filter" — dropping unparseable traffic would silently hide
// it from a capture that asked for everything.
func parseFrameTuple(frame []byte) frameTuple {
	if len(frame) < ethernetHeaderLen {
		return frameTuple{}
	}
	etherType := binary.BigEndian.Uint16(frame[12:14])
	payload := frame[ethernetHeaderLen:]
	if etherType == etherTypeVLAN {
		// An 802.1Q tag inserts four bytes: two of tag control information and
		// two carrying the real EtherType.
		if len(payload) < 4 {
			return frameTuple{}
		}
		etherType = binary.BigEndian.Uint16(payload[2:4])
		payload = payload[4:]
	}
	switch etherType {
	case etherTypeIPv4:
		return parseIPv4Tuple(payload)
	case etherTypeIPv6:
		return parseIPv6Tuple(payload)
	}
	return frameTuple{}
}

func parseIPv4Tuple(b []byte) frameTuple {
	const minimumIPv4Header = 20
	if len(b) < minimumIPv4Header {
		return frameTuple{}
	}
	headerLen := int(b[0]&0x0f) * 4
	if headerLen < minimumIPv4Header || len(b) < headerLen {
		return frameTuple{}
	}
	t := frameTuple{
		protocol:    ipProtocolName(b[9]),
		source:      net.IP(b[12:16]),
		destination: net.IP(b[16:20]),
		parsed:      true,
	}
	// A fragment after the first carries no transport header, so its ports are
	// genuinely unknown rather than zero.
	fragmentOffset := binary.BigEndian.Uint16(b[6:8]) & 0x1fff
	if fragmentOffset == 0 {
		t.sourcePort, t.destinationPort = parseTransportPorts(b[9], b[headerLen:])
	}
	return t
}

func parseIPv6Tuple(b []byte) frameTuple {
	const ipv6HeaderLen = 40
	if len(b) < ipv6HeaderLen {
		return frameTuple{}
	}
	nextHeader := b[6]
	t := frameTuple{
		protocol:    ipProtocolName(nextHeader),
		source:      net.IP(b[8:24]),
		destination: net.IP(b[24:40]),
		parsed:      true,
	}
	t.sourcePort, t.destinationPort = parseTransportPorts(nextHeader, b[ipv6HeaderLen:])
	return t
}

func parseTransportPorts(protocol byte, b []byte) (uint16, uint16) {
	// TCP and UDP both open with a source and destination port.
	if protocol != 6 && protocol != 17 {
		return 0, 0
	}
	if len(b) < 4 {
		return 0, 0
	}
	return binary.BigEndian.Uint16(b[0:2]), binary.BigEndian.Uint16(b[2:4])
}

func ipProtocolName(p byte) string {
	switch p {
	case 6:
		return "TCP"
	case 17:
		return "UDP"
	case 1:
		return "ICMP"
	case 58:
		return "ICMPv6"
	}
	return ""
}

// MatchesAny reports whether the frame satisfies at least one filter, which is
// how a capture with several filters behaves: the filters are alternatives, and
// a capture with none captures everything.
func MatchesAny(frame []byte, filters []CaptureFilter) bool {
	if len(filters) == 0 {
		return true
	}
	tuple := parseFrameTuple(frame)
	for _, f := range filters {
		if tuple.matches(f) {
			return true
		}
	}
	return false
}

// isUnconstrained reports whether the filter narrows nothing, in which case it
// admits every frame — including one whose headers cannot be read.
func (f CaptureFilter) isUnconstrained() bool {
	protocolConstrains := f.Protocol != "" && !strings.EqualFold(f.Protocol, "Any")
	return !protocolConstrains && f.LocalAddress == "" && f.LocalPort == "" &&
		f.RemoteAddress == "" && f.RemotePort == ""
}

func (t frameTuple) matches(f CaptureFilter) bool {
	if f.isUnconstrained() {
		return true
	}
	// A frame whose headers could not be read cannot be shown to satisfy a
	// filter that narrows anything, so it is not captured. A capture filtered
	// to TCP recording the link's ARP traffic — which carries no five-tuple at
	// all — would be reporting frames the operator excluded.
	if !t.parsed {
		return false
	}
	if f.Protocol != "" && !strings.EqualFold(f.Protocol, "Any") &&
		!strings.EqualFold(f.Protocol, t.protocol) {
		return false
	}
	// Local and remote are matched in both directions: the captured interface
	// is one end of the conversation, and a frame belongs to it whether it is
	// inbound or outbound.
	if !addressMatchesEitherEnd(f.LocalAddress, t) || !addressMatchesEitherEnd(f.RemoteAddress, t) {
		return false
	}
	if !portMatchesEitherEnd(f.LocalPort, t) || !portMatchesEitherEnd(f.RemotePort, t) {
		return false
	}
	return true
}

func addressMatchesEitherEnd(spec string, t frameTuple) bool {
	if spec == "" {
		return true
	}
	for _, candidate := range strings.Split(spec, ";") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if addressMatches(candidate, t.source) || addressMatches(candidate, t.destination) {
			return true
		}
	}
	return false
}

func addressMatches(spec string, ip net.IP) bool {
	if ip == nil {
		return false
	}
	if strings.Contains(spec, "/") {
		_, block, err := net.ParseCIDR(spec)
		if err != nil {
			return false
		}
		return block.Contains(ip)
	}
	parsed := net.ParseIP(spec)
	return parsed != nil && parsed.Equal(ip)
}

func portMatchesEitherEnd(spec string, t frameTuple) bool {
	if spec == "" {
		return true
	}
	for _, candidate := range strings.Split(spec, ";") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if portMatches(candidate, t.sourcePort) || portMatches(candidate, t.destinationPort) {
			return true
		}
	}
	return false
}

func portMatches(spec string, port uint16) bool {
	if port == 0 {
		return false
	}
	from, to, isRange := strings.Cut(spec, "-")
	low, err := strconv.Atoi(strings.TrimSpace(from))
	if err != nil {
		return false
	}
	if !isRange {
		return int(port) == low
	}
	high, err := strconv.Atoi(strings.TrimSpace(to))
	if err != nil {
		return false
	}
	return int(port) >= low && int(port) <= high
}
