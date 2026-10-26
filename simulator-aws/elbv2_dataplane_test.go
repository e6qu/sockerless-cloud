package main

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/lbplane"
)

// The data plane reads AWS WAF and Amazon ECS stores that their registrations
// populate; registering them makes the test independent of ordering.
func newELBv2DataPlaneServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	jsonRouter := NewAWSRouter()
	registerELBv2(NewAWSQueryRouter(), srv)
	registerWAFv2(jsonRouter, srv)
	registerECS(jsonRouter, srv)
	registerELBv2DataPlane(srv)
	// Stop the health checker with the test, or it keeps checking the next test's
	// stores.
	t.Cleanup(srv.StopBackground)
	return srv
}

func TestELBv2DataPlaneRoutesOnlyLoadBalancerHosts(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	var targetHostHeader string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		targetHostHeader = r.Host
		_, _ = w.Write([]byte("elbv2-data-plane"))
	}))
	defer target.Close()
	parsedTargetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL %s: %v", target.URL, err)
	}
	targetHost, targetPortText, err := net.SplitHostPort(parsedTargetURL.Host)
	if err != nil {
		t.Fatalf("target URL host has no port: %s: %v", target.URL, err)
	}
	targetPort, err := strconv.Atoi(targetPortText)
	if err != nil {
		t.Fatalf("target URL port is not numeric: %s: %v", target.URL, err)
	}

	lb := ELBv2LoadBalancer{
		Arn:     "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/test-lb/abc123",
		Name:    "test-lb",
		DNSName: "test-lb-abc123.elb.us-east-1.amazonaws.com",
		Type:    "application",
	}
	tg := ELBv2TargetGroup{
		Arn:                 "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/test-tg/abc123",
		Protocol:            "HTTP",
		Port:                80,
		HealthCheckProtocol: "HTTP",
		HealthCheckPath:     "/healthz",
		HealthCheckTimeout:  2,
		HealthCheckEnabled:  true,
		Targets: []ELBv2TargetDescription{{
			ID:   targetHost,
			Port: targetPort,
		}},
	}
	listener := ELBv2Listener{
		Arn:             "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/test-lb/abc123/def456",
		LoadBalancerArn: lb.Arn,
		Protocol:        "HTTP",
		Port:            80,
		DefaultActions: []ELBv2Action{{
			Type:           "forward",
			TargetGroupArn: tg.Arn,
		}},
	}
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	// Only a forwarded-to group is health-checked and only in-service targets get
	// traffic, so add the listener, then the check the checker would have run.
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	req := httptest.NewRequest(http.MethodGet, "http://simulator/proxy-check", nil)
	req.Host = lb.DNSName
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("ELBv2 data-plane status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "elbv2-data-plane" {
		t.Fatalf("ELBv2 data-plane body = %q", rr.Body.String())
	}
	if targetHostHeader != strings.ToLower(lb.DNSName) {
		t.Fatalf("ELBv2 target Host header = %q, want %q", targetHostHeader, strings.ToLower(lb.DNSName))
	}
	if stored, ok := elbv2Listeners.Get(listener.Arn); !ok || stored.Port != listener.Port || stored.LoadBalancerArn != listener.LoadBalancerArn {
		t.Fatalf("ELBv2 data-plane request mutated listener state: %+v found=%t", stored, ok)
	}
	if stored, ok := elbv2LoadBalancers.Get(lb.Arn); !ok || stored.DNSName != lb.DNSName || stored.Arn != lb.Arn {
		t.Fatalf("ELBv2 data-plane request mutated load-balancer state: %+v found=%t", stored, ok)
	}

	unknownHostReq := httptest.NewRequest(http.MethodGet, "http://simulator/proxy-check", nil)
	unknownHostReq.Host = "not-an-elb.localhost"
	unknownHostRR := httptest.NewRecorder()
	srv.ServeHTTP(unknownHostRR, unknownHostReq)
	if unknownHostRR.Code == http.StatusOK && strings.Contains(unknownHostRR.Body.String(), "elbv2-data-plane") {
		t.Fatalf("non-ELB host was routed to ELBv2 data plane")
	}
}

func TestELBv2DataPlaneDoesNotInterceptControlPlaneHost(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	srv.HandleFunc("POST /", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("query-control-plane"))
	})

	req := httptest.NewRequest(http.MethodPost, "http://localhost/", strings.NewReader("Action=DescribeLoadBalancers&Version=2015-12-01"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("control-plane status = %d, body = %q", rr.Code, rr.Body.String())
	}
	if strings.TrimSpace(rr.Body.String()) != "query-control-plane" {
		t.Fatalf("control-plane body = %q", rr.Body.String())
	}
}

func TestELBv2TargetHostHeaderMatchesALBDefaults(t *testing.T) {
	// The header helper reads the load-balancer store too.
	newELBv2DataPlaneServer(t)

	lb := ELBv2LoadBalancer{
		Arn:        "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/test-lb/abc123",
		Name:       "test-lb",
		DNSName:    "test-lb-abc123.elb.us-east-1.amazonaws.com",
		Type:       "application",
		Attributes: defaultELBv2LoadBalancerAttributes("application", "internet-facing"),
	}
	elbv2LoadBalancers.Put(lb.Arn, lb)

	listener := ELBv2Listener{LoadBalancerArn: lb.Arn, Port: 80}
	if got := elbv2TargetHostHeader("TEST-LB-ABC123.ELB.US-EAST-1.AMAZONAWS.COM", listener); got != strings.ToLower(lb.DNSName) {
		t.Fatalf("default-port Host header = %q", got)
	}

	listener.Port = 8080
	if got := elbv2TargetHostHeader("Example.COM", listener); got != "example.com:8080" {
		t.Fatalf("non-default-port Host header = %q", got)
	}

	lb.Attributes["routing.http.preserve_host_header.enabled"] = "true"
	elbv2LoadBalancers.Put(lb.Arn, lb)
	if got := elbv2TargetHostHeader("Example.COM", listener); got != "Example.COM" {
		t.Fatalf("preserved Host header = %q", got)
	}
}

// A load balancer returns the target's redirect; following it would drop its
// Set-Cookie and break an OpenID Connect sign-in. Every redirect status is
// covered: none is special.
func TestELBv2DataPlaneReturnsTargetRedirectsInsteadOfFollowingThem(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	const sessionCookie = "session=granted; Path=/; HttpOnly"
	var landingRequests int
	redirectStatus := http.StatusFound
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte("ok"))
		case "/landing":
			landingRequests++
			_, _ = w.Write([]byte("followed-the-redirect"))
		default:
			w.Header().Set("Location", "/landing")
			w.Header().Add("Set-Cookie", sessionCookie)
			w.WriteHeader(redirectStatus)
		}
	}))
	defer target.Close()
	parsedTargetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL %s: %v", target.URL, err)
	}
	targetHost, targetPortText, err := net.SplitHostPort(parsedTargetURL.Host)
	if err != nil {
		t.Fatalf("target URL host has no port: %s: %v", target.URL, err)
	}
	targetPort, err := strconv.Atoi(targetPortText)
	if err != nil {
		t.Fatalf("target URL port is not numeric: %s: %v", target.URL, err)
	}

	lb := ELBv2LoadBalancer{
		Arn:     "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/redirect-lb/abc123",
		Name:    "redirect-lb",
		DNSName: "redirect-lb-abc123.elb.us-east-1.amazonaws.com",
		Type:    "application",
	}
	tg := ELBv2TargetGroup{
		Arn:                 "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/redirect-tg/abc123",
		Protocol:            "HTTP",
		Port:                80,
		HealthCheckProtocol: "HTTP",
		HealthCheckPath:     "/healthz",
		HealthCheckTimeout:  2,
		HealthCheckEnabled:  true,
		Targets:             []ELBv2TargetDescription{{ID: targetHost, Port: targetPort}},
	}
	listener := ELBv2Listener{
		Arn:             "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/redirect-lb/abc123/def456",
		LoadBalancerArn: lb.Arn,
		Protocol:        "HTTP",
		Port:            80,
		DefaultActions:  []ELBv2Action{{Type: "forward", TargetGroupArn: tg.Arn}},
	}
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	// Only a forwarded-to group is health-checked and only in-service targets get
	// traffic, so add the listener, then the check the checker would have run.
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	for _, status := range []int{
		http.StatusMovedPermanently,
		http.StatusFound,
		http.StatusSeeOther,
		http.StatusTemporaryRedirect,
		http.StatusPermanentRedirect,
	} {
		redirectStatus = status
		landingRequests = 0

		req := httptest.NewRequest(http.MethodGet, "http://simulator/oauth/callback?code=abc", nil)
		req.Host = lb.DNSName
		rr := httptest.NewRecorder()
		srv.ServeHTTP(rr, req)

		if rr.Code != status {
			t.Errorf("target answered %d; client received %d (body %q)", status, rr.Code, rr.Body.String())
		}
		if got := rr.Header().Get("Location"); got != "/landing" {
			t.Errorf("%d: Location = %q, want %q", status, got, "/landing")
		}
		if got := rr.Header().Values("Set-Cookie"); len(got) != 1 || got[0] != sessionCookie {
			t.Errorf("%d: Set-Cookie = %q, want exactly [%q]", status, got, sessionCookie)
		}
		if landingRequests != 0 {
			t.Errorf("%d: data plane fetched the redirect target %d time(s) itself", status, landingRequests)
		}
		if strings.Contains(rr.Body.String(), "followed-the-redirect") {
			t.Errorf("%d: client received the redirect target's body instead of the redirect", status)
		}
	}
}

// A WebSocket must be tunnelled both ways; relaying the handshake yields a 101
// and a one-way pipe. Real sockets: httptest.NewRecorder cannot hijack.
func TestELBv2DataPlaneTunnelsUpgradedConnectionsBothWays(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		if !lbplane.IsUpgradeRequest(r) {
			http.Error(w, "expected an upgrade request", http.StatusBadRequest)
			return
		}
		conn, buf, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("target hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = buf.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: pinned\r\n\r\n")
		_ = buf.Flush()
		// Echo, so a reply proves the client's bytes arrived.
		for {
			line, err := buf.ReadString('\n')
			if err != nil {
				return
			}
			if _, err := buf.WriteString("echo:" + line); err != nil {
				return
			}
			if err := buf.Flush(); err != nil {
				return
			}
		}
	}))
	defer target.Close()
	parsedTargetURL, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL %s: %v", target.URL, err)
	}
	targetHost, targetPortText, err := net.SplitHostPort(parsedTargetURL.Host)
	if err != nil {
		t.Fatalf("target URL host has no port: %s: %v", target.URL, err)
	}
	targetPort, err := strconv.Atoi(targetPortText)
	if err != nil {
		t.Fatalf("target URL port is not numeric: %s: %v", target.URL, err)
	}

	lb := ELBv2LoadBalancer{
		Arn:     "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/upgrade-lb/abc123",
		Name:    "upgrade-lb",
		DNSName: "upgrade-lb-abc123.elb.us-east-1.amazonaws.com",
		Type:    "application",
	}
	tg := ELBv2TargetGroup{
		Arn:                 "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/upgrade-tg/abc123",
		Protocol:            "HTTP",
		Port:                80,
		HealthCheckProtocol: "HTTP",
		HealthCheckPath:     "/healthz",
		HealthCheckTimeout:  2,
		HealthCheckEnabled:  true,
		Targets:             []ELBv2TargetDescription{{ID: targetHost, Port: targetPort}},
	}
	listener := ELBv2Listener{
		Arn:             "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/upgrade-lb/abc123/def456",
		LoadBalancerArn: lb.Arn,
		Protocol:        "HTTP",
		Port:            80,
		DefaultActions:  []ELBv2Action{{Type: "forward", TargetGroupArn: tg.Arn}},
	}
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	// Only a forwarded-to group is health-checked and only in-service targets get
	// traffic, so add the listener, then the check the checker would have run.
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	dataPlane := httptest.NewServer(srv)
	defer dataPlane.Close()
	conn, err := net.Dial("tcp", strings.TrimPrefix(dataPlane.URL, "http://"))
	if err != nil {
		t.Fatalf("dial data plane: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if _, err := conn.Write([]byte("GET /socket HTTP/1.1\r\nHost: " + lb.DNSName +
		"\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\nSec-WebSocket-Version: 13\r\n\r\n")); err != nil {
		t.Fatalf("write upgrade request: %v", err)
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatalf("read upgrade response: %v", err)
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("upgrade response = %d, want %d", resp.StatusCode, http.StatusSwitchingProtocols)
	}
	if got := resp.Header.Get("Sec-WebSocket-Accept"); got != "pinned" {
		t.Errorf("Sec-WebSocket-Accept = %q, want %q — handshake headers must reach the client verbatim", got, "pinned")
	}

	if _, err := conn.Write([]byte("ping\n")); err != nil {
		t.Fatalf("write through the tunnel: %v", err)
	}
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("the target never answered, so the client's bytes never reached it: %v", err)
	}
	if line != "echo:ping\n" {
		t.Errorf("tunnel returned %q, want %q", line, "echo:ping\n")
	}
}

// Browsers abandon requests constantly; a client that went away is a 499, not
// a 502 that hides real failures.
func TestELBv2DataPlaneRecordsAClientDisconnectAsClientClosedRequest(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	reached := make(chan struct{})
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		close(reached)
		<-r.Context().Done() // Hold until the client gives up.
	}))
	defer target.Close()

	lb, tg, listener := elbv2TestTopologyForTarget(t, target, "cancel")
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/prefetch?_rsc=abc", nil).WithContext(ctx)
	req.Host = lb.DNSName
	rr := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		defer close(done)
		srv.ServeHTTP(rr, req)
	}()
	<-reached
	cancel()
	<-done

	if rr.Code != lbplane.StatusClientClosedRequest {
		t.Errorf("client disconnect recorded as %d, want %d", rr.Code, lbplane.StatusClientClosedRequest)
	}
}

// A target closing a fresh connection must still 502: Go replays only reused
// connections, and the data plane must not invent a replay.
func TestELBv2DataPlaneReportsATargetThatClosesAFreshConnection(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		_ = conn.Close()
	}))
	defer target.Close()

	lb, tg, listener := elbv2TestTopologyForTarget(t, target, "dead")
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	req := httptest.NewRequest(http.MethodGet, "/asset.js", nil)
	req.Host = lb.DNSName
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadGateway {
		t.Errorf("target closing a fresh connection recorded as %d, want %d", rr.Code, http.StatusBadGateway)
	}
	if !strings.Contains(rr.Body.String(), "EOF") {
		t.Errorf("body does not name the failure: %q", rr.Body.String())
	}
}

// "You can use self-signed certificates or certificates that have expired";
// the escaped path reaches the target unchanged.
func TestELBv2DataPlaneForwardsToASelfSignedHTTPSTarget(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		_, _ = w.Write([]byte("tls-target " + r.URL.RequestURI()))
	}))
	defer target.Close()

	lb, tg, listener := elbv2TestTopologyForTarget(t, target, "tls-target")
	tg.Protocol = "HTTPS"
	tg.HealthCheckProtocol = "HTTPS"
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	req := httptest.NewRequest(http.MethodGet, "/files/a%2Fb?x=1", nil)
	req.Host = lb.DNSName
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d, body %q", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "tls-target /files/a%2Fb?x=1" {
		t.Fatalf("target saw %q", rr.Body.String())
	}
}

// A web ACL inspects every request, so HTTPS is blocked as HTTP is.
func TestELBv2HTTPSListenerAppliesTheAssociatedWebACL(t *testing.T) {
	newELBv2DataPlaneServer(t)
	priorACLs, priorAssociations := wafWebACLs, wafAssociations
	wafWebACLs = sim.MakeStore[wafStoredWebACL](nil, "test_waf_webacls")
	wafAssociations = sim.MakeStore[wafAssociation](nil, "test_waf_associations")
	t.Cleanup(func() { wafWebACLs, wafAssociations = priorACLs, priorAssociations })

	reached := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" {
			reached = true
		}
	}))
	defer target.Close()
	lb, tg, listener := elbv2TestTopologyForTarget(t, target, "waf-https")
	listener.Protocol, listener.Port = "HTTPS", 443
	elbv2LoadBalancers.Put(lb.Arn, lb)
	elbv2TargetGroups.Put(tg.Arn, tg)
	elbv2Listeners.Put(listener.Arn, listener)
	elbv2CheckTargetHealth(context.Background(), time.Now())

	const aclARN = "arn:aws:wafv2:us-east-1:000000000000:regional/webacl/deny-all/abc"
	wafWebACLs.Put("REGIONAL/abc", wafStoredWebACL{Scope: "REGIONAL", WebACL: WAFWebACL{
		Name: "deny-all", Id: "abc", ARN: aclARN,
		DefaultAction:    json.RawMessage(`{"Block":{}}`),
		VisibilityConfig: json.RawMessage(`{}`),
	}})
	wafAssociations.Put(lb.Arn, wafAssociation{ResourceARN: lb.Arn, WebACLARN: aclARN})

	req := httptest.NewRequest(http.MethodGet, "https://"+lb.DNSName+"/", nil)
	rr := httptest.NewRecorder()
	elbv2HTTPSListenerHandler(listener.Arn).ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("HTTPS listener answered %d, want 403 from the web ACL", rr.Code)
	}
	if reached {
		t.Fatal("a request the web ACL blocks reached the target")
	}
}

func elbv2TestTopologyForTarget(t *testing.T, target *httptest.Server, name string) (ELBv2LoadBalancer, ELBv2TargetGroup, ELBv2Listener) {
	t.Helper()
	parsed, err := url.Parse(target.URL)
	if err != nil {
		t.Fatalf("parse target URL: %v", err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatalf("split target host: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("target port not numeric: %v", err)
	}
	lb := ELBv2LoadBalancer{
		Arn:     "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/" + name + "-lb/abc123",
		Name:    name + "-lb",
		DNSName: name + "-lb-abc123.elb.us-east-1.amazonaws.com",
		Type:    "application",
	}
	tg := ELBv2TargetGroup{
		Arn:                 "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/" + name + "-tg/abc123",
		Protocol:            "HTTP",
		Port:                80,
		HealthCheckProtocol: "HTTP",
		HealthCheckPath:     "/healthz",
		HealthCheckTimeout:  2,
		HealthCheckEnabled:  true,
		Targets:             []ELBv2TargetDescription{{ID: host, Port: port}},
	}
	listener := ELBv2Listener{
		Arn:             "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/app/" + name + "-lb/abc123/def456",
		LoadBalancerArn: lb.Arn,
		Protocol:        "HTTP",
		Port:            80,
		DefaultActions:  []ELBv2Action{{Type: "forward", TargetGroupArn: tg.Arn}},
	}
	return lb, tg, listener
}

func TestELBv2TraceIDFollowsTheRequestTracingRules(t *testing.T) {
	now := time.Unix(0x67891233, 0)
	id := `1-67891233-[0-9a-f]{24}`
	for _, tc := range []struct{ incoming, want string }{
		{"", `^Root=` + id + `$`},
		{"Root=1-67891233-abcdef012345678912345678", `^Self=` + id + `;Root=1-67891233-abcdef012345678912345678$`},
		{"Root=1-67891233-abcdef012345678912345678;CalledFrom=app", `^Self=` + id + `;Root=1-67891233-abcdef012345678912345678;CalledFrom=app$`},
		{"Self=1-67891233-12456789abcdef0123456789;Root=1-67891233-abcdef012345678912345678", `^Self=` + id + `;Root=1-67891233-abcdef012345678912345678$`},
	} {
		got := elbv2TraceID(tc.incoming, now)
		if ok, _ := regexp.MatchString(tc.want, got); !ok {
			t.Errorf("trace id for %q = %q, want %s", tc.incoming, got, tc.want)
		}
	}
}

func TestELBv2ForwardedHeadersFollowTheXFFAttributes(t *testing.T) {
	listener := ELBv2Listener{Protocol: "HTTPS", Port: 443}
	r := httptest.NewRequest(http.MethodGet, "https://lb.example/", nil)
	r.RemoteAddr = "[2001:db8::7]:50123"
	for _, tc := range []struct {
		mode, clientPort string
		want             []string
	}{
		{"append", "false", []string{"203.0.113.1, 2001:db8::7"}},
		{"append", "true", []string{"203.0.113.1, [2001:db8::7]:50123"}},
		{"preserve", "false", []string{"203.0.113.1"}},
		{"remove", "false", nil},
	} {
		h := http.Header{"X-Forwarded-For": {"203.0.113.1"}}
		elbv2ForwardedHeaders(r, listener, map[string]string{
			"routing.http.xff_header_processing.mode": tc.mode,
			"routing.http.xff_client_port.enabled":    tc.clientPort,
		})(h)
		if got := h.Values("X-Forwarded-For"); strings.Join(got, "|") != strings.Join(tc.want, "|") {
			t.Errorf("mode %s, client port %s: X-Forwarded-For = %q, want %q", tc.mode, tc.clientPort, got, tc.want)
		}
		if h.Get("X-Forwarded-Proto") != "https" || h.Get("X-Forwarded-Port") != "443" {
			t.Errorf("X-Forwarded-Proto = %q, X-Forwarded-Port = %q", h.Get("X-Forwarded-Proto"), h.Get("X-Forwarded-Port"))
		}
	}
}
