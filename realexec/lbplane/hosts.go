package lbplane

import (
	"fmt"
	"net"
	"runtime"
	"strconv"
	"sync"
	"syscall"

	"github.com/e6qu/sockerless-cloud/realexec"
)

// HostLeases hands each load balancer one stable address that all of its
// listeners bind, so the load balancer's DNS name resolves to a single address
// and "<name>:<listener port>" reaches the right listener — the way a real load
// balancer exposes every listener on its own address.
//
// A load balancer's first listener takes 127.0.0.1 when its port is free there,
// which any same-host client can reach. When another load balancer already holds
// that port, the load balancer leases its own address out of 127.0.0.0/8 instead,
// which Linux binds without configuration; so many load balancers can share a
// listener port the way real ones, each with its own address, do. Other systems
// bind only 127.0.0.1 by default, so there a second load balancer on the same
// port fails to bind rather than advertising an address it does not hold.
type HostLeases struct {
	mu     sync.Mutex
	pool   *realexec.IPAM
	leases map[string]*hostLease
}

type hostLease struct {
	host  string
	ip    net.IP
	users int
}

// NewHostLeases returns an empty lease table over 127.0.0.0/8, keeping
// 127.0.0.1 out of the leasable pool so no lease shadows the primary loopback.
func NewHostLeases() *HostLeases {
	pool, err := realexec.NewIPAM("127.0.0.0/8", net.IPv4(127, 0, 0, 1))
	if err != nil {
		panic(err)
	}
	return &HostLeases{pool: pool, leases: map[string]*hostLease{}}
}

// Acquire returns the owner's host, choosing it on the owner's first call with
// port as the port its first listener binds. Every Acquire is paired with a
// Release; the host returns to the pool when the last one is released.
func (h *HostLeases) Acquire(owner string, port int) (string, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if lease, ok := h.leases[owner]; ok {
		lease.users++
		return lease.host, nil
	}
	primary := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if ln, err := net.Listen("tcp", primary); err == nil {
		_ = ln.Close()
		h.leases[owner] = &hostLease{host: "127.0.0.1", users: 1}
		return "127.0.0.1", nil
	}
	if runtime.GOOS != "linux" {
		return "", fmt.Errorf("listener port %d already bound on 127.0.0.1 and this host binds no other loopback address (one load balancer per listener port off Linux): %w", port, syscall.EADDRINUSE)
	}
	ip, err := h.pool.Reserve(owner, nil)
	if err != nil {
		return "", fmt.Errorf("listener port %d already bound on 127.0.0.1 and no loopback address is left to lease: %w", port, syscall.EADDRINUSE)
	}
	h.leases[owner] = &hostLease{host: ip.String(), ip: ip, users: 1}
	return ip.String(), nil
}

// Release drops one use of the owner's host.
func (h *HostLeases) Release(owner string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lease, ok := h.leases[owner]
	if !ok {
		return
	}
	lease.users--
	if lease.users > 0 {
		return
	}
	if lease.ip != nil {
		h.pool.Release(lease.ip)
	}
	delete(h.leases, owner)
}

// Host returns the owner's host while it holds one.
func (h *HostLeases) Host(owner string) (string, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	lease, ok := h.leases[owner]
	if !ok {
		return "", false
	}
	return lease.host, true
}
