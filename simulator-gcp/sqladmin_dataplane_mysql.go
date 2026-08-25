package main

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"
)

// The MySQL side of the Cloud SQL front proxy.
//
// The proxy greets the client with a handshake derived from the real
// engine's, authenticates the presented password against the sealed
// credential the Cloud SQL Admin API holds for that user (mysql_clear_password
// on the front, TLS available), then logs into the engine as the same user —
// the users the API declares are reconciled into the engine as real MySQL
// accounts, so the backend session carries the client's own identity.

const (
	sqlMySQLClientConnectWithDB              uint32 = 0x00000008
	sqlMySQLClientProtocol41                 uint32 = 0x00000200
	sqlMySQLClientSSL                        uint32 = 0x00000800
	sqlMySQLClientSecureConnection           uint32 = 0x00008000
	sqlMySQLClientPluginAuth                 uint32 = 0x00080000
	sqlMySQLClientConnectAttrs               uint32 = 0x00100000
	sqlMySQLClientPluginAuthLenencClientData uint32 = 0x00200000
	sqlMySQLClientZSTDCompression            uint32 = 0x04000000
	sqlMySQLClientQueryAttributes            uint32 = 0x08000000
)

type sqlMySQLHandshake struct {
	serverVersion string
	connectionID  uint32
	authData      []byte
	capabilities  uint32
	charset       byte
	status        uint16
	plugin        string
}

type sqlMySQLLogin struct {
	capabilities uint32
	charset      byte
	username     string
	password     string
	database     string
}

func (runtime *sqlDataPlaneRuntime) serveMySQL(client net.Conn) {
	backendAddress, _, _ := runtime.snapshot()
	backend, err := net.DialTimeout("tcp", backendAddress, 5*time.Second)
	if err != nil {
		log.Printf("Cloud SQL %s/%s MySQL backend dial: %v", runtime.project, runtime.instance, err)
		return
	}
	defer backend.Close()

	_, backendHandshakePayload, err := sqlMySQLReadPacket(backend)
	if err != nil {
		log.Printf("Cloud SQL %s/%s MySQL backend handshake: %v", runtime.project, runtime.instance, err)
		return
	}
	backendHandshake, err := sqlMySQLParseHandshake(backendHandshakePayload)
	if err != nil {
		log.Printf("Cloud SQL %s/%s MySQL backend handshake parse: %v", runtime.project, runtime.instance, err)
		return
	}
	frontendHandshake := backendHandshake
	frontendHandshake.authData = make([]byte, 20)
	if _, err := rand.Read(frontendHandshake.authData); err != nil {
		return
	}
	frontendHandshake.capabilities |= sqlMySQLClientProtocol41 | sqlMySQLClientSecureConnection | sqlMySQLClientPluginAuth | sqlMySQLClientSSL
	frontendHandshake.plugin = "mysql_clear_password"
	if err := sqlMySQLWritePacket(client, 0, sqlMySQLEncodeHandshake(frontendHandshake)); err != nil {
		return
	}

	sequence, loginPayload, err := sqlMySQLReadPacket(client)
	if err != nil {
		log.Printf("Cloud SQL %s/%s MySQL client login: %v", runtime.project, runtime.instance, err)
		return
	}
	if len(loginPayload) == 32 && binary.LittleEndian.Uint32(loginPayload[:4])&sqlMySQLClientSSL != 0 {
		certificate, certErr := sqlServerCertificate()
		if certErr != nil {
			return
		}
		tlsClient := tls.Server(client, &tls.Config{
			Certificates: []tls.Certificate{certificate},
			MinVersion:   tls.VersionTLS12,
		})
		if err := tlsClient.Handshake(); err != nil {
			log.Printf("Cloud SQL %s/%s MySQL TLS handshake: %v", runtime.project, runtime.instance, err)
			return
		}
		client = tlsClient
		defer client.Close()
		sequence, loginPayload, err = sqlMySQLReadPacket(client)
		if err != nil {
			log.Printf("Cloud SQL %s/%s MySQL secure login: %v", runtime.project, runtime.instance, err)
			return
		}
	}
	login, err := sqlMySQLParseLogin(loginPayload)
	if err != nil {
		log.Printf("Cloud SQL %s/%s MySQL login parse: %v", runtime.project, runtime.instance, err)
		sqlMySQLWriteAuthError(client, sequence+1, "Access denied")
		return
	}
	if !sqlCredentialMatches(runtime.project, runtime.instance, login.username, login.password) {
		log.Printf("Cloud SQL %s/%s MySQL authentication rejected for %s", runtime.project, runtime.instance, login.username)
		sqlMySQLWriteAuthError(client, sequence+1, fmt.Sprintf("Access denied for user '%s'", login.username))
		return
	}

	// The engine holds this user as a real account with the same password
	// the API declares — the reconciliation put it there — so the backend
	// session is the client's own user.
	backendLogin := sqlMySQLEncodeBackendLogin(
		backendHandshake, login.capabilities, login.username,
		login.password, login.database, login.charset,
	)
	if err := sqlMySQLWritePacket(backend, 1, backendLogin); err != nil {
		log.Printf("Cloud SQL %s/%s MySQL backend login write: %v", runtime.project, runtime.instance, err)
		return
	}
	backendSequence, authResult, err := sqlMySQLReadPacket(backend)
	if err != nil {
		log.Printf("Cloud SQL %s/%s MySQL backend auth result: %v", runtime.project, runtime.instance, err)
		return
	}
	if len(authResult) > 1 && authResult[0] == 0xfe {
		plugin, offset, valid := sqlMySQLNULTerminated(authResult, 1)
		if !valid {
			log.Printf("Cloud SQL %s/%s MySQL backend auth switch was malformed", runtime.project, runtime.instance)
			return
		}
		seed := []byte(strings.TrimRight(string(authResult[offset:]), "\x00"))
		var response []byte
		switch plugin {
		case "mysql_native_password":
			response = sqlMySQLNativePassword(login.password, seed)
		case "caching_sha2_password":
			response = sqlMySQLCachingSHA2Password(login.password, seed)
		default:
			log.Printf("Cloud SQL %s/%s MySQL backend requested unsupported auth plugin %s", runtime.project, runtime.instance, plugin)
			return
		}
		if err := sqlMySQLWritePacket(backend, backendSequence+1, response); err != nil {
			return
		}
		_, authResult, err = sqlMySQLReadPacket(backend)
		if err != nil {
			log.Printf("Cloud SQL %s/%s MySQL backend auth switch result: %v", runtime.project, runtime.instance, err)
			return
		}
	}
	if len(authResult) == 0 || authResult[0] != 0x00 {
		log.Printf("Cloud SQL %s/%s MySQL backend rejected the proxied login: %x", runtime.project, runtime.instance, authResult)
		_ = sqlMySQLWritePacket(client, sequence+1, authResult)
		return
	}
	if err := sqlMySQLWritePacket(client, sequence+1, authResult); err != nil {
		return
	}
	sqlRelay(client, backend)
}

func sqlMySQLReadPacket(connection net.Conn) (byte, []byte, error) {
	header := make([]byte, 4)
	if _, err := io.ReadFull(connection, header); err != nil {
		return 0, nil, err
	}
	length := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	if length < 0 || length > 1<<24-1 {
		return 0, nil, fmt.Errorf("invalid MySQL packet length %d", length)
	}
	payload := make([]byte, length)
	_, err := io.ReadFull(connection, payload)
	return header[3], payload, err
}

func sqlMySQLWritePacket(connection net.Conn, sequence byte, payload []byte) error {
	header := []byte{byte(len(payload)), byte(len(payload) >> 8), byte(len(payload) >> 16), sequence}
	if _, err := connection.Write(header); err != nil {
		return err
	}
	_, err := connection.Write(payload)
	return err
}

func sqlMySQLParseHandshake(payload []byte) (sqlMySQLHandshake, error) {
	if len(payload) < 34 || payload[0] != 10 {
		return sqlMySQLHandshake{}, fmt.Errorf("invalid MySQL handshake")
	}
	offset := 1
	end := strings.IndexByte(string(payload[offset:]), 0)
	if end < 0 {
		return sqlMySQLHandshake{}, fmt.Errorf("invalid MySQL server version")
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
	offset++ // authentication-plugin-data length
	offset += 10
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
	return sqlMySQLHandshake{
		serverVersion: serverVersion, connectionID: connectionID, authData: authData,
		capabilities: capabilities, charset: charset, status: status, plugin: plugin,
	}, nil
}

func sqlMySQLEncodeHandshake(handshake sqlMySQLHandshake) []byte {
	authData := append(append([]byte(nil), handshake.authData...), 0)
	for len(authData) < 21 {
		authData = append(authData, 0)
	}
	payload := []byte{10}
	payload = append(payload, handshake.serverVersion...)
	payload = append(payload, 0)
	payload = binary.LittleEndian.AppendUint32(payload, handshake.connectionID)
	payload = append(payload, authData[:8]...)
	payload = append(payload, 0)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(handshake.capabilities))
	payload = append(payload, handshake.charset)
	payload = binary.LittleEndian.AppendUint16(payload, handshake.status)
	payload = binary.LittleEndian.AppendUint16(payload, uint16(handshake.capabilities>>16))
	payload = append(payload, byte(len(authData)))
	payload = append(payload, make([]byte, 10)...)
	payload = append(payload, authData[8:21]...)
	payload = append(payload, handshake.plugin...)
	payload = append(payload, 0)
	return payload
}

func sqlMySQLParseLogin(payload []byte) (sqlMySQLLogin, error) {
	if len(payload) < 32 {
		return sqlMySQLLogin{}, fmt.Errorf("short MySQL login")
	}
	login := sqlMySQLLogin{
		capabilities: binary.LittleEndian.Uint32(payload[:4]),
		charset:      payload[8],
	}
	offset := 32
	var ok bool
	login.username, offset, ok = sqlMySQLNULTerminated(payload, offset)
	if !ok {
		return sqlMySQLLogin{}, fmt.Errorf("invalid MySQL username")
	}
	switch {
	case login.capabilities&sqlMySQLClientPluginAuthLenencClientData != 0:
		length, next, valid := sqlMySQLLengthEncodedInteger(payload, offset)
		if !valid || next+int(length) > len(payload) {
			return sqlMySQLLogin{}, fmt.Errorf("invalid MySQL authentication response")
		}
		login.password = strings.TrimSuffix(string(payload[next:next+int(length)]), "\x00")
		offset = next + int(length)
	case login.capabilities&sqlMySQLClientSecureConnection != 0:
		if offset >= len(payload) {
			return sqlMySQLLogin{}, fmt.Errorf("invalid MySQL authentication response")
		}
		length := int(payload[offset])
		offset++
		if offset+length > len(payload) {
			return sqlMySQLLogin{}, fmt.Errorf("invalid MySQL authentication response")
		}
		login.password = strings.TrimSuffix(string(payload[offset:offset+length]), "\x00")
		offset += length
	default:
		login.password, offset, ok = sqlMySQLNULTerminated(payload, offset)
		if !ok {
			return sqlMySQLLogin{}, fmt.Errorf("invalid MySQL authentication response")
		}
	}
	if login.capabilities&sqlMySQLClientConnectWithDB != 0 {
		login.database, _, ok = sqlMySQLNULTerminated(payload, offset)
		if !ok {
			return sqlMySQLLogin{}, fmt.Errorf("invalid MySQL database name")
		}
	}
	return login, nil
}

func sqlMySQLEncodeBackendLogin(handshake sqlMySQLHandshake, clientCapabilities uint32, username, password, database string, charset byte) []byte {
	capabilities := clientCapabilities & handshake.capabilities
	capabilities |= sqlMySQLClientProtocol41 | sqlMySQLClientSecureConnection | sqlMySQLClientPluginAuth
	capabilities &^= sqlMySQLClientSSL | sqlMySQLClientConnectAttrs | sqlMySQLClientPluginAuthLenencClientData |
		sqlMySQLClientZSTDCompression | sqlMySQLClientQueryAttributes
	if database != "" {
		capabilities |= sqlMySQLClientConnectWithDB
	}
	plugin := handshake.plugin
	var response []byte
	switch plugin {
	case "caching_sha2_password":
		response = sqlMySQLCachingSHA2Password(password, handshake.authData)
	default:
		plugin = "mysql_native_password"
		response = sqlMySQLNativePassword(password, handshake.authData)
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

func sqlMySQLNativePassword(password string, seed []byte) []byte {
	first := sha1.Sum([]byte(password))
	second := sha1.Sum(first[:])
	input := append(append([]byte(nil), seed...), second[:]...)
	third := sha1.Sum(input)
	response := make([]byte, sha1.Size)
	for i := range response {
		response[i] = first[i] ^ third[i]
	}
	return response
}

func sqlMySQLCachingSHA2Password(password string, seed []byte) []byte {
	first := sha256.Sum256([]byte(password))
	second := sha256.Sum256(first[:])
	input := append(append([]byte(nil), second[:]...), seed...)
	third := sha256.Sum256(input)
	response := make([]byte, sha256.Size)
	for i := range response {
		response[i] = first[i] ^ third[i]
	}
	return response
}

func sqlMySQLNULTerminated(payload []byte, offset int) (string, int, bool) {
	if offset >= len(payload) {
		return "", offset, false
	}
	end := strings.IndexByte(string(payload[offset:]), 0)
	if end < 0 {
		return "", offset, false
	}
	return string(payload[offset : offset+end]), offset + end + 1, true
}

func sqlMySQLLengthEncodedInteger(payload []byte, offset int) (uint64, int, bool) {
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

func sqlMySQLWriteAuthError(connection net.Conn, sequence byte, message string) {
	payload := []byte{0xff, 0x15, 0x04, '#'}
	payload = append(payload, "28000"...)
	payload = append(payload, message...)
	_ = sqlMySQLWritePacket(connection, sequence, payload)
}
