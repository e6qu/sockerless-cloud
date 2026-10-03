package aws_cli_test

import (
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

// A CLI call that never gets an answer ends at its bound with the CLI's --debug
// log and the simulator's diagnostics, instead of holding the test until the
// binary's -timeout panics with neither.
func TestELBv2CLIHarnessEndsAStalledCallWithDiagnostics(t *testing.T) {
	silent, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := silent.Accept()
		if err == nil {
			accepted <- conn
		}
	}()

	cmd := awsCLI("elbv2", "get-resource-policy", "--resource-arn",
		"arn:aws:elasticloadbalancing:us-east-1:123456789012:truststore/stalled/0123456789abcdef")
	cmd.Env = append(cmd.Env, "AWS_ENDPOINT_URL=http://"+silent.Addr().String())
	cmd.Args = append(cmd.Args, "--debug")
	var stderr lockedBuffer
	cmd.Stderr = &stderr

	const bound = 3 * time.Second
	started := time.Now()
	err = runCLIBounded(t, cmd, bound)
	took := time.Since(started)
	select {
	case conn := <-accepted:
		conn.Close()
	default:
		t.Fatal("the CLI never connected to the silent endpoint")
	}
	if !errors.Is(err, errCLIBoundExceeded) {
		t.Fatalf("runCLIBounded = %v, want errCLIBoundExceeded", err)
	}
	if took > bound+cmdWaitDelay {
		t.Fatalf("a stalled call took %s to end, bound %s", took, bound)
	}
	if !strings.Contains(stderr.String(), "GetResourcePolicy") {
		t.Fatalf("the CLI's --debug log does not name the stalled operation:\n%s", tail(stderr.String(), cliDebugTail))
	}
	if inflight := simulatorDiagnostics("/debug/inflight"); !strings.Contains(inflight, "in-flight request(s)") {
		t.Fatalf("the simulator's /debug/inflight did not answer: %s", inflight)
	}
}
