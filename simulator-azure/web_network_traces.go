package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A site's network traces. The simulator records the requested trace —
// duration, start, destination — and captures no packets, so it names no .cap
// file; the status says so.

type webNetworkTrace struct {
	OperationID string `json:"operationId"`
	Site        string `json:"site"`
	Duration    int    `json:"durationInSeconds"`
	SasURL      string `json:"sasUrl"`
	StartedAt   string `json:"startedAt"`
	StoppedAt   string `json:"stoppedAt,omitempty"`
}

var webNetworkTraces sim.Store[webNetworkTrace]

func registerWebNetworkTraces(srv *sim.Server, both func(string, string, http.HandlerFunc)) {
	webNetworkTraces = sim.MakeStore[webNetworkTrace](srv.DB(), "web_network_traces")

	both("POST", "/networkTrace/start", webStartNetworkTrace)
	both("POST", "/networkTrace/startOperation", webStartNetworkTrace)
	both("POST", "/startNetworkTrace", webStartNetworkTrace)

	both("POST", "/networkTrace/stop", webStopNetworkTrace)
	both("POST", "/stopNetworkTrace", webStopNetworkTrace)

	both("GET", "/networkTrace/{operationId}", webGetNetworkTrace)
	both("GET", "/networkTraces/{operationId}", webGetNetworkTrace)
	both("GET", "/networkTrace/operationresults/{operationId}", webGetNetworkTrace)
	both("GET", "/networkTraces/current/operationresults/{operationId}", webGetNetworkTrace)
}

// The duration is what the caller asked for, all the simulator knows of the
// capture.
func webStartNetworkTrace(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	duration := 60
	if raw := r.URL.Query().Get("durationInSeconds"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			AzureError(w, "BadRequest",
				"durationInSeconds must be a positive number of seconds.", http.StatusBadRequest)
			return
		}
		duration = parsed
	}
	site := webResourceID(r)
	trace := webNetworkTrace{
		OperationID: "trace-" + sim.NewUUID(),
		Site:        site,
		Duration:    duration,
		SasURL:      r.URL.Query().Get("sasUrl"),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	webNetworkTraces.Put(trace.OperationID, trace)
	sim.WriteJSON(w, http.StatusOK, []any{webNetworkTraceDoc(trace)})
}

// A site with none running reports nothing to stop.
func webStopNetworkTrace(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	site := webResourceID(r)
	stopped := 0
	for _, trace := range webNetworkTraces.List() {
		if trace.Site != site || trace.StoppedAt != "" {
			continue
		}
		trace.StoppedAt = time.Now().UTC().Format(time.RFC3339)
		webNetworkTraces.Put(trace.OperationID, trace)
		stopped++
	}
	if stopped == 0 {
		AzureError(w, "NotFound",
			"No network trace is running on this site.", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func webGetNetworkTrace(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	operationID := sim.PathParam(r, "operationId")
	trace, ok := webNetworkTraces.Get(operationID)
	if !ok || trace.Site != webResourceID(r) {
		AzureError(w, "NotFound",
			fmt.Sprintf("No network trace with operation id %q was started on this site.", operationID),
			http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, []any{webNetworkTraceDoc(trace)})
}

// The path stays empty because no capture file was written, and the message
// says so.
func webNetworkTraceDoc(trace webNetworkTrace) map[string]any {
	status := "InProgress"
	message := fmt.Sprintf("Capturing for %d seconds.", trace.Duration)
	if trace.StoppedAt != "" {
		status = "Succeeded"
		message = "The capture was stopped."
	}
	return map[string]any{
		"status":  status,
		"message": message,
	}
}
