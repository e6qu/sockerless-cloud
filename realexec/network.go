package realexec

import (
	"context"
	"fmt"
	"hash/fnv"
	"net"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var linuxNameRE = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,15}$`)

// nftTableLocks serializes the destructive `nft delete table`+recreate sequences
// per table name. Several of these tables are shared per-VPC / per-subnet / per-
// route (their names derive from the VPC/subnet/route id), so concurrent
// instance/task/route operations on the same table race: one goroutine's
// `nft delete table` nukes another's freshly-created table mid-setup, failing
// the rule add with "No such file or directory".
var nftTableLocks sync.Map // tableName -> *sync.Mutex

// withTableLock serializes fn against any other configure operation on the same
// nft table name.
func withTableLock(tableName string, fn func() error) error {
	defer lockTable(tableName)()
	return fn()
}

// lockTable acquires the per-table-name lock and returns its release func, for
// `defer lockTable(name)()` at the top of a long-bodied configure function.
func lockTable(tableName string) func() {
	lkAny, _ := nftTableLocks.LoadOrStore(tableName, &sync.Mutex{})
	lk, ok := lkAny.(*sync.Mutex)
	if !ok {
		panic(fmt.Sprintf("nftTableLocks holds unexpected type %T for table %q", lkAny, tableName))
	}
	lk.Lock()
	return lk.Unlock
}

type Host struct {
	runner Runner
}

func NewHost() *Host {
	return &Host{runner: Runner{}}
}

type NetworkSpec struct {
	NamespaceName string
	BridgeName    string
	CIDR          string
}

type Network struct {
	NamespaceName string
	BridgeName    string
	Gateway       net.IP
	egress        *EgressLink
	defaultSubnet *Subnet
	subnets       map[string]*Subnet
	mu            sync.Mutex
	egressMu      sync.Mutex // serializes EnsureEgress so the veth pair is created once
	cleanupOnce   sync.Map   // tableName -> struct{}: a shared table's teardown is registered once
	cleanup       *CleanupStack
	runner        Runner
}

// registerTableCleanupOnce registers fn on the network's cleanup stack only the
// first time for a given table name — a shared per-VPC table is reconfigured
// once per instance/task, but its single teardown must be registered once, not
// N times (which would grow the cleanup stack unboundedly).
// removeStaleVethPair deletes a veth whose name is already taken before a new
// one is created under it. The names are derived deterministically from the
// resource they serve, which is what lets the simulator find them again — and
// also what makes a link left behind by a previous simulator process collide
// with the one a restarted process needs, failing with "RTNETLINK answers: File
// exists". A link with that name belongs to a process that is gone, so removing
// it reclaims the name rather than destroying anything live. Both ends go with
// either end, and a name that is not taken makes this a no-op.
func removeStaleVethPair(ctx context.Context, runner Runner, namespaceName, hostName string) {
	if namespaceName != "" {
		if err := runner.Run(ctx, "ip", "netns", "exec", namespaceName, "ip", "link", "del", hostName); err == nil {
			return
		}
	}
	_ = runner.Run(ctx, "ip", "link", "del", hostName)
}

func (n *Network) registerTableCleanupOnce(tableName string, fn func(context.Context) error) {
	if _, loaded := n.cleanupOnce.LoadOrStore(tableName, struct{}{}); loaded {
		return
	}
	n.cleanup.Add(fn)
}

const (
	MetadataIPv4        = "169.254.169.254"
	ECSTaskMetadataIPv4 = "169.254.170.2"
)

type EgressLink struct {
	HostVethName string
	NetVethName  string
	HostIP       net.IP
	NetIP        net.IP
	PrefixBits   int
}

func (h *Host) CreateNetwork(ctx context.Context, spec NetworkSpec) (*Network, error) {
	if spec.NamespaceName == "" {
		spec.NamespaceName = deriveLinuxName(spec.BridgeName, "ns")
	}
	if err := validateLinuxName("bridge", spec.BridgeName); err != nil {
		return nil, err
	}
	ip, network, err := net.ParseCIDR(spec.CIDR)
	if err != nil {
		return nil, err
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("only IPv4 networks are supported in this substrate phase: %s", spec.CIDR)
	}
	n, err := h.CreateNetworkNamespace(ctx, spec.NamespaceName)
	if err != nil {
		return nil, err
	}
	n.BridgeName = spec.BridgeName
	subnet, err := n.CreateSubnet(ctx, SubnetSpec{
		Name:       "default",
		BridgeName: spec.BridgeName,
		CIDR:       network.String(),
	})
	if err != nil {
		_ = n.Close(context.Background())
		return nil, err
	}
	n.defaultSubnet = subnet
	n.Gateway = append(net.IP(nil), subnet.Gateway...)
	return n, nil
}

func (h *Host) CreateNetworkNamespace(ctx context.Context, namespaceName string) (*Network, error) {
	if err := validateLinuxName("network namespace", namespaceName); err != nil {
		return nil, err
	}

	rollback := &CleanupStack{}
	if err := h.runner.Run(ctx, "ip", "netns", "add", namespaceName); err != nil {
		// A namespace of this name may be orphaned in the host-global
		// /run/netns/ by a prior sim process that was killed before its cleanup
		// ran (the namespace name is deterministic per resource, so a fresh sim
		// process derives the same one). Callers gate on their in-process owner
		// map before reaching here, so a collision means a dead-process leftover
		// with no live owner — reclaim it by deleting the orphan and retrying
		// once rather than failing every instance create with "File exists".
		_ = h.runner.Run(ctx, "ip", "netns", "del", namespaceName)
		if err := h.runner.Run(ctx, "ip", "netns", "add", namespaceName); err != nil {
			return nil, err
		}
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		return h.runner.Run(cleanupCtx, "ip", "netns", "del", namespaceName)
	})
	if err := h.runner.Run(ctx, "ip", "netns", "exec", namespaceName, "ip", "link", "set", "dev", "lo", "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	return &Network{
		NamespaceName: namespaceName,
		subnets:       map[string]*Subnet{},
		cleanup:       rollback,
		runner:        h.runner,
	}, nil
}

func (n *Network) Close(ctx context.Context) error {
	if n.cleanup == nil {
		return nil
	}
	return n.cleanup.Close(ctx)
}

func (n *Network) EnsureEgress(ctx context.Context) (*EgressLink, error) {
	n.mu.Lock()
	if n.egress != nil {
		link := *n.egress
		n.mu.Unlock()
		return &link, nil
	}
	n.mu.Unlock()

	// Serialize creation: without this, two concurrent first-uses both pass the
	// nil check above and both run `ip link add <veth>`, the second failing
	// "File exists" and failing its caller's task attach / DNAT setup.
	n.egressMu.Lock()
	defer n.egressMu.Unlock()
	n.mu.Lock()
	if n.egress != nil {
		link := *n.egress
		n.mu.Unlock()
		return &link, nil
	}
	n.mu.Unlock()

	hostName := deriveLinuxName("eh"+n.NamespaceName, "eh")
	netName := deriveLinuxName("en"+n.NamespaceName, "en")
	if err := validateLinuxName("egress host veth", hostName); err != nil {
		return nil, err
	}
	if err := validateLinuxName("egress network veth", netName); err != nil {
		return nil, err
	}
	hostIP, netIP, prefixBits := egressIPs(n.NamespaceName)

	rollback := &CleanupStack{}
	removeStaleVethPair(ctx, n.runner, n.NamespaceName, hostName)
	if err := n.runner.Run(ctx, "ip", "link", "add", hostName, "type", "veth", "peer", "name", netName); err != nil {
		return nil, err
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		if err := n.runner.Run(cleanupCtx, "ip", "link", "del", hostName); err == nil {
			return nil
		}
		return n.runner.Run(cleanupCtx, "ip", "netns", "exec", n.NamespaceName, "ip", "link", "del", netName)
	})
	if err := n.runner.Run(ctx, "ip", "link", "set", netName, "netns", n.NamespaceName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "addr", "replace", fmt.Sprintf("%s/%d", hostIP, prefixBits), "dev", hostName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "link", "set", "dev", hostName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "addr", "replace", fmt.Sprintf("%s/%d", netIP, prefixBits), "dev", netName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "link", "set", "dev", netName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "route", "replace", "default", "via", hostIP.String(), "dev", netName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	link := &EgressLink{
		HostVethName: hostName,
		NetVethName:  netName,
		HostIP:       hostIP,
		NetIP:        netIP,
		PrefixBits:   prefixBits,
	}
	installed := false
	n.mu.Lock()
	if n.egress == nil {
		n.egress = link
		n.cleanup.Add(func(cleanupCtx context.Context) error { return rollback.Close(cleanupCtx) })
		installed = true
	}
	out := *n.egress
	n.mu.Unlock()
	if !installed {
		_ = rollback.Close(context.Background())
	}
	return &out, nil
}

func (n *Network) ConfigureSNAT(ctx context.Context, sourceCIDR string, publicIP net.IP, tableName string) error {
	if publicIP == nil || publicIP.To4() == nil {
		return fmt.Errorf("SNAT public IPv4 address is required")
	}
	// Validate the caller-supplied source prefix up front (matching the sibling
	// ConfigureHostEgress/ConfigureEgressPolicy) so a malformed value yields a
	// clear error instead of an opaque nft failure deep in the rule install.
	if _, _, err := net.ParseCIDR(sourceCIDR); err != nil {
		return fmt.Errorf("invalid SNAT source CIDR %q: %w", sourceCIDR, err)
	}
	if tableName == "" {
		tableName = deriveLinuxName("sn"+n.NamespaceName, "sn")
	}
	link, err := n.EnsureEgress(ctx)
	if err != nil {
		return err
	}
	publicIP = publicIP.To4()
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "addr", "replace", publicIP.String()+"/32", "dev", link.NetVethName); err != nil {
		return err
	}
	if err := n.runner.Run(ctx, "ip", "route", "replace", publicIP.String()+"/32", "via", link.NetIP.String(), "dev", link.HostVethName); err != nil {
		return err
	}
	return withTableLock(tableName, func() error {
		_ = n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "inet", tableName)
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "table", "inet", tableName); err != nil {
			return err
		}
		n.registerTableCleanupOnce(tableName, func(cleanupCtx context.Context) error {
			_ = n.runner.Run(cleanupCtx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "inet", tableName)
			return nil
		})
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "chain", "inet", tableName, "postrouting", "{", "type", "nat", "hook", "postrouting", "priority", "srcnat", ";", "}"); err != nil {
			return err
		}
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "inet", tableName, "postrouting", "ip", "daddr", link.HostIP.String(), "oifname", link.NetVethName, "accept"); err != nil {
			return err
		}
		return n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "inet", tableName, "postrouting", "ip", "saddr", sourceCIDR, "oifname", link.NetVethName, "snat", "to", publicIP.String())
	})
}

func (n *Network) ConfigureHostEgress(ctx context.Context, sourceCIDR string, tableName string, cleanup *CleanupStack) error {
	if tableName == "" {
		tableName = deriveLinuxName("he"+n.NamespaceName, "he")
	}
	defer lockTable(tableName)()
	if _, _, err := net.ParseCIDR(sourceCIDR); err != nil {
		return err
	}
	if _, err := n.EnsureEgress(ctx); err != nil {
		return err
	}
	if err := n.runner.Run(ctx, "sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	_ = n.runner.Run(ctx, "nft", "delete", "table", "inet", tableName)
	if err := n.runner.Run(ctx, "nft", "add", "table", "inet", tableName); err != nil {
		return err
	}
	tableCreated := true
	defer func() {
		if tableCreated {
			_ = n.runner.Run(context.Background(), "nft", "delete", "table", "inet", tableName)
		}
	}()
	if cleanup != nil {
		cleanup.Add(func(cleanupCtx context.Context) error {
			_ = n.runner.Run(cleanupCtx, "nft", "delete", "table", "inet", tableName)
			return nil
		})
	}
	if err := n.runner.Run(ctx, "nft", "add", "chain", "inet", tableName, "postrouting", "{", "type", "nat", "hook", "postrouting", "priority", "srcnat", ";", "}"); err != nil {
		return err
	}
	if err := n.runner.Run(ctx, "nft", "add", "rule", "inet", tableName, "postrouting", "ip", "saddr", sourceCIDR, "masquerade"); err != nil {
		return err
	}
	tableCreated = false
	return nil
}

func (n *Network) ConfigureAddressDNAT(ctx context.Context, targetIPv4 string, targetPort int, tableName string) error {
	if net.ParseIP(targetIPv4).To4() == nil {
		return fmt.Errorf("metadata target IPv4 address is required, got %q", targetIPv4)
	}
	if targetPort <= 0 || targetPort > 65535 {
		return fmt.Errorf("metadata target port must be 1..65535, got %d", targetPort)
	}
	if tableName == "" {
		tableName = deriveLinuxName("md"+n.NamespaceName, "md")
	}
	link, err := n.EnsureEgress(ctx)
	if err != nil {
		return err
	}
	return withTableLock(tableName, func() error {
		_ = n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "ip", tableName)
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "table", "ip", tableName); err != nil {
			return err
		}
		n.registerTableCleanupOnce(tableName, func(cleanupCtx context.Context) error {
			_ = n.runner.Run(cleanupCtx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "ip", tableName)
			return nil
		})
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "chain", "ip", tableName, "prerouting", "{", "type", "nat", "hook", "prerouting", "priority", "dstnat", ";", "}"); err != nil {
			return err
		}
		return n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "ip", tableName, "prerouting", "ip", "daddr", targetIPv4, "tcp", "dport", "80", "dnat", "to", net.JoinHostPort(link.HostIP.String(), strconv.Itoa(targetPort)))
	})
}

// VPCResolverIPv4 is the link-local address a VPC serves DNS on. AWS answers
// there from inside any VPC, alongside the VPC's own base-plus-two address.
// Link-local is the one of the two that is never on the workload's own subnet,
// so a query for it is always routed through the gateway and always arrives
// here — where the redirect below is waiting — instead of depending on someone
// answering an address-resolution request for an address inside the subnet.
const VPCResolverIPv4 = "169.254.169.253"

// ConfigureResolverDNAT points a namespace's DNS traffic at a resolver the host
// runs. A workload namespace holds only its own interface, so the embedded
// resolver its container image was configured with — Docker's 127.0.0.11 —
// answers nothing once the container is detached from Docker's networks: every
// lookup inside blocks until it times out rather than failing, which reads as a
// workload that starts minutes late for no logged reason.
//
// Both protocols are carried because a resolver is reached over UDP and falls
// back to TCP for truncated answers, and port 53 is the address a resolv.conf
// entry implies. Nothing is assigned to an interface: the address is
// link-local, so the query is routed here and the prerouting hook claims it
// before any local-delivery decision — the same way the metadata addresses
// work.
func (n *Network) ConfigureResolverDNAT(ctx context.Context, resolverPort int, tableName string) error {
	if resolverPort <= 0 || resolverPort > 65535 {
		return fmt.Errorf("resolver port must be 1..65535, got %d", resolverPort)
	}
	if tableName == "" {
		return fmt.Errorf("resolver DNAT table name is required")
	}
	link, err := n.EnsureEgress(ctx)
	if err != nil {
		return err
	}
	target := net.JoinHostPort(link.HostIP.String(), strconv.Itoa(resolverPort))
	return withTableLock(tableName, func() error {
		_ = n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "ip", tableName)
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "table", "ip", tableName); err != nil {
			return err
		}
		n.registerTableCleanupOnce(tableName, func(cleanupCtx context.Context) error {
			_ = n.runner.Run(cleanupCtx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "ip", tableName)
			return nil
		})
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "chain", "ip", tableName, "prerouting", "{", "type", "nat", "hook", "prerouting", "priority", "dstnat", ";", "}"); err != nil {
			return err
		}
		for _, protocol := range []string{"udp", "tcp"} {
			if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "ip", tableName, "prerouting", "ip", "daddr", VPCResolverIPv4, protocol, "dport", "53", "dnat", "to", target); err != nil {
				return err
			}
		}
		return nil
	})
}

// RemoveAddressDNAT removes a previously configured address-specific DNAT
// table. It is idempotent because callers use it while unwinding short-lived
// workload control-plane listeners whose parent VPC can disappear concurrently.
func (n *Network) RemoveAddressDNAT(ctx context.Context, tableName string) error {
	if tableName == "" {
		return nil
	}
	return withTableLock(tableName, func() error {
		_ = n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "ip", tableName)
		return nil
	})
}

func (n *Network) ConfigureMetadataDNAT(ctx context.Context, targetPort int, tableName string) error {
	return n.ConfigureAddressDNAT(ctx, MetadataIPv4, targetPort, tableName)
}

func (s *Subnet) ConfigureMetadataDNAT(ctx context.Context, targetPort int, tableName string) error {
	if s == nil || s.network == nil {
		return fmt.Errorf("subnet is not attached to a network")
	}
	return s.network.ConfigureMetadataDNAT(ctx, targetPort, tableName)
}

func (s *Subnet) ConfigureAddressDNAT(ctx context.Context, targetIPv4 string, targetPort int, tableName string) error {
	if s == nil || s.network == nil {
		return fmt.Errorf("subnet is not attached to a network")
	}
	return s.network.ConfigureAddressDNAT(ctx, targetIPv4, targetPort, tableName)
}

// ConfigureResolverDNAT serves the resolver on the subnet's own bridge, which
// is the device a workload on that subnet resolves the address over. A network
// carries no bridge of its own — each of its subnets has one — so the device
// comes from here rather than from the network.
func (s *Subnet) ConfigureResolverDNAT(ctx context.Context, resolverPort int, tableName string) error {
	if s == nil || s.network == nil {
		return fmt.Errorf("subnet is not attached to a network")
	}
	return s.network.ConfigureResolverDNAT(ctx, resolverPort, tableName)
}

func (s *Subnet) RemoveAddressDNAT(ctx context.Context, tableName string) error {
	if s == nil || s.network == nil {
		return nil
	}
	return s.network.RemoveAddressDNAT(ctx, tableName)
}

func (n *Network) ConfigureEgressPolicy(ctx context.Context, allowedSourceCIDRs []string, tableName string) error {
	if tableName == "" {
		tableName = deriveLinuxName("eg"+n.NamespaceName, "eg")
	}
	link, err := n.EnsureEgress(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	var cidrs []string
	for _, cidr := range allowedSourceCIDRs {
		if cidr == "" || seen[cidr] {
			continue
		}
		if _, _, err := net.ParseCIDR(cidr); err != nil {
			return fmt.Errorf("invalid allowed egress source CIDR %q: %w", cidr, err)
		}
		seen[cidr] = true
		cidrs = append(cidrs, cidr)
	}
	sort.Strings(cidrs)

	return withTableLock(tableName, func() error {
		_ = n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "inet", tableName)
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "table", "inet", tableName); err != nil {
			return err
		}
		n.registerTableCleanupOnce(tableName, func(cleanupCtx context.Context) error {
			_ = n.runner.Run(cleanupCtx, "ip", "netns", "exec", n.NamespaceName, "nft", "delete", "table", "inet", tableName)
			return nil
		})
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "chain", "inet", tableName, "forward", "{", "type", "filter", "hook", "forward", "priority", "filter", ";", "}"); err != nil {
			return err
		}
		if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "inet", tableName, "forward", "oifname", link.NetVethName, "ip", "daddr", link.HostIP.String(), "accept"); err != nil {
			return err
		}
		for _, cidr := range cidrs {
			if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "inet", tableName, "forward", "oifname", link.NetVethName, "ip", "saddr", cidr, "accept"); err != nil {
				return err
			}
		}
		return n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "nft", "add", "rule", "inet", tableName, "forward", "oifname", link.NetVethName, "drop")
	})
}

func egressIPs(namespaceName string) (hostIP, netIP net.IP, prefixBits int) {
	h := fnv.New32a()
	_, _ = h.Write([]byte(namespaceName))
	index := h.Sum32() % 32768
	second := byte(18 + index/16384)
	third := byte((index / 64) % 256)
	fourth := byte((index % 64) * 4)
	return net.IPv4(198, second, third, fourth+1), net.IPv4(198, second, third, fourth+2), 30
}

type NamespaceNICSpec struct {
	NamespaceName string
	HostVethName  string
	GuestVethName string
	MAC           string
	PrivateIP     net.IP
}

type NamespaceNIC struct {
	NamespaceName string
	HostVethName  string
	GuestVethName string
	PrivateIP     net.IP
	Gateway       net.IP
	cleanup       *CleanupStack
	network       *Network
}

// ExternalNamespaceNICSpec attaches a NIC into an ALREADY-EXISTING network
// namespace owned by another process (e.g. a Docker container), identified by
// its PID. Unlike NamespaceNICSpec it does not create or destroy the namespace —
// the host veth lands in the subnet bridge, the guest veth is moved into the
// container's netns by PID and renamed to GuestIfName.
type ExternalNamespaceNICSpec struct {
	PID           int    // PID whose net namespace to join (e.g. the container's)
	HostVethName  string // veth end placed in the subnet bridge (VPC netns)
	GuestVethName string // veth end moved into the container netns
	GuestIfName   string // interface name inside the container (e.g. "eth0")
	MAC           string
	PrivateIP     net.IP
}

type TapNICSpec struct {
	TapName   string
	PrivateIP net.IP
	MAC       string
}

type TapNIC struct {
	TapName    string
	PrivateIP  net.IP
	Gateway    net.IP
	PrefixBits int
	cleanup    *CleanupStack
	subnet     *Subnet
	network    *Network
}

type PacketRule struct {
	Protocol   string
	SourceCIDR string
	FromPort   int
	ToPort     int
	Action     string
}

type SubnetSpec struct {
	Name       string
	BridgeName string
	CIDR       string
	Gateway    net.IP
}

type Subnet struct {
	Name       string
	BridgeName string
	CIDR       string
	Gateway    net.IP
	ipam       *IPAM
	cleanup    *CleanupStack
	network    *Network
}

func (n *Network) CreateSubnet(ctx context.Context, spec SubnetSpec) (*Subnet, error) {
	if spec.Name == "" {
		return nil, fmt.Errorf("subnet name is required")
	}
	if err := validateLinuxName("subnet bridge", spec.BridgeName); err != nil {
		return nil, err
	}
	ip, network, err := net.ParseCIDR(spec.CIDR)
	if err != nil {
		return nil, err
	}
	if ip.To4() == nil {
		return nil, fmt.Errorf("only IPv4 subnets are supported in this substrate phase: %s", spec.CIDR)
	}
	network.IP = append(net.IP(nil), ip.To4()...)
	gateway := spec.Gateway
	if gateway == nil {
		gateway = nextIPv4(network.IP.To4())
	}
	ipam, err := NewIPAM(network.String(), gateway)
	if err != nil {
		return nil, err
	}

	n.mu.Lock()
	if _, ok := n.subnets[spec.Name]; ok {
		n.mu.Unlock()
		return nil, fmt.Errorf("subnet %q already exists", spec.Name)
	}
	n.mu.Unlock()

	rollback := &CleanupStack{}
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "link", "add", "name", spec.BridgeName, "type", "bridge"); err != nil {
		return nil, err
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		return n.runner.Run(cleanupCtx, "ip", "netns", "exec", n.NamespaceName, "ip", "link", "del", spec.BridgeName)
	})
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "addr", "add", fmt.Sprintf("%s/%d", gateway, ipam.PrefixBits()), "dev", spec.BridgeName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "netns", "exec", n.NamespaceName, "ip", "link", "set", "dev", spec.BridgeName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	egress, err := n.EnsureEgress(ctx)
	if err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := n.runner.Run(ctx, "ip", "route", "replace", network.String(), "via", egress.NetIP.String(), "dev", egress.HostVethName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		if err := n.runner.Run(cleanupCtx, "ip", "route", "del", network.String(), "via", egress.NetIP.String(), "dev", egress.HostVethName); err != nil && !strings.Contains(err.Error(), "No such process") {
			return err
		}
		return nil
	})
	if err := n.ConfigureHostEgress(ctx, network.String(), deriveLinuxName("he"+n.NamespaceName+spec.Name, "he"), rollback); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	subnet := &Subnet{
		Name:       spec.Name,
		BridgeName: spec.BridgeName,
		CIDR:       network.String(),
		Gateway:    append(net.IP(nil), gateway.To4()...),
		ipam:       ipam,
		cleanup:    rollback,
		network:    n,
	}
	n.mu.Lock()
	n.subnets[spec.Name] = subnet
	n.mu.Unlock()
	return subnet, nil
}

func (s *Subnet) Close(ctx context.Context) error {
	if s.cleanup == nil {
		return nil
	}
	if err := s.cleanup.Close(ctx); err != nil {
		return err
	}
	s.network.mu.Lock()
	delete(s.network.subnets, s.Name)
	s.network.mu.Unlock()
	return nil
}

func (n *Network) AttachNamespaceNIC(ctx context.Context, spec NamespaceNICSpec) (*NamespaceNIC, error) {
	if n.defaultSubnet == nil {
		return nil, fmt.Errorf("network %s has no default subnet", n.NamespaceName)
	}
	return n.defaultSubnet.AttachNamespaceNIC(ctx, spec)
}

func (s *Subnet) AttachNamespaceNIC(ctx context.Context, spec NamespaceNICSpec) (*NamespaceNIC, error) {
	if err := validateLinuxName("namespace", spec.NamespaceName); err != nil {
		return nil, err
	}
	if err := validateLinuxName("host veth", spec.HostVethName); err != nil {
		return nil, err
	}
	if err := validateLinuxName("guest veth", spec.GuestVethName); err != nil {
		return nil, err
	}
	if spec.MAC != "" {
		if _, err := net.ParseMAC(spec.MAC); err != nil {
			return nil, fmt.Errorf("invalid MAC %q: %w", spec.MAC, err)
		}
	}

	ip, err := s.ipam.Reserve(spec.NamespaceName, spec.PrivateIP)
	if err != nil {
		return nil, err
	}
	rollback := &CleanupStack{}
	rollback.Add(func(context.Context) error {
		s.ipam.Release(ip)
		return nil
	})

	if err := s.network.runner.Run(ctx, "ip", "netns", "add", spec.NamespaceName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		return s.network.runner.Run(cleanupCtx, "ip", "netns", "del", spec.NamespaceName)
	})

	removeStaleVethPair(ctx, s.network.runner, s.network.NamespaceName, spec.HostVethName)
	if err := s.network.runner.Run(ctx, "ip", "link", "add", spec.HostVethName, "type", "veth", "peer", "name", spec.GuestVethName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		if err := s.network.runner.Run(cleanupCtx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "del", spec.HostVethName); err == nil {
			return nil
		}
		return s.network.runner.Run(cleanupCtx, "ip", "link", "del", spec.HostVethName)
	})

	if err := s.network.runner.Run(ctx, "ip", "link", "set", spec.HostVethName, "netns", s.network.NamespaceName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "set", spec.HostVethName, "master", s.BridgeName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "set", spec.HostVethName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "link", "set", spec.GuestVethName, "netns", spec.NamespaceName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	if spec.MAC != "" {
		if err := s.network.runner.Run(ctx, "ip", "netns", "exec", spec.NamespaceName, "ip", "link", "set", "dev", spec.GuestVethName, "address", spec.MAC); err != nil {
			_ = rollback.Close(context.Background())
			return nil, err
		}
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", spec.NamespaceName, "ip", "addr", "add", fmt.Sprintf("%s/%d", ip, s.ipam.PrefixBits()), "dev", spec.GuestVethName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", spec.NamespaceName, "ip", "link", "set", "dev", "lo", "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", spec.NamespaceName, "ip", "link", "set", "dev", spec.GuestVethName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", spec.NamespaceName, "ip", "route", "replace", "default", "via", s.Gateway.String(), "dev", spec.GuestVethName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	nic := &NamespaceNIC{
		NamespaceName: spec.NamespaceName,
		HostVethName:  spec.HostVethName,
		GuestVethName: spec.GuestVethName,
		PrivateIP:     append(net.IP(nil), ip...),
		Gateway:       append(net.IP(nil), s.Gateway...),
		cleanup:       rollback,
		network:       s.network,
	}
	return nic, nil
}

// AttachExternalNamespaceNIC plumbs a veth pair between the subnet bridge and an
// existing process's network namespace (the container's), giving that namespace
// a NIC at PrivateIP on the subnet. Because each VPC is its own netns with its
// own routing table, overlapping VPC CIDRs work natively — no remapping. The
// namespace is NOT created or destroyed here (it belongs to the container).
func (n *Network) AttachExternalNamespaceNIC(ctx context.Context, spec ExternalNamespaceNICSpec) (*NamespaceNIC, error) {
	if n.defaultSubnet == nil {
		return nil, fmt.Errorf("network %s has no default subnet", n.NamespaceName)
	}
	return n.defaultSubnet.AttachExternalNamespaceNIC(ctx, spec)
}

func (s *Subnet) AttachExternalNamespaceNIC(ctx context.Context, spec ExternalNamespaceNICSpec) (*NamespaceNIC, error) {
	if spec.PID <= 0 {
		return nil, fmt.Errorf("AttachExternalNamespaceNIC: PID must be > 0")
	}
	if err := validateLinuxName("host veth", spec.HostVethName); err != nil {
		return nil, err
	}
	if err := validateLinuxName("guest veth", spec.GuestVethName); err != nil {
		return nil, err
	}
	guestIf := spec.GuestIfName
	if guestIf == "" {
		guestIf = "eth0"
	}
	if err := validateLinuxName("guest interface", guestIf); err != nil {
		return nil, err
	}
	if spec.MAC != "" {
		if _, err := net.ParseMAC(spec.MAC); err != nil {
			return nil, fmt.Errorf("invalid MAC %q: %w", spec.MAC, err)
		}
	}

	reserveKey := fmt.Sprintf("pid-%d", spec.PID)
	ip, err := s.ipam.Reserve(reserveKey, spec.PrivateIP)
	if err != nil {
		return nil, err
	}
	rollback := &CleanupStack{}
	rollback.Add(func(context.Context) error { s.ipam.Release(ip); return nil })

	removeStaleVethPair(ctx, s.network.runner, s.network.NamespaceName, spec.HostVethName)
	if err := s.network.runner.Run(ctx, "ip", "link", "add", spec.HostVethName, "type", "veth", "peer", "name", spec.GuestVethName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	// Deleting either end removes the pair. The host end lives in the VPC netns
	// after the move below; the guest end lives in the container netns.
	rollback.Add(func(cleanupCtx context.Context) error {
		if err := s.network.runner.Run(cleanupCtx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "del", spec.HostVethName); err == nil {
			return nil
		}
		return s.network.runner.Run(cleanupCtx, "ip", "link", "del", spec.HostVethName)
	})

	if err := s.network.runner.Run(ctx, "ip", "link", "set", spec.HostVethName, "netns", s.network.NamespaceName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "set", spec.HostVethName, "master", s.BridgeName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "set", spec.HostVethName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	// Move the guest end into the container's netns (by PID) and configure it
	// there with nsenter — the container's netns is not named under /run/netns.
	pid := strconv.Itoa(spec.PID)
	if err := s.network.runner.Run(ctx, "ip", "link", "set", spec.GuestVethName, "netns", pid); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	ns := func(args ...string) error {
		return s.network.runner.Run(ctx, "nsenter", append([]string{"-t", pid, "-n", "--"}, args...)...)
	}
	if err := ns("ip", "link", "set", "dev", spec.GuestVethName, "name", guestIf); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if spec.MAC != "" {
		if err := ns("ip", "link", "set", "dev", guestIf, "address", spec.MAC); err != nil {
			_ = rollback.Close(context.Background())
			return nil, err
		}
	}
	if err := ns("ip", "addr", "add", fmt.Sprintf("%s/%d", ip, s.ipam.PrefixBits()), "dev", guestIf); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := ns("ip", "link", "set", "dev", "lo", "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := ns("ip", "link", "set", "dev", guestIf, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := ns("ip", "route", "replace", "default", "via", s.Gateway.String(), "dev", guestIf); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	return &NamespaceNIC{
		NamespaceName: reserveKey,
		HostVethName:  spec.HostVethName,
		GuestVethName: guestIf,
		PrivateIP:     append(net.IP(nil), ip...),
		Gateway:       append(net.IP(nil), s.Gateway...),
		cleanup:       rollback,
		network:       s.network,
	}, nil
}

func (s *Subnet) AttachTapNIC(ctx context.Context, spec TapNICSpec) (*TapNIC, error) {
	if err := validateLinuxName("tap", spec.TapName); err != nil {
		return nil, err
	}
	if spec.MAC != "" {
		if _, err := net.ParseMAC(spec.MAC); err != nil {
			return nil, fmt.Errorf("invalid MAC %q: %w", spec.MAC, err)
		}
	}

	ip, err := s.ipam.Reserve(spec.TapName, spec.PrivateIP)
	if err != nil {
		return nil, err
	}
	rollback := &CleanupStack{}
	rollback.Add(func(context.Context) error {
		s.ipam.Release(ip)
		return nil
	})

	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "tuntap", "add", "dev", spec.TapName, "mode", "tap"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	rollback.Add(func(cleanupCtx context.Context) error {
		return s.network.runner.Run(cleanupCtx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "del", spec.TapName)
	})
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "set", spec.TapName, "master", s.BridgeName); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}
	if err := s.network.runner.Run(ctx, "ip", "netns", "exec", s.network.NamespaceName, "ip", "link", "set", "dev", spec.TapName, "up"); err != nil {
		_ = rollback.Close(context.Background())
		return nil, err
	}

	nic := &TapNIC{
		TapName:    spec.TapName,
		PrivateIP:  append(net.IP(nil), ip...),
		Gateway:    append(net.IP(nil), s.Gateway...),
		PrefixBits: s.ipam.PrefixBits(),
		cleanup:    rollback,
		subnet:     s,
		network:    s.network,
	}
	return nic, nil
}

func (n *NamespaceNIC) Close(ctx context.Context) error {
	if n.cleanup == nil {
		return nil
	}
	return n.cleanup.Close(ctx)
}

func (n *TapNIC) Close(ctx context.Context) error {
	if n.cleanup == nil {
		return nil
	}
	return n.cleanup.Close(ctx)
}

func (n *NamespaceNIC) ConfigureIngressFilter(ctx context.Context, rules []PacketRule) error {
	if n.network == nil {
		return fmt.Errorf("NIC %s is not attached to a network", n.HostVethName)
	}
	return n.configureIngressFilter(ctx, n.HostVethName, rules)
}

func (n *TapNIC) ConfigureIngressFilter(ctx context.Context, rules []PacketRule) error {
	if n.network == nil {
		return fmt.Errorf("tap NIC %s is not attached to a network", n.TapName)
	}
	return configureBridgeIngressFilter(ctx, n.network, n.cleanup, n.TapName, rules)
}

func (n *NamespaceNIC) configureIngressFilter(ctx context.Context, devName string, rules []PacketRule) error {
	return configureBridgeIngressFilter(ctx, n.network, n.cleanup, devName, rules)
}

func configureBridgeIngressFilter(ctx context.Context, network *Network, cleanup *CleanupStack, devName string, rules []PacketRule) error {
	table := deriveLinuxName("fw"+devName, "fw")
	defer lockTable(table)()
	_ = network.runner.Run(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "delete", "table", "bridge", table)
	if err := network.runner.Run(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "add", "table", "bridge", table); err != nil {
		return err
	}
	cleanup.Add(func(cleanupCtx context.Context) error {
		_ = network.runner.Run(cleanupCtx, "ip", "netns", "exec", network.NamespaceName, "nft", "delete", "table", "bridge", table)
		return nil
	})
	if err := network.runner.Run(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "add", "chain", "bridge", table, "forward", "{", "type", "filter", "hook", "forward", "priority", "filter", ";", "policy", "accept", ";", "}"); err != nil {
		return err
	}
	if err := network.runner.Run(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "add", "rule", "bridge", table, "forward", "ct", "state", "established,related", "accept"); err != nil {
		return err
	}
	// Security groups filter IP traffic, not the link-layer neighbor discovery
	// needed to deliver that traffic. Permit ARP before the per-NIC terminal
	// drop so a newly attached peer can resolve the destination ENI instead of
	// depending on a neighbor-cache entry created before the filter existed.
	if err := network.runner.Run(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "add", "rule", "bridge", table, "forward", "oifname", devName, "ether", "type", "arp", "accept"); err != nil {
		return err
	}
	for _, rule := range rules {
		args := []string{"ip", "netns", "exec", network.NamespaceName, "nft", "add", "rule", "bridge", table, "forward", "oifname", devName}
		if rule.SourceCIDR != "" {
			args = append(args, "ip", "saddr", rule.SourceCIDR)
		}
		switch strings.ToLower(rule.Protocol) {
		case "tcp", "udp":
			proto := strings.ToLower(rule.Protocol)
			args = append(args, "ip", "protocol", proto, proto)
			if rule.FromPort > 0 || rule.ToPort > 0 {
				from, to := rule.FromPort, rule.ToPort
				if from == 0 {
					from = to
				}
				if to == 0 {
					to = from
				}
				if from == to {
					args = append(args, "dport", fmt.Sprintf("%d", from))
				} else {
					args = append(args, "dport", fmt.Sprintf("%d-%d", from, to))
				}
			}
		case "icmp":
			args = append(args, "ip", "protocol", "icmp")
		case "-1", "all", "":
		default:
			return fmt.Errorf("unsupported packet filter protocol %q", rule.Protocol)
		}
		action := strings.ToLower(rule.Action)
		if action == "" {
			action = "accept"
		}
		switch action {
		case "accept", "drop":
		default:
			return fmt.Errorf("unsupported packet filter action %q", rule.Action)
		}
		args = append(args, action)
		if err := network.runner.Run(ctx, args[0], args[1:]...); err != nil {
			return err
		}
	}
	return network.runner.Run(ctx, "ip", "netns", "exec", network.NamespaceName, "nft", "add", "rule", "bridge", table, "forward", "oifname", devName, "drop")
}

func (n *NamespaceNIC) ClearIngressFilter(ctx context.Context) error {
	if n.network == nil {
		return fmt.Errorf("NIC %s is not attached to a network", n.HostVethName)
	}
	table := deriveLinuxName("fw"+n.HostVethName, "fw")
	_ = n.network.runner.Run(ctx, "ip", "netns", "exec", n.network.NamespaceName, "nft", "delete", "table", "bridge", table)
	return nil
}

func (n *TapNIC) ClearIngressFilter(ctx context.Context) error {
	if n.network == nil {
		return fmt.Errorf("tap NIC %s is not attached to a network", n.TapName)
	}
	table := deriveLinuxName("fw"+n.TapName, "fw")
	_ = n.network.runner.Run(ctx, "ip", "netns", "exec", n.network.NamespaceName, "nft", "delete", "table", "bridge", table)
	return nil
}

func (n *TapNIC) NetworkNamespace() string {
	if n.network == nil {
		return ""
	}
	return n.network.NamespaceName
}

func validateLinuxName(label, name string) error {
	if !linuxNameRE.MatchString(name) {
		return fmt.Errorf("invalid %s name %q: must match %s", label, name, linuxNameRE.String())
	}
	return nil
}

func deriveLinuxName(base, suffix string) string {
	if len(base)+len(suffix) <= 15 {
		return base + suffix
	}
	if len(suffix) >= 15 {
		return suffix[:15]
	}
	return base[:15-len(suffix)] + suffix
}

func nextIPv4(ip net.IP) net.IP {
	out := append(net.IP(nil), ip.To4()...)
	out[3]++
	return out
}
