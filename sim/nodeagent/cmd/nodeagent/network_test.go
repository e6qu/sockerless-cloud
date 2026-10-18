package main

import (
	"strings"
	"testing"
)

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
	program, err := renderIngressProgram("t", "tap0", []packetRule{
		{Protocol: "tcp", SourceCIDR: "10.0.0.0/16", FromPort: 22, ToPort: 22},
		{Protocol: "6", SourceCIDR: "0.0.0.0/0", FromPort: 8000, ToPort: 8080},
		{Protocol: "icmp", SourceCIDR: "10.0.0.0/8"},
		{Protocol: "-1", SourceCIDR: "192.168.0.0/16", Action: "drop"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`oifname "tap0" ether type arp accept`,
		`oifname "tap0" ct state established,related accept`,
		`oifname "tap0" ip saddr 10.0.0.0/16 ip protocol tcp tcp dport 22 accept`,
		`oifname "tap0" ip saddr 0.0.0.0/0 ip protocol tcp tcp dport 8000-8080 accept`,
		`oifname "tap0" ip saddr 10.0.0.0/8 ip protocol icmp accept`,
		`oifname "tap0" ip saddr 192.168.0.0/16 drop`,
		`oifname "tap0" drop`,
	} {
		if !strings.Contains(program, want) {
			t.Errorf("program lacks %q:\n%s", want, program)
		}
	}
	if _, err := renderIngressProgram("t", "tap0", []packetRule{{Protocol: "tcp", Action: "log"}}); err == nil {
		t.Error("an action the filter cannot apply rendered")
	}
}
