package main

import "testing"

func TestParsePodmanMachineHostIPv4(t *testing.T) {
	route := "default via 192.168.127.1 dev enp0s1 proto dhcp src 192.168.127.2 metric 100\n"
	if got, want := parsePodmanMachineHostIPv4(route), "192.168.127.254"; got != want {
		t.Fatalf("parsePodmanMachineHostIPv4() = %q, want %q", got, want)
	}
}

func TestParsePodmanMachineHostIPv4NoSource(t *testing.T) {
	route := "default via 10.0.2.2 dev eth0\n"
	if got := parsePodmanMachineHostIPv4(route); got != "" {
		t.Fatalf("parsePodmanMachineHostIPv4() = %q, want empty", got)
	}
}
