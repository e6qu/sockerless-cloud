package dbengine

import (
	"io"
	"net"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestListenLoopbackGivesEachIdentityItsOwnAddress(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	first, err := ListenLoopback("project-a/orders", port)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := ListenLoopback("project-a/orders", port)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for _, listener := range []net.Listener{first, second} {
		address := listener.Addr().(*net.TCPAddr)
		if !address.IP.IsLoopback() || address.Port != port {
			t.Fatalf("listener address %s, want a loopback address at port %d", address, port)
		}
	}
	if first.Addr().String() == second.Addr().String() {
		t.Fatalf("two listeners share %s", first.Addr())
	}
}

// TestListenLoopbackKeepsTheAdvertisedPort pins that a taken port is an error,
// never a different port: the control plane advertises the engine's port, and
// an endpoint serving another one would make that coordinate a lie.
func TestListenLoopbackKeepsTheAdvertisedPort(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("only Linux refuses a specific-address bind under a listening wildcard socket")
	}
	occupant, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupant.Close()
	port := occupant.Addr().(*net.TCPAddr).Port
	listener, err := ListenLoopback("db-1", port)
	if err == nil {
		got := listener.Addr().String()
		_ = listener.Close()
		t.Fatalf("ListenLoopback bound %s while every loopback address's port %d was taken", got, port)
	}
}

func TestSelfSignedCertificateIsGeneratedOnce(t *testing.T) {
	certificate := SelfSignedCertificate("Managed database simulator")
	first, err := certificate()
	if err != nil {
		t.Fatal(err)
	}
	second, err := certificate()
	if err != nil {
		t.Fatal(err)
	}
	if first.Leaf == nil || first.Leaf.Subject.CommonName != "Managed database simulator" {
		t.Fatalf("certificate subject = %v", first.Leaf)
	}
	if string(first.Certificate[0]) != string(second.Certificate[0]) {
		t.Fatal("a second call generated a different certificate")
	}
}

func TestQuoting(t *testing.T) {
	for _, testCase := range []struct{ got, want string }{
		{QuoteIdentifier(`app"user`), `"app""user"`},
		{QuoteMySQLIdentifier("app`db"), "`app``db`"},
		{QuoteLiteral(`it's \n`), `'it''s \n'`},
		{QuoteMySQLLiteral(`it's \'`), `'it''s \\'''`},
		{ShellQuote(`it's`), `'it'\''s'`},
	} {
		if testCase.got != testCase.want {
			t.Errorf("quoted %s, want %s", testCase.got, testCase.want)
		}
	}
}

// TestRelayPropagatesHalfClose checks that a client finishing its side
// reaches the engine as EOF while the engine's answer still reaches the client.
func TestRelayPropagatesHalfClose(t *testing.T) {
	clientSide, proxyClient := tcpPair(t)
	proxyEngine, engineSide := tcpPair(t)
	go relay(proxyClient, proxyEngine)

	if _, err := clientSide.Write([]byte("SELECT 1")); err != nil {
		t.Fatal(err)
	}
	if err := clientSide.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = engineSide.SetDeadline(time.Now().Add(5 * time.Second))
	request, err := io.ReadAll(engineSide)
	if err != nil || string(request) != "SELECT 1" {
		t.Fatalf("engine read %q, %v", request, err)
	}
	if _, err := engineSide.Write([]byte("1")); err != nil {
		t.Fatal(err)
	}
	_ = engineSide.Close()
	_ = clientSide.SetDeadline(time.Now().Add(5 * time.Second))
	answer, err := io.ReadAll(clientSide)
	if err != nil || string(answer) != "1" {
		t.Fatalf("client read %q, %v", answer, err)
	}
}

func tcpPair(t *testing.T) (net.Conn, net.Conn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, _ := listener.Accept()
		accepted <- connection
	}()
	dialed, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(listener.Addr().(*net.TCPAddr).Port)))
	if err != nil {
		t.Fatal(err)
	}
	other := <-accepted
	t.Cleanup(func() { _ = dialed.Close(); _ = other.Close() })
	return dialed, other
}

func TestAnEngineWithoutAnImageIsRefused(t *testing.T) {
	i := &Instance{Name: "no-image", Engine: Postgres16}
	if _, _, err := i.start(); err == nil || !strings.Contains(err.Error(), "names no image") {
		t.Fatalf("an engine with no image started: %v", err)
	}
	if got := Postgres16.WithImage("example/postgres:16").Image; got != "example/postgres:16" {
		t.Fatalf("WithImage: %q", got)
	}
}
