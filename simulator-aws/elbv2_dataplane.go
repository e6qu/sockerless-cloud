package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
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

func elbv2LoadBalancerFromDataPlaneHost(host string) (ELBv2LoadBalancer, bool) {
	hostname := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostname = h
	}
	hostname = strings.TrimSuffix(strings.ToLower(hostname), ".")
	for _, lb := range elbv2LoadBalancers.List() {
		if strings.EqualFold(strings.TrimSuffix(lb.DNSName, "."), hostname) {
			return lb, true
		}
	}
	return ELBv2LoadBalancer{}, false
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
	if err := elbv2ProxyHTTPRequest(w, r, listener, targetGroup, address); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
}

func elbv2ListenerForDataPlaneRequest(r *http.Request, lb ELBv2LoadBalancer) (ELBv2Listener, bool) {
	port := elbv2DataPlaneListenerPort(r)
	for _, listener := range elbv2Listeners.Filter(func(l ELBv2Listener) bool {
		return l.LoadBalancerArn == lb.Arn && l.Port == port
	}) {
		return listener, true
	}
	return ELBv2Listener{}, false
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

// returnRedirectsToClient stops a forwarding client following a target's
// redirect. A load balancer hands the 3xx back to the caller; it never chases
// one itself. Following it fetches the redirect TARGET and answers with that
// instead, and because the forwarding client keeps no cookie jar, any
// Set-Cookie the redirect carried is discarded on the way. That silently breaks
// every OpenID Connect sign-in behind the data plane: the browser gets a 200 at
// the callback URL with no session and no error to explain it.
//
// Go replays a redirected request only when it can rewind the body. A request
// forwarded from a server has no rewindable body, so in production 307 and 308
// happen to survive while 301, 302 and 303 — the set every OpenID Connect
// library uses — are followed. That accident is why the live symptom looked
// selective; the defect itself is not status-specific.
func returnRedirectsToClient(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

func elbv2ProxyHTTPRequest(w http.ResponseWriter, r *http.Request, listener ELBv2Listener, tg ELBv2TargetGroup, address string) error {
	scheme := "http"
	if strings.EqualFold(tg.Protocol, "HTTPS") {
		scheme = "https"
	}
	upstreamURL := url.URL{
		Scheme:   scheme,
		Host:     address,
		Path:     r.URL.EscapedPath(),
		RawQuery: r.URL.RawQuery,
	}
	// A real ALB carries WebSockets, so the deadlines that bound an ordinary
	// request must not apply to an upgrade: those connections are meant to last
	// for hours, and a 30s cap would cut every one of them.
	ctx := r.Context()
	upgrade := sim.IsUpgradeRequest(r)
	if !upgrade {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, upstreamURL.String(), r.Body)
	if err != nil {
		return err
	}
	req.Header = r.Header.Clone()
	req.Host = elbv2TargetHostHeader(r.Host, listener)
	client := http.Client{CheckRedirect: returnRedirectsToClient}
	if !upgrade {
		client.Timeout = 30 * time.Second
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("forward to target %s: %w", address, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		return sim.TunnelUpgradedResponse(w, resp)
	}
	for key, values := range resp.Header {
		for _, value := range values {
			w.Header().Add(key, value)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, err = io.Copy(w, resp.Body)
	return err
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
func elbv2ProbeTarget(ctx context.Context, tg ELBv2TargetGroup, target ELBv2TargetDescription) error {
	// "HealthCheckPort — The port the load balancer uses when performing health
	// checks on targets. The default is to use the port on which each target
	// receives traffic from the load balancer."
	target.Port = elbv2EffectiveHealthCheckPort(tg, target)
	address, err := elbv2TargetAddress(tg, target)
	if err != nil {
		return err
	}
	protocol := tg.HealthCheckProtocol
	if protocol == "" || strings.EqualFold(protocol, "traffic-port") {
		protocol = tg.Protocol
	}
	timeout := time.Duration(tg.HealthCheckTimeout) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	// An HTTP or HTTPS health check is graded against the target group's
	// Matcher — "The codes to use when checking for a successful response from
	// a target" — so the check has to read the response code rather than treat
	// any answer as an answer. Every other health-check protocol is a
	// connection test, which is what a dial proves.
	if strings.EqualFold(protocol, "HTTP") || strings.EqualFold(protocol, "HTTPS") {
		return elbv2ProbeHTTPTarget(ctx, tg, protocol, address, timeout)
	}
	return realexec.ProbeTarget(ctx, realexec.ProbeSpec{
		Protocol: protocol,
		Address:  address,
		Timeout:  timeout,
	})
}

// elbv2ResponseCodeMismatch is a health check that reached the target and read
// an answer the target group's Matcher does not count as a success.
type elbv2ResponseCodeMismatch struct {
	StatusCode int
}

func (e elbv2ResponseCodeMismatch) Error() string {
	return fmt.Sprintf("health check returned HTTP %d, which the target group's success codes exclude", e.StatusCode)
}

// elbv2ProbeHTTPTarget runs one HTTP or HTTPS health check and grades the
// response code against the target group's Matcher.
func elbv2ProbeHTTPTarget(ctx context.Context, tg ELBv2TargetGroup, protocol, address string, timeout time.Duration) error {
	scheme := "http"
	transport := http.DefaultTransport
	if strings.EqualFold(protocol, "HTTPS") {
		scheme = "https"
		// "The load balancer establishes TLS connections with the targets using
		// certificates that you install on the targets. The load balancer does
		// not validate these certificates. Therefore, you can use self-signed
		// certificates or certificates that have expired."
		transport = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}
	// "HealthCheckPath — The destination for health checks on the targets ...
	// The default is /."
	path := tg.HealthCheckPath
	if path == "" {
		path = "/"
	}
	// "These protocols use the HTTP GET method to send health check requests."
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target := url.URL{Scheme: scheme, Host: address, Path: path}
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	client := http.Client{Timeout: timeout, Transport: transport}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	// "After each health check is completed, the load balancer node closes the
	// connection that was established for the health check."
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if !elbv2HealthCheckCodeMatches(tg, resp.StatusCode) {
		return elbv2ResponseCodeMismatch{StatusCode: resp.StatusCode}
	}
	return nil
}

// elbv2HealthCheckCodeMatches reports whether a health check response code is
// one the target group's Matcher counts as a success: "You can specify multiple
// values (for example, "200,202") or a range of values (for example,
// "200-299"). The default value is 200."
func elbv2HealthCheckCodeMatches(tg ELBv2TargetGroup, statusCode int) bool {
	codes := tg.MatcherHttpCode
	if codes == "" {
		codes = elbv2DefaultMatcher()
	}
	for _, value := range strings.Split(codes, ",") {
		low, high, isRange := strings.Cut(strings.TrimSpace(value), "-")
		first, err := strconv.Atoi(strings.TrimSpace(low))
		if err != nil {
			continue
		}
		last := first
		if isRange {
			if last, err = strconv.Atoi(strings.TrimSpace(high)); err != nil {
				continue
			}
		}
		if statusCode >= first && statusCode <= last {
			return true
		}
	}
	return false
}
