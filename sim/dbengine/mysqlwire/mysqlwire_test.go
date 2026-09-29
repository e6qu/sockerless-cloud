package mysqlwire

import (
	"bytes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/binary"
	"testing"
)

func TestHandshakeRoundTrip(t *testing.T) {
	original := handshake{
		serverVersion: "8.0.39",
		connectionID:  42,
		authData:      []byte("abcdefghij0123456789"),
		capabilities:  clientProtocol41 | clientSecureConnection | clientPluginAuth | clientSSL,
		charset:       0xff,
		status:        0x0002,
		plugin:        "caching_sha2_password",
	}
	parsed, err := parseHandshake(encodeHandshake(original))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.serverVersion != original.serverVersion || parsed.connectionID != original.connectionID ||
		!bytes.Equal(parsed.authData, original.authData) || parsed.capabilities != original.capabilities ||
		parsed.charset != original.charset || parsed.status != original.status || parsed.plugin != original.plugin {
		t.Fatalf("round trip = %+v, want %+v", parsed, original)
	}
}

// TestParseHandshakeRejectsTruncatedGreeting keeps a short greeting whose
// server version eats the fixed fields from indexing past the payload.
func TestParseHandshakeRejectsTruncatedGreeting(t *testing.T) {
	payload := append([]byte{protocolVersion10}, bytes.Repeat([]byte("9"), 40)...)
	payload = append(payload, 0, 1, 2, 3)
	if _, err := parseHandshake(payload); err == nil {
		t.Fatal("parseHandshake accepted a truncated greeting")
	}
}

func TestParseLogin(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		capabilities uint32
		password     []byte
	}{
		{name: "length-encoded auth response", capabilities: clientPluginAuthLenencClientData, password: append([]byte{7}, "s3cret!"...)},
		{name: "one-byte-length auth response", capabilities: clientSecureConnection, password: append([]byte{7}, "s3cret!"...)},
		{name: "NUL-terminated auth response", capabilities: 0, password: append([]byte("s3cret!"), 0)},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			capabilities := testCase.capabilities | clientProtocol41 | clientConnectWithDB
			payload := binary.LittleEndian.AppendUint32(nil, capabilities)
			payload = binary.LittleEndian.AppendUint32(payload, 1<<24-1)
			payload = append(payload, 45)
			payload = append(payload, make([]byte, 23)...)
			payload = append(payload, "dbadmin\x00"...)
			payload = append(payload, testCase.password...)
			payload = append(payload, "application\x00"...)
			parsed, err := parseLogin(payload)
			if err != nil {
				t.Fatal(err)
			}
			if parsed.username != "dbadmin" || parsed.password != "s3cret!" || parsed.database != "application" || parsed.charset != 45 {
				t.Fatalf("parsed login = %+v", parsed)
			}
		})
	}
}

// TestParseLoginRejectsOversizedLengthEncodedResponse keeps a client-declared
// 8-byte length that overflows int from slicing past the payload.
func TestParseLoginRejectsOversizedLengthEncodedResponse(t *testing.T) {
	payload := binary.LittleEndian.AppendUint32(nil, clientProtocol41|clientPluginAuthLenencClientData)
	payload = append(payload, make([]byte, 28)...)
	payload = append(payload, "dbadmin\x00"...)
	payload = append(payload, 0xfe)
	payload = binary.LittleEndian.AppendUint64(payload, 1<<63+5)
	payload = append(payload, "tail"...)
	if _, err := parseLogin(payload); err == nil {
		t.Fatal("parseLogin accepted an auth response longer than the packet")
	}
}

// TestNativePasswordVerifiesLikeTheServer checks the scramble with the check
// the server runs: XOR the response with SHA1(seed || stored) and hash the
// result back to the stored SHA1(SHA1(password)).
func TestNativePasswordVerifiesLikeTheServer(t *testing.T) {
	seed := []byte("0123456789abcdefghij")
	first := sha1.Sum([]byte("s3cret!"))
	stored := sha1.Sum(first[:])
	mask := sha1.Sum(append(append([]byte(nil), seed...), stored[:]...))
	response := nativePassword("s3cret!", seed)
	candidate := make([]byte, sha1.Size)
	for i := range candidate {
		candidate[i] = response[i] ^ mask[i]
	}
	if sha1.Sum(candidate) != stored {
		t.Fatal("mysql_native_password scramble does not verify against the stored hash")
	}
}

func TestCachingSHA2PasswordVerifiesLikeTheServer(t *testing.T) {
	seed := []byte("0123456789abcdefghij")
	first := sha256.Sum256([]byte("s3cret!"))
	stored := sha256.Sum256(first[:])
	mask := sha256.Sum256(append(append([]byte(nil), stored[:]...), seed...))
	response := cachingSHA2Password("s3cret!", seed)
	candidate := make([]byte, sha256.Size)
	for i := range candidate {
		candidate[i] = response[i] ^ mask[i]
	}
	if sha256.Sum256(candidate) != stored {
		t.Fatal("caching_sha2_password scramble does not verify against the stored hash")
	}
}

func TestEncodeBackendLoginDropsFrontendOnlyCapabilities(t *testing.T) {
	engine := handshake{
		authData:     []byte("0123456789abcdefghij"),
		capabilities: 0xffffffff,
		plugin:       "mysql_native_password",
	}
	payload := encodeBackendLogin(engine, clientSSL|clientConnectAttrs|clientPluginAuthLenencClientData|clientProtocol41,
		"dbadmin", "s3cret!", "application", 45)
	parsed, err := parseLogin(payload)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.capabilities&(clientSSL|clientConnectAttrs|clientPluginAuthLenencClientData) != 0 {
		t.Fatalf("backend login kept frontend-only capabilities: %#x", parsed.capabilities)
	}
	if parsed.username != "dbadmin" || parsed.database != "application" {
		t.Fatalf("backend login = %+v", parsed)
	}
	if parsed.password != string(nativePassword("s3cret!", engine.authData)) {
		t.Fatal("backend login does not carry the native scramble of the backend password")
	}
}

func TestGreets(t *testing.T) {
	greeting := encodeHandshake(handshake{serverVersion: "8.0.39", authData: []byte("0123456789abcdefghij")})
	var ready bytes.Buffer
	_ = writePacket(&ready, 0, greeting)
	if !Greets(&ready) {
		t.Fatal("Greets rejected a protocol-10 handshake")
	}
	var refused bytes.Buffer
	_ = writePacket(&refused, 0, []byte{0xff, 0x69, 0x04, '#', 'H', 'Y', '0', '0', '0'})
	if Greets(&refused) {
		t.Fatal("Greets accepted an error packet")
	}
	if Greets(&bytes.Buffer{}) {
		t.Fatal("Greets accepted a connection closed without a greeting")
	}
}

func TestWriteAuthErrorIsAccessDenied(t *testing.T) {
	var out bytes.Buffer
	writeAuthError(&out, 2, "Access denied for user 'dbadmin'")
	sequence, payload, err := readPacket(&out)
	if err != nil {
		t.Fatal(err)
	}
	if sequence != 2 || payload[0] != 0xff || binary.LittleEndian.Uint16(payload[1:3]) != 1045 || string(payload[3:9]) != "#28000" {
		t.Fatalf("auth error packet = seq %d %q", sequence, payload)
	}
}
