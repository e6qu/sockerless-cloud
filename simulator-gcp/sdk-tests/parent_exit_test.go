package gcp_sdk_test

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A simulator must not outlive the test process that started it. Every harness
// stops its simulator from a cleanup, which a `go test` killed outright never
// reaches, and the simulator's container reaper waits on it, so the pair would
// survive together holding ports and memory.
//
// TestMain sets SOCKERLESS_PARENT_PID to this process, and every simulator
// started here inherits it. The real parent cannot be killed to prove the
// point, so the test starts a simulator watching a stand-in, kills the
// stand-in, and requires the simulator to exit.
func TestSimulatorExitsWithItsParent(t *testing.T) {
	t.Parallel()
	standIn := exec.Command("sleep", "120")
	require.NoError(t, standIn.Start(), "start the stand-in parent")
	defer func() { _ = standIn.Process.Kill() }()

	sim := exec.Command(binaryPath)
	sim.Env = append(os.Environ(),
		fmt.Sprintf("SIM_LISTEN_ADDR=:%d", freeLocalPort(t)),
		fmt.Sprintf("SIM_GCP_GRPC_PORT=%d", freeLocalPort(t)),
		"SOCKERLESS_PARENT_PID"+"="+strconv.Itoa(standIn.Process.Pid),
	)
	require.NoError(t, sim.Start(), "start the simulator")
	exited := make(chan error, 1)
	go func() { exited <- sim.Wait() }()
	defer func() { _ = sim.Process.Kill() }()

	// A watch that fired on any input would pass the rest of this test without
	// looking at the pid, so the simulator must stay up while its parent lives.
	select {
	case err := <-exited:
		t.Fatalf("the simulator exited while its parent was alive: %v", err)
	case <-time.After(3 * time.Second):
	}

	require.NoError(t, standIn.Process.Kill(), "kill the stand-in parent")
	_, _ = standIn.Process.Wait()

	select {
	case <-exited:
	case <-time.After(30 * time.Second):
		t.Fatal("the simulator outlived the parent it was told to watch")
	}
}

// freeLocalPort reserves a port the simulator can bind, so this test's
// simulator never collides with the suite's own.
func freeLocalPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	port := listener.Addr().(*net.TCPAddr).Port
	require.NoError(t, listener.Close())
	return port
}
