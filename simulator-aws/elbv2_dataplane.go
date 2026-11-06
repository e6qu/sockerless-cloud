package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
	"github.com/e6qu/sockerless-cloud/sim/revisions"
)

func registerELBv2DataPlane(srv *sim.Server) {
	elbv2LoadStickinessKey(srv)
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

// Every request into the simulator meets this lookup first, an Amazon DynamoDB
// call included, so it must not scan the load-balancer store.
var elbv2LoadBalancersByDNSName sim.GenerationIndex[ELBv2LoadBalancer]

func elbv2LoadBalancerFromDataPlaneHost(host string) (ELBv2LoadBalancer, bool) {
	hostname := lbplane.Hostname(host)
	if hostname == "" {
		return ELBv2LoadBalancer{}, false
	}
	// A load balancer still being created has no DNS name; the index drops the
	// empty key, so a request without Host cannot match it.
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
	elbv2ServeListener(w, r, listener)
}

func elbv2ForwardToHealthyTarget(w http.ResponseWriter, r *http.Request, listener ELBv2Listener, actions []ELBv2Action) {
	targetGroup, target, respond, ok := elbv2HealthyTargetForActions(r, actions)
	if !ok {
		http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
		return
	}
	address, err := elbv2TargetAddress(targetGroup, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	elbv2RecordRoutedRequest(listener.LoadBalancerArn, targetGroup, time.Now())
	scheme := "http"
	if strings.EqualFold(targetGroup.Protocol, "HTTPS") {
		scheme = "https"
	}
	done := elbv2BeginRequest(targetGroup, target)
	defer done()
	idle := elbv2IdleTimeout(listener.LoadBalancerArn)
	started := time.Now()
	sim.DeclareWait(r.Context(), idle)
	_ = lbplane.Serve(w, r, lbplane.Upstream{
		Scheme:        scheme,
		Address:       address,
		Path:          r.URL.EscapedPath(),
		RawQuery:      r.URL.RawQuery,
		Host:          elbv2TargetHostHeader(r.Host, listener),
		RequestHeader: elbv2ForwardedHeaders(r, listener, elbv2LoadBalancerAttributes(listener.LoadBalancerArn)),
		IdleTimeout:   idle,
		Activity:      func() { sim.DeclareWait(r.Context(), time.Since(started)+idle) },
		ResponseHeader: func(_ int, header http.Header) {
			for _, f := range respond {
				f(header)
			}
		},
		// "The load balancer does not validate these certificates."
		SkipTargetVerification: true,
		Admit:                  elbv2AdmitForwardedFlow(listener, targetGroup, target),
	}, elbv2ForwardAnswers)
}

var elbv2ForwardAnswers = lbplane.Answers{
	// A target whose security group closes it to the load balancer is not in
	// service.
	NotAdmitted: func(w http.ResponseWriter, _ error) {
		http.Error(w, "no healthy targets", http.StatusServiceUnavailable)
	},
	// "HTTP 504: Gateway timeout ... the target did not respond before the idle
	// timeout period elapsed."
	Timeout: func(w http.ResponseWriter, _ error) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = io.WriteString(w, elbv2GatewayTimeoutPage)
	},
	Failed: func(w http.ResponseWriter, err error) { http.Error(w, err.Error(), http.StatusBadGateway) },
}

const elbv2GatewayTimeoutPage = "<html>\r\n<head><title>504 Gateway Time-out</title></head>\r\n<body>\r\n<center><h1>504 Gateway Time-out</h1></center>\r\n</body>\r\n</html>\r\n"

// X-Forwarded-For as the xff attributes direct, the listener's protocol and
// port, and X-Amzn-Trace-Id.
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

// A new Root when none, a Self inserted ahead of an existing Root, or Self
// updated; other fields pass through.
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

// Read per request, so ModifyLoadBalancerAttributes governs the next one.
func elbv2IdleTimeout(lbArn string) time.Duration {
	seconds, err := strconv.Atoi(elbv2LoadBalancerAttributes(lbArn)["idle_timeout.timeout_seconds"])
	if err != nil {
		seconds = elbv2DefaultIdleTimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

// Every proxied request resolves its listener through this.
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

// The connected port alone selects the listener; a client's
// X-Forwarded-Proto is never routed on.
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

// AWSALBTG carries a weighted forward's target group stickiness, AWSALB a
// target group's lb_cookie stickiness.
const (
	elbv2TargetGroupStickinessCookie = "AWSALBTG"
	elbv2TargetStickinessCookie      = "AWSALB"
)

// Signing stops a client naming its own target. The key persists, so cookies
// survive a restart.
var elbv2StickinessKey []byte

func elbv2LoadStickinessKey(srv *sim.Server) {
	keys := sim.MakeStore[string](srv.DB(), "elbv2_stickiness_keys")
	keys.Upsert("cookie", func(key *string) {
		if *key == "" {
			*key = sim.RandomHex(32)
		}
	})
	key, _ := keys.Get("cookie")
	elbv2StickinessKey = []byte(key)
}

func elbv2StickyCookie(name, payload string, duration int) *http.Cookie {
	mac := hmac.New(sha256.New, elbv2StickinessKey)
	mac.Write([]byte(payload))
	value := base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: duration}
}

// Only cookies the load balancer signed.
func elbv2StickyCookiePayload(r *http.Request, name string) (string, bool) {
	if r == nil {
		return "", false
	}
	cookie, err := r.Cookie(name)
	if err != nil {
		return "", false
	}
	encoded, signature, ok := strings.Cut(cookie.Value, ".")
	if !ok {
		return "", false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false
	}
	got, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return "", false
	}
	mac := hmac.New(sha256.New, elbv2StickinessKey)
	mac.Write(payload)
	if !hmac.Equal(got, mac.Sum(nil)) {
		return "", false
	}
	return string(payload), true
}

// A weighted forward divides requests by weight; weight 0 receives nothing.
// With target group stickiness, a request carrying AWSALBTG stays with that
// group while it has a target in service; otherwise it gets the cookie for the
// weighted choice.
func elbv2HealthyTargetForActions(r *http.Request, actions []ELBv2Action) (ELBv2TargetGroup, ELBv2TargetDescription, []func(http.Header), bool) {
	for _, action := range actions {
		var groups []revisions.Weighted[string]
		switch {
		case action.Forward != nil && len(action.Forward.TargetGroups) > 0:
			for _, tuple := range action.Forward.TargetGroups {
				weight := 1
				if tuple.Weight != nil {
					weight = *tuple.Weight
				}
				groups = append(groups, revisions.Weighted[string]{Revision: tuple.TargetGroupArn, Weight: weight})
			}
		case action.TargetGroupArn != "":
			groups = append(groups, revisions.Weighted[string]{Revision: action.TargetGroupArn, Weight: 1})
		}
		var ready []revisions.Weighted[ELBv2TargetGroup]
		for _, g := range groups {
			if tg, ok := elbv2TargetGroups.Get(g.Revision); ok && len(elbv2ReadyTargets(tg)) > 0 {
				ready = append(ready, revisions.Weighted[ELBv2TargetGroup]{Revision: tg, Weight: g.Weight})
			}
		}
		var respond []func(http.Header)
		stickiness := action.Forward != nil && action.Forward.Stickiness != nil &&
			action.Forward.Stickiness.Enabled && action.Forward.Stickiness.DurationSeconds != nil
		var tg ELBv2TargetGroup
		found := false
		if stickiness {
			if arn, ok := elbv2StickyCookiePayload(r, elbv2TargetGroupStickinessCookie); ok {
				if i := slices.IndexFunc(ready, func(w revisions.Weighted[ELBv2TargetGroup]) bool { return w.Revision.Arn == arn }); i >= 0 {
					tg, found = ready[i].Revision, true
				}
			}
		}
		if !found {
			if tg, found = revisions.Pick(ready); !found {
				continue
			}
			if stickiness {
				respond = append(respond, elbv2SetCookie(elbv2StickyCookie(elbv2TargetGroupStickinessCookie, tg.Arn, *action.Forward.Stickiness.DurationSeconds)))
			}
		}
		target, targetRespond := elbv2PickTarget(r, tg)
		if targetRespond != nil {
			respond = append(respond, targetRespond)
		}
		return tg, target, respond, true
	}
	return ELBv2TargetGroup{}, ELBv2TargetDescription{}, nil, false
}

// In registration order.
func elbv2ReadyTargets(tg ELBv2TargetGroup) []ELBv2TargetDescription {
	var ready []ELBv2TargetDescription
	for _, target := range tg.Targets {
		if elbv2TargetReceivesTraffic(tg, target) {
			ready = append(ready, target)
		}
	}
	return ready
}

// Per-group request counts for the default round_robin algorithm.
var elbv2RoundRobin = struct {
	sync.Mutex
	routed map[string]uint64
}{routed: map[string]uint64{}}

func elbv2RoundRobinNext(targetGroupArn string) uint64 {
	elbv2RoundRobin.Lock()
	defer elbv2RoundRobin.Unlock()
	n := elbv2RoundRobin.routed[targetGroupArn]
	elbv2RoundRobin.routed[targetGroupArn] = n + 1
	return n
}

// elbv2LBCookieMaxAge is the expiry of a load balancer generated stickiness
// cookie, "7 days which is non-configurable" (alb-target-group-attributes);
// the cookie carries its issue time, and the stickiness duration is enforced
// against that.
const elbv2LBCookieMaxAge = 7 * 24 * 3600

// elbv2AppStickinessCookie is the load balancer generated application cookie a
// client sees "AWSALBAPP-0" for a stickiness record under 4K.
const elbv2AppStickinessCookie = "AWSALBAPP-0"

func elbv2SetCookie(cookie *http.Cookie) func(http.Header) {
	return func(header http.Header) { header.Add("Set-Cookie", cookie.String()) }
}

// elbv2PickTarget chooses a request's target and returns what to add to the
// response. A sticky request whose cookie names a ready target within the
// stickiness duration stays with it, bypassing the routing algorithm; any
// other request goes to the target the group's algorithm selects.
func elbv2PickTarget(r *http.Request, tg ELBv2TargetGroup) (ELBv2TargetDescription, func(http.Header)) {
	ready := elbv2ReadyTargets(tg)
	attrs := tg.Attributes
	kind, cookieName, durationKey := "", "", ""
	if attrs["stickiness.enabled"] == "true" {
		switch attrs["stickiness.type"] {
		case "lb_cookie":
			kind, cookieName, durationKey = "lb_cookie", elbv2TargetStickinessCookie, "stickiness.lb_cookie.duration_seconds"
		case "app_cookie":
			kind, cookieName, durationKey = "app_cookie", elbv2AppStickinessCookie, "stickiness.app_cookie.duration_seconds"
		}
	}
	duration, err := strconv.Atoi(attrs[durationKey])
	if err != nil {
		duration = 86400
	}
	var target ELBv2TargetDescription
	stuck, presented := false, false
	if kind != "" {
		if payload, ok := elbv2StickyCookiePayload(r, cookieName); ok {
			presented = true
			target, stuck = elbv2StickyTarget(tg, ready, payload, duration)
		}
	}
	if !stuck {
		target = elbv2ChooseTarget(tg, ready)
	}
	cookie := elbv2StickyCookie(cookieName, elbv2TargetStickinessPayload(tg, target)+"|"+strconv.FormatInt(time.Now().Unix(), 10), elbv2LBCookieMaxAge)
	switch kind {
	case "lb_cookie":
		return target, elbv2SetCookie(cookie)
	case "app_cookie":
		// The load balancer generates its cookie once the target sets the
		// configured application cookie, and refreshes it on every response
		// of a session it holds or moved to a new target.
		appCookie := attrs["stickiness.app_cookie.cookie_name"]
		return target, func(header http.Header) {
			if stuck || presented || elbv2ResponseSetsCookie(header, appCookie) {
				header.Add("Set-Cookie", cookie.String())
			}
		}
	}
	return target, nil
}

// elbv2StickyTarget resolves a stickiness payload, "<target payload>|<issued>",
// to the ready target it names while the stickiness duration lasts.
func elbv2StickyTarget(tg ELBv2TargetGroup, ready []ELBv2TargetDescription, payload string, duration int) (ELBv2TargetDescription, bool) {
	i := strings.LastIndex(payload, "|")
	if i < 0 {
		return ELBv2TargetDescription{}, false
	}
	issued, err := strconv.ParseInt(payload[i+1:], 10, 64)
	if err != nil || time.Since(time.Unix(issued, 0)) > time.Duration(duration)*time.Second {
		return ELBv2TargetDescription{}, false
	}
	for _, t := range ready {
		if elbv2TargetStickinessPayload(tg, t) == payload[:i] {
			return t, true
		}
	}
	return ELBv2TargetDescription{}, false
}

func elbv2ResponseSetsCookie(header http.Header, name string) bool {
	if name == "" {
		return false
	}
	for _, line := range header.Values("Set-Cookie") {
		if cookie, err := http.ParseSetCookie(line); err == nil && cookie.Name == name {
			return true
		}
	}
	return false
}

// elbv2ChooseTarget applies the group's load_balancing.algorithm.type:
// round_robin "in a sequential order", least_outstanding_requests to "the
// targets with the lowest number of in progress requests", and weighted_random
// "evenly ... in a random order".
func elbv2ChooseTarget(tg ELBv2TargetGroup, ready []ELBv2TargetDescription) ELBv2TargetDescription {
	switch tg.Attributes["load_balancing.algorithm.type"] {
	case "least_outstanding_requests":
		start := int(elbv2RoundRobinNext(tg.Arn) % uint64(len(ready)))
		best, fewest := ready[start], int64(-1)
		for k := range ready {
			t := ready[(start+k)%len(ready)]
			if n := elbv2Outstanding(tg, t); fewest < 0 || n < fewest {
				best, fewest = t, n
			}
		}
		return best
	case "weighted_random":
		return ready[rand.IntN(len(ready))]
	}
	return ready[int(elbv2RoundRobinNext(tg.Arn)%uint64(len(ready)))]
}

// In-progress requests per target, for least_outstanding_requests.
var elbv2InFlight = struct {
	sync.Mutex
	count map[string]int64
}{count: map[string]int64{}}

func elbv2Outstanding(tg ELBv2TargetGroup, target ELBv2TargetDescription) int64 {
	elbv2InFlight.Lock()
	defer elbv2InFlight.Unlock()
	return elbv2InFlight.count[elbv2TargetStickinessPayload(tg, target)]
}

// elbv2BeginRequest counts a request in progress at its target until the
// returned func runs.
func elbv2BeginRequest(tg ELBv2TargetGroup, target ELBv2TargetDescription) func() {
	key := elbv2TargetStickinessPayload(tg, target)
	elbv2InFlight.Lock()
	elbv2InFlight.count[key]++
	elbv2InFlight.Unlock()
	return func() {
		elbv2InFlight.Lock()
		defer elbv2InFlight.Unlock()
		if elbv2InFlight.count[key]--; elbv2InFlight.count[key] <= 0 {
			delete(elbv2InFlight.count, key)
		}
	}
}

func elbv2TargetStickinessPayload(tg ELBv2TargetGroup, target ELBv2TargetDescription) string {
	return tg.Arn + "|" + target.ID + "|" + strconv.Itoa(target.Port)
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
	lb, ok := elbv2LoadBalancers.Get(lbArn)
	if !ok {
		return defaultELBv2LoadBalancerAttributes("application", "")
	}
	attrs := defaultELBv2LoadBalancerAttributes(lb.Type, lb.Scheme)
	maps.Copy(attrs, lb.Attributes)
	return attrs
}

// Returns why the check failed, which the checker turns into state and reason
// code.
func elbv2ProbeTarget(ctx context.Context, tg ELBv2TargetGroup, target ELBv2TargetDescription) (int, error) {
	// "The default is to use the port on which each target receives traffic from
	// the load balancer."
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
	// A security group dropping the probe makes the check time out, as on Elastic
	// Load Balancing.
	if lb, ok := elbv2LoadBalancerForTargetGroup(tg); ok {
		if admit := elbv2TargetAdmission(lb, tg, target, target.Port); admit != nil {
			if err := admit(netip.Addr{}); err != nil {
				return 0, &errELBv2HealthCheckBlocked{reason: err}
			}
		}
	}
	// Every protocol but HTTP and HTTPS is a connection test.
	if !strings.EqualFold(protocol, "HTTP") && !strings.EqualFold(protocol, "HTTPS") {
		return 0, lbplane.ProbeTCP(ctx, address, timeout)
	}
	// "Matcher — The codes to use when checking for a successful response from a
	// target."
	codes := tg.MatcherHttpCode
	if codes == "" {
		codes = elbv2DefaultMatcher()
	}
	match, err := lbplane.ParseStatusMatcher(codes)
	if err != nil {
		return 0, fmt.Errorf("target group %s matcher: %w", tg.Arn, err)
	}
	// HTTP GET to HealthCheckPath ("The default is /."), without validating the
	// target's certificate.
	return lbplane.ProbeHTTP(ctx, lbplane.HTTPProbe{
		Scheme:  strings.ToLower(protocol),
		Address: address,
		Path:    tg.HealthCheckPath,
		Timeout: timeout,
		Match:   match,
	})
}

// The load balancer whose listener forwards to the group sends its health
// checks.
func elbv2LoadBalancerForTargetGroup(tg ELBv2TargetGroup) (ELBv2LoadBalancer, bool) {
	for _, listener := range elbv2Listeners.List() {
		for _, arn := range elbv2ActionForwardTargets(listener.DefaultActions) {
			if arn == tg.Arn {
				return elbv2LoadBalancers.Get(listener.LoadBalancerArn)
			}
		}
	}
	for _, rule := range elbv2Rules.List() {
		for _, arn := range elbv2ActionForwardTargets(rule.Actions) {
			if arn != tg.Arn {
				continue
			}
			if listener, ok := elbv2Listeners.Get(rule.ListenerArn); ok {
				return elbv2LoadBalancers.Get(listener.LoadBalancerArn)
			}
		}
	}
	return ELBv2LoadBalancer{}, false
}
