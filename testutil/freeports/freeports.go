// Package freeports picks local TCP ports for a test harness to hand to the
// processes it starts.
package freeports

import (
	"errors"
	"fmt"
	"io"
	"net"
)

// Pick returns n distinct free ports on 127.0.0.1. It holds every port it
// finds until it has found them all, because the system may hand a port just
// released out again as the next one.
func Pick(n int) ([]int, error) {
	if n < 1 {
		return nil, errors.New("pick at least one port")
	}
	var held []io.Closer
	defer func() { closeAll(held) }()
	return pickTCP(n, &held)
}

func closeAll(held []io.Closer) {
	for _, c := range held {
		_ = c.Close()
	}
}

func pickTCP(n int, held *[]io.Closer) ([]int, error) {
	ports := make([]int, 0, n)
	for range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, fmt.Errorf("find a free port: %w", err)
		}
		*held = append(*held, ln)
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
	_, port, err := PickWithTCPUDP(0)
	return port, err
}

// PickWithTCPUDP returns n distinct free ports on 127.0.0.1 and one more port
// free on every interface for both TCP and UDP. It holds every port until it
// has found them all, so none of them can come back as another.
func PickWithTCPUDP(n int) ([]int, int, error) {
	var held []io.Closer
	defer func() { closeAll(held) }()
	ports, err := pickTCP(n, &held)
	if err != nil {
		return nil, 0, err
	}
	const attempts = 100
	for range attempts {
		tcpListener, err := net.Listen("tcp", ":0")
		if err != nil {
			return nil, 0, fmt.Errorf("find a free port: %w", err)
		}
		held = append(held, tcpListener)
		addr, ok := tcpListener.Addr().(*net.TCPAddr)
		if !ok {
			return nil, 0, fmt.Errorf("a TCP listener reported address %v", tcpListener.Addr())
		}
		udpListener, err := net.ListenPacket("udp", fmt.Sprintf(":%d", addr.Port))
		if err != nil {
			continue
		}
		held = append(held, udpListener)
		return ports, addr.Port, nil
	}
	return nil, 0, fmt.Errorf("no port was free on both TCP and UDP after %d attempts", attempts)
}
