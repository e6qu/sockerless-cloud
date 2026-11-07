package listenfds

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// TestInheritedSocketsServeTheirPorts expects a process started with
// LISTEN_PID and LISTEN_FDS to take the inherited sockets for the ports it
// binds, to bind a fresh socket for a port it did not inherit, and to unset
// the variables.
func TestInheritedSocketsServeTheirPorts(t *testing.T) {
	ln, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = udp.Close() }()
	tcpFile, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	udpFile, err := udp.File()
	if err != nil {
		t.Fatal(err)
	}
	tcpPort, udpPort := ln.Addr().(*net.TCPAddr).Port, udp.LocalAddr().(*net.UDPAddr).Port

	child := filepath.Join(t.TempDir(), "inherit")
	build := exec.Command("go", "build", "-o", child, "./testdata/inherit")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the inheriting process: %v\n%s", err, out)
	}
	cmd := exec.Command("/bin/sh", "-c", `LISTEN_PID=$$; export LISTEN_PID; exec "$0"`, child)
	cmd.Env = append(os.Environ(), "LISTEN_FDS=2", fmt.Sprintf("LISTENFDS_CHILD=%d %d", tcpPort, udpPort))
	cmd.ExtraFiles = []*os.File{tcpFile, udpFile}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	report, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if want := fmt.Sprintf("tcp %d udp %d fresh true env \"\"\n", tcpPort, udpPort); report != want {
		t.Fatalf("the child reported %q, want %q", report, want)
	}
	conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(tcpPort)))
	if err != nil {
		t.Fatal(err)
	}
	reply, err := bufio.NewReader(conn).ReadString('\n')
	_ = conn.Close()
	if err != nil || reply != "served\n" {
		t.Fatalf("the inherited socket answered %q, %v", reply, err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}

// TestSocketsForAnotherProcessAreLeftAlone expects a process whose ID
// LISTEN_PID does not name to bind its own sockets.
func TestSocketsForAnotherProcessAreLeftAlone(t *testing.T) {
	t.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()+1))
	t.Setenv("LISTEN_FDS", "1")
	load()
	if len(listeners)+len(packets) != 0 || loadErr != nil {
		t.Fatalf("took %d sockets meant for another process, %v", len(listeners)+len(packets), loadErr)
	}
	if os.Getenv("LISTEN_FDS") != "" {
		t.Fatal("LISTEN_FDS was left set")
	}
}
