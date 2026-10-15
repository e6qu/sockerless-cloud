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

// A task namespace is pointed at the resolver's bound port, which may be an
// ephemeral one.
func TestRoute53DNSPortReadsTheBoundServer(t *testing.T) {
	previous := r53DNS
	t.Cleanup(func() { r53DNS = previous })

	r53DNS = nil
	if _, err := route53DNSPort(); err == nil || !strings.Contains(err.Error(), "not listening") {
		t.Fatalf("a resolver that is not listening: %v", err)
	}
	srv := withRoute53Resolver(t)
	if port, err := route53DNSPort(); err != nil || port != srv.Port() {
		t.Fatalf("port = %d, %v; want %d", port, err, srv.Port())
	}
}
