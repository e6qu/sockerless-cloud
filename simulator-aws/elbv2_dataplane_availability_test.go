// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"net"
	"os"
	"syscall"
	"testing"
)

// A listener whose port the host will not give up is still created. Amazon
// ELBv2 never rejects CreateListener because another process holds a port, and
// the simulator used to: an SSH network load balancer on port 22 could not be
// created on any machine running sshd, which is every CI runner the
// container-mode topology uses.
func TestListenerIsCreatedWhenTheHostHoldsItsPort(t *testing.T) {
	elbv2InitStoresForTest(t)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("hold a port: %v", err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	lbArn := "arn:aws:elasticloadbalancing:us-east-1:123456789012:loadbalancer/net/busy/1"
	listenerArn := lbArn + "/listener-1"
	elbv2LoadBalancers.Put(lbArn, ELBv2LoadBalancer{Arn: lbArn, Type: "network", DNSName: "busy.elb.amazonaws.com"})
	t.Cleanup(func() { elbv2StopNLBProxy(listenerArn) })

	listener := ELBv2Listener{Arn: listenerArn, LoadBalancerArn: lbArn, Protocol: "TCP", Port: port}
	if err := elbv2StartListenerDataPlane(listener); err != nil {
		t.Fatalf("CreateListener refused a port the host holds: %v", err)
	}
}

func TestOnlyAnUnavailableAddressDegradesTheDataPlane(t *testing.T) {
	for _, unavailable := range []error{syscall.EADDRINUSE, syscall.EACCES, syscall.EPERM, syscall.EADDRNOTAVAIL} {
		wrapped := fmt.Errorf("start NLB TCP proxy on 127.0.0.2:22: %w", wrapped(unavailable))
		if !elbv2HostCannotOfferAddress(wrapped) {
			t.Errorf("%v should read as an address the host will not offer", unavailable)
		}
	}
	// A real configuration failure still fails the API call.
	if elbv2HostCannotOfferAddress(errors.New("listener has no target group")) {
		t.Error("a configuration error must not be mistaken for a busy address")
	}
}

// The shape net.Listen actually returns: *net.OpError wrapping
// *os.SyscallError wrapping the errno.
func wrapped(err error) error {
	return &net.OpError{Op: "listen", Net: "tcp", Err: os.NewSyscallError("bind", err)}
}
