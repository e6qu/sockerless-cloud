package main

import (
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The Compute Engine surfaces that report Google's own data rather than the
// caller's.
//
// Every other collection in this simulator answers from what a client put
// there. These do not: an interconnect location is a building Google operates,
// a licence code identifies an image Google publishes, a preview feature is one
// Google has opened, and a reliability risk is Google's assessment of a
// project's exposure. There is nothing for the simulator to derive them from,
// and a list of them would be a list Google never published — which is worse
// than no answer, because a client cannot tell an invented catalogue from a
// real one.
//
// So each is declared and answers 501 with the reason, which is a fact a client
// can act on. An empty list would not be: an empty interconnect locations list
// says Google operates no facilities, which is false.
//
// Each is mounted at a literal path rather than through a loop, so the surface
// tables show the gap where a reader looks for the surface.
func registerComputeCatalogs(srv *sim.Server) {
	catalog := func(what string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			sim.GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
				"the simulator serves no %s: the catalogue is Google's own, and neither an empty list nor an invented one is what the API returns", what)
		}
	}

	// The facilities Google operates Cloud Interconnect out of, and the
	// third-party facilities it peers with.
	locations := catalog("Cloud Interconnect locations")
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectLocations", locations)
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectLocations/{interconnectLocation}", locations)
	remote := catalog("Cloud Interconnect remote locations")
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectRemoteLocations", remote)
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnectRemoteLocations/{interconnectRemoteLocation}", remote)

	// Google's own assessment of what a project is exposed to.
	risks := catalog("reliability risks")
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/reliabilityRisks", risks)
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/reliabilityRisks/{reliabilityRisk}", risks)

	// A licence code identifies an image Google publishes, so the code and the
	// policy on it are both Google's.
	licences := catalog("licence codes")
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/licenseCodes/{licenseCode}", licences)
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/licenseCodes/{resource}/getIamPolicy", licences)
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/licenseCodes/{resource}/setIamPolicy", licences)
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/licenseCodes/{resource}/testIamPermissions", licences)

	// What an interconnect's hardware reports about itself: link status,
	// circuit identifiers and LACP state, read off the physical equipment at
	// both ends, which the simulator does not have. Its MACsec configuration is
	// a different thing — the caller's own keychain — and is served in
	// compute_interconnect_macsec.go.
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/interconnects/{interconnect}/getDiagnostics",
		catalog("interconnect diagnostics"))
}
