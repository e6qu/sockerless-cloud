package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// elbv2NLBProxies tracks one real TCP proxy per Network Load Balancer listener
// that speaks a stream protocol (TCP / TCP_UDP). A real NLB forwards the raw
// byte stream from the listener to a healthy registered target without parsing
// HTTP; the sim realizes that by binding a real listener socket and io.Copy-ing
// both directions to the target. The ALB/HTTP data plane (elbv2_dataplane.go)
// stays HTTP-only; NLB stream listeners go through this path instead.
//
// A real NLB exposes its listeners on the load balancer's own stable DNS name
// at the configured listener port. The sim mirrors that: the proxy binds the
// listener's configured port on a stable per-NLB address (never an ephemeral
// port), and the AWS-shaped DNS name resolves to that address via injected
// hosts entries (elbv2NLBHostEntries) — the same mechanism the metadata and
// Cloud Map services use. DescribeLoadBalancers therefore always returns the
// stable AWS-shaped DNSName, and `<dnsname>:<listenerPort>` reaches the proxy.
var (
	elbv2NLBProxyMu sync.Mutex
	elbv2NLBProxies = map[string]*elbv2NLBProxy{}
	// elbv2NLBHosts holds the stable proxy bind host per load balancer ARN, so
	// every listener of an NLB binds (and is advertised) on the same address.
	elbv2NLBHosts = map[string]elbv2NLBHost{}
)

// elbv2NLBProxy pairs the running TCP proxy with the load balancer it serves and
// the loopback address (if any) leased for it, so teardown can release the lease.
type elbv2NLBProxy struct {
	proxy   *realexec.TCPProxy
	lbArn   string
	leaseIP net.IP // non-nil only when a per-NLB loopback IP was leased (Linux)
}

// elbv2NLBHost is the stable bind host chosen for a load balancer's stream
// proxies, plus the loopback lease (if one was taken) so it is released once the
// last listener for the NLB goes away.
type elbv2NLBHost struct {
	host    string
	leaseIP net.IP
}

// elbv2ListenerIsStream reports whether a listener forwards a raw byte stream
// (NLB TCP / TCP_UDP) rather than HTTP. TLS and UDP are excluded: TLS terminates
// at the listener (needs a cert + handshake the sim doesn't model) and UDP is
// not a stream, so neither is faithfully a raw-TCP byte proxy.
func elbv2ListenerIsStream(listener ELBv2Listener) bool {
	switch strings.ToUpper(listener.Protocol) {
	case "TCP", "TCP_UDP":
		return true
	default:
		return false
	}
}

// elbv2StartNLBProxy binds a real TCP listener for an NLB stream listener and
// forwards every accepted connection to a healthy registered target, chosen at
// connect time (so target (de)registration and health are honored per
// connection, like a real NLB). It binds the listener's configured port on the
// load balancer's stable per-NLB host (see elbv2NLBHostForLB), so
// `<dnsname>:<listenerPort>` — resolved through the injected hosts entries —
// reaches the proxy exactly as it reaches a real NLB. Idempotent per listener
// ARN.
func elbv2StartNLBProxy(listener ELBv2Listener) error {
	if !elbv2ListenerIsStream(listener) {
		return nil
	}
	elbv2NLBProxyMu.Lock()
	defer elbv2NLBProxyMu.Unlock()
	if _, ok := elbv2NLBProxies[listener.Arn]; ok {
		return nil
	}
	listenerArn := listener.Arn
	resolver := func(ctx context.Context) (string, error) {
		current, ok := elbv2Listeners.Get(listenerArn)
		if !ok {
			return "", fmt.Errorf("listener %s no longer exists", listenerArn)
		}
		tg, target, ok := elbv2HealthyTargetForListener(current)
		if !ok {
			return "", fmt.Errorf("no healthy targets for listener %s", listenerArn)
		}
		return elbv2TargetAddress(tg, target)
	}
	host, proxy, err := elbv2BindNLBProxy(listener, resolver)
	if err != nil {
		return err
	}
	elbv2NLBHosts[listener.LoadBalancerArn] = host
	elbv2NLBProxies[listenerArn] = &elbv2NLBProxy{proxy: proxy, lbArn: listener.LoadBalancerArn, leaseIP: host.leaseIP}
	return nil
}

// elbv2BindNLBProxy binds a stream listener's proxy on the load balancer's
// stable host at the listener's configured port. The first stream or TLS
// listener of a load balancer binds 127.0.0.1:<port> — deterministically
// reachable for any same-host client (SDK/CLI/in-process) — unless that port is
// already taken on 127.0.0.1 by another load balancer; on Linux it then leases
// a distinct 127.0.0.0/8 address per load balancer and binds there, so many
// load balancers can expose the SAME listener port (e.g. 2222) without
// colliding — the way two real NLBs each have their own address. Off Linux only
// 127.0.0.1 is bound by default, so a same-port second load balancer fails to
// bind there (the documented single-LB-per-port limit of the dev-host path);
// the proxy never lies about its address. A load balancer's later listeners
// reuse the host already chosen for it. Caller holds elbv2NLBProxyMu.
func elbv2BindNLBProxy(listener ELBv2Listener, resolver realexec.ProxyTarget) (elbv2NLBHost, *realexec.TCPProxy, error) {
	host, err := elbv2AcquireStableHost(listener.LoadBalancerArn, listener.Port)
	if err != nil {
		return elbv2NLBHost{}, nil, err
	}
	bindAddr := net.JoinHostPort(host.host, strconv.Itoa(listener.Port))
	proxy, err := realexec.StartTCPProxy(bindAddr, resolver)
	if err != nil {
		return elbv2NLBHost{}, nil, fmt.Errorf("start NLB TCP proxy for listener %s on %s: %w", listener.Arn, bindAddr, err)
	}
	elbv2NLBHosts[listener.LoadBalancerArn] = host
	return host, proxy, nil
}

// elbv2ReleaseNLBHostIfUnused drops the load balancer's stable host (and frees
// its loopback lease) once no stream or TLS listener for the load balancer is
// still running. Caller holds elbv2NLBProxyMu.
func elbv2ReleaseNLBHostIfUnused(lbArn string) {
	for _, p := range elbv2NLBProxies {
		if p.lbArn == lbArn {
			return
		}
	}
	for _, p := range elbv2TLSProxies {
		if p.lbArn == lbArn {
			return
		}
	}
	if host, ok := elbv2NLBHosts[lbArn]; ok {
		realexec.ReleaseNLBLoopbackIPv4(host.leaseIP)
		delete(elbv2NLBHosts, lbArn)
	}
}

// elbv2AcquireStableHost returns the load balancer's stable bind host (leasing
// it on first use), the single address every stream and TLS listener for the
// load balancer binds so `<dnsname>:<listenerPort>` reaches the right proxy.
// Caller holds elbv2NLBProxyMu. The host is released by
// elbv2ReleaseNLBHostIfUnused once the last listener for the LB stops.
func elbv2AcquireStableHost(lbArn string, port int) (elbv2NLBHost, error) {
	if existing, ok := elbv2NLBHosts[lbArn]; ok {
		return existing, nil
	}
	primary := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	if ln, err := net.Listen("tcp", primary); err == nil {
		_ = ln.Close()
		return elbv2NLBHost{host: "127.0.0.1"}, nil
	}
	ip, leased := realexec.ReserveNLBLoopbackIPv4(lbArn)
	if !leased {
		return elbv2NLBHost{}, fmt.Errorf("listener port %d already bound on 127.0.0.1 and no per-NLB loopback address is available on this host (one load balancer per listener port off Linux)", port)
	}
	return elbv2NLBHost{host: ip.String(), leaseIP: ip}, nil
}

// elbv2StopNLBProxy closes and forgets the TCP proxy for a listener (on
// DeleteListener / DeleteLoadBalancer). No-op if none is running.
func elbv2StopNLBProxy(listenerArn string) {
	elbv2NLBProxyMu.Lock()
	entry := elbv2NLBProxies[listenerArn]
	delete(elbv2NLBProxies, listenerArn)
	if entry != nil {
		elbv2ReleaseNLBHostIfUnused(entry.lbArn)
	}
	elbv2NLBProxyMu.Unlock()
	if entry != nil {
		_ = entry.proxy.Close()
	}
}

// elbv2NLBProxyAddress returns the host:port a client connects to in order to
// reach an NLB stream listener's data plane. A real NLB exposes the listener on
// the load balancer's DNS name at the listener port; the sim binds the listener
// port on the NLB's stable host, so this address is exactly what the AWS-shaped
// DNS name resolves to (see elbv2NLBHostEntries). Empty string if no proxy is
// running for the listener.
func elbv2NLBProxyAddress(listenerArn string) string {
	elbv2NLBProxyMu.Lock()
	defer elbv2NLBProxyMu.Unlock()
	entry := elbv2NLBProxies[listenerArn]
	if entry == nil {
		return ""
	}
	return entry.proxy.Address
}

// elbv2NLBHostEntries returns the hosts entries that make a load balancer's
// AWS-shaped DNS name resolve to its stream / TLS proxy's stable host, for
// every load balancer that has a running stream or TLS listener. A workload
// that resolves the LB's DNS name (the value DescribeLoadBalancers returns) and
// connects on the listener port therefore reaches the proxy — the faithful
// analogue of a real LB's DNS name resolving to its addresses. This is the
// production consumer of the proxy host: it is merged into workload-container
// hosts the same way the metadata and Cloud Map host entries are.
func elbv2NLBHostEntries() []sim.HostEntry {
	var entries []sim.HostEntry
	for _, lb := range elbv2LoadBalancers.List() {
		if lb.DNSName == "" {
			continue
		}
		host := elbv2NLBHostForReporting(lb.Arn)
		if host == "" {
			continue
		}
		entries = append(entries, sim.HostEntry{IP: host, Name: strings.TrimSuffix(lb.DNSName, ".")})
	}
	return entries
}

// elbv2NLBHostForReporting returns the host a load balancer's AWS-shaped DNS
// name resolves to: the host of an actually-running stream or TLS proxy for one
// of the load balancer's listeners (read back from the bound socket via
// elbv2NLBProxyAddress / elbv2TLSProxyAddress, so the advertised address can
// never drift from where the proxy really listens), or empty if the LB has no
// running stream or TLS listener.
func elbv2NLBHostForReporting(lbArn string) string {
	for _, listener := range elbv2Listeners.Filter(func(l ELBv2Listener) bool {
		return l.LoadBalancerArn == lbArn && (elbv2ListenerIsStream(l) || elbv2ListenerIsTLS(l))
	}) {
		if elbv2ListenerIsTLS(listener) {
			if host := elbv2TLSProxyHostPort(listener.Arn); host != "" {
				return host
			}
		} else if addr := elbv2NLBProxyAddress(listener.Arn); addr != "" {
			if host, _, err := net.SplitHostPort(addr); err == nil {
				return host
			}
		}
	}
	return ""
}

// elbv2WorkloadExtraHosts renders elbv2NLBHostEntries as Docker `--add-host`
// (name:ip) entries, merged onto a workload container's ExtraHosts so it can
// resolve every NLB's AWS-shaped DNS name to that NLB's stream proxy and connect
// on the listener port — the same shape the metadata/Cloud Map host entries use.
func elbv2WorkloadExtraHosts() []string {
	entries := elbv2NLBHostEntries()
	if len(entries) == 0 {
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name+":"+e.IP)
	}
	return out
}
