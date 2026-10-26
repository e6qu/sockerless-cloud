package sim

import (
	"context"
	"net"
	"net/http"
	"sync"
)

// A network sidecar forwards a workload's metadata request to the simulator
// with the token the simulator minted for it and the workload's address. The
// simulator trusts the address only with a registered token, so a workload
// reaching the simulator directly cannot claim another's address: SidecarTrust
// reads the pair, strips it from every request, and makes the trusted address
// the request's RemoteAddr, which the metadata handlers already key on.
const (
	sidecarTokenHeader  = "X-Sockerless-Sidecar-Token"
	sidecarClientHeader = "X-Sockerless-Sidecar-Client"
)

var sidecarTokens sync.Map

// RegisterSidecarToken trusts token as the sidecar of network.
func RegisterSidecarToken(token, network string) {
	if token != "" {
		sidecarTokens.Store(token, network)
	}
}

// UnregisterSidecarToken stops trusting token.
func UnregisterSidecarToken(token string) {
	sidecarTokens.Delete(token)
}

// SidecarNetwork is the engine network whose sidecar forwarded the request,
// or "" when it came from anywhere else.
func SidecarNetwork(ctx context.Context) string {
	network, _ := ctx.Value(sidecarNetworkKey).(string)
	return network
}

// NewSidecarToken mints a token for one sidecar.
func NewSidecarToken() string { return RandomHex(48) }

// SidecarTrust is the middleware that reads the pair: a server applies it to
// everything it routes.
func SidecarTrust(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get(sidecarTokenHeader)
		client := r.Header.Get(sidecarClientHeader)
		r.Header.Del(sidecarTokenHeader)
		r.Header.Del(sidecarClientHeader)
		if token != "" {
			if network, ok := sidecarTokens.Load(token); ok {
				if ip := net.ParseIP(client); ip != nil {
					r.RemoteAddr = net.JoinHostPort(ip.String(), "0")
				}
				name, _ := network.(string)
				r = r.WithContext(context.WithValue(r.Context(), sidecarNetworkKey, name))
			}
		}
		next.ServeHTTP(w, r)
	})
}
