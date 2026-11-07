package heldports

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

func TestHandPassesTheHeldSocketsAcrossRestarts(t *testing.T) {
	api, err := TCP("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	dns, err := TCPUDP("127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { Release(api, dns) })
	child := filepath.Join(t.TempDir(), "inherit")
	build := exec.Command("go", "build", "-o", child, "./testdata/inherit")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the inheriting process: %v\n%s", err, out)
	}
	for run := range 2 {
		cmd := exec.Command(child)
		cmd.Env = append(os.Environ(), "LISTEN_FDS=99")
		if err := Hand(cmd, api, dns); err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		lines := bufio.NewScanner(out)
		var got []string
		for lines.Scan() && lines.Text() != "ready" {
			got = append(got, lines.Text())
		}
		want := []string{fmt.Sprintf("tcp %d", api), fmt.Sprintf("tcp %d", dns), fmt.Sprintf("udp %d", dns)}
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("run %d inherited %v, want %v", run, got, want)
		}
		conn, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(api)))
		if err != nil {
			t.Fatal(err)
		}
		reply, err := bufio.NewReader(conn).ReadString('\n')
		_ = conn.Close()
		if err != nil || reply != "served\n" {
			t.Fatalf("run %d answered %q, %v", run, reply, err)
		}
		if err := cmd.Wait(); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
	}
}

func TestHandRefusesAPortNotHeld(t *testing.T) {
	if err := Hand(exec.Command("true"), 1); err == nil {
		t.Fatal("Hand passed a port it does not hold")
	}
}
