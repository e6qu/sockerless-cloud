package realexec

import (
	"reflect"
	"strings"
	"testing"
)

func TestPortRange(t *testing.T) {
	for spec, want := range map[string][2]int{"": {0, 0}, "*": {0, 0}, "22": {22, 22}, "80-81": {80, 81}, " 443 ": {443, 443}, "0-65535": {0, 65535}} {
		from, to, err := PortRange(spec)
		if err != nil || from != want[0] || to != want[1] {
			t.Errorf("PortRange(%q) = %d, %d, %v; want %d, %d", spec, from, to, err, want[0], want[1])
		}
	}
	for _, bad := range []string{"abc", "22-abc", "70000", "81-80", "-5", "1-2-3"} {
		if _, _, err := PortRange(bad); err == nil {
			t.Errorf("PortRange(%q) accepted", bad)
		}
	}
}

func TestExpandRulesCrossesSourcesWithPorts(t *testing.T) {
	rules, err := ExpandRules("tcp", []string{"10.0.0.0/8", "192.168.0.0/16"}, []string{"22", "80-81"}, "accept")
	if err != nil {
		t.Fatal(err)
	}
	want := []PacketRule{
		{Protocol: "tcp", SourceCIDR: "10.0.0.0/8", FromPort: 22, ToPort: 22, Action: "accept"},
		{Protocol: "tcp", SourceCIDR: "10.0.0.0/8", FromPort: 80, ToPort: 81, Action: "accept"},
		{Protocol: "tcp", SourceCIDR: "192.168.0.0/16", FromPort: 22, ToPort: 22, Action: "accept"},
		{Protocol: "tcp", SourceCIDR: "192.168.0.0/16", FromPort: 80, ToPort: 81, Action: "accept"},
	}
	if !reflect.DeepEqual(rules, want) {
		t.Fatalf("rules = %+v, want %+v", rules, want)
	}
	all, err := ExpandRules("icmp", []string{"0.0.0.0/0"}, nil, "drop")
	if err != nil || len(all) != 1 || all[0].FromPort != 0 || all[0].ToPort != 0 {
		t.Fatalf("no ports = %+v, %v; want one every-port rule", all, err)
	}
	if _, err := ExpandRules("tcp", []string{"0.0.0.0/0"}, []string{"ssh"}, "accept"); err == nil {
		t.Fatal("an unparseable port expanded")
	}
}

func TestFlattenByPriorityPutsDenyFirstAtEqualPriority(t *testing.T) {
	allowA := PacketRule{SourceCIDR: "10.0.0.1/32", Action: "accept"}
	denyB := PacketRule{SourceCIDR: "10.0.0.2/32", Action: "drop"}
	allowC := PacketRule{SourceCIDR: "10.0.0.3/32", Action: "accept"}
	allowD := PacketRule{SourceCIDR: "10.0.0.4/32", Action: "accept"}
	got := FlattenByPriority([]PrioritizedRule{
		{Priority: 1000, Rule: allowA},
		{Priority: 1000, Rule: denyB},
		{Priority: 10, Rule: allowC},
		{Priority: 1000, Rule: allowD},
	})
	want := []PacketRule{allowC, denyB, allowA, allowD}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("order = %+v, want %+v", got, want)
	}
}

func TestRenderStagedIngressFilterProgram(t *testing.T) {
	program, err := renderStagedIngressFilterProgram("fw-t", "t0", [][]PacketRule{
		{{Protocol: "tcp", SourceCIDR: "10.0.0.0/8", FromPort: 22, ToPort: 22}, {Protocol: "*", SourceCIDR: "0.0.0.0/0", Action: "drop"}},
		{{Protocol: "6", SourceCIDR: "10.1.0.0/16", FromPort: 22, ToPort: 22}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"table bridge fw-t {}",
		"delete table bridge fw-t",
		"table bridge fw-t {",
		"\tchain stage1 {",
		"\t\toifname \"t0\" ip saddr 10.1.0.0/16 ip protocol tcp tcp dport 22 accept",
		"\t\tdrop",
		"\t}",
		"\tchain stage0 {",
		"\t\toifname \"t0\" ip saddr 10.0.0.0/8 ip protocol tcp tcp dport 22 goto stage1",
		"\t\toifname \"t0\" ip saddr 0.0.0.0/0 drop",
		"\t\tdrop",
		"\t}",
		"\tchain forward {",
		"\t\ttype filter hook forward priority filter; policy accept;",
		"\t\tct state established,related accept",
		"\t\toifname \"t0\" ether type arp accept",
		"\t\toifname \"t0\" goto stage0",
		"\t}",
		"}",
		"",
	}, "\n")
	if program != want {
		t.Fatalf("program:\n%s\nwant:\n%s", program, want)
	}
}

func TestPacketProtocolSpellings(t *testing.T) {
	for in, want := range map[string]string{"": "", "-1": "", "all": "", "*": "", "Tcp": "tcp", "6": "tcp", "17": "udp", "1": "icmp", "132": "sctp", "esp": "esp", "47": "47"} {
		got, err := packetProtocol(in)
		if err != nil || got != want {
			t.Errorf("packetProtocol(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"bogus", "256", "-2"} {
		if _, err := packetProtocol(bad); err == nil {
			t.Errorf("packetProtocol(%q) accepted", bad)
		}
	}
}
