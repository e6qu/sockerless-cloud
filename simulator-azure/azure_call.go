package main

import (
	"bytes"
	"context"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One Azure service reaches another through that service's API, as Azure Event
// Grid dead-letters an event to Blob Storage with Put Blob, so the receiving
// service authorizes the request and answers in its own shapes.

var azureServer *sim.Server

var azureTrustedServices = map[string]bool{"Microsoft.EventGrid": true}

// azureTrustedServiceCall reports an in-process call a trusted Microsoft
// service makes as itself.
func azureTrustedServiceCall(r *http.Request) bool {
	caller, ok := sim.CallerOf(r.Context())
	return ok && sim.InProcess(r) && azureTrustedServices[caller.Service]
}

// azureCallAs sends a request to the simulator's own API as the service named,
// acting for source.
func azureCallAs(ctx context.Context, service, source, method, path string, header http.Header, body []byte) (int, []byte, error) {
	ctx = sim.WithCaller(azureServer.RequestContext(ctx), sim.Caller{Service: service, Source: source})
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for name, values := range header {
		req.Header[name] = values
	}
	response := azureServer.Call(req)
	return response.StatusCode, response.Body.Bytes(), nil
}
