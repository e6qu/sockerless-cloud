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

	// A reliability risk is something Google's analysis detected about a
	// project, not a set it publishes: the resource carries the type of risk
	// and how long it has been running, and nothing about it is written by the
	// caller. So this is the observational case rather than the catalogue one —
	// the simulator runs no such analysis and has therefore detected nothing,
	// which an empty collection says exactly, the way an App Service site's
	// recommendations already do. A named risk is not found, because none was.
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/reliabilityRisks",
		func(w http.ResponseWriter, r *http.Request) {
			project := sim.PathParam(r, "project")
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"id":       "projects/" + project + "/global/reliabilityRisks",
				"selfLink": computeSelfLink("projects/" + project + "/global/reliabilityRisks"),
				"items":    []any{},
			})
		})
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/reliabilityRisks/{reliabilityRisk}",
		func(w http.ResponseWriter, r *http.Request) {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"reliability risk %q not found", sim.PathParam(r, "reliabilityRisk"))
		})

	// A licence code is read where it is issued, beside the licences
	// collection. The policy a project puts on one is the caller's own
	// binding, written by setIamPolicy and read back by the other two.
	licenceIAM := func(r *http.Request) string {
		return "compute/projects/" + sim.PathParam(r, "project") +
			"/global/licenseCodes/" + sim.PathParam(r, "resource")
	}
	srv.HandleFunc("GET /compute/v1/projects/{project}/global/licenseCodes/{resource}/getIamPolicy",
		func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies, licenceIAM(r), "getIamPolicy")
		})
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/licenseCodes/{resource}/setIamPolicy",
		func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies, licenceIAM(r), "setIamPolicy")
		})
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/licenseCodes/{resource}/testIamPermissions",
		func(w http.ResponseWriter, r *http.Request) {
			handleResourceIAM(w, r, gcpResourcePolicies, licenceIAM(r), "testIamPermissions")
		})

	// interconnects.getDiagnostics reports from the interconnect's own record
	// and is served in compute_interconnect_diagnostics.go.
}
