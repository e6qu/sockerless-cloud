package main

import (
	"net/netip"
	"strings"
	"testing"
)

func TestDefaultRouteViaReadsOnlyTheInterfacesOwnDefaultRoute(t *testing.T) {
	routes := "default via 172.19.0.1 dev eth0 proto static\n"
	if gw := defaultRouteVia(routes, "eth0"); gw != netip.MustParseAddr("172.19.0.1") {
		t.Fatalf("eth0's default gateway = %v", gw)
	}
	if gw := defaultRouteVia(routes, "eth1"); gw.IsValid() {
		t.Fatalf("eth1 has no default route, got %v", gw)
	}
	if gw := defaultRouteVia("", "eth0"); gw.IsValid() {
		t.Fatalf("no routes at all, got %v", gw)
	}
}

func TestRenderIngressProgramAdmitsEverythingWithoutRules(t *testing.T) {
	program, err := renderIngressProgram("t", "tap0", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(program, "drop") || !strings.Contains(program, "policy accept") {
		t.Fatalf("a guest with no rules must receive everything:\n%s", program)
	}
}

func TestRenderIngressProgramFiltersAtTheTap(t *testing.T) {
	program, err := renderIngressProgram("t", "tap0", [][]packetRule{{
		{Protocol: "tcp", SourceCIDR: "10.0.0.0/16", FromPort: 22, ToPort: 22},
		{Protocol: "6", SourceCIDR: "0.0.0.0/0", FromPort: 8000, ToPort: 8080},
		{Protocol: "icmp", SourceCIDR: "10.0.0.0/8"},
		{Protocol: "-1", SourceCIDR: "192.168.0.0/16", Action: "drop"},
		{Protocol: "tcp", SourceCIDR: "172.16.0.0/12", Action: "drop"},
		{Protocol: "udp"},
	}, {
		{Protocol: "-1", SourceCIDR: "10.0.0.0/8"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`oifname "tap0" ether type arp accept`,
		`oifname "tap0" ct state established,related accept`,
		`oifname "tap0" jump stage0`,
		`oifname "tap0" jump stage1`,
		`oifname "tap0" accept`,
		`ip saddr 10.0.0.0/16 ip protocol tcp tcp dport 22 return`,
		`ip saddr 0.0.0.0/0 ip protocol tcp tcp dport 8000-8080 return`,
		`ip saddr 10.0.0.0/8 ip protocol icmp return`,
		`ip saddr 192.168.0.0/16 drop`,
		"\t\tip saddr 172.16.0.0/12 ip protocol tcp drop\n",
		"\t\tip protocol udp return\n",
		"chain stage1 {\n\t\tip saddr 10.0.0.0/8 return\n\t\tdrop\n\t}",
	} {
		if !strings.Contains(program, want) {
			t.Errorf("program lacks %q:\n%s", want, program)
		}
	}
	if strings.Count(program, "\t\tdrop\n") != 2 {
		t.Errorf("each stage ends in one drop:\n%s", program)
	}
	if _, err := renderIngressProgram("t", "tap0", [][]packetRule{{{Protocol: "tcp", Action: "log"}}}); err == nil {
		t.Error("an action the filter cannot apply rendered")
	}
	empty, err := renderIngressProgram("t", "tap0", [][]packetRule{{}})
	if err != nil || !strings.Contains(empty, "chain stage0 {\n\t\tdrop\n\t}") {
		t.Errorf("an empty stage admits nothing (%v):\n%s", err, empty)
	}
}
