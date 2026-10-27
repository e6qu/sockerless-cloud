package aws_sdk_test

import (
	"fmt"
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFreeSimulatorPortPairIsSimultaneouslyBindable expects the two ports to be
// distinct and bindable at once by the HTTP server and the Route 53 listener.
// The helper rules a collision out structurally, by holding the HTTP
// reservation across the DNS probe; this test cannot reproduce the original
// race, which needs the ephemeral allocator to wrap onto the released port, but
// fails at once on a rewrite that always collides or returns an unbindable
// port.
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
