package main

import (
	"net/http"

	"github.com/e6qu/sockerless-cloud/sim"
)

// crmBoundariesUnservedWhy is why the Boundaries collection and a node's
// BoundaryConfig answer 501: the Discovery document is their only vendored
// source, and it states neither how a create provisions the Management
// Project it promises nor when a Boundary leaves CREATING, and no Go client,
// gcloud command or Terraform resource exposes them to test through.
const crmBoundariesUnservedWhy = "no vendored source states how a Boundary provisions its Management Project or leaves CREATING (BUGS.md 3580)"

func crmBoundariesUnserved(method string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		GCPErrorf(w, http.StatusNotImplemented, "UNIMPLEMENTED", "the simulator serves no cloudresourcemanager.%s: %s", method, crmBoundariesUnservedWhy)
	}
}

// registerCRMBoundaries mounts the Boundaries collection and the BoundaryConfig
// singleton on the two hierarchy nodes that publish them.
func registerCRMBoundaries(srv *sim.Server) {
	for _, node := range []string{"folders", "organizations"} {
		base := "/v3/" + node + "/{parent}"
		srv.HandleFunc("POST "+base+"/boundaries", crmBoundariesUnserved(node+".boundaries.create"))
		srv.HandleFunc("GET "+base+"/boundaries", crmBoundariesUnserved(node+".boundaries.list"))
		srv.HandleFunc("GET "+base+"/boundaries/{boundary}", crmBoundariesUnserved(node+".boundaries.get"))
		srv.HandleFunc("PATCH "+base+"/boundaries/{boundary}", crmBoundariesUnserved(node+".boundaries.patch"))
		srv.HandleFunc("DELETE "+base+"/boundaries/{boundary}", crmBoundariesUnserved(node+".boundaries.delete"))
		srv.HandleFunc("GET "+base+"/boundaryConfig", crmBoundariesUnserved(node+".getBoundaryConfig"))
		srv.HandleFunc("PATCH "+base+"/boundaryConfig", crmBoundariesUnserved(node+".updateBoundaryConfig"))
	}
}
