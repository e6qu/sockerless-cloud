package sim

import (
	"net"
	"testing"
)

// The diagnostics listener serves goroutine dumps and a CPU profiler, so a
// simulator that names no address must not offer them to the network.
func TestDiagnosticsListenerDefaultsToLoopback(t *testing.T) {
	host, _, err := net.SplitHostPort(defaultDiagnosticsAddr)
	if err != nil {
		t.Fatalf("default address %q: %v", defaultDiagnosticsAddr, err)
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		t.Fatalf("default address %q is not a loopback address", defaultDiagnosticsAddr)
	}
}
