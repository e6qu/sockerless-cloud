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

// A simulator must not outlive the test process that started it.
//
// Every harness here stops the simulator from its own cleanup, which covers
// each ordinary ending and none of the ending that matters: a `go test` killed
// outright never reaches cleanup, and the simulator it started keeps running.
// Its container reaper waits on the simulator, so the pair survives together —
// simulators aged two to twelve days were found this way, across all three
// clouds, holding ports and memory.
//
// TestMain sets SOCKERLESS_PARENT_PID to this process, and every simulator
// started here inherits it through os.Environ(). This drives the whole chain
// against a stand-in parent, because the real one cannot be killed to prove
// the point: a simulator is started watching a process that is not this test,
// that process is killed, and the simulator has to go with it.
func TestSimulatorExitsWithItsParent(t *testing.T) {
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

	// It stays up while its parent does; a watch that fired on every input
	// would pass the rest of this test without ever looking at the pid.
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
