package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Compute Engine surfaces that report Google's own observations rather
// than the caller's data. The simulator runs none of the analyses behind them,
// so it serves an empty collection where nothing was observed and declares
// the rest unserved. Published catalogues are vendored instead, as the
// interconnect locations are in compute_interconnect_locations.go and
// compute_interconnect_remote_locations.go.
func registerComputeCatalogs(srv *sim.Server) {
	// A reliability risk is something Google's analysis detected about a project.
	// The simulator runs no such analysis, so the collection is empty and a named
	// risk is not found.
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
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"reliability risk %q not found", sim.PathParam(r, "reliabilityRisk"))
		})

	// A licence code's policy is the caller's own binding, written by
	// setIamPolicy and read back by the other two verbs.
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

	// interconnects.getDiagnostics is served in
	// compute_interconnect_diagnostics.go.

	// regions.projectViews.get exists to expose a regional read replica's lag
	// behind the canonical project; the simulator replicates nothing, so it
	// declares the method unserved rather than answer a global read.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/projectViews",
		func(w http.ResponseWriter, r *http.Request) {
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
				"the simulator serves no regional project view: it holds one canonical "+
					"copy of a project and runs no regional replication to lag behind")
		})

	// managedRulesets are Cloud Armor's own rule catalogue, every field generated
	// by Google's rules pipeline; the simulator holds no ruleset.
	for _, path := range []string{
		"GET /compute/v1/projects/{project}/global/managedRulesets",
		"GET /compute/v1/projects/{project}/global/managedRulesets/{managedRuleset}",
	} {
		srv.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
				"the simulator serves no Cloud Armor managed rulesets: Google generates "+
					"them and their rule IDs, and this deployment holds none")
		})
	}

	// globalFrontendSettings is a billing fact about a real account; the
	// simulator bills nothing.
	for _, path := range []string{
		"GET /compute/v1/projects/{project}/global/globalFrontendSettings",
		"PATCH /compute/v1/projects/{project}/global/globalFrontendSettings",
	} {
		srv.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
				"the simulator serves no Global Frontend billing bundle settings: the "+
					"bundle is a billing fact about a real account, and this deployment bills nothing")
		})
	}

	// regions.advice.capacity and .capacityHistory are Google's capacity
	// forecasting and history, an analysis the simulator does not run.
	adviceUnimplemented := func(what string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED",
				"the simulator serves no %s: it runs no capacity forecasting or "+
					"observation to answer from", what)
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/advice/capacity",
		adviceUnimplemented("regions.advice.capacity"))
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/advice/capacityHistory",
		adviceUnimplemented("regions.advice.capacityHistory"))
}
