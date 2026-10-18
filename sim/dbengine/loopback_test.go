package dbengine

import (
	"net"
	"strings"
	"testing"
)

// An identifier keeps its loopback address while its listener is closed, as a
// stopped instance keeps its endpoint: another identifier that would have
// landed on the same address takes the next one, and the first binds its own
// address again when it starts. Deleting the identifier gives the address up.
func TestLoopbackAddressStaysWithItsIdentifierUntilReleased(t *testing.T) {
	first, err := ListenLoopback("ab", 0)
	if err != nil {
		t.Fatal(err)
	}
	address := first.Addr().(*net.TCPAddr).IP.String()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ReleaseLoopback("ab"); ReleaseLoopback("ba") })

	// "ba" sums to the same seed as "ab" and would start its search at the
	// same address.
	second, err := ListenLoopback("ba", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if other := second.Addr().(*net.TCPAddr).IP.String(); other == address {
		t.Fatalf("a second identifier took the stopped identifier's address %s", address)
	}

	again, err := ListenLoopbackAt("ab", address, 0)
	if err != nil {
		t.Fatalf("the stopped identifier could not bind its own address again: %v", err)
	}
	defer again.Close()
	if got := again.Addr().(*net.TCPAddr).IP.String(); got != address {
		t.Fatalf("restart bound %s, want %s", got, address)
	}
	if _, err := ListenLoopbackAt("ba", address, 0); err == nil || !strings.Contains(err.Error(), "held by ab") {
		t.Fatalf("another identifier bound an address the first holds: %v", err)
	}

	if err := again.Close(); err != nil {
		t.Fatal(err)
	}
	ReleaseLoopback("ab")
	freed, err := ListenLoopbackAt("ba", address, 0)
	if err != nil {
		t.Fatalf("a released address must be free to take: %v", err)
	}
	defer freed.Close()
}
