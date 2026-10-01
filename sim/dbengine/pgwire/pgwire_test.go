package pgwire

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// TestAcceptsConnections pins the readiness classification the endpoint
// applies before it relays a client to its engine. PostgreSQL's postmaster
// binds its port as soon as it starts and answers every client with "the
// database system is starting up" (SQLSTATE 57P03) until recovery finishes, so
// an ErrorResponse alone does not mean the server is serving. Every other
// answer — an authentication request, or a rejection only a serving server
// produces — does.
func TestAcceptsConnections(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		response []byte
		accepts  bool
	}{
		{name: "authentication request", response: authenticationCleartextPassword(), accepts: true},
		{name: "still starting up", response: errorResponse("FATAL", "57P03", "the database system is starting up"), accepts: false},
		{name: "invalid password", response: errorResponse("FATAL", "28P01", `password authentication failed for user "dbadmin"`), accepts: true},
		{name: "database does not exist", response: errorResponse("FATAL", "3D000", `database "application" does not exist`), accepts: true},
		{name: "connection closed without an answer", response: nil, accepts: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			startup := make(chan []byte, 1)
			go func() {
				defer server.Close()
				_ = server.SetDeadline(time.Now().Add(5 * time.Second))
				packet, err := readStartupPacket(server)
				if err != nil {
					startup <- nil
					return
				}
				startup <- packet
				if testCase.response != nil {
					_, _ = server.Write(testCase.response)
				}
			}()
			_ = client.SetDeadline(time.Now().Add(5 * time.Second))
			if got := AcceptsConnections(client, "dbadmin", "application"); got != testCase.accepts {
				t.Fatalf("AcceptsConnections = %v, want %v", got, testCase.accepts)
			}
			packet := <-startup
			if len(packet) < 8 || binary.BigEndian.Uint32(packet[4:8]) != protocolVersion3 {
				t.Fatalf("probe did not send a protocol 3.0 startup packet: %q", packet)
			}
			if startupParameter(packet, "user") != "dbadmin" || startupParameter(packet, "database") != "application" {
				t.Fatalf("probe startup carried the wrong parameters: %q", packet)
			}
		})
	}
}

// TestFrontendRejectsWrongPasswordLikePostgreSQL pins the refusal a real
// PostgreSQL server sends for a wrong password: a FATAL ErrorResponse, severity
// in both S and V, SQLSTATE 28P01.
func TestFrontendRejectsWrongPasswordLikePostgreSQL(t *testing.T) {
	frontend := Frontend{Authenticate: func(user, password string, secure bool) bool {
		return user == "dbadmin" && password == "correct"
	}}
	fields := runPlaintextClient(t, frontend, "dbadmin", "wrong", false)
	if fields['S'] != "FATAL" || fields['V'] != "FATAL" || fields['C'] != "28P01" {
		t.Fatalf("refusal fields = %v, want S=FATAL V=FATAL C=28P01", fields)
	}
	if fields['M'] != `password authentication failed for user "dbadmin"` {
		t.Fatalf("refusal message = %q", fields['M'])
	}
}

func TestFrontendRefusesPlaintextWhenAsked(t *testing.T) {
	frontend := Frontend{
		RefusePlaintext: func() (string, bool) { return "TLS is required", true },
		Authenticate:    func(string, string, bool) bool { return true },
	}
	fields := runPlaintextClient(t, frontend, "dbadmin", "correct", true)
	if fields['S'] != "FATAL" || fields['C'] != "28000" || fields['M'] != "TLS is required" {
		t.Fatalf("refusal fields = %v, want FATAL 28000 %q", fields, "TLS is required")
	}
}

func TestFrontendAcceptsInsideTLS(t *testing.T) {
	certificate := testCertificate(t)
	var sawSecure bool
	frontend := Frontend{
		Certificate:     func() (tls.Certificate, error) { return certificate, nil },
		RefusePlaintext: func() (string, bool) { return "TLS is required", true },
		Authenticate: func(user, password string, secure bool) bool {
			sawSecure = secure
			return user == "dbadmin" && password == "correct"
		},
	}
	client, server := net.Pipe()
	defer client.Close()
	type accepted struct {
		startup []byte
		session net.Conn
		err     error
	}
	result := make(chan accepted, 1)
	go func() {
		startup, session, err := frontend.Accept(server)
		result <- accepted{startup, session, err}
	}()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	request := make([]byte, 8)
	binary.BigEndian.PutUint32(request[:4], 8)
	binary.BigEndian.PutUint32(request[4:], sslRequestCode)
	if _, err := client.Write(request); err != nil {
		t.Fatal(err)
	}
	answer := make([]byte, 1)
	if _, err := io.ReadFull(client, answer); err != nil || answer[0] != 'S' {
		t.Fatalf("SSLRequest answer = %q, %v", answer, err)
	}
	secureClient := tls.Client(client, &tls.Config{InsecureSkipVerify: true}) //nolint:gosec // the test certificate is self-signed
	if _, err := secureClient.Write(startupPacket("dbadmin", "application")); err != nil {
		t.Fatal(err)
	}
	expectPasswordRequest(t, secureClient)
	if _, err := secureClient.Write(passwordMessage("correct")); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("Accept: %v", got.err)
	}
	if !sawSecure {
		t.Fatal("Authenticate was not told the session runs inside TLS")
	}
	if _, isTLS := got.session.(*tls.Conn); !isTLS {
		t.Fatalf("session continues on %T, want the TLS connection", got.session)
	}
	if startupParameter(got.startup, "database") != "application" {
		t.Fatalf("startup returned for relay = %q", got.startup)
	}
}

// TestFrontendOpensReadOnlySessions pins that a read-only session reaches the
// engine with default_transaction_read_only on, whatever the client asked for.
func TestFrontendOpensReadOnlySessions(t *testing.T) {
	frontend := Frontend{
		Authenticate: func(string, string, bool) bool { return true },
		ReadOnly:     true,
	}
	client, server := net.Pipe()
	defer client.Close()
	type accepted struct {
		startup []byte
		err     error
	}
	result := make(chan accepted, 1)
	go func() {
		startup, _, err := frontend.Accept(server)
		result <- accepted{startup, err}
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	parameters := []byte("user\x00dbadmin\x00Default_Transaction_Read_Only\x00off\x00database\x00application\x00\x00")
	packet := make([]byte, 8+len(parameters))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(packet)))
	binary.BigEndian.PutUint32(packet[4:8], protocolVersion3)
	copy(packet[8:], parameters)
	if _, err := client.Write(packet); err != nil {
		t.Fatal(err)
	}
	expectPasswordRequest(t, client)
	if _, err := client.Write(passwordMessage("correct")); err != nil {
		t.Fatal(err)
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("Accept: %v", got.err)
	}
	if int(binary.BigEndian.Uint32(got.startup[:4])) != len(got.startup) || binary.BigEndian.Uint32(got.startup[4:8]) != protocolVersion3 {
		t.Fatalf("rewritten startup packet has a wrong header: %q", got.startup)
	}
	want := "user\x00dbadmin\x00database\x00application\x00default_transaction_read_only\x00on\x00\x00"
	if string(got.startup[8:]) != want {
		t.Fatalf("startup parameters = %q, want %q", got.startup[8:], want)
	}
}

func runPlaintextClient(t *testing.T, frontend Frontend, user, password string, refusedBeforePassword bool) map[byte]string {
	t.Helper()
	client, server := net.Pipe()
	defer client.Close()
	done := make(chan error, 1)
	go func() {
		_, _, err := frontend.Accept(server)
		_ = server.Close()
		done <- err
	}()
	_ = client.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := client.Write(startupPacket(user, "application")); err != nil {
		t.Fatal(err)
	}
	if !refusedBeforePassword {
		expectPasswordRequest(t, client)
		if _, err := client.Write(passwordMessage(password)); err != nil {
			t.Fatal(err)
		}
	}
	header := make([]byte, 5)
	if _, err := io.ReadFull(client, header); err != nil {
		t.Fatal(err)
	}
	if header[0] != 'E' {
		t.Fatalf("message type = %q, want ErrorResponse", header[0])
	}
	body := make([]byte, binary.BigEndian.Uint32(header[1:])-4)
	if _, err := io.ReadFull(client, body); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("Accept returned no error for a refused client")
	}
	fields := map[byte]string{}
	for _, field := range strings.Split(strings.TrimRight(string(body), "\x00"), "\x00") {
		if field != "" {
			fields[field[0]] = field[1:]
		}
	}
	return fields
}

func expectPasswordRequest(t *testing.T, connection io.Reader) {
	t.Helper()
	request := make([]byte, 9)
	if _, err := io.ReadFull(connection, request); err != nil {
		t.Fatal(err)
	}
	if request[0] != 'R' || binary.BigEndian.Uint32(request[5:]) != authCleartext {
		t.Fatalf("authentication request = %q, want AuthenticationCleartextPassword", request)
	}
}

func startupPacket(user, database string) []byte {
	parameters := []byte("user\x00" + user + "\x00database\x00" + database + "\x00\x00")
	packet := make([]byte, 8+len(parameters))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(packet)))
	binary.BigEndian.PutUint32(packet[4:8], protocolVersion3)
	copy(packet[8:], parameters)
	return packet
}

func passwordMessage(password string) []byte {
	message := make([]byte, 5+len(password)+1)
	message[0] = 'p'
	binary.BigEndian.PutUint32(message[1:5], uint32(len(password)+5))
	copy(message[5:], password)
	return message
}

func authenticationCleartextPassword() []byte {
	message := make([]byte, 9)
	message[0] = 'R'
	binary.BigEndian.PutUint32(message[1:5], 8)
	binary.BigEndian.PutUint32(message[5:9], authCleartext)
	return message
}

func errorResponse(severity, code, text string) []byte {
	body := []byte("S" + severity + "\x00V" + severity + "\x00C" + code + "\x00M" + text + "\x00\x00")
	message := make([]byte, 5+len(body))
	message[0] = 'E'
	binary.BigEndian.PutUint32(message[1:5], uint32(len(body)+4))
	copy(message[5:], body)
	return message
}

func testCertificate(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pgwire test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
