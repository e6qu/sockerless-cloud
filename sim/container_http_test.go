package sim

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// A workload that serves on a port other than 8080 is reachable through the
// port the engine publishes for it, as a host that routes no container
// address needs.
func TestStartHTTPContainerPublishesTheDeclaredPort(t *testing.T) {
	InitDocker("aws", true, t.TempDir())
	platform := "linux/" + runtime.GOARCH
	if err := PullImage(t.Context(), volumeDeclaringTestImage, platform); err != nil {
		t.Fatal(err)
	}
	containerID, err := StartHTTPContainer(t.Context(), HTTPContainerConfig{
		Image:        volumeDeclaringTestImage,
		Architecture: platform,
		Args:         []string{"redis-server", "--port", "9091"},
		Name:         fmt.Sprintf("sockerless-sim-published-port-%d", time.Now().UnixNano()),
		PublishPorts: []int{9091},
	})
	if err != nil {
		t.Fatalf("start workload: %v", err)
	}
	t.Cleanup(func() { StopAndRemoveContainer(containerID, 0) })

	// Redis announces on its log when it accepts connections.
	ready := make(chan struct{})
	var once sync.Once
	logCtx, stopLogs := context.WithCancel(t.Context())
	defer stopLogs()
	go StreamContainerLogs(logCtx, containerID, FuncSink(func(line LogLine) {
		if strings.Contains(line.Text, "Ready to accept connections") {
			once.Do(func() { close(ready) })
		}
	}))
	select {
	case <-ready:
	case <-time.After(time.Minute):
		t.Fatal("the workload never logged that it accepts connections")
	}

	hostPort, err := PublishedHostPort(t.Context(), containerID, 9091)
	if err != nil {
		t.Fatal(err)
	}
	reply, err := redisPing(net.JoinHostPort("127.0.0.1", strconv.Itoa(hostPort)))
	if err != nil {
		t.Fatalf("the workload's port 9091 is not reachable through its published port: %v", err)
	}
	if reply != "+PONG" {
		t.Fatalf("the workload on port 9091 answered %q", reply)
	}
}

func redisPing(address string) (string, error) {
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(time.Second)); err != nil {
		return "", err
	}
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return "", err
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		return "", err
	}
	return line[:len(line)-2], nil
}

func TestStartHTTPContainerRequiresAPort(t *testing.T) {
	if _, err := StartHTTPContainer(t.Context(), HTTPContainerConfig{Image: volumeDeclaringTestImage}); err == nil {
		t.Fatal("an HTTP container started with no port to publish")
	}
}
