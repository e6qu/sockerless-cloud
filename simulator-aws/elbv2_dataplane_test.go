package main

import (
	"bufio"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// newELBv2DataPlaneServer builds a simulator carrying everything the load
// balancer data plane reads, not only the load balancers themselves.
//
// The data plane consults AWS WAFV2 for a web ACL association and Amazon ECS
// for a task's published port, both through package-level stores that the
// owning subsystem's registration populates. A test that registers only
// Elastic Load Balancing therefore passes on whatever those stores happen to
// hold — a zero value when the test runs alone, another test's leavings when
// it does not — so what it proves depends on test ordering rather than on the
// handler (GitHub issue #907). Registering the subsystems it reads makes the
// dependency the test's own.
func newELBv2DataPlaneServer(t *testing.T) *sim.Server {
	t.Helper()
	t.Setenv("SIM_RUNTIME", "process")
	srv, err := sim.NewServer(sim.Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	jsonRouter := sim.NewAWSRouter()
	registerELBv2(sim.NewAWSQueryRouter(), srv)
	registerWAFv2(jsonRouter, srv)
	registerECS(jsonRouter, srv)
	registerELBv2DataPlane(srv)
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
	elbv2Listeners.Put(listener.Arn, listener)

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
	// This one exercises the header helper rather than the served path, but it
	// still reads the load-balancer store the registration populates.
	newELBv2DataPlaneServer(t)

	lb := ELBv2LoadBalancer{
		Arn:        "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/app/test-lb/abc123",
		Name:       "test-lb",
		DNSName:    "test-lb-abc123.elb.us-east-1.amazonaws.com",
		Type:       "application",
		Attributes: defaultELBv2LoadBalancerAttributes(),
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

// A load balancer returns its target's redirect to the client. Following one
// itself fetches the redirect target and answers with that instead, discarding
// the Set-Cookie the redirect carried — which is exactly how an OpenID Connect
// sign-in behind this data plane fails with no error anywhere (issue #883).
//
// Every redirect status is covered deliberately. In production the defect looked
// selective — 307 and 308 reached the browser intact while 301, 302 and 303 did
// not — but that was only because a request forwarded from a server has no
// rewindable body for Go to replay. Nothing about the defect is status-specific,
// and a bodyless request like the one below follows all five.
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
	elbv2Listeners.Put(listener.Arn, listener)

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

// A WebSocket is not delivered by relaying its handshake. The data plane used to
// forward with an ordinary client and copy the response body to the
// ResponseWriter, which produces a 101 the client accepts and then a one-way pipe:
// nothing the client sends can reach the target, because a ResponseWriter has no
// route back. The failure is silent in exactly the wrong way — the peer sees a
// successful upgrade followed by silence and reports a handshake timeout.
//
// This runs over real sockets. httptest.NewRecorder cannot hijack, so a recorder
// would pass while production fails.
func TestELBv2DataPlaneTunnelsUpgradedConnectionsBothWays(t *testing.T) {
	srv := newELBv2DataPlaneServer(t)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte("ok"))
			return
		}
		if !sim.IsUpgradeRequest(r) {
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
		// Echo, so a reply proves the CLIENT's bytes arrived — the direction the
		// old implementation dropped. A target that merely spoke first would pass
		// against a one-way pipe.
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
	elbv2Listeners.Put(listener.Arn, listener)

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
