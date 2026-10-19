package main

import (
	"bytes"
	"context"
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// One Azure service reaches another through that service's API, as Azure
// Event Grid writes a dead-lettered event to Blob Storage with Put Blob: the
// request takes the same path a client's does, so the receiving service
// authorizes it and answers in its own shapes.

// azureServer is the simulator serving this process.
var azureServer *sim.Server

// azureTrustedServices are the Microsoft services a storage account admits as
// trusted services when they act on their own behalf.
var azureTrustedServices = map[string]bool{"Microsoft.EventGrid": true}

// azureTrustedServiceCall reports whether a request is an in-process call a
// trusted Microsoft service makes as itself.
func azureTrustedServiceCall(r *http.Request) bool {
	caller, ok := sim.CallerOf(r.Context())
	return ok && sim.InProcess(r) && azureTrustedServices[caller.Service]
}

// azureCallAs sends a request to the simulator's own API as the service named,
// acting for source, and returns the status and body it answered.
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
