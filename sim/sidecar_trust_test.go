package sim

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// A request carrying a registered sidecar token takes the address the sidecar
// names as its remote address and the sidecar's network; a request with a
// forged or missing token keeps its own address and the headers never reach
// the handler.
func TestSidecarTrustMiddlewareTrustsOnlyARegisteredToken(t *testing.T) {
	RegisterSidecarToken("good", "net-a")
	t.Cleanup(func() { UnregisterSidecarToken("good") })
	var remote, network, leaked string
	h := sidecarTrustMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		remote, network = r.RemoteAddr, SidecarNetwork(r.Context())
		leaked = r.Header.Get(sidecarTokenHeader) + r.Header.Get(sidecarClientHeader)
	}))
	for _, tc := range []struct {
		token, client, wantRemote, wantNetwork string
	}{
		{"good", "10.0.0.7", "10.0.0.7:0", "net-a"},
		{"forged", "10.0.0.7", "192.0.2.1:1234", ""},
		{"", "10.0.0.7", "192.0.2.1:1234", ""},
		{"good", "not-an-address", "192.0.2.1:1234", "net-a"},
	} {
		req := httptest.NewRequest(http.MethodGet, "/latest/meta-data/", nil)
		req.RemoteAddr = "192.0.2.1:1234"
		req.Header.Set(sidecarTokenHeader, tc.token)
		req.Header.Set(sidecarClientHeader, tc.client)
		h.ServeHTTP(httptest.NewRecorder(), req)
		if remote != tc.wantRemote || network != tc.wantNetwork || leaked != "" {
			t.Fatalf("token %q client %q: remote %q network %q leaked %q", tc.token, tc.client, remote, network, leaked)
		}
	}
}
