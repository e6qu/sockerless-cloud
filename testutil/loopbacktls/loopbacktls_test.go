package loopbacktls

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteIssuesALeafTheAuthorityVerifies(t *testing.T) {
	files, err := Write(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	ts.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	ts.StartTLS()
	defer ts.Close()

	pool, err := files.Pool()
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}
	resp, err := client.Get(ts.URL)
	if err != nil {
		t.Fatalf("GET over the issued certificate: %v", err)
	}
	resp.Body.Close()
}
