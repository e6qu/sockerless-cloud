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
