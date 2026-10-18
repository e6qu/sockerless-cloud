package netrules

import (
	"reflect"
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
