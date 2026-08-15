package main

import (
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

// TestRDSPostgreSQLAcceptsConnections pins the classification the Amazon RDS
// endpoint applies before it forwards a client to its engine. PostgreSQL's
// postmaster binds its port as soon as it starts and answers every client with
// "the database system is starting up" (SQLSTATE 57P03) until the startup
// process finishes recovery, so an ErrorResponse alone does not mean the server
// is serving. Every other answer — an authentication request, or a rejection
// the server could only produce once it was serving — does.
func TestRDSPostgreSQLAcceptsConnections(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		response []byte
		accepts  bool
	}{
		{
			name:     "authentication request",
			response: postgreSQLAuthenticationCleartextPassword(),
			accepts:  true,
		},
		{
			name:     "still starting up",
			response: postgreSQLErrorResponse("FATAL", "57P03", "the database system is starting up"),
			accepts:  false,
		},
		{
			name:     "invalid password",
			response: postgreSQLErrorResponse("FATAL", "28P01", `password authentication failed for user "dbadmin"`),
			accepts:  true,
		},
		{
			name:     "database does not exist",
			response: postgreSQLErrorResponse("FATAL", "3D000", `database "application" does not exist`),
			accepts:  true,
		},
		{
			name:     "connection closed without an answer",
			response: nil,
			accepts:  false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			startup := make(chan []byte, 1)
			go func() {
				defer server.Close()
				_ = server.SetDeadline(time.Now().Add(5 * time.Second))
				packet, err := readPostgreSQLStartupPacket(server)
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
			if got := rdsPostgreSQLAcceptsConnections(client, "dbadmin", "application"); got != testCase.accepts {
				t.Fatalf("rdsPostgreSQLAcceptsConnections = %v, want %v", got, testCase.accepts)
			}
			packet := <-startup
			if len(packet) < 8 || binary.BigEndian.Uint32(packet[4:8]) != 196608 {
				t.Fatalf("probe did not send a protocol 3.0 startup packet: %q", packet)
			}
		})
	}
}

func readPostgreSQLStartupPacket(connection net.Conn) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	packet := make([]byte, binary.BigEndian.Uint32(header))
	copy(packet, header)
	if _, err := io.ReadFull(connection, packet[4:]); err != nil {
		return nil, err
	}
	return packet, nil
}

func postgreSQLAuthenticationCleartextPassword() []byte {
	message := make([]byte, 9)
	message[0] = 'R'
	binary.BigEndian.PutUint32(message[1:5], 8)
	binary.BigEndian.PutUint32(message[5:9], 3)
	return message
}

func postgreSQLErrorResponse(severity, code, text string) []byte {
	body := []byte("S" + severity + "\x00V" + severity + "\x00C" + code + "\x00M" + text + "\x00\x00")
	message := make([]byte, 5+len(body))
	message[0] = 'E'
	binary.BigEndian.PutUint32(message[1:5], uint32(len(body)+4))
	copy(message[5:], body)
	return message
}
