package main

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	idle := elbv2IdleTimeout(listener.LoadBalancerArn)
	started := time.Now()
	sim.DeclareWait(r.Context(), idle)
	err = lbplane.Forward(w, r, lbplane.Upstream{
		Scheme:        scheme,
		Address:       address,
		Path:          r.URL.EscapedPath(),
		RawQuery:      r.URL.RawQuery,
		Host:          elbv2TargetHostHeader(r.Host, listener),
		RequestHeader: elbv2ForwardedHeaders(r, listener, elbv2LoadBalancerAttributes(listener.LoadBalancerArn)),
		IdleTimeout:   idle,
		Activity:      func() { sim.DeclareWait(r.Context(), time.Since(started)+idle) },
		// "The load balancer establishes TLS connections with the targets
		// using certificates that you install on the targets. The load balancer
		// does not validate these certificates."
		SkipTargetVerification: true,
	})
	abortStartedForward(w, err)
	switch {
	case err == nil:
	case errors.Is(err, lbplane.ErrClientWentAway):
		// Nothing reaches a client that has gone; the status records the
		// abandoned request the way the load balancer access logs do.
		w.WriteHeader(lbplane.StatusClientClosedRequest)
	case errors.Is(err, lbplane.ErrIdleTimeout) && errors.As(err, new(*lbplane.SendError)):
		// "HTTP 504: Gateway timeout ... The load balancer established a
		// connection to the target but the target did not respond before the
		// idle timeout period elapsed."
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, elbv2GatewayTimeoutPage)
	default:
		http.Error(w, err.Error(), http.StatusBadGateway)
	}
}

const elbv2GatewayTimeoutPage = "<html>\r\n<head><title>504 Gateway Time-out</title></head>\r\n<body>\r\n<center><h1>504 Gateway Time-out</h1></center>\r\n</body>\r\n</html>\r\n"

// elbv2ForwardedHeaders adds the headers an Application Load Balancer sends
// every target: X-Forwarded-For as routing.http.xff_header_processing.mode and
// routing.http.xff_client_port.enabled direct, the protocol and port of the
// listener the client connected to, and X-Amzn-Trace-Id.
func elbv2ForwardedHeaders(r *http.Request, listener ELBv2Listener, attrs map[string]string) func(http.Header) {
	clientIP, clientPort := lbplane.ClientAddress(r)
	return func(h http.Header) {
		switch attrs["routing.http.xff_header_processing.mode"] {
		case "append":
			client := clientIP
			if attrs["routing.http.xff_client_port.enabled"] == "true" {
				client = net.JoinHostPort(clientIP, clientPort)
			}
			lbplane.AppendToHeaderList(h, "X-Forwarded-For", ", ", client)
		case "remove":
			h.Del("X-Forwarded-For")
		}
		h.Set("X-Forwarded-Proto", strings.ToLower(listener.Protocol))
		h.Set("X-Forwarded-Port", strconv.Itoa(listener.Port))
		h.Set("X-Amzn-Trace-Id", elbv2TraceID(h.Get("X-Amzn-Trace-Id"), time.Now()))
	}
}

// elbv2TraceID is the X-Amzn-Trace-Id the load balancer forwards: a new Root
// when the request carries none, a Self inserted ahead of a Root the request
// carries, or its Self updated. Every other field passes through.
func elbv2TraceID(existing string, now time.Time) string {
	id := fmt.Sprintf("1-%08x-%s", now.Unix(), lbplane.RandomHex(12))
	if existing == "" {
		return "Root=" + id
	}
	fields := strings.Split(existing, ";")
	hasRoot := false
	for i, field := range fields {
		switch name, _, _ := strings.Cut(strings.TrimSpace(field), "="); name {
		case "Self":
			fields[i] = "Self=" + id
			return strings.Join(fields, ";")
		case "Root":
			hasRoot = true
		}
	}
	if hasRoot {
		return "Self=" + id + ";" + existing
	}
	return "Root=" + id + ";" + existing
}

// elbv2IdleTimeout reads the load balancer's idle_timeout.timeout_seconds for
// each request, so ModifyLoadBalancerAttributes governs the next one.
func elbv2IdleTimeout(lbArn string) time.Duration {
	seconds, err := strconv.Atoi(elbv2LoadBalancerAttributes(lbArn)["idle_timeout.timeout_seconds"])
	if err != nil {
		seconds = elbv2DefaultIdleTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
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

// elbv2DataPlaneListenerPort is the port the client connected to, which alone
// selects the listener: a client's X-Forwarded-Proto is a claim the load
// balancer forwards, never one it routes on.
func elbv2DataPlaneListenerPort(r *http.Request) int {
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
