package placement

import (
	"net/netip"
	"testing"
)

func TestGuestMACFollowsTheCloudsPrefix(t *testing.T) {
	addr := netip.MustParseAddr("10.86.0.7")
	if got := guestMAC("42:01", addr, "gce-boot"); got != "42:01:0a:56:00:07" {
		t.Fatalf("Compute Engine MAC = %s, want the address under 42:01", got)
	}
	aws := guestMAC("0e:a7:7d", addr, "aws-instance-i-1")
	if len(aws) != 17 || aws[:9] != "0e:a7:7d:" {
		t.Fatalf("three-octet prefix MAC = %s", aws)
	}
	if again := guestMAC("0e:a7:7d", netip.MustParseAddr("10.0.0.9"), "aws-instance-i-1"); again != aws {
		t.Fatalf("a hashed MAC changed with the address: %s then %s", aws, again)
	}
}

func TestMatchesLabelsAsksForEveryWantedLabel(t *testing.T) {
	have := map[string]string{"sockerless-sim-state": "abc", "sockerless-sim-instance": "i-1"}
	if !matchesLabels(have, map[string]string{"sockerless-sim-instance": "i-1", "sockerless-sim-state": "abc"}) {
		t.Fatal("every wanted label is there")
	}
	if !matchesLabels(have, map[string]string{"sockerless-sim-instance": ""}) {
		t.Fatal("an empty wanted value asks only for the key")
	}
	if matchesLabels(have, map[string]string{"sockerless-sim-instance": "i-2"}) {
		t.Fatal("a different value must not match")
	}
	if matchesLabels(have, map[string]string{"sockerless-sim-type": "vm"}) {
		t.Fatal("a missing key must not match")
	}
}
