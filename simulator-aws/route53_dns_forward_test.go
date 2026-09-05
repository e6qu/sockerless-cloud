package main

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"

	"golang.org/x/net/dns/dnsmessage"
)

// startStubUpstream answers every A query with one fixed address and reports how
// many queries it was asked. It stands in for the recursive resolver the host
// would provide.
func startStubUpstream(t *testing.T, answer [4]byte, asked *atomic.Int64) string {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen stub upstream: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, addr, rerr := conn.ReadFrom(buf)
			if rerr != nil {
				return
			}
			var p dnsmessage.Parser
			hdr, perr := p.Start(buf[:n])
			if perr != nil {
				continue
			}
			q, qerr := p.Question()
			if qerr != nil {
				continue
			}
			asked.Add(1)
			b := dnsmessage.NewBuilder(nil, dnsmessage.Header{
				ID: hdr.ID, Response: true, RCode: dnsmessage.RCodeSuccess,
			})
			_ = b.StartQuestions()
			_ = b.Question(q)
			_ = b.StartAnswers()
			_ = b.AResource(
				dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
				dnsmessage.AResource{A: answer},
			)
			msg, berr := b.Finish()
			if berr != nil {
				continue
			}
			_, _ = conn.WriteTo(msg, addr)
		}
	}()
	return conn.LocalAddr().String()
}

// withEmptyZoneStore gives the test its own Route 53 store. The resolver reads a
// package-level store that the subsystem's registration populates, so a test
// that does not set it up either panics on nil or reads another test's zones.
func withEmptyZoneStore(t *testing.T) {
	t.Helper()
	previous := r53Zones
	t.Cleanup(func() { r53Zones = previous })
	// Background work from an earlier test must finish before the stores
	// it is reading are replaced.
	AwaitSimulatorBackground()
	r53Zones = sim.MakeStore[r53StoredZone](nil, "route53_zones")
}

func queryBytes(t *testing.T, name string) []byte {
	t.Helper()
	b := dnsmessage.NewBuilder(nil, dnsmessage.Header{ID: 0x1234, RecursionDesired: true})
	_ = b.StartQuestions()
	if err := b.Question(dnsmessage.Question{
		Name:  dnsmessage.MustNewName(name),
		Type:  dnsmessage.TypeA,
		Class: dnsmessage.ClassINET,
	}); err != nil {
		t.Fatalf("build question for %s: %v", name, err)
	}
	msg, err := b.Finish()
	if err != nil {
		t.Fatalf("finish query for %s: %v", name, err)
	}
	return msg
}

// A task's only nameserver is this resolver, so a name outside every hosted zone
// has to be recursed for. Answering NXDOMAIN instead is what broke the deployed
// ecs-dev-desktop control plane: its container start reaches a package registry,
// which failed with ENOTFOUND while the resolver itself was perfectly reachable.
func TestRoute53DNSForwardsNamesOutsideItsZones(t *testing.T) {
	withEmptyZoneStore(t)
	// Counted from the stub's own goroutine and read from the test's, so it
	// has to be atomic — an int here is a data race the detector reports
	// against the test rather than the simulator.
	var asked atomic.Int64
	upstream := startStubUpstream(t, [4]byte{203, 0, 113, 7}, &asked)
	t.Setenv(route53UpstreamOverride, upstream)

	response, err := answerRoute53DNS(queryBytes(t, "registry.npmjs.org."))
	if err != nil {
		t.Fatalf("answer query: %v", err)
	}
	if got := asked.Load(); got != 1 {
		t.Fatalf("upstream was asked %d times, want exactly 1", got)
	}

	var p dnsmessage.Parser
	hdr, err := p.Start(response)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if hdr.RCode != dnsmessage.RCodeSuccess {
		t.Errorf("rcode = %v, want success — a forwarded answer must not be rewritten", hdr.RCode)
	}
	if hdr.ID != 0x1234 {
		t.Errorf("response ID = %#x, want %#x: a client drops a reply whose ID does not match", hdr.ID, 0x1234)
	}
	if err := p.SkipAllQuestions(); err != nil {
		t.Fatalf("skip questions: %v", err)
	}
	answers, err := p.AllAnswers()
	if err != nil {
		t.Fatalf("read answers: %v", err)
	}
	if len(answers) != 1 {
		t.Fatalf("got %d answers, want 1", len(answers))
	}
	a, ok := answers[0].Body.(*dnsmessage.AResource)
	if !ok {
		t.Fatalf("answer body is %T, want *dnsmessage.AResource", answers[0].Body)
	}
	if a.A != [4]byte{203, 0, 113, 7} {
		t.Errorf("A = %v, want 203.0.113.7 — the upstream's answer must pass through unchanged", a.A)
	}
}

// A forwarding failure is not proof the name is absent, and clients cache
// NXDOMAIN. SERVFAIL leaves the client free to ask again.
func TestRoute53DNSReportsServerFailureWhenNoUpstreamAnswers(t *testing.T) {
	withEmptyZoneStore(t)
	// A port nothing listens on: the exchange times out rather than replying.
	t.Setenv(route53UpstreamOverride, "127.0.0.1:1")

	done := make(chan []byte, 1)
	go func() {
		response, err := answerRoute53DNS(queryBytes(t, "registry.npmjs.org."))
		if err != nil {
			done <- nil
			return
		}
		done <- response
	}()
	var response []byte
	select {
	case response = <-done:
	case <-time.After(route53UpstreamTimeout + 5*time.Second):
		t.Fatal("answerRoute53DNS did not return within the upstream timeout")
	}
	if response == nil {
		t.Fatal("answerRoute53DNS returned an error")
	}
	var p dnsmessage.Parser
	hdr, err := p.Start(response)
	if err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if hdr.RCode != dnsmessage.RCodeServerFailure {
		t.Errorf("rcode = %v, want SERVFAIL", hdr.RCode)
	}
}

// Forwarding to ourselves is a query loop: the reply never comes and every name
// costs the client its full timeout. Both the address we listen on and the
// link-local address the task's DNAT rule rewrites into it must be refused.
func TestRoute53UpstreamServersDropSelfReferences(t *testing.T) {
	previous := r53DNSAddr
	t.Cleanup(func() { r53DNSAddr = previous })
	r53DNSAddr = "127.0.0.1:5353"

	t.Setenv(route53UpstreamOverride, "127.0.0.1:5353,169.254.169.253,9.9.9.9,1.1.1.1:53")
	got := route53UpstreamServers()

	want := []string{"9.9.9.9:53", "1.1.1.1:53"}
	if len(got) != len(want) {
		t.Fatalf("upstreams = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("upstream[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
