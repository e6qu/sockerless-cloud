package main

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The outbound network dependencies of an App Service Environment, measured.
//
// This reads as a catalogue and is not one. Its EndpointDetail carries an
// ipAddress the domain name "currently resolves to", a latency that is "the
// time in milliseconds it takes for a TCP connection to be created", and an
// isAccessible saying "whether it is possible to create a TCP connection from
// the App Service Environment" — three things an environment learns by trying,
// not by reading a published list. Azure answers it from the environment's own
// probes of the platform it depends on.
//
// An environment here depends on this simulator: it is the cloud the
// environment's sites reach, at the coordinate the caller reached it on. So the
// dependency is resolved and connected to for real, and what comes back is what
// happened — the address it resolved to, whether the connection was made, and
// how long making it took. Nothing is described that was not tried.

// webOutboundEndpointDetail is one measured address of one dependency.
type webOutboundEndpointDetail struct {
	IPAddress    string  `json:"ipAddress"`
	Port         int     `json:"port"`
	Latency      float64 `json:"latency"`
	IsAccessible bool    `json:"isAccessible"`
}

// webMeasureOutboundDependency resolves a host and connects to it, reporting
// what each address answered. An address that refuses is reported as refusing
// rather than left out: "this dependency is unreachable" is the finding the
// operation exists to surface.
func webMeasureOutboundDependency(host, port string) []webOutboundEndpointDetail {
	number, err := strconv.Atoi(port)
	if err != nil {
		return nil
	}
	addresses, err := net.LookupHost(host)
	if err != nil || len(addresses) == 0 {
		return nil
	}
	details := make([]webOutboundEndpointDetail, 0, len(addresses))
	for _, address := range addresses {
		started := time.Now()
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(address, port), 3*time.Second)
		elapsed := float64(time.Since(started).Microseconds()) / 1000
		if dialErr == nil {
			_ = conn.Close()
		}
		details = append(details, webOutboundEndpointDetail{
			IPAddress:    address,
			Port:         number,
			Latency:      elapsed,
			IsAccessible: dialErr == nil,
		})
	}
	return details
}

// handleASEOutboundNetworkDependencies answers
// AppServiceEnvironments_GetOutboundNetworkDependenciesEndpoints.
func handleASEOutboundNetworkDependencies(w http.ResponseWriter, r *http.Request) {
	if _, ok := aseLookup(w, r); !ok {
		return
	}
	// The environment's dependency is the cloud its sites call, which is this
	// simulator at the address this request arrived on — the same coordinate
	// its workloads are configured with.
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
		port = "80"
		if r.TLS != nil {
			port = "443"
		}
	}
	details := webMeasureOutboundDependency(host, port)
	if len(details) == 0 {
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": []any{}})
		return
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{
		"value": []any{map[string]any{
			"category": "Azure Resource Manager",
			"endpoints": []any{map[string]any{
				"domainName":      host,
				"endpointDetails": details,
			}},
		}},
	})
}
