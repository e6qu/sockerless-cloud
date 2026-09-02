package main

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
)

// A site's network traces.
//
// App Service captures packets on the instance a site runs on for a fixed
// duration, and a client starts one, stops it, and reads how it went. The
// simulator records the trace against the site — its duration, when it started
// and where it was asked to be written — so a read reports the trace that was
// actually requested.
//
// What it does not do is claim a capture file. A trace's path names a .cap the
// platform wrote, and this simulator captures no packets; reporting a path
// would name a file that does not exist, which is worse for a client than
// saying the capture produced nothing. The status carries that.

// webNetworkTrace is a trace a client asked for.
type webNetworkTrace struct {
	OperationID string `json:"operationId"`
	Site        string `json:"site"`
	Duration    int    `json:"durationInSeconds"`
	SasURL      string `json:"sasUrl"`
	StartedAt   string `json:"startedAt"`
	StoppedAt   string `json:"stoppedAt,omitempty"`
}

var webNetworkTraces sim.Store[webNetworkTrace]

// registerWebNetworkTraces mounts the trace verbs on a site and its slots.
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

// webStartNetworkTrace records a capture against the site. The duration is what
// the caller asked for, because that is the only thing about the capture the
// simulator knows.
func webStartNetworkTrace(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	duration := 60
	if raw := r.URL.Query().Get("durationInSeconds"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			sim.AzureError(w, "BadRequest",
				"durationInSeconds must be a positive number of seconds.", http.StatusBadRequest)
			return
		}
		duration = parsed
	}
	site := webResourceID(r)
	trace := webNetworkTrace{
		OperationID: "trace-" + generateUUID(),
		Site:        site,
		Duration:    duration,
		SasURL:      r.URL.Query().Get("sasUrl"),
		StartedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	webNetworkTraces.Put(trace.OperationID, trace)
	sim.WriteJSON(w, http.StatusOK, []any{webNetworkTraceDoc(trace)})
}

// webStopNetworkTrace ends the site's running captures. A site with none
// running has nothing to stop, which is what it reports.
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
		sim.AzureError(w, "NotFound",
			"No network trace is running on this site.", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// webGetNetworkTrace reports how a trace went.
func webGetNetworkTrace(w http.ResponseWriter, r *http.Request) {
	if webMissing(w, r) {
		return
	}
	operationID := sim.PathParam(r, "operationId")
	trace, ok := webNetworkTraces.Get(operationID)
	if !ok || trace.Site != webResourceID(r) {
		sim.AzureError(w, "NotFound",
			fmt.Sprintf("No network trace with operation id %q was started on this site.", operationID),
			http.StatusNotFound)
		return
	}
	sim.WriteJSON(w, http.StatusOK, []any{webNetworkTraceDoc(trace)})
}

// webNetworkTraceDoc renders a trace. The path stays empty because no capture
// file was written — naming one would point a client at a file that is not
// there — and the message says so.
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
