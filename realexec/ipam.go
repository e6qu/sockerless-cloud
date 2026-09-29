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
	first    int
	last     int
}

// NewIPAM reserves the network address, the gateway and the broadcast address.
func NewIPAM(cidr string, gateway net.IP) (*IPAM, error) {
	return NewIPAMWithReserved(cidr, gateway, HostReservation{First: 1, Last: 1})
}

// HostReservation is how many addresses at each end of a subnet a cloud keeps
// for itself. First counts the network address and Last the broadcast address,
// so neither may be below one.
type HostReservation struct {
	First int
	Last  int
}

// NewIPAMWithReserved hands out only the addresses a cloud leaves to its
// tenants: Amazon VPC and Azure Virtual Network keep the first four and the
// last, Google Cloud VPC the first two and the last two. The gateway stays
// reserved wherever it falls.
func NewIPAMWithReserved(cidr string, gateway net.IP, keep HostReservation) (*IPAM, error) {
	if keep.First < 1 || keep.Last < 1 {
		return nil, fmt.Errorf("a subnet reserves at least its network and broadcast addresses, got first=%d last=%d", keep.First, keep.Last)
	}
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
		first:    keep.First,
		last:     keep.Last,
	}, nil
}

func (i *IPAM) Reserve(owner string, requested net.IP) (net.IP, error) {
	i.mu.Lock()
	defer i.mu.Unlock()

	if requested != nil {
		ip := requested.To4()
		if ip == nil || !i.network.Contains(ip) {
			return nil, fmt.Errorf("requested IP %s is not usable in %s", requested, i.network)
		}
		if i.hostReserved(ipToBig(ip)) {
			return nil, fmt.Errorf("requested IP %s is reserved in %s", requested, i.network)
		}
		key := ip.String()
		if current, ok := i.reserved[key]; ok {
			return nil, fmt.Errorf("IP %s already leased to %s", ip, current)
		}
		i.reserved[key] = owner
		return append(net.IP(nil), ip...), nil
	}

	start := new(big.Int).Add(ipToBig(i.network.IP), big.NewInt(int64(i.first)))
	end := new(big.Int).Sub(broadcastBig(i.network), big.NewInt(int64(i.last-1)))
	for n := start; n.Cmp(end) < 0; n.Add(n, big.NewInt(1)) {
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

func (i *IPAM) hostReserved(n *big.Int) bool {
	low := new(big.Int).Add(ipToBig(i.network.IP), big.NewInt(int64(i.first)))
	high := new(big.Int).Sub(broadcastBig(i.network), big.NewInt(int64(i.last-1)))
	return n.Cmp(low) < 0 || n.Cmp(high) >= 0
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
