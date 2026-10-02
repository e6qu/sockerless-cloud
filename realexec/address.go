package realexec

import (
	"context"
	"fmt"
	"net"
	"slices"
)

// ReserveAddress leases an address of the subnet to owner: requested when it
// is set, the lowest free tenant address otherwise.
func (s *Subnet) ReserveAddress(owner string, requested net.IP) (net.IP, error) {
	return s.ipam.Reserve(owner, requested)
}

// ReleaseAddress returns a leased address to the subnet.
func (s *Subnet) ReleaseAddress(ip net.IP) {
	s.ipam.Release(ip)
}

// PrefixBits is the length of the subnet's network prefix.
func (s *Subnet) PrefixBits() int {
	return s.ipam.PrefixBits()
}

// AddAddress leases a further address of the interface's subnet, requested
// when it is set, and adds it to the interface beside its primary address.
// Adding an address the interface already carries as a secondary returns it.
// Closing the interface removes the address and returns the lease.
func (n *NamespaceNIC) AddAddress(ctx context.Context, requested net.IP) (net.IP, error) {
	if n.subnet == nil || n.inNamespace == nil {
		return nil, fmt.Errorf("network interface %s belongs to no subnet", n.NamespaceName)
	}
	n.addrMu.Lock()
	defer n.addrMu.Unlock()
	if requested != nil {
		if requested.Equal(n.PrivateIP) {
			return nil, fmt.Errorf("address %s is already the primary address of %s", requested, n.NamespaceName)
		}
		if i := indexIP(n.secondary, requested); i >= 0 {
			return append(net.IP(nil), n.secondary[i]...), nil
		}
	}
	ip, err := n.subnet.ReserveAddress(n.NamespaceName, requested)
	if err != nil {
		return nil, err
	}
	if err := n.inNamespace(ctx, "ip", "addr", "add", n.addressCIDR(ip), "dev", n.GuestVethName); err != nil {
		n.subnet.ReleaseAddress(ip)
		return nil, err
	}
	n.secondary = append(n.secondary, ip)
	n.cleanup.Add(func(cleanupCtx context.Context) error {
		return n.removeAddress(cleanupCtx, ip, false)
	})
	return append(net.IP(nil), ip...), nil
}

// RemoveAddress takes a secondary address off the interface and returns its
// lease to the subnet.
func (n *NamespaceNIC) RemoveAddress(ctx context.Context, ip net.IP) error {
	return n.removeAddress(ctx, ip, true)
}

func (n *NamespaceNIC) removeAddress(ctx context.Context, ip net.IP, mustHold bool) error {
	n.addrMu.Lock()
	defer n.addrMu.Unlock()
	i := indexIP(n.secondary, ip)
	if i < 0 {
		if mustHold {
			return fmt.Errorf("address %s is not a secondary address of %s", ip, n.NamespaceName)
		}
		return nil
	}
	if err := n.inNamespace(ctx, "ip", "addr", "del", n.addressCIDR(ip), "dev", n.GuestVethName); err != nil {
		return err
	}
	n.secondary = slices.Delete(n.secondary, i, i+1)
	n.subnet.ReleaseAddress(ip)
	return nil
}

// Addresses lists the interface's secondary addresses in the order they were
// added.
func (n *NamespaceNIC) Addresses() []net.IP {
	n.addrMu.Lock()
	defer n.addrMu.Unlock()
	return cloneIPs(n.secondary)
}

func (n *NamespaceNIC) addressCIDR(ip net.IP) string {
	return fmt.Sprintf("%s/%d", ip, n.subnet.PrefixBits())
}

// AddAddress leases a further address of the tap's subnet to the machine
// behind it. The bridge delivers the address's traffic to the tap once the
// guest operating system configures it, which is the guest's to do, as on a
// cloud whose DHCP hands a guest only its primary address. Adding an address
// the tap already holds returns it; closing the tap returns the lease.
func (n *TapNIC) AddAddress(requested net.IP) (net.IP, error) {
	n.addrMu.Lock()
	defer n.addrMu.Unlock()
	if requested != nil {
		if requested.Equal(n.PrivateIP) {
			return nil, fmt.Errorf("address %s is already the primary address of %s", requested, n.TapName)
		}
		if i := indexIP(n.secondary, requested); i >= 0 {
			return append(net.IP(nil), n.secondary[i]...), nil
		}
	}
	ip, err := n.subnet.ReserveAddress(n.TapName, requested)
	if err != nil {
		return nil, err
	}
	n.secondary = append(n.secondary, ip)
	n.cleanup.Add(func(context.Context) error {
		n.releaseAddress(ip)
		return nil
	})
	return append(net.IP(nil), ip...), nil
}

// RemoveAddress returns a secondary address the tap holds to the subnet.
func (n *TapNIC) RemoveAddress(ip net.IP) error {
	if !n.releaseAddress(ip) {
		return fmt.Errorf("address %s is not a secondary address of %s", ip, n.TapName)
	}
	return nil
}

func (n *TapNIC) releaseAddress(ip net.IP) bool {
	n.addrMu.Lock()
	defer n.addrMu.Unlock()
	i := indexIP(n.secondary, ip)
	if i < 0 {
		return false
	}
	n.secondary = slices.Delete(n.secondary, i, i+1)
	n.subnet.ReleaseAddress(ip)
	return true
}

// Addresses lists the secondary addresses the tap holds in the order they
// were added.
func (n *TapNIC) Addresses() []net.IP {
	n.addrMu.Lock()
	defer n.addrMu.Unlock()
	return cloneIPs(n.secondary)
}

func indexIP(ips []net.IP, ip net.IP) int {
	return slices.IndexFunc(ips, func(candidate net.IP) bool { return candidate.Equal(ip) })
}

func cloneIPs(ips []net.IP) []net.IP {
	out := make([]net.IP, len(ips))
	for i, ip := range ips {
		out[i] = append(net.IP(nil), ip...)
	}
	return out
}
