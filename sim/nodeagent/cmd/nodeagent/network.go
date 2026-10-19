package main

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// errNetworkInUse refuses to release a network's bridge while a guest's tap
// is still on it.
var errNetworkInUse = errors.New("a guest is still on that network")

// networks turns each engine-network interface the agent's container holds
// into a bridge its guests' taps join: the interface's address moves onto the
// bridge, so the agent stays reachable at it, and a guest on a tap is one more
// member of the engine network at the layer below addresses.
type networks struct {
	mu      sync.Mutex
	bridges map[string]string // agent address -> bridge
	taps    map[string]string // tap -> bridge
	// detached closes, and is replaced, each time a tap leaves a bridge, so
	// a release waiting for a network's last guest wakes on it.
	detached chan struct{}
}

func newNetworks() *networks {
	return &networks{bridges: map[string]string{}, taps: map[string]string{}, detached: make(chan struct{})}
}

// bridgeFor returns the bridge carrying the interface that holds the agent's
// address, building it the first time.
func (n *networks) bridgeFor(ctx context.Context, agentAddress string) (string, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	if br, ok := n.bridges[agentAddress]; ok {
		return br, nil
	}
	iface, prefix, err := interfaceHolding(agentAddress)
	if err != nil {
		return "", err
	}
	br := shortName("br", agentAddress)
	// Flushing the interface's address drops the routes through it, the
	// default route among them when it leaves through this interface; that
	// one comes back on the bridge, and a default route through another
	// interface is left where it is.
	routes, err := output(ctx, "ip", "route", "show", "default")
	if err != nil {
		return "", err
	}
	gw := defaultRouteVia(routes, iface)
	steps := [][]string{
		{"ip", "link", "add", br, "type", "bridge"},
		{"ip", "link", "set", br, "up"},
		{"ip", "addr", "flush", "dev", iface},
		{"ip", "link", "set", iface, "master", br},
		{"ip", "addr", "add", prefix.String(), "dev", br},
	}
	if gw.IsValid() {
		steps = append(steps, []string{"ip", "route", "replace", "default", "via", gw.String(), "dev", br})
	}
	for _, step := range steps {
		if err := run(ctx, step...); err != nil {
			return "", err
		}
	}
	n.bridges[agentAddress] = br
	return br, nil
}

// release dissolves the bridge built for the network the agent holds
// agentAddress on, ahead of the container leaving that network: the engine
// removes the interface with the endpoint, and a later join of a network at
// the same address builds a bridge for that one. A tap still on the bridge
// is a guest on its way out — a cloud deletes a network once nothing of its
// own is on it, and a stopped guest's tap goes with its machine a moment
// after its process ended — so the release waits for the last tap as the
// engine waits for the last endpoint, as long as ctx allows. The bridge
// carrying the agent's default route is the network the agent started on,
// which is not released.
func (n *networks) release(ctx context.Context, agentAddress string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	br, ok := n.bridges[agentAddress]
	if !ok {
		return nil
	}
	for {
		var on []string
		for tap, bridge := range n.taps {
			if bridge == br {
				on = append(on, tap)
			}
		}
		if len(on) == 0 {
			break
		}
		detached := n.detached
		n.mu.Unlock()
		select {
		case <-detached:
			n.mu.Lock()
		case <-ctx.Done():
			n.mu.Lock()
			return fmt.Errorf("%w: %s on %s", errNetworkInUse, strings.Join(on, ", "), br)
		}
	}
	if _, err := output(ctx, "ip", "link", "show", br); err != nil {
		if strings.Contains(err.Error(), "does not exist") {
			delete(n.bridges, agentAddress)
			return nil
		}
		return err
	}
	routes, err := output(ctx, "ip", "route", "show", "default")
	if err != nil {
		return err
	}
	if defaultRouteVia(routes, br).IsValid() {
		return fmt.Errorf("%s carries the agent's default route: the network the agent started on is not left", br)
	}
	if err := run(ctx, "ip", "link", "del", br); err != nil {
		return err
	}
	delete(n.bridges, agentAddress)
	return nil
}

// defaultRouteVia is the gateway of the default route that leaves through
// iface, read from `ip route show default`, or the zero value when no
// default route leaves through it.
func defaultRouteVia(routes, iface string) netip.Addr {
	for _, line := range strings.Split(routes, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || fields[0] != "default" || fields[1] != "via" {
			continue
		}
		for i := 3; i+1 < len(fields); i++ {
			if fields[i] == "dev" && fields[i+1] == iface {
				if gw, err := netip.ParseAddr(fields[2]); err == nil {
					return gw
				}
			}
		}
	}
	return netip.Addr{}
}

// attachTap creates the guest's tap on the bridge of its network and returns
// its name.
func (n *networks) attachTap(ctx context.Context, guestID, agentAddress string) (string, error) {
	br, err := n.bridgeFor(ctx, agentAddress)
	if err != nil {
		return "", err
	}
	tap := shortName("tap", guestID)
	_ = run(ctx, "ip", "link", "del", tap)
	for _, step := range [][]string{
		{"ip", "tuntap", "add", "dev", tap, "mode", "tap"},
		{"ip", "link", "set", tap, "master", br},
		{"ip", "link", "set", tap, "up"},
	} {
		if err := run(ctx, step...); err != nil {
			_ = run(ctx, "ip", "link", "del", tap)
			return "", err
		}
	}
	n.mu.Lock()
	n.taps[tap] = br
	n.mu.Unlock()
	return tap, nil
}

func (n *networks) detachTap(tap string) {
	_ = run(context.Background(), "nft", "delete", "table", "bridge", nftTable(tap))
	_ = run(context.Background(), "ip", "link", "del", tap)
	n.mu.Lock()
	delete(n.taps, tap)
	close(n.detached)
	n.detached = make(chan struct{})
	n.mu.Unlock()
}

// applyIngress programs the guest's ingress rules at its tap, in the bridge
// family the tap's frames traverse: what the rules admit reaches the guest,
// return traffic of what it started always does, and nothing else.
func (n *networks) applyIngress(ctx context.Context, tap string, stages [][]packetRule) error {
	program, err := renderIngressProgram(nftTable(tap), tap, stages)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "nft", "-f", "-")
	cmd.Stdin = strings.NewReader(program)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("nft: %w: %s\n%s", err, strings.TrimSpace(string(out)), program)
	}
	return nil
}

func nftTable(tap string) string { return "sockerless_" + tap }

// renderIngressProgram writes an nftables program that replaces the table:
// with no stages every frame passes, as a guest with nothing attached
// receives everything the network sends; with stages a frame goes through
// each stage's chain in turn, where the first matching rule decides, an
// accept returns it to the next stage and nothing matched drops it, and a
// frame every stage returned is accepted. A group of groups on one
// interface, or a subnet's group before a NIC's, is that many stages.
func renderIngressProgram(table, tap string, stages [][]packetRule) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "table bridge %s {}\n", table)
	fmt.Fprintf(&b, "delete table bridge %s\n", table)
	fmt.Fprintf(&b, "table bridge %s {\n", table)
	for i, rules := range stages {
		fmt.Fprintf(&b, "\tchain stage%d {\n", i)
		for _, rule := range rules {
			expr, err := renderIngressRule(rule)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\t\t%s\n", expr)
		}
		fmt.Fprintf(&b, "\t\tdrop\n")
		fmt.Fprintf(&b, "\t}\n")
	}
	fmt.Fprintf(&b, "\tchain forward {\n")
	fmt.Fprintf(&b, "\t\ttype filter hook forward priority filter; policy accept;\n")
	if len(stages) > 0 {
		fmt.Fprintf(&b, "\t\toifname %q ether type arp accept\n", tap)
		fmt.Fprintf(&b, "\t\toifname %q ct state established,related accept\n", tap)
		for i := range stages {
			fmt.Fprintf(&b, "\t\toifname %q jump stage%d\n", tap, i)
		}
		fmt.Fprintf(&b, "\t\toifname %q accept\n", tap)
	}
	fmt.Fprintf(&b, "\t}\n}\n")
	return b.String(), nil
}

// renderIngressRule is one rule of a stage's chain: an accept returns the
// frame to the forward chain, which hands it to the next stage.
func renderIngressRule(rule packetRule) (string, error) {
	var parts []string
	if rule.SourceCIDR != "" {
		parts = append(parts, "ip", "saddr", rule.SourceCIDR)
	}
	proto, err := packetProtocol(rule.Protocol)
	if err != nil {
		return "", err
	}
	switch proto {
	case "tcp", "udp", "sctp":
		parts = append(parts, "ip", "protocol", proto, proto)
		if rule.FromPort > 0 || rule.ToPort > 0 {
			from, to := rule.FromPort, rule.ToPort
			if from == 0 {
				from = to
			}
			if to == 0 {
				to = from
			}
			if from == to {
				parts = append(parts, "dport", strconv.Itoa(from))
			} else {
				parts = append(parts, "dport", fmt.Sprintf("%d-%d", from, to))
			}
		}
	case "":
	default:
		parts = append(parts, "ip", "protocol", proto)
	}
	switch action := strings.ToLower(rule.Action); action {
	case "", "accept":
		parts = append(parts, "return")
	case "drop":
		parts = append(parts, "drop")
	default:
		return "", fmt.Errorf("packet rule action %q is not accept or drop", rule.Action)
	}
	if len(parts) == 1 {
		// A rule matching everything is the chain's verdict.
		return parts[0], nil
	}
	return strings.Join(parts, " "), nil
}

func packetProtocol(protocol string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(protocol))
	switch p {
	case "", "-1", "all", "*":
		return "", nil
	case "tcp", "udp", "sctp", "icmp", "esp", "ah", "gre", "ipip":
		return p, nil
	}
	if n, err := strconv.Atoi(p); err == nil && n >= 0 && n <= 255 {
		switch n {
		case 1:
			return "icmp", nil
		case 6:
			return "tcp", nil
		case 17:
			return "udp", nil
		case 132:
			return "sctp", nil
		}
		return p, nil
	}
	return "", fmt.Errorf("packet rule protocol %q is not one the filter knows", protocol)
}

// interfaceHolding finds the interface that carries the address, which is how
// the agent knows which engine network a guest belongs on.
func interfaceHolding(address string) (string, netip.Prefix, error) {
	want, err := netip.ParseAddr(address)
	if err != nil {
		return "", netip.Prefix{}, err
	}
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", netip.Prefix{}, err
	}
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
			ip, ok := netip.AddrFromSlice(ipnet.IP.To4())
			if !ok || ip != want {
				continue
			}
			ones, _ := ipnet.Mask.Size()
			return iface.Name, netip.PrefixFrom(ip, ones), nil
		}
	}
	return "", netip.Prefix{}, fmt.Errorf("no interface holds %s: the agent's container is not on that network", address)
}

// defaultGatewayFor is the first host of the agent's own range, where the
// engine's bridge answers; the agent keeps its route out through it after its
// interface moved onto the guests' bridge.
func shortName(prefix, id string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(id))
	name := prefix + strconv.FormatUint(h.Sum64(), 36)
	if len(name) > 15 {
		name = name[:15]
	}
	return name
}

func output(ctx context.Context, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func run(ctx context.Context, args ...string) error {
	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
