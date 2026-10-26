package ipam

import (
	"net"
	"testing"
)

func TestDeriveMACSeparatesPermutedIdentifiers(t *testing.T) {
	// A positional XOR fold would give these two the same address.
	a := DeriveMAC("02:15:5d", "nic-ab")
	b := DeriveMAC("02:15:5d", "nic-ba")
	if a == b {
		t.Fatalf("permuted identifiers share MAC %s", a)
	}
	mac, err := net.ParseMAC(a)
	if err != nil {
		t.Fatal(err)
	}
	if mac.String()[:8] != "02:15:5d" {
		t.Fatalf("MAC %s lost its prefix", mac)
	}
	if DeriveMAC("02:15:5d", "nic-ab") != a {
		t.Fatal("MAC is not stable")
	}
}

func TestFirstHostGateway(t *testing.T) {
	for cidr, want := range map[string]string{
		"10.0.1.0/24":  "10.0.1.1",
		"10.0.1.77/24": "10.0.1.1",
		"10.128.0.0/9": "10.128.0.1",
	} {
		if got := FirstHostGateway(cidr); got.String() != want {
			t.Errorf("FirstHostGateway(%q) = %s, want %s", cidr, got, want)
		}
	}
	for _, bad := range []string{"", "nonsense", "2001:db8::/64", "10.0.0.1/32"} {
		if got := FirstHostGateway(bad); got != nil {
			t.Errorf("FirstHostGateway(%q) = %s, want nil", bad, got)
		}
	}
}
