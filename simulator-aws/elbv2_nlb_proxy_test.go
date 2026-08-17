package main

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-aws/shared"
)

// elbv2InitStoresForTest initializes the ELBv2 stores (registerELBv2 calls
// MakeStore), so a test can Put directly into them without going through the
// HTTP control plane.
func elbv2InitStoresForTest(t *testing.T) {
	t.Helper()
	srv, err := sim.NewServer(sim.Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	registerELBv2(sim.NewAWSQueryRouter(), srv)
	// The Elastic Load Balancing target health checker runs under the
	// server lifecycle; without this it outlives the test and keeps
	// checking whatever the next test puts in the package stores.
	t.Cleanup(srv.StopBackground)
}

// elbv2TestNLBEndpoint resolves a load balancer's stable AWS-shaped DNSName to
// its stream proxy host through the production hosts-entry mapping
// (elbv2NLBHostEntries), then joins the listener port — the faithful path an
// in-network client uses (resolve the DNS name, connect on the listener port),
// never a host:port read out of DNSName.
func elbv2TestNLBEndpoint(t *testing.T, lb ELBv2LoadBalancer, listenerPort int) string {
	t.Helper()
	for _, entry := range elbv2NLBHostEntries() {
		if entry.Name == strings.TrimSuffix(lb.DNSName, ".") {
			return net.JoinHostPort(entry.IP, strconv.Itoa(listenerPort))
		}
	}
	t.Fatalf("no NLB host entry resolves DNSName %q to a proxy host", lb.DNSName)
	return ""
}

// TestELBv2NLBProxyForwardsRawTCP proves the NLB stream data plane forwards a
// raw byte stream (not HTTP) to a healthy registered target, both directions,
// like a real Network Load Balancer — the path SSH-through-NLB relies on.
func TestELBv2NLBProxyForwardsRawTCP(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	elbv2InitStoresForTest(t)

	// A raw-TCP echo backend: reads a line, writes "echo:<line>" back. No HTTP.
	backend, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen backend: %v", err)
	}
	defer backend.Close()
	go func() {
		for {
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				reader := bufio.NewReader(c)
				for {
					line, err := reader.ReadString('\n')
					if err != nil {
						return
					}
					if _, err := c.Write([]byte("echo:" + line)); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	backendHost, backendPortText, err := net.SplitHostPort(backend.Addr().String())
	if err != nil {
		t.Fatalf("split backend addr: %v", err)
	}
	backendPort, err := strconv.Atoi(backendPortText)
	if err != nil {
		t.Fatalf("backend port: %v", err)
	}

	lbArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/nlb-raw/abc123"
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/nlb-raw-tg/abc123"
	listenerArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/net/nlb-raw/abc123/def456"

	elbv2LoadBalancers.Put(lbArn, ELBv2LoadBalancer{Arn: lbArn, Name: "nlb-raw", Type: "network",
		DNSName: "nlb-raw-abc123.elb.us-east-1.amazonaws.com"})
	// TCP target group with a TCP health check (dial succeeds → healthy).
	elbv2TargetGroups.Put(tgArn, ELBv2TargetGroup{
		Arn: tgArn, Protocol: "TCP", Port: backendPort,
		HealthCheckProtocol: "TCP", HealthCheckTimeout: 2, TargetType: "ip",
		HealthCheckEnabled: true,
		Targets:            []ELBv2TargetDescription{{ID: backendHost, Port: backendPort}},
	})
	listener := ELBv2Listener{
		Arn: listenerArn, LoadBalancerArn: lbArn, Protocol: "TCP", Port: 2022,
		DefaultActions: []ELBv2Action{{Type: "forward", TargetGroupArn: tgArn}},
	}
	elbv2Listeners.Put(listenerArn, listener)
	// A load balancer forwards only to targets its health checker has put in
	// service, so run the check the checker would have run by now.
	elbv2CheckTargetHealth(context.Background(), time.Now())

	if err := elbv2StartNLBProxy(listener); err != nil {
		t.Fatalf("start NLB proxy: %v", err)
	}
	defer elbv2StopNLBProxy(listenerArn)

	// Discover the endpoint the way an in-network client does: resolve the
	// stable AWS-shaped DNSName (what DescribeLoadBalancers returns) to the
	// proxy host through the injected hosts entries, then connect on the
	// listener port — never a host:port read from DNSName.
	lb, ok := elbv2LoadBalancers.Get(lbArn)
	if !ok {
		t.Fatal("load balancer not found")
	}
	endpoint := elbv2TestNLBEndpoint(t, lb, listener.Port)

	conn, err := net.DialTimeout("tcp", endpoint, 5*time.Second)
	if err != nil {
		t.Fatalf("dial NLB endpoint %s: %v", endpoint, err)
	}
	defer conn.Close()

	if _, err := conn.Write([]byte("hello-raw-tcp\n")); err != nil {
		t.Fatalf("write to proxy: %v", err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read from proxy: %v", err)
	}
	if got != "echo:hello-raw-tcp\n" {
		t.Fatalf("NLB raw-TCP round trip = %q, want %q", got, "echo:hello-raw-tcp\n")
	}
}

// TestELBv2NLBProxyNoHealthyTargets proves a connection through an NLB stream
// listener with no healthy target is closed without data (like a real NLB
// dropping the connection), not answered with an HTTP error.
func TestELBv2NLBProxyNoHealthyTargets(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	elbv2InitStoresForTest(t)

	lbArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/nlb-empty/abc123"
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/nlb-empty-tg/abc123"
	listenerArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/net/nlb-empty/abc123/def456"

	elbv2LoadBalancers.Put(lbArn, ELBv2LoadBalancer{Arn: lbArn, Name: "nlb-empty", Type: "network",
		DNSName: "nlb-empty-abc123.elb.us-east-1.amazonaws.com"})
	// No registered targets → no healthy target.
	elbv2TargetGroups.Put(tgArn, ELBv2TargetGroup{Arn: tgArn, Protocol: "TCP", Port: 443,
		HealthCheckProtocol: "TCP", HealthCheckTimeout: 1, TargetType: "ip"})
	listener := ELBv2Listener{Arn: listenerArn, LoadBalancerArn: lbArn, Protocol: "TCP", Port: 2022,
		DefaultActions: []ELBv2Action{{Type: "forward", TargetGroupArn: tgArn}}}
	elbv2Listeners.Put(listenerArn, listener)

	if err := elbv2StartNLBProxy(listener); err != nil {
		t.Fatalf("start NLB proxy: %v", err)
	}
	defer elbv2StopNLBProxy(listenerArn)

	lb, ok := elbv2LoadBalancers.Get(lbArn)
	if !ok {
		t.Fatal("load balancer not found")
	}
	conn, err := net.DialTimeout("tcp", elbv2TestNLBEndpoint(t, lb, listener.Port), 5*time.Second)
	if err != nil {
		t.Fatalf("dial NLB endpoint: %v", err)
	}
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 16)
	n, err := conn.Read(buf)
	if err == nil && n > 0 {
		t.Fatalf("NLB proxy with no healthy target returned %d bytes %q, want closed", n, buf[:n])
	}
}
