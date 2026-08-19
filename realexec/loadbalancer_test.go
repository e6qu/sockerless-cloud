package realexec

import (
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Close must mean that nothing is still running, not merely that nothing new
// is accepted.
//
// It used to close the listener and wait for the accept loop, while the
// handlers that loop had already spawned kept calling the caller's resolver.
// In the AWS simulator that resolver reads the load-balancer and target-group
// stores, so a test that closed its proxy and then replaced those stores raced
// its own teardown — which is how the race detector found this.
func TestTCPProxyCloseWaitsForItsHandlers(t *testing.T) {
	// A resolver that blocks until released, so a handler is guaranteed to be
	// inside it when Close is called.
	entered := make(chan struct{})
	release := make(chan struct{})
	var resolving atomic.Bool
	var resolverReturned atomic.Bool

	proxy, err := StartTCPProxy("127.0.0.1:0", func(context.Context) (string, error) {
		if resolving.CompareAndSwap(false, true) {
			close(entered)
		}
		<-release
		resolverReturned.Store(true)
		return "", context.Canceled
	})
	require.NoError(t, err)

	client, err := net.Dial("tcp", proxy.Address)
	require.NoError(t, err)
	defer client.Close()

	select {
	case <-entered:
	case <-time.After(10 * time.Second):
		t.Fatal("the proxy never called its resolver, so this proves nothing")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		require.NoError(t, proxy.Close())
	}()

	// Close must still be blocked: its handler is inside the resolver.
	select {
	case <-done:
		t.Fatal("Close returned while a handler was still resolving a target")
	case <-time.After(200 * time.Millisecond):
	}

	close(release)

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close did not return after its handler finished")
	}
	require.True(t, resolverReturned.Load(),
		"Close returned before the handler it was waiting for had finished")
}

// A proxied stream can last for hours, so Close closes in-flight connections
// rather than waiting for them to end on their own.
func TestTCPProxyCloseEndsAStreamInFlight(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer upstream.Close()
	go func() {
		for {
			conn, acceptErr := upstream.Accept()
			if acceptErr != nil {
				return
			}
			// Hold the connection open and never speak, which is what a long
			// idle stream looks like.
			go func() { <-make(chan struct{}) }()
			_ = conn
		}
	}()

	proxy, err := StartTCPProxy("127.0.0.1:0", func(context.Context) (string, error) {
		return upstream.Addr().String(), nil
	})
	require.NoError(t, err)

	client, err := net.Dial("tcp", proxy.Address)
	require.NoError(t, err)
	defer client.Close()
	// Give the handler time to reach its copy loops.
	require.Eventually(t, func() bool {
		proxy.mu.Lock()
		defer proxy.mu.Unlock()
		return len(proxy.conns) == 1
	}, 10*time.Second, 10*time.Millisecond, "the handler never registered its connection")

	done := make(chan struct{})
	go func() { defer close(done); require.NoError(t, proxy.Close()) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Close waited for an idle stream instead of ending it")
	}
}
