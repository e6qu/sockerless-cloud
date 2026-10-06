package main

import (
	"net"
	"testing"
)

// TestCloudRunHostOnNetworkOf decides whether this host routes to a container
// address from its own interfaces, without connecting anywhere.
func TestCloudRunHostOnNetworkOf(t *testing.T) {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Fatal(err)
	}
	var onLink net.IP
	for _, assigned := range addrs {
		if network, ok := assigned.(*net.IPNet); ok && !network.IP.IsLoopback() && network.IP.To4() != nil {
			onLink = network.IP
			break
		}
	}
	if onLink == nil {
		t.Fatal("this host has no non-loopback IPv4 interface")
	}
	if on, err := cloudRunHostOnNetworkOf(onLink.String()); err != nil || !on {
		t.Errorf("an address on this host's own network: on=%v err=%v", on, err)
	}
	if on, err := cloudRunHostOnNetworkOf("127.0.0.2"); err != nil || on {
		t.Errorf("loopback is no container network: on=%v err=%v", on, err)
	}
	if _, err := cloudRunHostOnNetworkOf("not-an-address"); err == nil {
		t.Error("a malformed address is an error")
	}
}
