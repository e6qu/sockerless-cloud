package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// A Network Load Balancer stream listener (TCP / TCP_UDP) forwards the raw byte
// stream to a healthy target without parsing it; a TLS listener does the same
// after terminating TLS with the listener's certificates; and an Application
// Load Balancer HTTPS listener terminates TLS and forwards the decrypted HTTP
// requests. Each binds a real listener at its configured port on the load
// balancer's stable host, and the load balancer's AWS-shaped DNS name resolves
// to that host through injected hosts entries (elbv2NLBHostEntries) — the same
// mechanism the metadata and Cloud Map services use — so
// "<dnsname>:<listenerPort>" reaches the listener exactly as it reaches a real
// load balancer. Plain HTTP listeners are served by the simulator's own HTTP
// front end (elbv2_dataplane.go) instead.
var (
	elbv2ListenerProxyMu sync.Mutex
	elbv2ListenerProxies = map[string]*elbv2ListenerProxy{}
	// elbv2LoadBalancerHosts gives every listener of one load balancer the same
	// bind host.
	elbv2LoadBalancerHosts = lbplane.NewHostLeases()
)

// elbv2ListenerProxy is one running listener and the load balancer whose host
// lease it holds.
type elbv2ListenerProxy struct {
	lbArn   string
	address string
	closer  interface{ Close() error }
}

// elbv2ListenerIsStream reports whether a listener forwards a raw byte stream
// (NLB TCP / TCP_UDP) rather than HTTP. UDP is not a stream, so it is not a
// raw-TCP byte proxy.
func elbv2ListenerIsStream(listener ELBv2Listener) bool {
	switch strings.ToUpper(listener.Protocol) {
	case "TCP", "TCP_UDP":
		return true
	default:
		return false
	}
}

// elbv2ListenerIsTLS reports whether a listener terminates TLS at the load
// balancer (HTTPS for an ALB, TLS for an NLB).
func elbv2ListenerIsTLS(listener ELBv2Listener) bool {
	switch strings.ToUpper(listener.Protocol) {
	case "HTTPS", "TLS":
		return true
	default:
		return false
	}
}

// elbv2StartListenerProxy binds the real listener behind a stream, TLS or HTTPS
// listener, choosing the target per connection (or per request) so target
// registration and health apply as they change. Idempotent per listener ARN;
// a no-op for a listener the HTTP front end serves.
func elbv2StartListenerProxy(listener ELBv2Listener) error {
	if !elbv2ListenerIsStream(listener) && !elbv2ListenerIsTLS(listener) {
		return nil
	}
	var tlsConfig *tls.Config
	if elbv2ListenerIsTLS(listener) {
		if _, err := elbv2ListenerTLSConfig(listener); err != nil {
			return err
		}
		// Each handshake takes the certificates the listener holds then, so
		// ModifyListener and AddListenerCertificates reach a running listener.
		arn := listener.Arn
		tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			current, ok := elbv2Listeners.Get(arn)
			if !ok {
				return nil, fmt.Errorf("listener %s no longer exists", arn)
			}
			return elbv2ListenerTLSConfig(current)
		}}
	}
	elbv2ListenerProxyMu.Lock()
	defer elbv2ListenerProxyMu.Unlock()
	if _, ok := elbv2ListenerProxies[listener.Arn]; ok {
		return nil
	}
	host, err := elbv2LoadBalancerHosts.Acquire(listener.LoadBalancerArn, listener.Port)
	if err != nil {
		return err
	}
	bindAddr := net.JoinHostPort(host, strconv.Itoa(listener.Port))
	listenerArn := listener.Arn
	// admissions remembers, per target address the resolver chose, what the
	// target's security groups admit, for the proxy's admission of the flow.
	var admissions sync.Map
	resolver := func(context.Context) (string, error) {
		current, ok := elbv2Listeners.Get(listenerArn)
		if !ok {
			return "", fmt.Errorf("listener %s no longer exists", listenerArn)
		}
		tg, target, ok := elbv2HealthyTargetForListener(current)
		if !ok {
			return "", fmt.Errorf("no healthy targets for listener %s", listenerArn)
		}
		address, err := elbv2TargetAddress(tg, target)
		if err != nil {
			return "", err
		}
		if admit := elbv2AdmitForwardedFlow(current, tg, target); admit != nil {
			admissions.Store(address, admit)
		} else {
			admissions.Delete(address)
		}
		return address, nil
	}
	admit := func(client netip.Addr, target string) error {
		if v, ok := admissions.Load(target); ok {
			if rules, ok := v.(func(netip.Addr) error); ok {
				return rules(client)
			}
		}
		return nil
	}
	proxy := &elbv2ListenerProxy{lbArn: listener.LoadBalancerArn}
	switch {
	case strings.EqualFold(listener.Protocol, "HTTPS"):
		srv, err := lbplane.StartHTTPSServer(bindAddr, tlsConfig, elbv2HTTPSListenerHandler(listenerArn), elbv2IdleTimeout(listener.LoadBalancerArn))
		if err != nil {
			elbv2LoadBalancerHosts.Release(listener.LoadBalancerArn)
			return fmt.Errorf("bind HTTPS listener %s on %s: %w", listenerArn, bindAddr, err)
		}
		proxy.address, proxy.closer = srv.Address, srv
	case tlsConfig != nil:
		tcp, err := lbplane.StartTLSProxy(bindAddr, tlsConfig, resolver)
		if err != nil {
			elbv2LoadBalancerHosts.Release(listener.LoadBalancerArn)
			return fmt.Errorf("bind TLS listener %s on %s: %w", listenerArn, bindAddr, err)
		}
		tcp.SetAdmit(admit)
		proxy.address, proxy.closer = tcp.Address, tcp
	default:
		tcp, err := lbplane.StartTCPProxy(bindAddr, resolver)
		if err != nil {
			elbv2LoadBalancerHosts.Release(listener.LoadBalancerArn)
			return fmt.Errorf("start NLB TCP proxy for listener %s on %s: %w", listenerArn, bindAddr, err)
		}
		tcp.SetAdmit(admit)
		proxy.address, proxy.closer = tcp.Address, tcp
	}
	elbv2ListenerProxies[listenerArn] = proxy
	return nil
}

// elbv2StopListenerProxy closes and forgets the listener's proxy (on
// DeleteListener, DeleteLoadBalancer, or a ModifyListener restart). A no-op if
// none is running.
func elbv2StopListenerProxy(listenerArn string) {
	elbv2ListenerProxyMu.Lock()
	entry := elbv2ListenerProxies[listenerArn]
	delete(elbv2ListenerProxies, listenerArn)
	elbv2ListenerProxyMu.Unlock()
	if entry == nil {
		return
	}
	_ = entry.closer.Close()
	elbv2LoadBalancerHosts.Release(entry.lbArn)
}

// elbv2ListenerProxyAddress returns the host:port a client connects to in
// order to reach a stream, TLS or HTTPS listener, read back from the bound
// socket so the advertised address can never drift from where the proxy
// really listens. Empty if no proxy is running for the listener.
func elbv2ListenerProxyAddress(listenerArn string) string {
	elbv2ListenerProxyMu.Lock()
	defer elbv2ListenerProxyMu.Unlock()
	entry := elbv2ListenerProxies[listenerArn]
	if entry == nil {
		return ""
	}
	return entry.address
}

// elbv2NLBHostEntries returns the hosts entries that make a load balancer's
// AWS-shaped DNS name resolve to its stream / TLS proxy's stable host, for
// every load balancer that has a running stream or TLS listener. A workload
// that resolves the LB's DNS name (the value DescribeLoadBalancers returns) and
// connects on the listener port therefore reaches the proxy — the faithful
// analogue of a real LB's DNS name resolving to its addresses.
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
// of the load balancer's listeners, or empty if it has none.
func elbv2NLBHostForReporting(lbArn string) string {
	for _, listener := range elbv2Listeners.Filter(func(l ELBv2Listener) bool {
		return l.LoadBalancerArn == lbArn && (elbv2ListenerIsStream(l) || elbv2ListenerIsTLS(l))
	}) {
		if addr := elbv2ListenerProxyAddress(listener.Arn); addr != "" {
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
