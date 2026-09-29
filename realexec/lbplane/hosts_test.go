package lbplane

import (
	"net"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHostLeasesShareAHostPerOwnerAndSplitABusyPort(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer busy.Close()
	port := busy.Addr().(*net.TCPAddr).Port

	free, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	freePort := free.Addr().(*net.TCPAddr).Port
	require.NoError(t, free.Close())

	leases := NewHostLeases()
	first, err := leases.Acquire("lb-a", freePort)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1", first)
	again, err := leases.Acquire("lb-a", port)
	require.NoError(t, err)
	require.Equal(t, first, again, "every listener of one load balancer shares its host")

	if runtime.GOOS == "linux" {
		other, err := leases.Acquire("lb-b", port)
		require.NoError(t, err)
		require.NotEqual(t, "127.0.0.1", other, "a load balancer whose port is taken on 127.0.0.1 leases its own address")
		ln, err := net.Listen("tcp", net.JoinHostPort(other, strconv.Itoa(port)))
		require.NoError(t, err, "the leased address binds the same port")
		require.NoError(t, ln.Close())
		leases.Release("lb-b")
		_, held := leases.Host("lb-b")
		require.False(t, held)
	} else {
		_, err := leases.Acquire("lb-b", port)
		require.Error(t, err)
	}

	leases.Release("lb-a")
	host, held := leases.Host("lb-a")
	require.True(t, held, "one listener still holds the host")
	require.Equal(t, first, host)
	leases.Release("lb-a")
	_, held = leases.Host("lb-a")
	require.False(t, held)
}
