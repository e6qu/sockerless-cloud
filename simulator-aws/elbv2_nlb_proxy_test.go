package main

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Lets a test Put into the stores directly.
func elbv2InitStoresForTest(t *testing.T) {
	t.Helper()
	srv, err := sim.NewServer(sim.Config{Provider: "aws", LogLevel: "disabled"})
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	registerELBv2(NewAWSQueryRouter(), srv)
	// Stop the health checker with the test, or it keeps checking the next test's
	// stores.
	t.Cleanup(srv.StopBackground)
}

// Resolve the DNS name through the production hosts entries, then join the
// listener port, as an in-network client does.
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

// Raw bytes both ways, the path SSH through a Network Load Balancer relies on.
func TestELBv2NLBProxyForwardsRawTCP(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	elbv2InitStoresForTest(t)

	// Reads a line and writes "echo:<line>" back.
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
	// A TCP health check: a successful dial is healthy.
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
	// Only in-service targets get traffic; run the check the checker would have
	// run by now.
	elbv2CheckTargetHealth(context.Background(), time.Now())

	if err := elbv2StartListenerProxy(listener); err != nil {
		t.Fatalf("start NLB proxy: %v", err)
	}
	defer elbv2StopListenerProxy(listenerArn)

	// Resolve the DNS name through the hosts entries, as an in-network client
	// does.
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

// The connection is closed without data, not answered with an HTTP error.
func TestELBv2NLBProxyNoHealthyTargets(t *testing.T) {
	t.Setenv("SIM_RUNTIME", "process")
	elbv2InitStoresForTest(t)

	lbArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:loadbalancer/net/nlb-empty/abc123"
	tgArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:targetgroup/nlb-empty-tg/abc123"
	listenerArn := "arn:aws:elasticloadbalancing:us-east-1:000000000000:listener/net/nlb-empty/abc123/def456"

	elbv2LoadBalancers.Put(lbArn, ELBv2LoadBalancer{Arn: lbArn, Name: "nlb-empty", Type: "network",
		DNSName: "nlb-empty-abc123.elb.us-east-1.amazonaws.com"})
	// No registered target means no healthy target.
	elbv2TargetGroups.Put(tgArn, ELBv2TargetGroup{Arn: tgArn, Protocol: "TCP", Port: 443,
		HealthCheckProtocol: "TCP", HealthCheckTimeout: 1, TargetType: "ip"})
	listener := ELBv2Listener{Arn: listenerArn, LoadBalancerArn: lbArn, Protocol: "TCP", Port: 2022,
		DefaultActions: []ELBv2Action{{Type: "forward", TargetGroupArn: tgArn}}}
	elbv2Listeners.Put(listenerArn, listener)

	if err := elbv2StartListenerProxy(listener); err != nil {
		t.Fatalf("start NLB proxy: %v", err)
	}
	defer elbv2StopListenerProxy(listenerArn)

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
