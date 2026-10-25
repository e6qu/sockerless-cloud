package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/ipam"
	"github.com/e6qu/sockerless-cloud/sim/sidecar"
	"github.com/e6qu/sockerless-cloud/sim/sidecar/egress"
)

// registerComputeAddressesAndRouters mounts regional addresses, Cloud Routers
// with their Cloud NAT configurations, and the NAT flow report the egress
// sidecars post to.
func registerComputeAddressesAndRouters(srv *sim.Server, addresses sim.Store[ComputeAddress], routers sim.Store[ComputeRouter]) {
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		var addr ComputeAddress
		if err := sim.ReadJSON(r, &addr); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, addr.Name) {
			return
		}
		addr.Kind = "compute#address"
		addr.Id = computeNumericID()
		addr.SelfLink = computeRegionalAddressLink(project, region, addr.Name)
		if _, exists := addresses.Get(addr.SelfLink); computeConflict(w, exists, "address", addr.Name) {
			return
		}
		addr.Region = fmt.Sprintf("projects/%s/regions/%s", project, region)
		addr.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if addr.AddressType == "" {
			addr.AddressType = "EXTERNAL"
		}
		if addr.IPVersion == "" {
			addr.IPVersion = "IPV4"
		}
		if addr.NetworkTier == "" {
			addr.NetworkTier = "PREMIUM"
		}
		if addr.Status == "" {
			addr.Status = "RESERVED"
		}
		if addr.Address == "" && strings.EqualFold(addr.IPVersion, "IPV4") {
			ip, err := ipam.ReserveGCPPublicIPv4(addr.SelfLink, nil)
			if err != nil {
				GCPErrorf(w, http.StatusServiceUnavailable, "FAILED_PRECONDITION", "failed to reserve real public IPv4 lease: %v", err)
				return
			}
			addr.Address = ip.String()
		}
		if addr.LabelFingerprint == "" {
			addr.LabelFingerprint = computeFingerprint()
		}
		addresses.Put(addr.SelfLink, addr)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, addr.SelfLink, "insert"))
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/addresses/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := computeRegionalAddressLink(project, region, name)
		addr, ok := addresses.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "address %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, addr)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/addresses", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		prefix := fmt.Sprintf("projects/%s/regions/%s/addresses/", project, region)
		computeWriteScopedList(w, r, addresses, "compute#addressList", prefix, func(v ComputeAddress) string { return v.SelfLink }, func(v ComputeAddress) string { return v.Name })
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/addresses/{name}/setLabels", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := computeRegionalAddressLink(project, region, name)
		var req struct {
			Labels           map[string]string `json:"labels"`
			LabelFingerprint string            `json:"labelFingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		ok := addresses.Update(selfLink, func(addr *ComputeAddress) {
			addr.Labels = req.Labels
			addr.LabelFingerprint = computeFingerprint()
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "address %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, selfLink, "setLabels"))
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/addresses/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := computeRegionalAddressLink(project, region, name)
		addr, ok := addresses.Get(selfLink)
		if computeNotFound(w, ok, "address", name) {
			return
		}
		ipam.ReleasePublicIPv4(net.ParseIP(addr.Address))
		addresses.Delete(selfLink)
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "regions/"+region, selfLink, "delete"))
	})

	srv.HandleFunc("POST "+egress.ReportPath, sidecar.EgressReports(gcpTakeNATEgressReport))

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/routers", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		var rt ComputeRouter
		if err := sim.ReadJSON(r, &rt); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, rt.Name) {
			return
		}
		if err := validateRouterNATAddresses(project, region, rt.Nats, addresses); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err)
			return
		}
		rt.Kind = "compute#router"
		rt.Id = computeNumericID()
		rt.SelfLink = fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, rt.Name)
		rt.Region = fmt.Sprintf("projects/%s/regions/%s", project, region)
		rt.CreationTimestamp = time.Now().UTC().Format(time.RFC3339)
		if _, exists := routers.Get(rt.SelfLink); computeConflict(w, exists, "router", rt.Name) {
			return
		}
		defaultRouterNATTypes(rt.Nats)
		routers.Put(rt.SelfLink, rt)
		gcpReapplyNAT()
		op := newComputeOpWithType(project, "regions/"+region, rt.SelfLink, "insert")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		rt, ok := routers.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rt)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		prefix := fmt.Sprintf("projects/%s/regions/%s/routers/", project, region)
		computeWriteScopedList(w, r, routers, "compute#routerList", prefix, func(v ComputeRouter) string { return v.SelfLink }, func(v ComputeRouter) string { return v.Name })
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/routers/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		if computeNotFound(w, routers.Delete(selfLink), "router", name) {
			return
		}
		gcpReapplyNAT()
		op := newComputeOpWithType(project, "regions/"+region, selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("PATCH /compute/v1/projects/{project}/regions/{region}/routers/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		var patch ComputeRouter
		if err := sim.ReadJSON(r, &patch); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if err := validateRouterNATAddresses(project, region, patch.Nats, addresses); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%s", err)
			return
		}
		ok := routers.Update(selfLink, func(rt *ComputeRouter) {
			if patch.Description != "" {
				rt.Description = patch.Description
			}
			if patch.Network != "" {
				rt.Network = patch.Network
			}
			if patch.Bgp != nil {
				rt.Bgp = patch.Bgp
			}
			if patch.Nats != nil {
				defaultRouterNATTypes(patch.Nats)
				rt.Nats = patch.Nats
			}
		})
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		gcpReapplyNAT()
		op := newComputeOpWithType(project, "regions/"+region, selfLink, "patch")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/routers/{name}/getRouterStatus", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/routers/%s", project, region, name)
		rt, ok := routers.Get(selfLink)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "router %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#routerStatusResponse",
			"result": map[string]any{
				"network":    rt.Network,
				"bestRoutes": []map[string]any{},
			},
		})
	})
}
