package lbplane

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func selfSigned(t *testing.T, names ...string) *tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	cert, err := ListenerCertificate(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	require.NoError(t, err)
	require.NotNil(t, cert.Leaf)
	return cert
}

func servedName(t *testing.T, address, serverName string) string {
	t.Helper()
	conn, err := tls.Dial("tcp", address, &tls.Config{ServerName: serverName, InsecureSkipVerify: true}) //nolint:gosec // the test reads which certificate was presented
	require.NoError(t, err)
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0].Subject.CommonName
}

func TestHTTPSServerSelectsTheSNICertificate(t *testing.T) {
	config := ListenerTLSConfig(selfSigned(t, "default.example"), []*tls.Certificate{selfSigned(t, "api.example")})
	srv, err := StartHTTPSServer("127.0.0.1:0", config, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "decrypted")
	}), time.Minute)
	require.NoError(t, err)
	defer srv.Close()

	require.Equal(t, "api.example", servedName(t, srv.Address, "api.example"))
	require.Equal(t, "default.example", servedName(t, srv.Address, "other.example"))
	require.Zero(t, srv.srv.WriteTimeout, "a write deadline would cut every long response the load balancer streams")
	require.Zero(t, srv.srv.ReadTimeout, "a read deadline would cut every long upload the load balancer carries")

	client := http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // self-signed test listener
	resp, err := client.Get("https://" + srv.Address + "/")
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, "decrypted", string(body))
}

func TestTLSProxyForwardsTheDecryptedStream(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer upstream.Close()
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()
	proxy, err := StartTLSProxy("127.0.0.1:0", ListenerTLSConfig(selfSigned(t, "nlb.example"), nil),
		func(context.Context) (string, error) { return upstream.Addr().String(), nil })
	require.NoError(t, err)
	defer proxy.Close()

	conn, err := tls.Dial("tcp", proxy.Address, &tls.Config{ServerName: "nlb.example", InsecureSkipVerify: true}) //nolint:gosec // self-signed test listener
	require.NoError(t, err)
	defer conn.Close()
	_, err = io.WriteString(conn, "plain after termination\n")
	require.NoError(t, err)
	require.NoError(t, conn.SetReadDeadline(time.Now().Add(5*time.Second)))
	echoed, err := bufio.NewReader(conn).ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "plain after termination\n", echoed)
}
