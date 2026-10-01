package sim

import (
	"context"
	"fmt"
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// publishTestImage carries busybox's nc, whose -e answers every connection
// with a fixed greeting.
const publishTestImage = "public.ecr.aws/docker/library/busybox:latest"

// readGreeting dials the host port until the workload's listener answers. The
// engine's port forwarder accepts a connection before the listener behind it
// is up and closes it empty, and the engine reports no event for the listener
// coming up, so the dial repeats until the greeting arrives.
func readGreeting(t *testing.T, hostPort int) string {
	t.Helper()
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort))
	deadline := time.Now().Add(time.Minute)
	for {
		conn, err := net.DialTimeout("tcp", address, 5*time.Second)
		if err == nil {
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			greeting, readErr := io.ReadAll(conn)
			_ = conn.Close()
			if readErr == nil && len(greeting) > 0 {
				return strings.TrimSpace(string(greeting))
			}
			err = readErr
		}
		if time.Now().After(deadline) {
			t.Fatalf("no greeting on %s: %v", address, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// The engine allocates the host port of a published container port, and the
// handle reads back the port the engine bound: a client dialling it reaches
// the workload's listener.
func TestPublishedPortReadsTheEngineAllocatedPort(t *testing.T) {
	if _, err := InitDocker("aws", true, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	const containerPort = 8080
	handle, err := StartContainerSync(ContainerConfig{
		Image:        publishTestImage,
		Architecture: "linux/" + runtime.GOARCH,
		Command:      []string{"nc"},
		Args:         []string{"-lk", "-p", strconv.Itoa(containerPort), "-e", "echo", "published"},
		Name:         fmt.Sprintf("sockerless-sim-publish-%d", time.Now().UnixNano()),
		PublishPorts: []int{containerPort},
	}, FuncSink(func(LogLine) {}))
	if err != nil {
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() {
		handle.Cancel()
		_ = handle.Wait()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	hostPort, err := handle.PublishedPort(ctx, containerPort)
	if err != nil {
		t.Fatalf("read the published port: %v", err)
	}
	if hostPort <= 0 || hostPort > 65535 {
		t.Fatalf("published port %d is not a TCP port", hostPort)
	}
	if got := readGreeting(t, hostPort); got != "published" {
		t.Fatalf("127.0.0.1:%d answered %q, want the workload's greeting", hostPort, got)
	}

	if _, err := handle.PublishedPort(ctx, containerPort+1); err == nil {
		t.Fatalf("a container port the container does not publish reported a host port")
	}
}
