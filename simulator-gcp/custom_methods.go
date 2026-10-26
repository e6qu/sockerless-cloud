package main

import (
	"net/http"
	"strings"
)

// AIP-136 custom methods ("POST .../secrets/{secret}:addVersion") share their
// URI segment with the resource id, so the pattern mounted on that segment
// receives every custom method addressed to the resource. Google's API
// frontend resolves the method first and answers a URI matching no method
// with 404 "Method not found." without consulting the resource, so handlers
// split the segment with gcpCustomMethod and answer anything they do not serve
// with gcpMethodNotFound.

// gcpCustomMethod splits a "{id}:{verb}" URI segment into the resource id and
// the AIP-136 custom method it names. found is false when the segment names no
// custom method (a plain resource id).
func gcpCustomMethod(segment string) (id, verb string, found bool) {
	return strings.Cut(segment, ":")
}

// gcpMethodNotFound writes Google's API frontend answer for a URI that matches
// no method: 404, NOT_FOUND, "Method not found." The simulator gives it for a
// documented method it does not serve, too.
func gcpMethodNotFound(w http.ResponseWriter) {
	GCPError(w, http.StatusNotFound, "Method not found.", "NOT_FOUND")
}
