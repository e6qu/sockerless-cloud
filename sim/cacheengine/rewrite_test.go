package cacheengine

import (
	"bufio"
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"
)

// An engine that names its nodes' network addresses reaches a client naming
// their endpoints: in a redirection, whatever command drew it, and in the
// replies of the commands that report the cluster's topology, but never in a
// key's value.
func TestRelayRepliesNameEndpoints(t *testing.T) {
	addresses := map[string]string{"172.18.0.5": "127.0.0.9", "172.18.0.50": "127.0.0.10"}
	client, clientEnd := net.Pipe()
	backend, engineEnd := net.Pipe()
	commands := &commandQueue{}
	for _, args := range [][]string{{"GET", "k"}, {"CLUSTER", "SLOTS"}, {"cluster", "nodes"}, {"SET", "k", "v"}, {"GET", "k"}} {
		commands.push(args)
	}
	go func() { _ = relayReplies(clientEnd, backend, commands, func() map[string]string { return addresses }) }()
	nodes := "abc 172.18.0.5:6390@16390 myself,master - 0 0 1 connected"
	go func() {
		_, _ = engineEnd.Write([]byte(strings.Join([]string{
			"-MOVED 3999 172.18.0.50:6390",
			"*1", "*3", ":0", ":8191", "*3", "$10", "172.18.0.5", ":6390", "$2", "id",
			"$" + strconv.Itoa(len(nodes)), nodes,
			"+OK",
			"$10", "172.18.0.5",
		}, "\r\n") + "\r\n"))
	}()
	reader := bufio.NewReader(client)
	var got []string
	for range 5 {
		v, err := readValue(reader)
		if err != nil {
			t.Fatal(err)
		}
		var b bytes.Buffer
		v.encode(&b)
		got = append(got, b.String())
	}
	want := []string{
		"-MOVED 3999 127.0.0.10:6390\r\n",
		"*1\r\n*3\r\n:0\r\n:8191\r\n*3\r\n$9\r\n127.0.0.9\r\n:6390\r\n$2\r\nid\r\n",
		"$" + strconv.Itoa(len(nodes)-1) + "\r\nabc 127.0.0.9:6390@16390 myself,master - 0 0 1 connected\r\n",
		"+OK\r\n",
		"$10\r\n172.18.0.5\r\n",
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("reply %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A connection that subscribes receives messages no command asked for, so
// its replies stop pairing with commands and only a redirection is rewritten.
func TestCommandQueueStopsPairingOnceSubscribed(t *testing.T) {
	commands := &commandQueue{}
	commands.push([]string{"CLUSTER", "SLOTS"})
	commands.push([]string{"SUBSCRIBE", "news"})
	if _, ok := commands.pop(); ok {
		t.Fatal("a subscribed connection paired a reply with a command")
	}
}

// Every RESP3 type round-trips through the relay byte for byte.
func TestRESP3ValuesRoundTrip(t *testing.T) {
	for _, wire := range []string{
		"%1\r\n+ip\r\n$10\r\n172.18.0.5\r\n",
		"~2\r\n:1\r\n_\r\n",
		">3\r\n$7\r\nmessage\r\n$1\r\nc\r\n$1\r\nm\r\n",
		"|1\r\n+ttl\r\n:3\r\n$1\r\nv\r\n",
		"=8\r\ntxt:abcd\r\n",
		"$-1\r\n", "*-1\r\n", ",1.5\r\n", "#t\r\n", "(12345678901234567890\r\n", "!3\r\nerr\r\n",
	} {
		v, err := readValue(bufio.NewReader(strings.NewReader(wire)))
		if err != nil {
			t.Fatalf("%q: %v", wire, err)
		}
		var b bytes.Buffer
		v.encode(&b)
		if b.String() != wire {
			t.Fatalf("%q round-trips as %q", wire, b.String())
		}
	}
}
