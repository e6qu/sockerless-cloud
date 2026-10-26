package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

func registerComputeNetworks(srv *sim.Server, networks sim.Store[ComputeNetwork], subnetworks sim.Store[ComputeSubnetwork]) {
	srv.HandleFunc("POST /compute/v1/projects/{project}/global/networks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")

		var req struct {
			Name                  string `json:"name"`
			AutoCreateSubnetworks bool   `json:"autoCreateSubnetworks"`
			// Peerings is the network's peerings, which addPeering and its siblings
			// write and listPeeringRoutes reads the exchanged ranges from.
			Peerings      []ComputeNetworkPeering `json:"peerings,omitempty"`
			RoutingConfig struct {
				RoutingMode string `json:"routingMode"`
			} `json:"routingConfig"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if computeRefuseResourceName(w, req.Name) {
			return
		}

		selfLink := computeNetworkSelfLink(project, req.Name)
		net := ComputeNetwork{
			Kind:                  "compute#network",
			Id:                    computeNumericID(),
			Name:                  req.Name,
			SelfLink:              selfLink,
			AutoCreateSubnetworks: req.AutoCreateSubnetworks,
			CreationTimestamp:     time.Now().UTC().Format(time.RFC3339),
		}
		net.RoutingConfig.RoutingMode = req.RoutingConfig.RoutingMode
		if net.RoutingConfig.RoutingMode == "" {
			net.RoutingConfig.RoutingMode = "REGIONAL"
		}
		net.NetworkFirewallPolicyEnforcementOrder = "AFTER_CLASSIC_FIREWALL"
		if _, exists := networks.Get(selfLink); computeConflict(w, exists, "network", req.Name) {
			return
		}
		networks.Put(selfLink, net)

		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeNetworkSelfLink(project, name)

		net, ok := networks.Get(selfLink)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Network %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, net)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/global/networks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		prefix := fmt.Sprintf("projects/%s/global/networks/", project)
		computeWriteScopedList(w, r, networks, "compute#networkList", prefix, func(v ComputeNetwork) string { return v.SelfLink }, func(v ComputeNetwork) string { return v.Name })
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/global/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeNetworkSelfLink(project, name)

		if _, ok := networks.Get(selfLink); computeNotFound(w, ok, "network", name) {
			return
		}
		for _, subnet := range subnetworks.List() {
			if gcpNetworkKey(subnet.SelfLink, subnet.Network) != selfLink {
				continue
			}
			if err := gcpRemoveSubnetworkNetwork(r.Context(), subnet); err != nil {
				GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "remove the network of subnetwork %s: %v", subnet.Name, err)
				return
			}
		}
		networks.Delete(selfLink)
		// Compute Engine stamps a delete operation `operationType: "delete"`; gcloud
		// reports the resource deleted on it instead of re-fetching a 404.
		op := newComputeOpWithType(project, "global", selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("PATCH /compute/v1/projects/{project}/global/networks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		name := sim.PathParam(r, "name")
		selfLink := computeNetworkSelfLink(project, name)

		var req struct {
			RoutingConfig struct {
				RoutingMode string `json:"routingMode"`
			} `json:"routingConfig"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		networks.Update(selfLink, func(n *ComputeNetwork) {
			if req.RoutingConfig.RoutingMode != "" {
				n.RoutingConfig.RoutingMode = req.RoutingConfig.RoutingMode
			}
		})

		op := newComputeOp(project, "global", selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")

		var req struct {
			Name                  string `json:"name"`
			Network               string `json:"network"`
			IpCidrRange           string `json:"ipCidrRange"`
			PrivateIpGoogleAccess bool   `json:"privateIpGoogleAccess"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, req.Name)
		subnet := ComputeSubnetwork{
			Kind:                  "compute#subnetwork",
			Id:                    computeNumericID(),
			Name:                  req.Name,
			SelfLink:              selfLink,
			Network:               req.Network,
			IpCidrRange:           req.IpCidrRange,
			Region:                fmt.Sprintf("projects/%s/regions/%s", project, region),
			GatewayAddress:        gcpSubnetGateway(req.IpCidrRange),
			PrivateIpGoogleAccess: req.PrivateIpGoogleAccess,
			CreationTimestamp:     time.Now().UTC().Format(time.RFC3339),
		}
		if _, exists := subnetworks.Get(selfLink); computeConflict(w, exists, "subnetwork", req.Name) {
			return
		}
		subnetworks.Put(selfLink, subnet)

		op := newComputeOp(project, "regions/"+region, selfLink)
		sim.WriteJSON(w, http.StatusOK, op)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)

		subnet, ok := subnetworks.Get(selfLink)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, subnet)
	})

	// subnetworks.list answers a SubnetworkList (`kind` and `items`), not the
	// scope-keyed map aggregatedList returns.
	srv.HandleFunc("GET /compute/v1/projects/{project}/regions/{region}/subnetworks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		prefix := fmt.Sprintf("projects/%s/regions/%s/subnetworks/", project, region)
		computeWriteScopedList(w, r, subnetworks, "compute#subnetworkList", prefix, func(v ComputeSubnetwork) string { return v.SelfLink }, func(v ComputeSubnetwork) string { return v.Name })
	})

	// A subnetwork's range only grows: shrinking it would strand addresses already
	// handed out, so Compute Engine refuses it. A smaller prefix length is a
	// larger range.
	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/expandIpCidrRange", func(w http.ResponseWriter, r *http.Request) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
		var req struct {
			IpCidrRange string `json:"ipCidrRange"`
		}
		if err := sim.ReadJSON(r, &req); err != nil || req.IpCidrRange == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"expandIpCidrRange needs the range to expand to")
			return
		}
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)
		held, ok := subnetworks.Get(selfLink)
		if !ok {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		wider, err := computeRangeIsWider(held.IpCidrRange, req.IpCidrRange)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
			return
		}
		if !wider {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a subnetwork range can only expand: %s is not wider than %s", req.IpCidrRange, held.IpCidrRange)
			return
		}
		held.IpCidrRange = req.IpCidrRange
		subnetworks.Put(selfLink, held)
		gcpForgetSubnetworkIPAM(selfLink)
		sim.WriteJSON(w, http.StatusOK,
			newComputeOpWithType(project, "regions/"+region, selfLink, "expandIpCidrRange"))
	})

	// A subnetwork's patch edits the members that change without recreating it.
	srv.HandleFunc("PATCH /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
		var body map[string]any
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)
		found, err := computeTypedWrite(subnetworks, selfLink, body, false)
		if err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid subnetwork: %v", err)
			return
		}
		if !found {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK,
			newComputeOpWithType(project, "regions/"+region, selfLink, "patch"))
	})

	srv.HandleFunc("POST /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}/setPrivateIpGoogleAccess", func(w http.ResponseWriter, r *http.Request) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "name")
		var req struct {
			PrivateIpGoogleAccess bool `json:"privateIpGoogleAccess"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)
		if !subnetworks.Update(selfLink, func(s *ComputeSubnetwork) { s.PrivateIpGoogleAccess = req.PrivateIpGoogleAccess }) {
			GCPErrorf(w, 404, "NOT_FOUND", "Subnetwork %s not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK,
			newComputeOpWithType(project, "regions/"+region, selfLink, "setPrivateIpGoogleAccess"))
	})

	srv.HandleFunc("DELETE /compute/v1/projects/{project}/regions/{region}/subnetworks/{name}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		region := sim.PathParam(r, "region")
		name := sim.PathParam(r, "name")
		selfLink := fmt.Sprintf("projects/%s/regions/%s/subnetworks/%s", project, region, name)

		subnet, ok := subnetworks.Get(selfLink)
		if computeNotFound(w, ok, "subnetwork", name) {
			return
		}
		if err := gcpRemoveSubnetworkNetwork(r.Context(), subnet); err != nil {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL", "remove the network of subnetwork %s: %v", name, err)
			return
		}
		subnetworks.Delete(selfLink)
		gcpForgetSubnetworkIPAM(selfLink)
		// Compute Engine stamps a delete operation `operationType: "delete"`; gcloud
		// reports the resource deleted on it instead of re-fetching a 404.
		op := newComputeOpWithType(project, "regions/"+region, selfLink, "delete")
		sim.WriteJSON(w, http.StatusOK, op)
	})
}
