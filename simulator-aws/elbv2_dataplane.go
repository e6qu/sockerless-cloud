package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/realexec/lbplane"
	"github.com/e6qu/sockerless-cloud/sim"
)

func registerELBv2DataPlane(srv *sim.Server) {
	srv.WrapHandler(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if lb, ok := elbv2LoadBalancerFromDataPlaneHost(r.Host); ok {
				handleELBv2DataPlane(w, r, lb)
				return
			}
			next.ServeHTTP(w, r)
		})
	})
}

// elbv2LoadBalancersByDNSName indexes load balancers by the hostname their
// data plane answers on.
//
// This lookup is the first thing every request into the simulator meets: the
// middleware above runs ahead of every service's handler, so an Amazon DynamoDB
// call pays it too. Answering it by reading and JSON-decoding the whole
// load-balancer store would cost every request a scan.
var elbv2LoadBalancersByDNSName sim.GenerationIndex[ELBv2LoadBalancer]

func elbv2LoadBalancerFromDataPlaneHost(host string) (ELBv2LoadBalancer, bool) {
	hostname := lbplane.Hostname(host)
	if hostname == "" {
		return ELBv2LoadBalancer{}, false
	}
	// A load balancer still being created has no DNS name yet and answers on
	// nothing; the index drops the empty key, so a malformed request with no
	// Host header cannot match it.
	return elbv2LoadBalancersByDNSName.Lookup(elbv2LoadBalancers, hostname,
		func(lb ELBv2LoadBalancer) []string {
			return []string{lbplane.Hostname(lb.DNSName)}
		})
}

func handleELBv2DataPlane(w http.ResponseWriter, r *http.Request, lb ELBv2LoadBalancer) {
	if !wafAssociatedRequestAllowed(lb.Arn, r) {
		http.Error(w, "AWS WAF blocked the request", http.StatusForbidden)
		return
	}
	listener, ok := elbv2ListenerForDataPlaneRequest(r, lb)
	if !ok {
		http.Error(w, "no matching load balancer listener", http.StatusNotFound)
		return
	}
	elbv2ForwardToHealthyTarget(w, r, listener)
}

// elbv2ForwardToHealthyTarget forwards a request a listener accepted to a
// target in service, which is the whole of what an HTTP or HTTPS listener does
// once it has decoded the request.
func elbv2ForwardToHealthyTarget(w http.ResponseWriter, r *http.Request, listener ELBv2Listener) {
	targetGroup, target, ok := elbv2HealthyTargetForListener(listener)
	if !ok {
		http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
		return
	}
	address, err := elbv2TargetAddress(targetGroup, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	scheme := "http"
	if strings.EqualFold(targetGroup.Protocol, "HTTPS") {
		scheme = "https"
	}
	const exchangeTimeout = 30 * time.Second
	sim.DeclareWait(r.Context(), exchangeTimeout)
	err = lbplane.Forward(w, r, lbplane.Upstream{
		Scheme:   scheme,
		Address:  address,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
		Host:     elbv2TargetHostHeader(r.Host, listener),
		Timeout:  exchangeTimeout,
		// "The load balancer establishes TLS connections with the targets
		// using certificates that you install on the targets. The load balancer
		// does not validate these certificates."
		SkipTargetVerification: true,
	})
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		// Nothing reaches a client that has gone; the status records the
		// abandoned request the way the load balancer access logs do.
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

// elbv2ListenersByLoadBalancerPort indexes listeners by the load balancer and
// port that select one, which is what every proxied request resolves before it
// is forwarded.
var elbv2ListenersByLoadBalancerPort sim.GenerationIndex[ELBv2Listener]

func elbv2ListenerKey(loadBalancerArn string, port int) string {
	return loadBalancerArn + "\x00" + strconv.Itoa(port)
}

func elbv2ListenerForDataPlaneRequest(r *http.Request, lb ELBv2LoadBalancer) (ELBv2Listener, bool) {
	return elbv2ListenersByLoadBalancerPort.Lookup(elbv2Listeners,
		elbv2ListenerKey(lb.Arn, elbv2DataPlaneListenerPort(r)),
		func(l ELBv2Listener) []string {
			return []string{elbv2ListenerKey(l.LoadBalancerArn, l.Port)}
		})
}

func elbv2DataPlaneListenerPort(r *http.Request) int {
	if proto := r.Header.Get("X-Forwarded-Proto"); strings.EqualFold(proto, "https") {
		return 443
	}
	if r.TLS != nil {
		return 443
	}
	if _, port, err := net.SplitHostPort(r.Host); err == nil {
		if parsed, perr := strconv.Atoi(port); perr == nil {
			return parsed
		}
	}
	return 80
}

// elbv2HealthyTargetForListener picks a target the load balancer may forward
// to. A load balancer routes from the health its checker maintains — "Each
// load balancer node routes requests only to the healthy targets in the
// enabled Availability Zones" — rather than checking a target because a
// request arrived for it.
func elbv2HealthyTargetForListener(listener ELBv2Listener) (ELBv2TargetGroup, ELBv2TargetDescription, bool) {
	for _, action := range listener.DefaultActions {
		if action.TargetGroupArn == "" {
			continue
		}
		tg, ok := elbv2TargetGroups.Get(action.TargetGroupArn)
		if !ok {
			continue
		}
		for _, target := range tg.Targets {
			if elbv2TargetReceivesTraffic(tg, target) {
				return tg, target, true
			}
		}
	}
	return ELBv2TargetGroup{}, ELBv2TargetDescription{}, false
}

func elbv2TargetHostHeader(incomingHost string, listener ELBv2Listener) string {
	host := incomingHost
	if attr, ok := elbv2LoadBalancerAttributes(listener.LoadBalancerArn)["routing.http.preserve_host_header.enabled"]; ok && strings.EqualFold(attr, "true") {
		return host
	}
	hostname := host
	port := ""
	if h, p, err := net.SplitHostPort(host); err == nil {
		hostname = h
		port = p
	}
	hostname = strings.ToLower(hostname)
	if port != "" {
		return net.JoinHostPort(hostname, port)
	}
	if listener.Port != 80 && listener.Port != 443 {
		return net.JoinHostPort(hostname, strconv.Itoa(listener.Port))
	}
	return hostname
}

func elbv2LoadBalancerAttributes(lbArn string) map[string]string {
	attrs := defaultELBv2LoadBalancerAttributes()
	if lb, ok := elbv2LoadBalancers.Get(lbArn); ok {
		for key, value := range lb.Attributes {
			attrs[key] = value
		}
	}
	return attrs
}

// elbv2ProbeTarget runs one health check against a target and reports why it
// failed, which is what the target health checker turns into the state and
// reason code DescribeTargetHealth reports.
func elbv2ProbeTarget(ctx context.Context, tg ELBv2TargetGroup, target ELBv2TargetDescription) (int, error) {
	// "HealthCheckPort — The port the load balancer uses when performing health
	// checks on targets. The default is to use the port on which each target
	// receives traffic from the load balancer."
	target.Port = elbv2EffectiveHealthCheckPort(tg, target)
	address, err := elbv2TargetAddress(tg, target)
	if err != nil {
		return 0, err
	}
	protocol := tg.HealthCheckProtocol
	if protocol == "" || strings.EqualFold(protocol, "traffic-port") {
		protocol = tg.Protocol
	}
	timeout := time.Duration(tg.HealthCheckTimeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	// Every health-check protocol but HTTP and HTTPS is a connection test,
	// which is what a dial proves.
	if !strings.EqualFold(protocol, "HTTP") && !strings.EqualFold(protocol, "HTTPS") {
		return 0, lbplane.ProbeTCP(ctx, address, timeout)
	}
	// "Matcher — The codes to use when checking for a successful response
	// from a target."
	codes := tg.MatcherHttpCode
	if codes == "" {
		codes = elbv2DefaultMatcher()
	}
	match, err := lbplane.ParseStatusMatcher(codes)
	if err != nil {
		return 0, fmt.Errorf("target group %s matcher: %w", tg.Arn, err)
	}
	// "These protocols use the HTTP GET method to send health check
	// requests", to HealthCheckPath — "The default is /." The load balancer
	// does not validate the target's certificate.
	return lbplane.ProbeHTTP(ctx, lbplane.HTTPProbe{
		Scheme:  strings.ToLower(protocol),
		Address: address,
		Path:    tg.HealthCheckPath,
		Timeout: timeout,
		Match:   match,
	})
}
