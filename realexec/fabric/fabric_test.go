package fabric

import (
	"context"
	"net"
	"reflect"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

func TestLinuxNameHashesTheWholeIdentifier(t *testing.T) {
	// Truncating these to their last ten characters named both the same.
	a := LinuxName("at", "eni-0123456789abcdef0")
	b := LinuxName("at", "eni-1123456789abcdef0")
	if a == b {
		t.Fatalf("identifiers differing in their head share the name %q", a)
	}
	if len(a) > 15 || a[:2] != "at" {
		t.Fatalf("name %q is not a prefixed Linux interface name", a)
	}
	if LinuxName("zn", "/subscriptions/S/resourceGroups/RG") != LinuxName("zn", "/subscriptions/s/resourcegroups/rg") {
		t.Fatal("identifiers differing only in case name different host objects")
	}
}

func TestDeriveMACSeparatesPermutedIdentifiers(t *testing.T) {
	// A positional XOR fold gave these two the same address.
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

func TestReconcileLivenessReportsOnlyTheDead(t *testing.T) {
	var lost []string
	ReconcileLiveness([]string{"a", "b", "c"}, func(k string) bool { return k == "b" }, func(k string) { lost = append(lost, k) })
	if !reflect.DeepEqual(lost, []string{"a", "c"}) {
		t.Fatalf("lost = %v, want [a c]", lost)
	}
}

func TestVMAliveWithoutAMachine(t *testing.T) {
	f := New[string](Options{})
	if f.VMAlive("i-none") {
		t.Fatal("a key with no machine reads as alive")
	}
}

// A public address reserved for an owner is the same on every call, and
// releasing the owner returns it to the pool.
func TestReservePublicIPIsOwnedUntilReleased(t *testing.T) {
	f := New[string](Options{})
	first, err := f.ReservePublicIP("router/nat", realexec.ReserveGCPPublicIPv4)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.ReservePublicIP("router/nat", realexec.ReserveGCPPublicIPv4)
	if err != nil {
		t.Fatal(err)
	}
	if !first.Equal(again) {
		t.Fatalf("second reservation for the same owner = %s, want %s", again, first)
	}
	if _, err := realexec.ReserveGCPPublicIPv4("someone-else", first); err == nil {
		t.Fatal("an owned address was leased to another owner")
	}
	if err := f.ReleaseOwned(context.Background(), "router/nat"); err != nil {
		t.Fatal(err)
	}
	if f.OwnedPublicIP("router/nat") != nil {
		t.Fatal("the owner still holds an address after release")
	}
	reused, err := realexec.ReserveGCPPublicIPv4("someone-else", first)
	if err != nil {
		t.Fatalf("the released address did not return to the pool: %v", err)
	}
	realexec.ReleasePublicIPv4(reused)
}
