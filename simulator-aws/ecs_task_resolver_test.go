package main

import (
	"net"
	"strings"
	"testing"

	realexec "github.com/e6qu/sockerless-cloud/realexec"
)

// A workload namespace holds only its own interface, so the resolver its image
// was configured with — Docker's embedded 127.0.0.11, written before the pause
// container was detached from Docker's networks — answers nothing. Lookups then
// block until they time out rather than failing, which is why a workload can
// bind its ports minutes after its container started with nothing logged in
// between (GitHub issue #905). A VPC serves DNS at its own base address plus
// two, which is what AmazonProvidedDNS means.
// A task in its own namespace cannot reach the resolver Docker would configure
// — that one lives on the Docker networks the namespace was detached from — so
// the task is pointed at the VPC's, which the namespace redirects to the
// simulator's own. Without this the redirect is in place and nothing uses it.
func TestTaskResolverIsTheVPCResolver(t *testing.T) {
	if realexec.VPCResolverIPv4 == "" {
		t.Fatal("no VPC resolver address is defined for a task to ask")
	}
	ip := net.ParseIP(realexec.VPCResolverIPv4)
	if ip == nil || !ip.IsLinkLocalUnicast() {
		t.Fatalf("the VPC resolver %q is not a link-local address, so a task whose "+
			"subnet contains it would resolve it on the link instead of through the gateway",
			realexec.VPCResolverIPv4)
	}
}

// The resolver port is read from the address actually bound, because the
// simulator may take an ephemeral one.
func TestRoute53DNSPortReadsTheBoundAddress(t *testing.T) {
	previous := r53DNSAddr
	t.Cleanup(func() { r53DNSAddr = previous })

	r53DNSAddr = ""
	if _, err := route53DNSPort(); err == nil {
		t.Error("a resolver that is not listening reported a port")
	} else if !strings.Contains(err.Error(), "not listening") {
		t.Errorf("the error does not say the resolver is not listening: %v", err)
	}

	r53DNSAddr = "127.0.0.1:5353"
	port, err := route53DNSPort()
	if err != nil {
		t.Fatalf("read the bound port: %v", err)
	}
	if port != 5353 {
		t.Errorf("port = %d, want 5353", port)
	}

	r53DNSAddr = "[::]:41234"
	if port, err = route53DNSPort(); err != nil || port != 41234 {
		t.Errorf("an ephemeral port on the wildcard address read as (%d, %v)", port, err)
	}
}
