// Package pgwire speaks the front half of the PostgreSQL frontend/backend
// protocol a managed-database endpoint owns: the startup packet, the SSLRequest
// upgrade, a cleartext-password exchange, and ErrorResponse refusals. The
// session after authentication is the engine's, relayed byte for byte.
package pgwire

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
)

const (
	protocolVersion3 = 196608
	sslRequestCode   = 80877103
	maxStartupLength = 1 << 20
	authCleartext    = 3
)

const (
	sqlStateInvalidAuthorization = "28000"
	sqlStateInvalidPassword      = "28P01"
	sqlStateCannotConnectNow     = "57P03"
)

// Frontend authenticates a client before its session is relayed to the engine.
type Frontend struct {
	Certificate func() (tls.Certificate, error)
	// RefusePlaintext, when set, answers whether a client that did not ask for
	// TLS is refused, and with which message.
	RefusePlaintext func() (message string, refuse bool)
	// Authenticate checks the password a client presented for user; secure
	// reports whether the session runs inside TLS.
	Authenticate func(user, password string, secure bool) bool
	// ReadOnly opens the session with default_transaction_read_only on, so
	// every transaction it starts refuses writes the way a hot standby does.
	ReadOnly bool
}

// Accept runs the client's opening exchange and returns its startup packet
// and the connection the session continues on — the TLS connection when the
// client upgraded. A refused client has already been sent its ErrorResponse.
func (f Frontend) Accept(client net.Conn) ([]byte, net.Conn, error) {
	startup, err := readStartupPacket(client)
	if err != nil {
		return nil, nil, fmt.Errorf("read startup packet: %w", err)
	}
	secure := false
	if len(startup) == 8 && binary.BigEndian.Uint32(startup[4:]) == sslRequestCode {
		if _, err := client.Write([]byte{'S'}); err != nil {
			return nil, nil, err
		}
		certificate, err := f.Certificate()
		if err != nil {
			return nil, nil, fmt.Errorf("server certificate: %w", err)
		}
		upgraded := tls.Server(client, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := upgraded.Handshake(); err != nil {
			return nil, nil, fmt.Errorf("TLS handshake: %w", err)
		}
		client, secure = upgraded, true
		if startup, err = readStartupPacket(client); err != nil {
			return nil, nil, fmt.Errorf("read startup packet inside TLS: %w", err)
		}
	} else if f.RefusePlaintext != nil {
		if message, refuse := f.RefusePlaintext(); refuse {
			writeErrorResponse(client, "FATAL", sqlStateInvalidAuthorization, message)
			return nil, nil, fmt.Errorf("refused a plaintext client")
		}
	}
	user := startupParameter(startup, "user")
	password, err := requestCleartextPassword(client)
	if err == nil && f.Authenticate(user, password, secure) {
		if f.ReadOnly {
			startup = withStartupParameter(startup, "default_transaction_read_only", "on")
		}
		return startup, client, nil
	}
	writeErrorResponse(client, "FATAL", sqlStateInvalidPassword, fmt.Sprintf("password authentication failed for user %q", user))
	if err != nil {
		return nil, nil, fmt.Errorf("password exchange for user %q: %w", user, err)
	}
	return nil, nil, fmt.Errorf("password authentication failed for user %q", user)
}

func readStartupPacket(connection io.Reader) ([]byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := int(binary.BigEndian.Uint32(header))
	if length < 8 || length > maxStartupLength {
		return nil, fmt.Errorf("invalid PostgreSQL startup packet length %d", length)
	}
	packet := make([]byte, length)
	copy(packet, header)
	_, err := io.ReadFull(connection, packet[4:])
	return packet, err
}

func startupParameter(packet []byte, wanted string) string {
	if len(packet) < 9 {
		return ""
	}
	fields := strings.Split(string(packet[8:len(packet)-1]), "\x00")
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == wanted {
			return fields[i+1]
		}
	}
	return ""
}

// withStartupParameter sets name in a startup packet, replacing any value the
// client sent. PostgreSQL applies a startup parameter after the switches in
// "options", so the value holds against a -c switch too.
func withStartupParameter(packet []byte, name, value string) []byte {
	parameters := []byte{}
	if len(packet) > 9 {
		fields := strings.Split(string(packet[8:len(packet)-1]), "\x00")
		for i := 0; i+1 < len(fields); i += 2 {
			if strings.EqualFold(fields[i], name) {
				continue
			}
			parameters = append(parameters, fields[i]+"\x00"+fields[i+1]+"\x00"...)
		}
	}
	parameters = append(parameters, name+"\x00"+value+"\x00\x00"...)
	rewritten := make([]byte, 8+len(parameters))
	binary.BigEndian.PutUint32(rewritten[:4], uint32(len(rewritten)))
	copy(rewritten[4:8], packet[4:8])
	copy(rewritten[8:], parameters)
	return rewritten
}

func requestCleartextPassword(connection io.ReadWriter) (string, error) {
	request := make([]byte, 9)
	request[0] = 'R'
	binary.BigEndian.PutUint32(request[1:5], 8)
	binary.BigEndian.PutUint32(request[5:9], authCleartext)
	if _, err := connection.Write(request); err != nil {
		return "", err
	}
	header := make([]byte, 5)
	if _, err := io.ReadFull(connection, header); err != nil {
		return "", err
	}
	if header[0] != 'p' {
		return "", fmt.Errorf("expected a PasswordMessage, got message type %q", header[0])
	}
	length := int(binary.BigEndian.Uint32(header[1:]))
	if length < 5 || length > maxStartupLength {
		return "", fmt.Errorf("invalid PasswordMessage length %d", length)
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(connection, body); err != nil {
		return "", err
	}
	return strings.TrimSuffix(string(body), "\x00"), nil
}

// writeErrorResponse sends the ErrorResponse a server sends a client it will
// not serve: severity in both the localized (S) and non-localized (V) fields.
func writeErrorResponse(connection io.Writer, severity, sqlState, message string) {
	payload := []byte("S" + severity + "\x00V" + severity + "\x00C" + sqlState + "\x00M" + message + "\x00\x00")
	packet := make([]byte, 5+len(payload))
	packet[0] = 'E'
	binary.BigEndian.PutUint32(packet[1:5], uint32(len(payload)+4))
	copy(packet[5:], payload)
	_, _ = connection.Write(packet)
}

// AcceptsConnections classifies a startup exchange the way libpq's PQping —
// and so pg_isready — does. A server that answers is up even when it rejects
// the startup, except while it answers every client "the database system is
// starting up" (SQLSTATE 57P03) because recovery has not finished.
func AcceptsConnections(connection io.ReadWriter, user, database string) bool {
	parameters := []byte("user\x00" + user + "\x00database\x00" + database + "\x00\x00")
	packet := make([]byte, 8+len(parameters))
	binary.BigEndian.PutUint32(packet[:4], uint32(len(packet)))
	binary.BigEndian.PutUint32(packet[4:8], protocolVersion3)
	copy(packet[8:], parameters)
	if _, err := connection.Write(packet); err != nil {
		return false
	}
	header := make([]byte, 5)
	if _, err := io.ReadFull(connection, header); err != nil {
		return false
	}
	switch header[0] {
	case 'R':
		return true
	case 'E':
		return errorSQLState(connection, header) != sqlStateCannotConnectNow
	default:
		return false
	}
}

func errorSQLState(connection io.Reader, header []byte) string {
	length := int(binary.BigEndian.Uint32(header[1:5]))
	if length < 4 || length > 1<<16 {
		return ""
	}
	body := make([]byte, length-4)
	if _, err := io.ReadFull(connection, body); err != nil {
		return ""
	}
	for _, field := range strings.Split(string(body), "\x00") {
		if strings.HasPrefix(field, "C") {
			return field[1:]
		}
	}
	return ""
}
