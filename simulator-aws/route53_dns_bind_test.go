package main

import (
	"errors"
	"net"
	"strings"
	"testing"
)

// A DNS server needs one port on both protocols, because a resolver that gets a
// truncated UDP answer retries the query over TCP on that same port. Asking the
// kernel for a free port asks it about one protocol only — the two spaces are
// independent — so a UDP port it hands out can have its TCP twin already taken.
// That crashed the simulator at startup on a busy runner and left everything
// waiting on a server that never came up.
//
// Whether the kernel hands out such a port is not something a test can arrange
// by holding ports and hoping, so the single attempt is replaced here and the
// retry driven directly. Doing it the other way passed with the retry removed,
// which is worse than not testing it.
func TestRoute53DNSBindRetriesUntilBothProtocolsAgree(t *testing.T) {
	original := r53DNSBindPair
	t.Cleanup(func() { r53DNSBindPair = original })

	taken := errors.New("listen tcp: address already in use")
	attempts := 0
	r53DNSBindPair = func(port string) (net.PacketConn, net.Listener, error) {
		attempts++
		if attempts < 3 {
			return nil, nil, taken
		}
		return original(port)
	}

	udpConn, tcpLn := bindRoute53DNS("0")
	defer udpConn.Close()
	defer tcpLn.Close()

	if attempts != 3 {
		t.Errorf("gave up or over-tried: %d attempts, want 3", attempts)
	}
	if udpConn.LocalAddr().String() != tcpLn.Addr().String() {
		t.Errorf("bound UDP %s and TCP %s — a DNS server needs one port on both",
			udpConn.LocalAddr(), tcpLn.Addr())
	}
}

// A host where no chosen port is ever free on both has a problem the simulator
// should report rather than keep asking about, so the search is bounded and
// says how hard it tried.
func TestRoute53DNSBindGivesUpLoudly(t *testing.T) {
	original := r53DNSBindPair
	t.Cleanup(func() { r53DNSBindPair = original })
	attempts := 0
	r53DNSBindPair = func(string) (net.PacketConn, net.Listener, error) {
		attempts++
		return nil, nil, errors.New("listen tcp: address already in use")
	}

	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatal("a host with no usable port started anyway")
		}
		if msg, _ := recovered.(string); !strings.Contains(msg, "after") {
			t.Errorf("panic = %q, want it to say how many attempts were made", recovered)
		}
		if attempts != r53DNSEphemeralAttempts {
			t.Errorf("made %d attempts, want the bound of %d", attempts, r53DNSEphemeralAttempts)
		}
	}()
	bindRoute53DNS("0")
}

// A port the operator configured is a request, not a suggestion. Retrying it
// would either loop on the same failure or, worse, serve DNS somewhere the
// operator did not ask for, so it fails on the first attempt.
func TestRoute53DNSConfiguredPortFailsOnTheFirstAttempt(t *testing.T) {
	original := r53DNSBindPair
	t.Cleanup(func() { r53DNSBindPair = original })
	attempts := 0
	r53DNSBindPair = func(string) (net.PacketConn, net.Listener, error) {
		attempts++
		return nil, nil, errors.New("listen udp: address already in use")
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("a configured port that is already taken started anyway")
		} else if msg, _ := recovered.(string); !strings.Contains(msg, "configured") {
			t.Errorf("panic = %q, want it to name the configured endpoint", recovered)
		}
		if attempts != 1 {
			t.Errorf("made %d attempts on a configured port, want exactly 1", attempts)
		}
	}()
	bindRoute53DNS("5353")
}
