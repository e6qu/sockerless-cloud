package realexec

import (
	"net"
	"runtime"
	"sync"
)

// nlbLoopbackPool hands out stable per-load-balancer loopback addresses so each
// Network Load Balancer's stream proxy can bind the listener's configured port
// on its own address — exactly as a real NLB exposes the listener on the load
// balancer's own IP, letting many NLBs share a listener port (e.g. 2222)
// without colliding. Every address in 127.0.0.0/8 is bindable on Linux, so a
// distinct 127.x.y.z per NLB is a faithful local stand-in for the NLB's
// dedicated address. The whole 127.0.0.0/8 is the pool; 127.0.0.1 (the gateway
// reservation) stays out so it never shadows the host's primary loopback.
var nlbLoopbackPool = struct {
	sync.Mutex
	ipam *IPAM
}{
	ipam: mustNLBLoopbackIPAM(),
}

func mustNLBLoopbackIPAM() *IPAM {
	ipam, err := NewIPAM("127.0.0.0/8", net.IPv4(127, 0, 0, 1))
	if err != nil {
		panic(err)
	}
	return ipam
}

// ReserveNLBLoopbackIPv4 leases a stable loopback address for a Network Load
// Balancer's stream proxy. It is meaningful only on Linux, where every
// 127.0.0.0/8 address is locally bindable; on other platforms (notably macOS,
// where only 127.0.0.1 is bound by default) it returns ok=false and the caller
// binds the listener port on 127.0.0.1 instead — accepting the one-NLB-per-port
// limit the comment in elbv2StartNLBProxy documents. The address is owned until
// ReleaseNLBLoopbackIPv4 is called (on listener teardown).
func ReserveNLBLoopbackIPv4(owner string) (net.IP, bool) {
	if runtime.GOOS != "linux" {
		return nil, false
	}
	nlbLoopbackPool.Lock()
	defer nlbLoopbackPool.Unlock()
	ip, err := nlbLoopbackPool.ipam.Reserve(owner, nil)
	if err != nil {
		return nil, false
	}
	return ip, true
}

// ReleaseNLBLoopbackIPv4 frees a leased NLB loopback address (no-op for nil).
func ReleaseNLBLoopbackIPv4(ip net.IP) {
	if ip == nil {
		return
	}
	nlbLoopbackPool.Lock()
	defer nlbLoopbackPool.Unlock()
	nlbLoopbackPool.ipam.Release(ip)
}
