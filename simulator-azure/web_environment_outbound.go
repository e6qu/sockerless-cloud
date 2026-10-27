package main

import (
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// An App Service Environment's outbound network dependencies, measured.
// EndpointDetail carries the ipAddress a name "currently resolves to", the
// "time in milliseconds it takes for a TCP connection to be created", and
// whether "it is possible to create a TCP connection from the App Service
// Environment" — findings, not a published list. An environment here depends
// on this simulator, at the coordinate the caller reached it on, so the
// dependency is resolved and connected to, and the report is what happened.

type webOutboundEndpointDetail struct {
	IPAddress    string  `json:"ipAddress"`
	Port         int     `json:"port"`
	Latency      float64 `json:"latency"`
	IsAccessible bool    `json:"isAccessible"`
}

// An address that refuses is reported as refusing, not left out: an
// unreachable dependency is the finding the operation exists for.
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

func handleASEOutboundNetworkDependencies(w http.ResponseWriter, r *http.Request) {
	if _, ok := aseLookup(w, r); !ok {
		return
	}
	// The cloud the sites call is this simulator at the address this request
	// arrived on, the coordinate the workloads are configured with.
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
