package realexec

import (
	"errors"
	"net"
	"testing"
)

func TestIPAMAllocatesLeasesFromCIDR(t *testing.T) {
	ipam, err := NewIPAM("10.42.0.0/29", net.ParseIP("10.42.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	first, err := ipam.Reserve("first", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ipam.Reserve("second", nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.String() != "10.42.0.2" || second.String() != "10.42.0.3" {
		t.Fatalf("leases = %s, %s; want 10.42.0.2, 10.42.0.3", first, second)
	}
	ipam.Release(first)
	again, err := ipam.Reserve("again", nil)
	if err != nil {
		t.Fatal(err)
	}
	if again.String() != "10.42.0.2" {
		t.Fatalf("released lease was not reusable: got %s", again)
	}
}

// TestIPAMReleaseIPv6NoPanic verifies Release ignores a non-IPv4 address
// instead of panicking on the nil result of (*net.IP).To4().
func TestIPAMReleaseIPv6NoPanic(t *testing.T) {
	ipam, err := NewIPAM("10.42.0.0/29", net.ParseIP("10.42.0.1"))
	if err != nil {
		t.Fatal(err)
	}
	// Must not panic.
	ipam.Release(net.ParseIP("fe80::1"))
	ipam.Release(net.ParseIP("::1"))
}

func TestIPAMRejectsReservedAndUnusableAddresses(t *testing.T) {
	ipam, err := NewIPAM("10.42.1.0/30", net.ParseIP("10.42.1.1"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ipam.Reserve("network", net.ParseIP("10.42.1.0")); err == nil {
		t.Fatal("expected network address to be rejected")
	}
	if _, err := ipam.Reserve("gateway", net.ParseIP("10.42.1.1")); err == nil {
		t.Fatal("expected gateway address to be rejected as already leased")
	}
	if _, err := ipam.Reserve("broadcast", net.ParseIP("10.42.1.3")); err == nil {
		t.Fatal("expected broadcast address to be rejected")
	}
	if _, err := ipam.Reserve("only-host", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ipam.Reserve("exhausted", nil); !errors.Is(err, ErrNoAvailableIP) {
		t.Fatalf("got %v, want ErrNoAvailableIP", err)
	}
}
