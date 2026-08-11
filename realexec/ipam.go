package realexec

import (
	"errors"
	"fmt"
	"math/big"
	"net"
	"sync"
)

var ErrNoAvailableIP = errors.New("no available IP address in subnet")

// IPAM tracks explicit leases for a subnet. Allocation is based on the CIDR
// address space and current leases, never on store length or creation order.
type IPAM struct {
	mu       sync.Mutex
	network  *net.IPNet
	gateway  net.IP
	reserved map[string]string
}

func NewIPAM(cidr string, gateway net.IP) (*IPAM, error) {
	ip, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}
	ip4 := ip.To4()
	if ip4 == nil {
		return nil, fmt.Errorf("only IPv4 subnets are supported by this substrate phase: %s", cidr)
	}
	network.IP = append(net.IP(nil), ip4...)
	if !network.Contains(gateway) {
		return nil, fmt.Errorf("gateway %s is outside %s", gateway, cidr)
	}
	return &IPAM{
		network:  network,
		gateway:  append(net.IP(nil), gateway.To4()...),
		reserved: map[string]string{gateway.String(): "gateway"},
	}, nil
}

func (i *IPAM) Reserve(owner string, requested net.IP) (net.IP, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if requested != nil {
		ip := requested.To4()
		if ip == nil || !i.network.Contains(ip) || isNetworkAddress(i.network, ip) || isBroadcastAddress(i.network, ip) {
			return nil, fmt.Errorf("requested IP %s is not usable in %s", requested, i.network)
		}
		key := ip.String()
		if current, ok := i.reserved[key]; ok {
			return nil, fmt.Errorf("IP %s already leased to %s", ip, current)
		}
		i.reserved[key] = owner
		return append(net.IP(nil), ip...), nil
	}

	start := ipToBig(i.network.IP)
	end := broadcastBig(i.network)
	for n := new(big.Int).Add(start, big.NewInt(1)); n.Cmp(end) < 0; n.Add(n, big.NewInt(1)) {
		ip := bigToIPv4(n)
		key := ip.String()
		if _, ok := i.reserved[key]; ok {
			continue
		}
		i.reserved[key] = owner
		return append(net.IP(nil), ip...), nil
	}
	return nil, ErrNoAvailableIP
}

func (i *IPAM) Release(ip net.IP) {
	if ip == nil {
		return
	}
	ip4 := ip.To4()
	if ip4 == nil {
		// Non-IPv4 address: this allocator only ever hands out IPv4, so a
		// non-IPv4 value can't be one of ours. Bail rather than panic on the
		// nil .To4() deref below.
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	key := ip4.String()
	if i.reserved[key] == "gateway" {
		return
	}
	delete(i.reserved, key)
}

func (i *IPAM) Gateway() net.IP {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append(net.IP(nil), i.gateway...)
}

func (i *IPAM) CIDR() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.network.String()
}

func (i *IPAM) PrefixBits() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	ones, _ := i.network.Mask.Size()
	return ones
}

func isNetworkAddress(network *net.IPNet, ip net.IP) bool {
	return ip.Equal(network.IP)
}

func isBroadcastAddress(network *net.IPNet, ip net.IP) bool {
	return ip.Equal(bigToIPv4(broadcastBig(network)))
}

func broadcastBig(network *net.IPNet) *big.Int {
	start := ipToBig(network.IP)
	ones, bits := network.Mask.Size()
	hostCount := new(big.Int).Lsh(big.NewInt(1), uint(bits-ones))
	return new(big.Int).Add(start, new(big.Int).Sub(hostCount, big.NewInt(1)))
}

func ipToBig(ip net.IP) *big.Int {
	return new(big.Int).SetBytes(ip.To4())
}

func bigToIPv4(n *big.Int) net.IP {
	b := n.Bytes()
	out := make([]byte, 4)
	copy(out[4-len(b):], b)
	return net.IP(out)
}
