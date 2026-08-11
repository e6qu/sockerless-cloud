package aws_sdk_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFreeSimulatorPortPairIsSimultaneouslyBindable pins the contract a
// launching simulator depends on: the two coordinates are distinct, and the
// HTTP server and the Route 53 dual-protocol listener can hold them at the same
// time.
//
// What makes a collision impossible is structural, not statistical — the helper
// holds the HTTP reservation open across the DNS probe, so the operating system
// cannot hand the same port twice, and it errors loudly if the two ever match
// anyway. This test does not reproduce the original race: reserving with two
// independent probes still passes it on an idle machine, because the collision
// needs the ephemeral allocator to wrap onto the just-released port. It guards
// the weaker property that is deterministically checkable — a rewrite that
// collides always, or returns a coordinate the simulator cannot bind, fails
// here immediately.
func TestFreeSimulatorPortPairIsSimultaneouslyBindable(t *testing.T) {
	for range 50 {
		httpPort, dnsPort, err := freeSimulatorPortPair()
		require.NoError(t, err)
		require.NotEqual(t, httpPort, dnsPort,
			"the Route 53 coordinate must never be the port the HTTP server binds")

		httpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", httpPort))
		require.NoError(t, err, "http coordinate %d must be bindable", httpPort)
		dnsTCP, err := net.Listen("tcp", fmt.Sprintf(":%d", dnsPort))
		require.NoError(t, err, "Route 53 coordinate %d must be bindable on TCP alongside the HTTP server", dnsPort)
		dnsUDP, err := net.ListenPacket("udp", fmt.Sprintf(":%d", dnsPort))
		require.NoError(t, err, "Route 53 coordinate %d must be bindable on UDP alongside the HTTP server", dnsPort)

		require.NoError(t, httpListener.Close())
		require.NoError(t, dnsTCP.Close())
		require.NoError(t, dnsUDP.Close())
	}
}

func TestFreeTCPUDPPortSupportsRoute53DualProtocolBind(t *testing.T) {
	port, err := freeTCPUDPPort()
	require.NoError(t, err)

	tcpListener, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, tcpListener.Close()) })

	udpListener, err := net.ListenPacket("udp", fmt.Sprintf(":%d", port))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, udpListener.Close()) })
}
