package main

import (
	"net/http"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// The Compute Engine surfaces that report Google's own data rather than the
// caller's.
//
// Every other collection in this simulator answers from what a client put
// there. These do not: a licence code identifies an image Google publishes, and
// a reliability risk is Google's assessment of a project's exposure.
//
// The interconnect locations were described here as having nothing to derive
// them from. That was wrong twice over, and the correction is the reason this
// comment is shorter than it was: the catalogue is published, and a published
// catalogue is vendored here rather than declined — with its source cited, its
// counts locked by a test, and every field the source does not state left
// absent. Both interconnect catalogues are served that way now.
//
// What is left is the observational case, which is a different thing from a
// catalogue: the simulator runs no reliability analysis, so it has detected no
// risks, and an empty collection states that truthfully.
func registerComputeCatalogs(srv *sim.Server) {
	// The facilities Google operates Cloud Interconnect out of, and the
	// third-party ones it peers with, were the declared gaps here. Both are
	// vendored from Google's own documentation now and served in
	// compute_interconnect_locations.go and
	// compute_interconnect_remote_locations.go, which leaves this file the
	// observational cases below rather than any catalogue.

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
