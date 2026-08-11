package main

import (
	"net/http"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// AIP-136 custom methods ("POST .../secrets/{secret}:addVersion") carry the
// method name in the same URI segment as the resource id, so one Go mux pattern
// mounted on that segment receives every custom method a client addresses to
// the resource — including methods this simulator does not serve.
//
// Real Google resolves the method before the resource. Its API frontend matches
// the request URI against the service's method definitions and answers a URI
// that matches none with 404 "Method not found.", without ever consulting the
// resource or the caller's access to it. A handler that instead falls through
// to resource semantics answers a method it does not serve with a misleading
// "resource not found" or "caller does not have permission" — or, worse, treats
// the custom method as data and acts on it.
//
// Handlers mounted on a "{id}" segment therefore split the segment with
// gcpCustomMethod and answer anything they do not serve with gcpMethodNotFound.

// gcpCustomMethod splits a "{id}:{verb}" URI segment into the resource id and
// the AIP-136 custom method it names. found is false when the segment names no
// custom method (a plain resource id).
func gcpCustomMethod(segment string) (id, verb string, found bool) {
	return strings.Cut(segment, ":")
}

// gcpMethodNotFound writes the response Google's API frontend returns for a
// request URI that matches no method of the service: 404, status NOT_FOUND,
// message "Method not found." In this simulator it is also the honest answer
// for a method Google documents but the simulator does not serve — the client
// learns the simulator has no implementation instead of being told a story
// about the resource.
func gcpMethodNotFound(w http.ResponseWriter) {
	sim.GCPError(w, http.StatusNotFound, "Method not found.", "NOT_FOUND")
}
