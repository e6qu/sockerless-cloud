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

func TestIPAMWithReservedKeepsEachCloudsAddresses(t *testing.T) {
	cases := []struct {
		name       string
		keep       HostReservation
		first      string
		reserved   []string
		lastUsable string
	}{
		{"Amazon VPC and Azure Virtual Network", HostReservation{First: 4, Last: 1}, "10.0.1.4",
			[]string{"10.0.1.0", "10.0.1.2", "10.0.1.3", "10.0.1.255"}, "10.0.1.254"},
		{"Google Cloud VPC", HostReservation{First: 2, Last: 2}, "10.0.1.2",
			[]string{"10.0.1.0", "10.0.1.254", "10.0.1.255"}, "10.0.1.253"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ipam, err := NewIPAMWithReserved("10.0.1.0/24", net.ParseIP("10.0.1.1"), tc.keep)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ipam.Reserve("first", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got.String() != tc.first {
				t.Fatalf("first dynamic lease = %s, want %s", got, tc.first)
			}
			for _, ip := range tc.reserved {
				if _, err := ipam.Reserve("static", net.ParseIP(ip)); err == nil {
					t.Fatalf("static lease of reserved %s was granted", ip)
				}
			}
			if _, err := ipam.Reserve("last", net.ParseIP(tc.lastUsable)); err != nil {
				t.Fatalf("static lease of the last usable address %s: %v", tc.lastUsable, err)
			}
		})
	}
}

func TestIPAMWithReservedExhaustsBeforeTheReservedTail(t *testing.T) {
	ipam, err := NewIPAMWithReserved("10.0.1.0/29", net.ParseIP("10.0.1.1"), HostReservation{First: 4, Last: 1})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for {
		ip, err := ipam.Reserve("x", nil)
		if errors.Is(err, ErrNoAvailableIP) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ip.String())
	}
	if len(got) != 3 || got[0] != "10.0.1.4" || got[2] != "10.0.1.6" {
		t.Fatalf("leases = %v, want 10.0.1.4 through 10.0.1.6", got)
	}
}

func TestIPAMWithReservedRefusesToForgetNetworkOrBroadcast(t *testing.T) {
	if _, err := NewIPAMWithReserved("10.0.1.0/24", net.ParseIP("10.0.1.1"), HostReservation{First: 0, Last: 1}); err == nil {
		t.Fatal("a reservation without the network address was accepted")
	}
}
