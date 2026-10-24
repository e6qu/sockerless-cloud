package sim

import (
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
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// A TLS deployment serves the same chain plain on a second port, the one the
// workloads' sidecars forward to, and Listen resolves that port before Serve
// so what starts beside the server can hand it out.
func TestListenOpensAPlainCallbackListenerBesideTLS(t *testing.T) {
	t.Setenv(runtimeModeVariable, string(RuntimeModeAPIOnly))
	certPath, keyPath := writeTestServerCertificate(t)
	srv, err := NewServer(Config{
		Provider: "aws", LogLevel: "disabled",
		ListenAddr: "127.0.0.1:0", TLSCert: certPath, TLSKey: keyPath,
		CallbackListenAddr: "127.0.0.1:0",
	})
	if err != nil {
		t.Fatal(err)
	}
	srv.HandleFunc("GET /whoami", func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			_, _ = io.WriteString(w, "tls")
			return
		}
		_, _ = io.WriteString(w, "plain")
	})
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	apiAddr := srv.ln.Addr().String()
	callbackAddr := srv.CallbackListenAddr()
	if callbackAddr == apiAddr || callbackAddr == "127.0.0.1:0" {
		t.Fatalf("callback listener at %q, want a bound port of its own beside %q", callbackAddr, apiAddr)
	}

	served := make(chan error, 1)
	go func() { served <- srv.Serve() }()
	// The listeners are bound, so a request waits in the backlog until Serve
	// accepts it; the signal handler is registered by then.
	get := func(client *http.Client, url string) string {
		resp, err := client.Get(url)
		if err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	if got := get(http.DefaultClient, "http://"+callbackAddr+"/whoami"); got != "plain" {
		t.Errorf("the callback listener answered %q, want the chain served plain", got)
	}
	tlsClient := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}} //nolint:gosec // a self-signed test certificate
	if got := get(tlsClient, "https://"+apiAddr+"/whoami"); got != "tls" {
		t.Errorf("the API listener answered %q, want TLS", got)
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatalf("Serve returned %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Serve did not return after SIGTERM")
	}
	if _, err := net.Dial("tcp", callbackAddr); err == nil {
		t.Error("the callback listener still accepts after shutdown")
	}
}

// Without TLS the one listener serves everything and is the callback address.
func TestCallbackListenAddrIsTheListenAddrWithoutTLS(t *testing.T) {
	t.Setenv(runtimeModeVariable, string(RuntimeModeAPIOnly))
	srv, err := NewServer(Config{Provider: "gcp", LogLevel: "disabled", ListenAddr: "127.0.0.1:0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Listen(); err != nil {
		t.Fatal(err)
	}
	defer srv.closeListeners()
	if srv.callbackLn != nil {
		t.Fatal("a plain deployment opened a second listener")
	}
	if got := srv.CallbackListenAddr(); got != "127.0.0.1:0" {
		t.Fatalf("CallbackListenAddr = %q, want the configured ListenAddr", got)
	}
}

func writeTestServerCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certPath, keyPath
}
