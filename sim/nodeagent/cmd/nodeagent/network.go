package main

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// networks turns each engine-network interface the agent's container holds
// into a bridge its guests' taps join: the interface's address moves onto the
// bridge, so the agent stays reachable at it, and a guest on a tap is one more
// member of the engine network at the layer below addresses.
type networks struct {
	mu      sync.Mutex
	bridges map[string]string // agent address -> bridge
}

func newNetworks() *networks { return &networks{bridges: map[string]string{}} }

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
	return tap, nil
}

func (n *networks) detachTap(tap string) {
	_ = run(context.Background(), "nft", "delete", "table", "bridge", nftTable(tap))
	_ = run(context.Background(), "ip", "link", "del", tap)
}

// applyIngress programs the guest's ingress rules at its tap, in the bridge
// family the tap's frames traverse: what the rules admit reaches the guest,
// return traffic of what it started always does, and nothing else.
func (n *networks) applyIngress(ctx context.Context, tap string, rules []packetRule) error {
	program, err := renderIngressProgram(nftTable(tap), tap, rules)
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
// with no rules every frame passes, as a guest with nothing attached receives
// everything the network sends; with rules the first match decides and the
// rest is dropped.
func renderIngressProgram(table, tap string, rules []packetRule) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "table bridge %s {}\n", table)
	fmt.Fprintf(&b, "delete table bridge %s\n", table)
	fmt.Fprintf(&b, "table bridge %s {\n", table)
	fmt.Fprintf(&b, "\tchain forward {\n")
	fmt.Fprintf(&b, "\t\ttype filter hook forward priority filter; policy accept;\n")
	if len(rules) > 0 {
		fmt.Fprintf(&b, "\t\toifname %q ether type arp accept\n", tap)
		fmt.Fprintf(&b, "\t\toifname %q ct state established,related accept\n", tap)
		for _, rule := range rules {
			expr, err := renderIngressRule(tap, rule)
			if err != nil {
				return "", err
			}
			fmt.Fprintf(&b, "\t\t%s\n", expr)
		}
		fmt.Fprintf(&b, "\t\toifname %q drop\n", tap)
	}
	fmt.Fprintf(&b, "\t}\n}\n")
	return b.String(), nil
}

func renderIngressRule(tap string, rule packetRule) (string, error) {
	parts := []string{"oifname", fmt.Sprintf("%q", tap)}
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
		parts = append(parts, "accept")
	case "drop":
		parts = append(parts, "drop")
	default:
		return "", fmt.Errorf("packet rule action %q is not accept or drop", rule.Action)
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
