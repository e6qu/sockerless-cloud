// Package freeports picks local TCP ports for a test harness to hand to the
// processes it starts.
package freeports

import (
	"errors"
	"fmt"
	"net"
)

// Pick returns n distinct free ports on 127.0.0.1. It holds every port it
// finds until it has found them all, because the system may hand a port just
// released out again as the next one.
func Pick(n int) ([]int, error) {
	if n < 1 {
		return nil, errors.New("pick at least one port")
	}
	listeners := make([]net.Listener, 0, n)
	defer func() {
		for _, ln := range listeners {
			_ = ln.Close()
		}
	}()
	ports := make([]int, 0, n)
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("find a free port: %w", err)
		}
		listeners = append(listeners, ln)
		addr, ok := ln.Addr().(*net.TCPAddr)
		if !ok {
			return nil, fmt.Errorf("a TCP listener reported address %v", ln.Addr())
		}
		ports = append(ports, addr.Port)
	}
	return ports, nil
}

// TCPUDP returns a port free on every interface for both TCP and UDP, for a
// listener such as a DNS server that binds both on the wildcard address.
func TCPUDP() (int, error) {
	const attempts = 100
	for range attempts {
		tcpListener, err := net.Listen("tcp", ":0")
		if err != nil {
			return 0, fmt.Errorf("find a free port: %w", err)
		}
		addr, ok := tcpListener.Addr().(*net.TCPAddr)
		if !ok {
			_ = tcpListener.Close()
			return 0, fmt.Errorf("a TCP listener reported address %v", tcpListener.Addr())
		}
		port := addr.Port
		udpListener, udpErr := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
		tcpCloseErr := tcpListener.Close()
		if udpErr != nil {
			continue
		}
		if err := errors.Join(tcpCloseErr, udpListener.Close()); err != nil {
			return 0, err
		}
		return port, nil
	}
	return 0, fmt.Errorf("no port was free on both TCP and UDP after %d attempts", attempts)
}
