package main

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/e6qu/sockerless-cloud/sim/sidecar/gwlb"
)

// As `sidecar gwlb` the program is one zone's node of a Gateway Load
// Balancer. It sits on the load balancer's subnet at the load balancer's
// address and on each endpoint's network at the endpoint's address. What the
// route tables send to an endpoint arrives on that network's interface; a
// policy rule per subnet that routes through the endpoint sends it into the
// endpoint's TUN device, and the first endpoint on an interface takes what no
// subnet rule claims. The node wraps each packet in
// GENEVE with the endpoint, attachment and flow-cookie options of option
// class 0x0108 and sends it to the appliance the flow sticks to. What an
// appliance returns goes, unwrapped, back out through the TUN device, where a
// second rule routes it on as the endpoint's subnet routes it. It holds
// NET_ADMIN and the TUN device and nothing else.

// The tables and rule priorities the node programs, one pair per endpoint.
// Subnet rules precede an interface's catch-all.
const (
	gwlbCaptureTable = 100
	gwlbOnwardTable  = 200
	gwlbRulePriority = 1000
	gwlbCatchAll     = 1500
	gwlbOnwardOffset = 1000
	// A Gateway Load Balancer interface takes packets up to 8,500 bytes and
	// drops larger ones; the VPC around it carries 9001.
	gwlbMaxPacket     = 8500
	gwlbTunMTU        = 9001
	gwlbTunName       = "gwlb%d"
	gwlbFlowSweep     = 30 * time.Second
	gwlbStatePollWait = (gwlb.StateWaitSeconds + 10) * time.Second
)

// gwlbLink is one endpoint: the interface its traffic arrives on, its TUN
// device, the subnets whose rules send their traffic there, and whether it
// takes the interface's unclaimed traffic.
type gwlbLink struct {
	index    int
	name     string
	prefix   netip.Prefix
	endpoint string
	tun      *os.File
	tunIndex int
	onward   string
	primary  bool
	sources  []netip.Prefix
}

type gwlbFlow struct {
	target   string
	cookie   uint32
	link     *gwlbLink
	lastSeen time.Time
}

type gwlbNode struct {
	listen    netip.Addr
	conn      *net.UDPConn
	links     []*gwlbLink
	byID      map[string]*gwlbLink
	mu        sync.Mutex
	state     gwlb.State
	flows     map[string]*gwlbFlow
	byCookie  map[uint32]*gwlbFlow
	upstream  *url.URL
	token     string
	node      string
	stateOnce sync.Once
	stateSeen chan struct{}
}

func serveGWLB(ctx context.Context, out io.Writer) error {
	listen, err := netip.ParseAddr(os.Getenv(gwlb.EnvListen))
	if err != nil || !listen.Is4() {
		return fmt.Errorf("%s %q: an IPv4 address is required", gwlb.EnvListen, os.Getenv(gwlb.EnvListen))
	}
	upstream, err := url.Parse(os.Getenv("SIDECAR_HTTP_UPSTREAM"))
	if err != nil || upstream.Host == "" {
		return fmt.Errorf("SIDECAR_HTTP_UPSTREAM %q is not an http URL", os.Getenv("SIDECAR_HTTP_UPSTREAM"))
	}
	n := &gwlbNode{
		listen:    listen,
		byID:      map[string]*gwlbLink{},
		flows:     map[string]*gwlbFlow{},
		byCookie:  map[uint32]*gwlbFlow{},
		upstream:  upstream,
		token:     os.Getenv("SIDECAR_TOKEN"),
		node:      os.Getenv(gwlb.EnvNode),
		stateSeen: make(chan struct{}),
	}
	if n.token == "" || n.node == "" {
		return errors.New("a Gateway Load Balancer node needs SIDECAR_TOKEN and " + gwlb.EnvNode)
	}
	if err := n.setUpLinks(os.Getenv(gwlb.EnvEndpoints)); err != nil {
		return err
	}
	conn, err := net.ListenUDP("udp4", net.UDPAddrFromAddrPort(netip.AddrPortFrom(listen, gwlb.Port)))
	if err != nil {
		return fmt.Errorf("listen for GENEVE at %s: %w", listen, err)
	}
	defer func() { _ = conn.Close() }()
	// A Gateway Load Balancer never fragments: its 8,500-byte packets and 68
	// bytes of GENEVE fit the VPC's 9001-byte frames whole.
	if raw, err := conn.SyscallConn(); err == nil {
		_ = raw.Control(func(fd uintptr) {
			_ = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_DO)
		})
	}
	n.conn = conn

	go n.followState(ctx, out)
	select {
	case <-n.stateSeen:
	case <-ctx.Done():
		return nil
	}
	for _, link := range n.links {
		go n.readLink(ctx, link)
	}
	go n.readGeneve(ctx)
	go n.sweepFlows(ctx)
	fmt.Fprintf(out, "%s%s\n", gwlb.ServingPrefix, listen)
	<-ctx.Done()
	return nil
}

// setUpLinks finds the interface each endpoint's address is on, adding the
// address when the engine left it off (a second address on one network), and
// gives each endpoint network its TUN device, tables and rules.
func (n *gwlbNode) setUpLinks(spec string) error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return err
	}
	for _, item := range strings.Split(spec, ",") {
		if item == "" {
			continue
		}
		id, addrText, ok := strings.Cut(item, "=")
		prefix, err := netip.ParsePrefix(addrText)
		if !ok || err != nil || !prefix.Addr().Is4() {
			return fmt.Errorf("%s item %q: want <vpce-id>=<IPv4 address>/<bits>", gwlb.EnvEndpoints, item)
		}
		iface, onIt, err := gwlbInterfaceFor(ifaces, prefix)
		if err != nil {
			return fmt.Errorf("endpoint %s: %w", id, err)
		}
		if !onIt {
			if err := routeRequest(prefixAddressMessage(iface.Index, prefix)); err != nil && !errors.Is(err, unix.EEXIST) {
				return fmt.Errorf("endpoint %s: add %s to %s: %w", id, prefix, iface.Name, err)
			}
		}
		primary := true
		for _, existing := range n.links {
			if existing.index == iface.Index {
				primary = false
			}
		}
		link := &gwlbLink{index: iface.Index, name: iface.Name, prefix: prefix.Masked(), endpoint: id, primary: primary}
		if err := n.programLink(len(n.links), link); err != nil {
			return fmt.Errorf("endpoint %s: %w", id, err)
		}
		n.links = append(n.links, link)
		n.byID[id] = link
	}
	if len(n.links) == 0 {
		return errors.New("a Gateway Load Balancer node needs an endpoint")
	}
	return nil
}

// gwlbInterfaceFor is the interface whose network holds prefix's address,
// and whether the address is already on it.
func gwlbInterfaceFor(ifaces []net.Interface, prefix netip.Prefix) (net.Interface, bool, error) {
	for _, iface := range ifaces {
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			have, ok := netip.AddrFromSlice(ipnet.IP.To4())
			if !ok {
				continue
			}
			bits, _ := ipnet.Mask.Size()
			if netip.PrefixFrom(have, bits).Contains(prefix.Addr()) {
				return iface, have == prefix.Addr(), nil
			}
		}
	}
	return net.Interface{}, false, fmt.Errorf("no interface is on the network of %s", prefix)
}

// programLink creates the link's TUN device and routes what arrives on the
// link into it, and what comes back out of it onward through the link.
func (n *gwlbNode) programLink(k int, link *gwlbLink) error {
	tun, name, err := openTun(fmt.Sprintf(gwlbTunName, k))
	if err != nil {
		return err
	}
	tunIface, err := net.InterfaceByName(name)
	if err != nil {
		return err
	}
	link.tun, link.tunIndex = tun, tunIface.Index
	if err := routeRequest(linkUpMessage(link.tunIndex, gwlbTunMTU)); err != nil {
		return fmt.Errorf("bring %s up: %w", name, err)
	}
	capture, onward := uint32(gwlbCaptureTable+k), uint32(gwlbOnwardTable+k)
	steps := []struct {
		what string
		msg  []byte
	}{
		{"route captured traffic into " + name, tableRouteMessage(capture, netip.Prefix{}, netip.Addr{}, link.tunIndex, unix.RT_SCOPE_LINK)},
		{"route " + link.prefix.String() + " on " + link.name, tableRouteMessage(onward, link.prefix, netip.Addr{}, link.index, unix.RT_SCOPE_LINK)},
		{"route on what leaves " + name, ruleMessage(unix.RTM_NEWRULE, name, netip.Prefix{}, onward, uint32(gwlbRulePriority+gwlbOnwardOffset+k))},
	}
	if link.primary {
		steps = append(steps, struct {
			what string
			msg  []byte
		}{"capture what arrives on " + link.name, ruleMessage(unix.RTM_NEWRULE, link.name, netip.Prefix{}, capture, uint32(gwlbCatchAll+k))})
	}
	for _, step := range steps {
		if err := routeRequest(step.msg); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("%s: %w", step.what, err)
		}
	}
	return nil
}

// setOnward points the link's onward table's default route at the next hop
// the endpoint's subnet names, or removes it when the subnet names none.
func (n *gwlbNode) setOnward(k int, link *gwlbLink, onward string) error {
	if link.onward == onward {
		return nil
	}
	table := uint32(gwlbOnwardTable + k)
	err := routeRequest(tableRouteDelete(table))
	if err != nil && !errors.Is(err, unix.ESRCH) {
		return fmt.Errorf("clear the onward route of %s: %w", link.name, err)
	}
	link.onward = ""
	if onward == "" {
		return nil
	}
	via, err := netip.ParseAddr(onward)
	if err != nil || !via.Is4() {
		return fmt.Errorf("onward next hop %q: an IPv4 address is required", onward)
	}
	if err := routeRequest(tableRouteMessage(table, netip.Prefix{}, via, link.index, unix.RT_SCOPE_UNIVERSE)); err != nil {
		return fmt.Errorf("route %s onward through %s: %w", link.name, via, err)
	}
	link.onward = onward
	return nil
}

// setSources points a rule at the endpoint's capture table for each subnet
// whose route table sends its traffic through the endpoint, and removes the
// rules of subnets that no longer do.
func (n *gwlbNode) setSources(k int, link *gwlbLink, sources []string) error {
	var want []netip.Prefix
	for _, text := range sources {
		p, err := netip.ParsePrefix(text)
		if err != nil || !p.Addr().Is4() {
			continue
		}
		want = append(want, p.Masked())
	}
	capture, priority := uint32(gwlbCaptureTable+k), uint32(gwlbRulePriority+k)
	for _, have := range link.sources {
		if slices.Contains(want, have) {
			continue
		}
		if err := routeRequest(ruleMessage(unix.RTM_DELRULE, link.name, have, capture, priority)); err != nil && !errors.Is(err, unix.ENOENT) {
			return fmt.Errorf("stop capturing %s for %s: %w", have, link.endpoint, err)
		}
	}
	for _, p := range want {
		if slices.Contains(link.sources, p) {
			continue
		}
		if err := routeRequest(ruleMessage(unix.RTM_NEWRULE, link.name, p, capture, priority)); err != nil && !errors.Is(err, unix.EEXIST) {
			return fmt.Errorf("capture %s for %s: %w", p, link.endpoint, err)
		}
	}
	link.sources = want
	return nil
}

// followState long-polls the simulator for the node's state.
func (n *gwlbNode) followState(ctx context.Context, out io.Writer) {
	client := &http.Client{Timeout: gwlbStatePollWait}
	version := ""
	for ctx.Err() == nil {
		state, err := n.fetchState(ctx, client, version)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Fprintf(out, "sidecar: gwlb state: %v\n", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		version = state.Version
		n.apply(state, out)
		n.stateOnce.Do(func() { close(n.stateSeen) })
	}
}

func (n *gwlbNode) fetchState(ctx context.Context, client *http.Client, version string) (gwlb.State, error) {
	u := *n.upstream
	u.Path = gwlb.StatePath
	u.RawQuery = url.Values{"node": {n.node}, "version": {version}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return gwlb.State{}, err
	}
	req.Header.Set(tokenHeader, n.token)
	resp, err := client.Do(req)
	if err != nil {
		return gwlb.State{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return gwlb.State{}, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var state gwlb.State
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		return gwlb.State{}, err
	}
	return state, nil
}

func (n *gwlbNode) apply(state gwlb.State, out io.Writer) {
	n.mu.Lock()
	n.state = state
	n.mu.Unlock()
	for _, endpoint := range state.Endpoints {
		link, ok := n.byID[endpoint.ID]
		if !ok || link.endpoint != endpoint.ID {
			continue
		}
		k := slices.Index(n.links, link)
		if err := n.setOnward(k, link, endpoint.Onward); err != nil {
			fmt.Fprintf(out, "sidecar: gwlb: %v\n", err)
		}
		if err := n.setSources(k, link, endpoint.Sources); err != nil {
			fmt.Fprintf(out, "sidecar: gwlb: %v\n", err)
		}
	}
}

// readLink takes what the capture rule routes into the link's TUN device and
// sends each packet to its flow's appliance.
func (n *gwlbNode) readLink(ctx context.Context, link *gwlbLink) {
	buf := make([]byte, 65535)
	for ctx.Err() == nil {
		size, err := link.tun.Read(buf)
		if err != nil {
			return
		}
		packet := buf[:size]
		if size > gwlbMaxPacket {
			continue
		}
		flow := n.flowFor(link, packet)
		if flow == nil {
			continue
		}
		datagram := n.encapsulate(link, flow, packet)
		target, err := netip.ParseAddr(flow.target)
		if err != nil {
			continue
		}
		_, _ = n.conn.WriteToUDPAddrPort(datagram, netip.AddrPortFrom(target, gwlb.Port))
	}
}

// flowFor is the flow a packet belongs to, made on its first packet: the
// stickiness picks a healthy target by hashing the flow's addresses (and
// protocol and ports, as the stickiness takes them) in both directions alike.
// With no healthy target the flow goes to a target picked at random; with no
// target at all the packet is dropped.
func (n *gwlbNode) flowFor(link *gwlbLink, packet []byte) *gwlbFlow {
	n.mu.Lock()
	defer n.mu.Unlock()
	key, ok := gwlbFlowKey(n.state.Stickiness, packet)
	if !ok {
		return nil
	}
	now := time.Now()
	if flow, ok := n.flows[key]; ok {
		if !n.state.Rebalance || n.targetHealthy(flow.target) {
			flow.lastSeen = now
			return flow
		}
	}
	target := n.pickTarget(key)
	if target == "" {
		return nil
	}
	flow := &gwlbFlow{target: target, cookie: rand.Uint32(), link: link, lastSeen: now}
	for n.byCookie[flow.cookie] != nil {
		flow.cookie = rand.Uint32()
	}
	if old := n.flows[key]; old != nil {
		delete(n.byCookie, old.cookie)
	}
	n.flows[key] = flow
	n.byCookie[flow.cookie] = flow
	return flow
}

func (n *gwlbNode) targetHealthy(address string) bool {
	for _, t := range n.state.Targets {
		if t.Address == address {
			return t.Healthy
		}
	}
	return false
}

func (n *gwlbNode) pickTarget(key string) string {
	var healthy, all []string
	for _, t := range n.state.Targets {
		all = append(all, t.Address)
		if t.Healthy {
			healthy = append(healthy, t.Address)
		}
	}
	if len(all) == 0 {
		return ""
	}
	if len(healthy) == 0 {
		return all[rand.IntN(len(all))]
	}
	slices.Sort(healthy)
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return healthy[h.Sum64()%uint64(len(healthy))]
}

// gwlbFlowKey is the part of an IPv4 packet the stickiness keys a flow on,
// the same for a packet and its reply.
func gwlbFlowKey(stickiness string, packet []byte) (string, bool) {
	if len(packet) < 20 || packet[0]>>4 != 4 {
		return "", false
	}
	headerLen := int(packet[0]&0x0f) * 4
	if headerLen < 20 || len(packet) < headerLen {
		return "", false
	}
	proto := packet[9]
	src, _ := netip.AddrFromSlice(packet[12:16])
	dst, _ := netip.AddrFromSlice(packet[16:20])
	var sport, dport uint16
	if (proto == unix.IPPROTO_TCP || proto == unix.IPPROTO_UDP) && len(packet) >= headerLen+4 {
		sport = binary.BigEndian.Uint16(packet[headerLen:])
		dport = binary.BigEndian.Uint16(packet[headerLen+2:])
	}
	a, b := netip.AddrPortFrom(src, sport), netip.AddrPortFrom(dst, dport)
	if b.Compare(a) < 0 {
		a, b = b, a
	}
	switch stickiness {
	case gwlb.SourceIPDestIP:
		lo, hi := a.Addr(), b.Addr()
		if hi.Less(lo) {
			lo, hi = hi, lo
		}
		return lo.String() + "|" + hi.String(), true
	case gwlb.SourceIPDestIPProto:
		lo, hi := a.Addr(), b.Addr()
		if hi.Less(lo) {
			lo, hi = hi, lo
		}
		return lo.String() + "|" + hi.String() + "|" + strconv.Itoa(int(proto)), true
	}
	return a.String() + "|" + b.String() + "|" + strconv.Itoa(int(proto)), true
}

// encapsulate is the GENEVE datagram carrying packet: the header with the
// IPv4 protocol type, then options of class 0x0108 — type 1 the endpoint's
// identifier, type 2 the attachment, type 3 the flow cookie — then the packet.
func (n *gwlbNode) encapsulate(link *gwlbLink, flow *gwlbFlow, packet []byte) []byte {
	n.mu.Lock()
	var attachment uint64
	for _, e := range n.state.Endpoints {
		if e.ID == link.endpoint {
			attachment = e.Attachment
		}
	}
	n.mu.Unlock()
	endpointID, _ := strconv.ParseUint(strings.TrimPrefix(link.endpoint, "vpce-"), 16, 64)
	const optionsLen = (4 + 8) + (4 + 8) + (4 + 4)
	b := make([]byte, 8+optionsLen, 8+optionsLen+len(packet))
	b[0] = optionsLen / 4
	binary.BigEndian.PutUint16(b[2:], unix.ETH_P_IP)
	off := 8
	option := func(typ byte, value []byte) {
		binary.BigEndian.PutUint16(b[off:], gwlb.OptionClass)
		b[off+2] = typ
		b[off+3] = byte(len(value) / 4)
		copy(b[off+4:], value)
		off += 4 + len(value)
	}
	option(gwlb.OptionEndpoint, binary.BigEndian.AppendUint64(nil, endpointID))
	option(gwlb.OptionAttachment, binary.BigEndian.AppendUint64(nil, attachment))
	option(gwlb.OptionFlowCookie, binary.BigEndian.AppendUint32(nil, flow.cookie))
	return append(b, packet...)
}

// readGeneve takes what the appliances return and writes the packet inside
// out through the TUN device of the flow its cookie names, or of the
// endpoint its options name.
func (n *gwlbNode) readGeneve(ctx context.Context) {
	buf := make([]byte, 65535)
	for ctx.Err() == nil {
		size, from, err := n.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return
		}
		if !n.isTarget(from.Addr().Unmap().String()) {
			continue
		}
		endpointID, cookie, inner, ok := gwlbDecapsulate(buf[:size])
		if !ok {
			continue
		}
		link := n.linkFor(endpointID, cookie)
		if link == nil {
			continue
		}
		_, _ = link.tun.Write(inner)
	}
}

func (n *gwlbNode) isTarget(address string) bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, t := range n.state.Targets {
		if t.Address == address {
			return true
		}
	}
	return false
}

func (n *gwlbNode) linkFor(endpointID uint64, cookie uint32) *gwlbLink {
	n.mu.Lock()
	defer n.mu.Unlock()
	if flow, ok := n.byCookie[cookie]; ok {
		flow.lastSeen = time.Now()
		return flow.link
	}
	for id, link := range n.byID {
		if v, err := strconv.ParseUint(strings.TrimPrefix(id, "vpce-"), 16, 64); err == nil && v == endpointID {
			return link
		}
	}
	return nil
}

// gwlbDecapsulate reads a GENEVE datagram: the endpoint and flow cookie its
// class 0x0108 options carry, and the IPv4 packet after its options.
func gwlbDecapsulate(d []byte) (endpoint uint64, cookie uint32, inner []byte, ok bool) {
	if len(d) < 8 || d[0]&0xc0 != 0 || binary.BigEndian.Uint16(d[2:]) != unix.ETH_P_IP {
		return 0, 0, nil, false
	}
	end := 8 + int(d[0]&0x3f)*4
	if end > len(d) {
		return 0, 0, nil, false
	}
	for off := 8; end-off >= 4; {
		class := binary.BigEndian.Uint16(d[off:])
		typ := d[off+2]
		length := int(d[off+3]&0x1f) * 4
		if end-off-4 < length {
			break
		}
		value := d[off+4 : off+4+length]
		if class == gwlb.OptionClass {
			switch {
			case typ == gwlb.OptionEndpoint && length == 8:
				endpoint = binary.BigEndian.Uint64(value)
			case typ == gwlb.OptionFlowCookie && length == 4:
				cookie = binary.BigEndian.Uint32(value)
			}
		}
		off += 4 + length
	}
	return endpoint, cookie, d[end:], len(d) > end
}

// sweepFlows forgets flows idle past the load balancer's idle timeout.
func (n *gwlbNode) sweepFlows(ctx context.Context) {
	ticker := time.NewTicker(gwlbFlowSweep)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			n.mu.Lock()
			idle := time.Duration(n.state.IdleTimeS) * time.Second
			for key, flow := range n.flows {
				if now.Sub(flow.lastSeen) > idle {
					delete(n.flows, key)
					delete(n.byCookie, flow.cookie)
				}
			}
			n.mu.Unlock()
		}
	}
}

// openTun creates a TUN device carrying bare IP packets.
func openTun(name string) (*os.File, string, error) {
	fd, err := unix.Open("/dev/net/tun", unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, "", fmt.Errorf("open /dev/net/tun: %w", err)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		_ = unix.Close(fd)
		return nil, "", err
	}
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		_ = unix.Close(fd)
		return nil, "", fmt.Errorf("create %s: %w", name, err)
	}
	return os.NewFile(uintptr(fd), "/dev/net/tun"), ifr.Name(), nil
}

// prefixAddressMessage is RTM_NEWADDR for the prefix's address with its
// network's length on the interface.
func prefixAddressMessage(index int, prefix netip.Prefix) []byte {
	ifa := make([]byte, unix.SizeofIfAddrmsg)
	ifa[0] = unix.AF_INET
	ifa[1] = byte(prefix.Bits())
	ifa[3] = unix.RT_SCOPE_UNIVERSE
	binary.NativeEndian.PutUint32(ifa[4:], uint32(index))
	a := prefix.Addr().As4()
	ifa = append(ifa, rtattr(unix.IFA_LOCAL, a[:])...)
	ifa = append(ifa, rtattr(unix.IFA_ADDRESS, a[:])...)
	return nlmsg(unix.RTM_NEWADDR, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_EXCL|unix.NLM_F_ACK, ifa)
}

// linkUpMessage is RTM_NEWLINK setting IFF_UP and the MTU on the interface.
func linkUpMessage(index, mtu int) []byte {
	ifi := make([]byte, unix.SizeofIfInfomsg)
	ifi[0] = unix.AF_UNSPEC
	binary.NativeEndian.PutUint32(ifi[4:], uint32(index))
	binary.NativeEndian.PutUint32(ifi[8:], unix.IFF_UP)
	binary.NativeEndian.PutUint32(ifi[12:], unix.IFF_UP)
	ifi = append(ifi, rtattr(unix.IFLA_MTU, binary.NativeEndian.AppendUint32(nil, uint32(mtu)))...)
	return nlmsg(unix.RTM_NEWLINK, unix.NLM_F_REQUEST|unix.NLM_F_ACK, ifi)
}

// tableRouteMessage is RTM_NEWROUTE in table to destination (the default
// route for the zero prefix) out of the interface, through via when valid.
func tableRouteMessage(table uint32, destination netip.Prefix, via netip.Addr, oif int, scope byte) []byte {
	rt := make([]byte, unix.SizeofRtMsg)
	rt[0] = unix.AF_INET
	rt[4] = unix.RT_TABLE_UNSPEC
	rt[5] = unix.RTPROT_BOOT
	rt[6] = scope
	rt[7] = unix.RTN_UNICAST
	if destination.IsValid() {
		rt[1] = byte(destination.Bits())
		d := destination.Addr().As4()
		rt = append(rt, rtattr(unix.RTA_DST, d[:])...)
	}
	if via.IsValid() {
		g := via.As4()
		rt = append(rt, rtattr(unix.RTA_GATEWAY, g[:])...)
	}
	rt = append(rt, rtattr(unix.RTA_OIF, binary.NativeEndian.AppendUint32(nil, uint32(oif)))...)
	rt = append(rt, rtattr(unix.RTA_TABLE, binary.NativeEndian.AppendUint32(nil, table))...)
	return nlmsg(unix.RTM_NEWROUTE, unix.NLM_F_REQUEST|unix.NLM_F_CREATE|unix.NLM_F_REPLACE|unix.NLM_F_ACK, rt)
}

// tableRouteDelete is RTM_DELROUTE of the default route in table.
func tableRouteDelete(table uint32) []byte {
	rt := make([]byte, unix.SizeofRtMsg)
	rt[0] = unix.AF_INET
	rt[4] = unix.RT_TABLE_UNSPEC
	rt[6] = unix.RT_SCOPE_NOWHERE
	rt = append(rt, rtattr(unix.RTA_TABLE, binary.NativeEndian.AppendUint32(nil, table))...)
	return nlmsg(unix.RTM_DELROUTE, unix.NLM_F_REQUEST|unix.NLM_F_ACK, rt)
}

// ruleMessage is RTM_NEWRULE or RTM_DELRULE for the rule sending what
// arrives on the interface, from source when it is valid, to table.
func ruleMessage(typ uint16, iif string, source netip.Prefix, table, priority uint32) []byte {
	// struct fib_rule_hdr: family, dst_len, src_len, tos, table, res1, res2,
	// action, flags.
	hdr := make([]byte, 12)
	hdr[0] = unix.AF_INET
	hdr[7] = unix.FR_ACT_TO_TBL
	if source.IsValid() {
		hdr[2] = byte(source.Bits())
		a := source.Addr().As4()
		hdr = append(hdr, rtattr(unix.FRA_SRC, a[:])...)
	}
	hdr = append(hdr, rtattr(unix.FRA_IIFNAME, append([]byte(iif), 0))...)
	hdr = append(hdr, rtattr(unix.FRA_TABLE, binary.NativeEndian.AppendUint32(nil, table))...)
	hdr = append(hdr, rtattr(unix.FRA_PRIORITY, binary.NativeEndian.AppendUint32(nil, priority))...)
	flags := uint16(unix.NLM_F_REQUEST | unix.NLM_F_ACK)
	if typ == unix.RTM_NEWRULE {
		flags |= unix.NLM_F_CREATE | unix.NLM_F_EXCL
	}
	return nlmsg(typ, flags, hdr)
}
