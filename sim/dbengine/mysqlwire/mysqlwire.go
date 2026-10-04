// Package mysqlwire speaks the connection phase of the MySQL client/server
// protocol a managed-database endpoint owns. The endpoint greets the client
// with a handshake derived from the engine's own, takes the client's password
// through mysql_clear_password (TLS on offer), checks it, and logs into the
// engine with the credential the endpoint chooses. The command phase after
// that is the engine's, relayed byte for byte.
package mysqlwire

import (
	"bytes"
	"compress/zlib"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"strings"
)

const (
	clientConnectWithDB              uint32 = 0x00000008
	clientCompress                   uint32 = 0x00000020
	clientProtocol41                 uint32 = 0x00000200
	clientSSL                        uint32 = 0x00000800
	clientSecureConnection           uint32 = 0x00008000
	clientPluginAuth                 uint32 = 0x00080000
	clientConnectAttrs               uint32 = 0x00100000
	clientPluginAuthLenencClientData uint32 = 0x00200000
	clientZSTDCompression            uint32 = 0x04000000
	clientQueryAttributes            uint32 = 0x08000000

	protocolVersion10 = 10
	sslRequestLength  = 32
	comQuery          = 0x03
)

type handshake struct {
	serverVersion string
	connectionID  uint32
	authData      []byte
	capabilities  uint32
	charset       byte
	status        uint16
	plugin        string
}

type login struct {
	capabilities uint32
	charset      byte
	username     string
	password     string
	database     string
}

// Frontend authenticates a client before its session is relayed to the engine.
type Frontend struct {
	Certificate func() (tls.Certificate, error)
	// Authenticate checks the password a client presented for user; secure
	// reports whether the session runs inside TLS.
	Authenticate func(user, password string, secure bool) bool
	// BackendLogin names the engine account the authenticated client's
	// session logs in as.
	BackendLogin func(user, password string) (backendUser, backendPassword string, err error)
	// ReadOnly makes every transaction of the session read-only before the
	// client's first command reaches the engine.
	ReadOnly bool
}

// Accept runs the connection phase against both the client and the engine
// and returns the connection the client's session continues on — the TLS
// connection when the client upgraded. The engine's answer to the proxied
// login, success or refusal, reaches the client verbatim.
func (f Frontend) Accept(client, backend net.Conn) (net.Conn, error) {
	_, greeting, err := readPacket(backend)
	if err != nil {
		return nil, fmt.Errorf("read engine handshake: %w", err)
	}
	engineHandshake, err := parseHandshake(greeting)
	if err != nil {
		return nil, fmt.Errorf("parse engine handshake: %w", err)
	}
	offered := engineHandshake
	offered.authData = make([]byte, 20)
	if _, err := rand.Read(offered.authData); err != nil {
		return nil, err
	}
	offered.capabilities |= clientProtocol41 | clientSecureConnection | clientPluginAuth | clientSSL
	offered.plugin = "mysql_clear_password"
	if err := writePacket(client, 0, encodeHandshake(offered)); err != nil {
		return nil, err
	}

	sequence, payload, err := readPacket(client)
	if err != nil {
		return nil, fmt.Errorf("read client login: %w", err)
	}
	secure := false
	if len(payload) == sslRequestLength && binary.LittleEndian.Uint32(payload[:4])&clientSSL != 0 {
		certificate, err := f.Certificate()
		if err != nil {
			return nil, fmt.Errorf("server certificate: %w", err)
		}
		upgraded := tls.Server(client, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := upgraded.Handshake(); err != nil {
			return nil, fmt.Errorf("TLS handshake: %w", err)
		}
		client, secure = upgraded, true
		if sequence, payload, err = readPacket(client); err != nil {
			return nil, fmt.Errorf("read client login inside TLS: %w", err)
		}
	}
	presented, err := parseLogin(payload)
	if err != nil {
		writeAuthError(client, sequence+1, "Access denied")
		return nil, fmt.Errorf("parse client login: %w", err)
	}
	if !f.Authenticate(presented.username, presented.password, secure) {
		writeAuthError(client, sequence+1, fmt.Sprintf("Access denied for user '%s'", presented.username))
		return nil, fmt.Errorf("authentication rejected for %s", presented.username)
	}
	backendUser, backendPassword, err := f.BackendLogin(presented.username, presented.password)
	if err != nil {
		writeAuthError(client, sequence+1, err.Error())
		return nil, err
	}

	capabilities := sessionCapabilities(engineHandshake, presented.capabilities, presented.database)
	loginPayload := encodeBackendLogin(engineHandshake, capabilities, backendUser,
		backendPassword, presented.database, presented.charset)
	if err := writePacket(backend, 1, loginPayload); err != nil {
		return nil, fmt.Errorf("write engine login: %w", err)
	}
	backendSequence, result, err := readPacket(backend)
	if err != nil {
		return nil, fmt.Errorf("read engine auth result: %w", err)
	}
	scramble := engineHandshake.authData
	if len(result) > 1 && result[0] == 0xfe {
		plugin, offset, valid := nulTerminated(result, 1)
		if !valid {
			return nil, fmt.Errorf("engine sent a malformed auth switch")
		}
		seed := []byte(strings.TrimRight(string(result[offset:]), "\x00"))
		scramble = seed
		var response []byte
		switch plugin {
		case "mysql_native_password":
			response = nativePassword(backendPassword, seed)
		case "caching_sha2_password":
			response = cachingSHA2Password(backendPassword, seed)
		case "sha256_password":
			// sha256_password over a link without TLS asks for the
			// engine's public key with 0x01 in place of a response.
			if result, err = rsaPasswordExchange(backend, backendSequence, 0x01, backendPassword, seed); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("engine requested unsupported auth plugin %s", plugin)
		}
		if response != nil {
			if err := writePacket(backend, backendSequence+1, response); err != nil {
				return nil, err
			}
			if backendSequence, result, err = readPacket(backend); err != nil {
				return nil, fmt.Errorf("read engine auth switch result: %w", err)
			}
		}
	}
	if result, err = completeCachingSHA2(backend, backendSequence, result, backendPassword, scramble); err != nil {
		return nil, err
	}
	if len(result) == 0 || result[0] != 0x00 {
		_ = writePacket(client, sequence+1, result)
		return nil, fmt.Errorf("engine rejected the proxied login: %x", result)
	}
	if f.ReadOnly {
		refusal, err := setSessionReadOnly(backend, capabilities&clientCompress != 0)
		if err != nil {
			return nil, err
		}
		if refusal != nil {
			_ = writePacket(client, sequence+1, refusal)
			return nil, fmt.Errorf("engine refused a read-only session: %x", refusal)
		}
	}
	if err := writePacket(client, sequence+1, result); err != nil {
		return nil, err
	}
	return client, nil
}

// Greets reports whether the server behind connection is serving clients.
// MySQL and MariaDB bind their client port only once the server serves, and
// greet every accepted connection with a protocol-version-10 handshake;
// anything else — a connection a port forwarder accepted before the engine
// listened, or an error packet — is not a server ready for clients.
func Greets(connection io.Reader) bool {
	_, payload, err := readPacket(connection)
	return err == nil && len(payload) > 0 && payload[0] == protocolVersion10
}

func readPacket(connection io.Reader) (byte, []byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return 0, nil, err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	payload := make([]byte, length)
	_, err := io.ReadFull(connection, payload)
	return header[3], payload, err
}

func writePacket(connection io.Writer, sequence byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), sequence}
	if _, err := connection.Write(header); err != nil {
		return err
	}
	_, err := connection.Write(payload)
	return err
}

func parseHandshake(payload []byte) (handshake, error) {
	if len(payload) < 34 || payload[0] != protocolVersion10 {
		return handshake{}, fmt.Errorf("invalid MySQL handshake")
	}
	offset := 1
	end := strings.IndexByte(string(payload[offset:]), 0)
	if end < 0 || offset+end+1+4+9+2+1+2+2+1+10 > len(payload) {
		return handshake{}, fmt.Errorf("invalid MySQL server version")
	}
	serverVersion := string(payload[offset : offset+end])
	offset += end + 1
	connectionID := binary.LittleEndian.Uint32(payload[offset : offset+4])
	offset += 4
	authData := append([]byte(nil), payload[offset:offset+8]...)
	offset += 9
	capabilities := uint32(binary.LittleEndian.Uint16(payload[offset : offset+2]))
	offset += 2
	charset := payload[offset]
	offset++
	status := binary.LittleEndian.Uint16(payload[offset : offset+2])
	offset += 2
	capabilities |= uint32(binary.LittleEndian.Uint16(payload[offset:offset+2])) << 16
	offset += 2
	authLength := int(payload[offset])
	offset += 1 + 10
	authSecondLength := authLength - 8
	if authSecondLength < 13 {
		authSecondLength = 13
	}
	if offset+authSecondLength > len(payload) {
		authSecondLength = len(payload) - offset
	}
	authData = append(authData, payload[offset:offset+authSecondLength]...)
	authData = []byte(strings.TrimRight(string(authData), "\x00"))
	offset += authSecondLength
	plugin := ""
	if offset < len(payload) {
		plugin = strings.TrimRight(string(payload[offset:]), "\x00")
	}
	return handshake{
		serverVersion: serverVersion, connectionID: connectionID, authData: authData,
		capabilities: capabilities, charset: charset, status: status, plugin: plugin,
	}, nil
}

func encodeHandshake(h handshake) []byte {
	authData := append(append([]byte(nil), h.authData...), 0)
	for len(authData) < 21 {
		authData = append(authData, 0)
	}
	payload := []byte{protocolVersion10}
	payload = append(payload, h.serverVersion...)
	payload = append(payload, 0)
	payload = binary.LittleEndian.AppendUint32(payload, h.connectionID)
	payload = append(payload, authData[:8]...)
	payload = append(payload, 0)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(h.capabilities))
	payload = append(payload, h.charset)
	payload = binary.LittleEndian.AppendUint16(payload, h.status)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(h.capabilities>>16))
	payload = append(payload, byte(len(authData)))
	payload = append(payload, make([]byte, 10)...)
	payload = append(payload, authData[8:21]...)
	payload = append(payload, h.plugin...)
	payload = append(payload, 0)
	return payload
}

func parseLogin(payload []byte) (login, error) {
	if len(payload) < 32 {
		return login{}, fmt.Errorf("short MySQL login")
	}
	parsed := login{
		capabilities: binary.LittleEndian.Uint32(payload[:4]),
		charset:      payload[8],
	}
	offset := 32
	var ok bool
	parsed.username, offset, ok = nulTerminated(payload, offset)
	if !ok {
		return login{}, fmt.Errorf("invalid MySQL username")
	}
	switch {
	case parsed.capabilities&clientPluginAuthLenencClientData != 0:
		length, next, valid := lengthEncodedInteger(payload, offset)
		if !valid || length > uint64(len(payload)-next) {
			return login{}, fmt.Errorf("invalid MySQL authentication response")
		}
		parsed.password = strings.TrimSuffix(string(payload[next:next+int(length)]), "\x00")
		offset = next + int(length)
	case parsed.capabilities&clientSecureConnection != 0:
		if offset >= len(payload) {
			return login{}, fmt.Errorf("invalid MySQL authentication response")
		}
		length := int(payload[offset])
		offset++
		if offset+length > len(payload) {
			return login{}, fmt.Errorf("invalid MySQL authentication response")
		}
		parsed.password = strings.TrimSuffix(string(payload[offset:offset+length]), "\x00")
		offset += length
	default:
		parsed.password, offset, ok = nulTerminated(payload, offset)
		if !ok {
			return login{}, fmt.Errorf("invalid MySQL authentication response")
		}
	}
	if parsed.capabilities&clientConnectWithDB != 0 {
		parsed.database, _, ok = nulTerminated(payload, offset)
		if !ok {
			return login{}, fmt.Errorf("invalid MySQL database name")
		}
	}
	return parsed, nil
}

// sessionCapabilities are the capabilities the endpoint's login to the engine
// negotiates on the client's behalf.
func sessionCapabilities(h handshake, clientCapabilities uint32, database string) uint32 {
	capabilities := clientCapabilities & h.capabilities
	capabilities |= clientProtocol41 | clientSecureConnection | clientPluginAuth
	capabilities &^= clientSSL | clientConnectAttrs | clientPluginAuthLenencClientData |
		clientZSTDCompression | clientQueryAttributes
	if database != "" {
		capabilities |= clientConnectWithDB
	}
	return capabilities
}

// setSessionReadOnly runs SET SESSION TRANSACTION READ ONLY on the engine
// session and returns the engine's ERR packet when it refuses. A session that
// negotiated compression carries the command in a compressed-protocol frame,
// since the engine expects one from the first command on.
func setSessionReadOnly(backend io.ReadWriter, compressed bool) ([]byte, error) {
	command := append([]byte{comQuery}, "SET SESSION TRANSACTION READ ONLY"...)
	var response []byte
	var err error
	if compressed {
		var packet bytes.Buffer
		_ = writePacket(&packet, 0, command)
		if err = writeCompressedFrame(backend, 0, packet.Bytes()); err != nil {
			return nil, fmt.Errorf("write engine read-only command: %w", err)
		}
		var frame []byte
		if frame, err = readCompressedFrame(backend); err != nil {
			return nil, fmt.Errorf("read engine read-only answer: %w", err)
		}
		_, response, err = readPacket(bytes.NewReader(frame))
	} else {
		if err = writePacket(backend, 0, command); err != nil {
			return nil, fmt.Errorf("write engine read-only command: %w", err)
		}
		_, response, err = readPacket(backend)
	}
	if err != nil {
		return nil, fmt.Errorf("read engine read-only answer: %w", err)
	}
	switch {
	case len(response) > 0 && response[0] == 0x00:
		return nil, nil
	case len(response) > 0 && response[0] == 0xff:
		return response, nil
	default:
		return nil, fmt.Errorf("engine answered the read-only command with %x", response)
	}
}

// writeCompressedFrame sends payload in one compressed-protocol frame left
// uncompressed, which an uncompressed length of zero declares.
func writeCompressedFrame(connection io.Writer, sequence byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), sequence, 0, 0, 0}
	if _, err := connection.Write(header); err != nil {
		return err
	}
	_, err := connection.Write(payload)
	return err
}

func readCompressedFrame(connection io.Reader) ([]byte, error) {
	header := make([]byte, 7)
	if _, err := io.ReadFull(connection, header); err != nil {
		return nil, err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	uncompressedLength := int(header[4]) | int(header[5])<<8 | int(header[6])<<16
	payload := make([]byte, length)
	if _, err := io.ReadFull(connection, payload); err != nil {
		return nil, err
	}
	if uncompressedLength == 0 {
		return payload, nil
	}
	inflater, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer inflater.Close()
	uncompressed := make([]byte, uncompressedLength)
	if _, err := io.ReadFull(inflater, uncompressed); err != nil {
		return nil, err
	}
	return uncompressed, nil
}

func encodeBackendLogin(h handshake, capabilities uint32, username, password, database string, charset byte) []byte {
	plugin := h.plugin
	var response []byte
	switch plugin {
	case "caching_sha2_password":
		response = cachingSHA2Password(password, h.authData)
	default:
		plugin = "mysql_native_password"
		response = nativePassword(password, h.authData)
	}
	payload := binary.LittleEndian.AppendUint32(nil, capabilities)
	payload = binary.LittleEndian.AppendUint32(payload, 1<<24-1)
	payload = append(payload, charset)
	payload = append(payload, make([]byte, 23)...)
	payload = append(payload, username...)
	payload = append(payload, 0, byte(len(response)))
	payload = append(payload, response...)
	if database != "" {
		payload = append(payload, database...)
		payload = append(payload, 0)
	}
	payload = append(payload, plugin...)
	payload = append(payload, 0)
	return payload
}

// nativePassword is mysql_native_password's scramble:
// SHA1(password) XOR SHA1(seed || SHA1(SHA1(password))).
func nativePassword(password string, seed []byte) []byte {
	first := sha1.Sum([]byte(password))
	second := sha1.Sum(first[:])
	third := sha1.Sum(append(append([]byte(nil), seed...), second[:]...))
	response := make([]byte, sha1.Size)
	for i := range response {
		response[i] = first[i] ^ third[i]
	}
	return response
}

// cachingSHA2Password is caching_sha2_password's fast-path scramble:
// SHA256(password) XOR SHA256(SHA256(SHA256(password)) || seed).
func cachingSHA2Password(password string, seed []byte) []byte {
	first := sha256.Sum256([]byte(password))
	second := sha256.Sum256(first[:])
	third := sha256.Sum256(append(append([]byte(nil), second[:]...), seed...))
	response := make([]byte, sha256.Size)
	for i := range response {
		response[i] = first[i] ^ third[i]
	}
	return response
}

// completeCachingSHA2 finishes a caching_sha2_password exchange the engine
// continued with an AuthMoreData packet: fast-auth success is followed by the
// result, and a password the engine has not cached yet needs full
// authentication. The link to the engine is not TLS, so the password travels
// RSA-OAEP encrypted under the public key the engine sends on request.
func completeCachingSHA2(backend net.Conn, sequence byte, result []byte, password string, scramble []byte) ([]byte, error) {
	if len(result) != 2 || result[0] != 0x01 {
		return result, nil
	}
	switch result[1] {
	case 0x03:
		_, result, err := readPacket(backend)
		if err != nil {
			return nil, fmt.Errorf("read engine fast-auth result: %w", err)
		}
		return result, nil
	case 0x04:
	default:
		return result, nil
	}
	return rsaPasswordExchange(backend, sequence, 0x02, password, scramble)
}

// rsaPasswordExchange asks the engine for its RSA public key with request,
// sends the password XORed with the scramble under RSA-OAEP, and returns the
// engine's answer.
func rsaPasswordExchange(backend net.Conn, sequence, request byte, password string, scramble []byte) ([]byte, error) {
	if err := writePacket(backend, sequence+1, []byte{request}); err != nil {
		return nil, fmt.Errorf("request engine public key: %w", err)
	}
	sequence, keyPacket, err := readPacket(backend)
	if err != nil {
		return nil, fmt.Errorf("read engine public key: %w", err)
	}
	if len(keyPacket) < 2 || keyPacket[0] != 0x01 {
		return keyPacket, nil
	}
	block, _ := pem.Decode(keyPacket[1:])
	if block == nil {
		return nil, fmt.Errorf("engine sent no PEM public key")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse engine public key: %w", err)
	}
	publicKey, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("engine public key is %T, not RSA", parsed)
	}
	plain := append([]byte(password), 0)
	for i := range plain {
		plain[i] ^= scramble[i%len(scramble)]
	}
	encrypted, err := rsa.EncryptOAEP(sha1.New(), rand.Reader, publicKey, plain, nil)
	if err != nil {
		return nil, fmt.Errorf("encrypt password for the engine: %w", err)
	}
	if err := writePacket(backend, sequence+1, encrypted); err != nil {
		return nil, fmt.Errorf("write engine full authentication: %w", err)
	}
	_, result, err := readPacket(backend)
	if err != nil {
		return nil, fmt.Errorf("read engine full authentication result: %w", err)
	}
	return result, nil
}

func nulTerminated(payload []byte, offset int) (string, int, bool) {
	if offset >= len(payload) {
		return "", offset, false
	}
	end := strings.IndexByte(string(payload[offset:]), 0)
	if end < 0 {
		return "", offset, false
	}
	return string(payload[offset : offset+end]), offset + end + 1, true
}

func lengthEncodedInteger(payload []byte, offset int) (uint64, int, bool) {
	if offset >= len(payload) {
		return 0, offset, false
	}
	switch payload[offset] {
	case 0xfc:
		if offset+3 > len(payload) {
			return 0, offset, false
		}
		return uint64(binary.LittleEndian.Uint16(payload[offset+1:])), offset + 3, true
	case 0xfd:
		if offset+4 > len(payload) {
			return 0, offset, false
		}
		value := uint64(payload[offset+1]) | uint64(payload[offset+2])<<8 | uint64(payload[offset+3])<<16
		return value, offset + 4, true
	case 0xfe:
		if offset+9 > len(payload) {
			return 0, offset, false
		}
		return binary.LittleEndian.Uint64(payload[offset+1:]), offset + 9, true
	default:
		return uint64(payload[offset]), offset + 1, true
	}
}

// writeAuthError sends ER_ACCESS_DENIED_ERROR (1045, SQLSTATE 28000).
func writeAuthError(connection io.Writer, sequence byte, message string) {
	payload := []byte{0xff, 0x15, 0x04, '#'}
	payload = append(payload, "28000"...)
	payload = append(payload, message...)
	_ = writePacket(connection, sequence, payload)
}
