package freeports

import (
	"net"
	"strconv"
	"testing"
)

func TestPickReturnsDistinctFreePorts(t *testing.T) {
	for range 50 {
		ports, err := Pick(4)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[int]bool{}
		for _, port := range ports {
			if seen[port] {
				t.Fatalf("Pick returned port %d twice: %v", port, ports)
			}
			seen[port] = true
			ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				t.Fatalf("port %d is not free after Pick: %v", port, err)
			}
			_ = ln.Close()
		}
	}
}

func TestPickRefusesZero(t *testing.T) {
	if _, err := Pick(0); err == nil {
		t.Fatal("Pick(0) returned no error")
	}
}
