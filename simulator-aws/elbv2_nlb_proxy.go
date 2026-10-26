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

// A Network Load Balancer TCP / TCP_UDP listener forwards the raw byte stream,
// a TLS listener does so after terminating TLS, and an Application Load
// Balancer HTTPS listener terminates TLS and forwards HTTP. Each binds a real
// listener on the load balancer's stable host, and injected hosts entries
// (elbv2NLBHostEntries) resolve the DNS name there, so
// "<dnsname>:<listenerPort>" works as on AWS. Plain HTTP listeners are served
// by the simulator's front end (elbv2_dataplane.go).
var (
	elbv2ListenerProxyMu sync.Mutex
	elbv2ListenerProxies = map[string]*elbv2ListenerProxy{}
	// Every listener of one load balancer shares a bind host.
	elbv2LoadBalancerHosts = lbplane.NewHostLeases()
)

type elbv2ListenerProxy struct {
	lbArn   string
	address string
	closer  interface{ Close() error }
}

// UDP is not a stream.
func elbv2ListenerIsStream(listener ELBv2Listener) bool {
	switch strings.ToUpper(listener.Protocol) {
	case "TCP", "TCP_UDP":
		return true
	default:
		return false
	}
}

// HTTPS for an Application Load Balancer, TLS for a Network Load Balancer.
func elbv2ListenerIsTLS(listener ELBv2Listener) bool {
	switch strings.ToUpper(listener.Protocol) {
	case "HTTPS", "TLS":
		return true
	default:
		return false
	}
}

// Chooses the target per connection or request, so registration and health
// changes apply. Idempotent per listener ARN; a no-op for HTTP front-end
// listeners.
func elbv2StartListenerProxy(listener ELBv2Listener) error {
	if !elbv2ListenerIsStream(listener) && !elbv2ListenerIsTLS(listener) {
		return nil
	}
	var tlsConfig *tls.Config
	if elbv2ListenerIsTLS(listener) {
		if _, err := elbv2ListenerTLSConfig(listener); err != nil {
			return err
		}
		// Each handshake reads the current certificates, so ModifyListener and
		// AddListenerCertificates reach a running listener.
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
	// What each chosen target's security groups admit, per target address.
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

// A no-op when none is running.
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

// Read back from the bound socket so the advertised address cannot drift.
// Empty when no proxy runs.
func elbv2ListenerProxyAddress(listenerArn string) string {
	elbv2ListenerProxyMu.Lock()
	defer elbv2ListenerProxyMu.Unlock()
	entry := elbv2ListenerProxies[listenerArn]
	if entry == nil {
		return ""
	}
	return entry.address
}

// Resolves each load balancer's DNS name, as DescribeLoadBalancers returns it,
// to its running stream or TLS proxy's host.
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

// Empty when no stream or TLS listener runs.
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

// As Docker `--add-host` name:ip entries for workload containers.
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
