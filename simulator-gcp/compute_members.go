package main

import (
	"fmt"
	"net/http"
	"sort"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Compute Engine collections whose verbs manage a membership: the instances
// and health checks a target pool balances across, a network's peerings, the
// nodes in a sole-tenant node group, and a rollout's progress through its
// stages.
//
// Every one of these writes a list the resource carries, so the read beside it
// is what proves the write landed. A target pool that accepted addInstance and
// listed nothing afterwards would look identical to one that worked.

// computeMemberList reads a string list off a resource record.
func computeMemberList(resource map[string]any, field string) []string {
	raw, _ := resource[field].([]any)
	out := make([]string, 0, len(raw))
	for _, entry := range raw {
		if value, ok := entry.(string); ok {
			out = append(out, value)
		}
	}
	return out
}

func computeSetMemberList(resource map[string]any, field string, values []string) {
	entries := make([]any, 0, len(values))
	for _, value := range values {
		entries = append(entries, value)
	}
	resource[field] = entries
}

// computeReferenceNames reads the {"instances":[{"instance":"url"}]} shape the
// target-pool and node-group verbs take their members in.
func computeReferenceNames(entries []map[string]string, field string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if value := entry[field]; value != "" {
			out = append(out, value)
		}
	}
	return out
}

// The stores the member verbs share with the collections that own them.
var (
	gcpComputeTargetPools  sim.Store[map[string]any]
	gcpComputeNodeGroups   sim.Store[map[string]any]
	gcpComputeReservations sim.Store[map[string]any]
	gcpComputeNetworks     sim.Store[ComputeNetwork]
	gcpComputeSubnetworks  sim.Store[ComputeSubnetwork]

	gcpComputeCrossSiteNetworks               sim.Store[map[string]any]
	gcpComputeRegionalPublicDelegatedPrefixes sim.Store[map[string]any]
)

func registerComputeMemberVerbs(srv *sim.Server) {
	mk := func(table string) sim.Store[map[string]any] {
		return sim.MakeStore[map[string]any](srv.DB(), table)
	}
	rollouts := mk("compute_rollouts")

	// A rollout moves through its stages, and the state it is in is the whole
	// of what the verbs change.
	(computeMetaResource{
		collection: "rollouts", kind: "compute#rollout", scope: cScopeGlobal,
		// A rollout is created by the change it rolls out, so the document
		// declares no insert; its PATCH is the cancel.
		store: rollouts, skipInsert: true, patch: true,
	}).register(srv)

	const rolloutBase = "/compute/v1/projects/{project}/global/rollouts"
	rolloutState := func(verb, state string) {
		srv.HandleFunc("POST "+rolloutBase+"/{rollout}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			project, name := sim.PathParam(r, "project"), sim.PathParam(r, "rollout")
			key := "projects/" + project + "/global/rollouts/" + name
			rollout, ok := rollouts.Get(key)
			if !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "rollout %q not found", name)
				return
			}
			current, _ := rollout["state"].(string)
			if current == state {
				sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
					"rollout %q is already %s", name, state)
				return
			}
			rollout["state"] = state
			rollouts.Put(key, rollout)
			sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(project, "global", computeSelfLink(key), verb))
		})
	}
	rolloutState("pause", "PAUSED")
	rolloutState("resume", "RUNNING")
	rolloutState("advance", "ADVANCING")

	registerComputeTargetPoolMembers(srv)
	registerComputeNetworkPeerings(srv)
	registerComputeNodeGroupNodes(srv, gcpComputeNodeGroups, mk("compute_node_group_nodes"))
}

// registerComputeTargetPoolMembers serves the instances and health checks a
// target pool balances across, and the health it reports for them.
func registerComputeTargetPoolMembers(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/regions/{region}/targetPools"
	pools := gcpComputeTargetPools

	load := func(w http.ResponseWriter, r *http.Request) (string, map[string]any, bool) {
		project, region, name := sim.PathParam(r, "project"), sim.PathParam(r, "region"), sim.PathParam(r, "targetPool")
		key := "projects/" + project + "/regions/" + region + "/targetPools/" + name
		pool, ok := pools.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "targetPool %q not found", name)
			return "", nil, false
		}
		return key, pool, true
	}
	operation := func(r *http.Request, key, verb string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"),
			"regions/"+sim.PathParam(r, "region"), computeSelfLink(key), verb)
	}

	member := func(verb, field, requestField string, add bool) {
		srv.HandleFunc("POST "+base+"/{targetPool}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key, pool, ok := load(w, r)
			if !ok {
				return
			}
			var req map[string][]map[string]string
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			wanted := computeReferenceNames(req[requestField], requestField[:len(requestField)-1])
			held := computeMemberList(pool, field)
			for _, value := range wanted {
				at := computeIndexOfString(held, value)
				if add {
					if at >= 0 {
						sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
							"%s is already in this target pool", value)
						return
					}
					held = append(held, value)
					continue
				}
				if at < 0 {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"%s is not in this target pool", value)
					return
				}
				held = append(held[:at], held[at+1:]...)
			}
			computeSetMemberList(pool, field, held)
			pools.Put(key, pool)
			sim.WriteJSON(w, http.StatusOK, operation(r, key, verb))
		})
	}
	member("addInstance", "instances", "instances", true)
	member("removeInstance", "instances", "instances", false)
	member("addHealthCheck", "healthChecks", "healthChecks", true)
	member("removeHealthCheck", "healthChecks", "healthChecks", false)

	srv.HandleFunc("POST "+base+"/{targetPool}/setBackup", func(w http.ResponseWriter, r *http.Request) {
		key, pool, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			Target string `json:"target"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		pool["backupPool"] = req.Target
		if ratio := r.URL.Query().Get("failoverRatio"); ratio != "" {
			pool["failoverRatio"] = ratio
		}
		pools.Put(key, pool)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "setBackup"))
	})

	srv.HandleFunc("POST "+base+"/{targetPool}/setSecurityPolicy", func(w http.ResponseWriter, r *http.Request) {
		key, pool, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			SecurityPolicy string `json:"securityPolicy"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		pool["securityPolicy"] = req.SecurityPolicy
		pools.Put(key, pool)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "setSecurityPolicy"))
	})

	// The IAM verbs on a target pool are registered by the shared
	// registerComputeIAMTriplet, beside every other collection that declares
	// them.

	// The health a pool reports for one of its instances. It is healthy when
	// the pool holds it, which is the only thing this simulator knows about it
	// — and it says so rather than reporting a check nothing ran.
	srv.HandleFunc("POST "+base+"/{targetPool}/getHealth", func(w http.ResponseWriter, r *http.Request) {
		_, pool, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			Instance string `json:"instance"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !computeStringInSlice(computeMemberList(pool, "instances"), req.Instance) {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"instance %s is not in this target pool", req.Instance)
			return
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#targetPoolInstanceHealth",
			"healthStatus": []any{map[string]any{
				"instance": req.Instance, "healthState": "HEALTHY",
			}},
		})
	})
}

// computeNetworkSelfLink is the key the networks store is written and read
// under. One spelling, shared by every handler that touches that store: two
// would read past each other.
func computeNetworkSelfLink(project, name string) string {
	return fmt.Sprintf("projects/%s/global/networks/%s", project, name)
}

// registerComputeNetworkPeerings serves a network's peerings and the reads
// derived from them.
func registerComputeNetworkPeerings(srv *sim.Server) {
	const base = "/compute/v1/projects/{project}/global/networks"

	load := func(w http.ResponseWriter, r *http.Request) (string, ComputeNetwork, bool) {
		project, name := sim.PathParam(r, "project"), sim.PathParam(r, "network")
		key := computeNetworkSelfLink(project, name)
		network, ok := gcpComputeNetworks.Get(key)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "network %q not found", name)
			return "", ComputeNetwork{}, false
		}
		return key, network, true
	}
	operation := func(r *http.Request, key, verb string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"), "global", key, verb)
	}
	findPeering := func(network *ComputeNetwork, name string) int {
		for i := range network.Peerings {
			if network.Peerings[i].Name == name {
				return i
			}
		}
		return -1
	}

	srv.HandleFunc("POST "+base+"/{network}/addPeering", func(w http.ResponseWriter, r *http.Request) {
		key, network, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			Name             string                 `json:"name"`
			PeerNetwork      string                 `json:"peerNetwork"`
			NetworkPeering   *ComputeNetworkPeering `json:"networkPeering"`
			AutoCreateRoutes bool                   `json:"autoCreateRoutes"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		peering := ComputeNetworkPeering{Name: req.Name, Network: req.PeerNetwork}
		if req.NetworkPeering != nil {
			peering = *req.NetworkPeering
		}
		if peering.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "a peering must be named", "INVALID_ARGUMENT")
			return
		}
		if findPeering(&network, peering.Name) >= 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"a peering named %q already exists on this network", peering.Name)
			return
		}
		// A peering is ACTIVE once both sides exist; until the peer names this
		// network back it is INACTIVE, which is what Compute Engine reports.
		peering.State = "INACTIVE"
		peering.StateDetails = "waiting for the peer network to peer back"
		if peer, held := gcpComputeNetworks.Get(peering.Network); held {
			for _, back := range peer.Peerings {
				if back.Network == key {
					peering.State = "ACTIVE"
					peering.StateDetails = "matching peering is up"
				}
			}
		}
		network.Peerings = append(network.Peerings, peering)
		gcpComputeNetworks.Put(key, network)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "addPeering"))
	})

	srv.HandleFunc("POST "+base+"/{network}/removePeering", func(w http.ResponseWriter, r *http.Request) {
		key, network, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		at := findPeering(&network, req.Name)
		if at < 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"no peering named %q on this network", req.Name)
			return
		}
		network.Peerings = append(network.Peerings[:at], network.Peerings[at+1:]...)
		gcpComputeNetworks.Put(key, network)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "removePeering"))
	})

	srv.HandleFunc("PATCH "+base+"/{network}/updatePeering", func(w http.ResponseWriter, r *http.Request) {
		key, network, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			NetworkPeering *ComputeNetworkPeering `json:"networkPeering"`
		}
		if err := sim.ReadJSON(r, &req); err != nil || req.NetworkPeering == nil {
			sim.GCPError(w, http.StatusBadRequest,
				"networkPeering is required to update a peering", "INVALID_ARGUMENT")
			return
		}
		at := findPeering(&network, req.NetworkPeering.Name)
		if at < 0 {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"no peering named %q on this network", req.NetworkPeering.Name)
			return
		}
		// The state is the service's to report, not the caller's to set.
		updated := *req.NetworkPeering
		updated.State = network.Peerings[at].State
		updated.StateDetails = network.Peerings[at].StateDetails
		network.Peerings[at] = updated
		gcpComputeNetworks.Put(key, network)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "updatePeering"))
	})

	// A removal a peer has to consent to, and the cancellation of that request.
	peeringRequest := func(verb, details string) {
		srv.HandleFunc("POST "+base+"/{network}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key, network, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				Name string `json:"name"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			at := findPeering(&network, req.Name)
			if at < 0 {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"no peering named %q on this network", req.Name)
				return
			}
			network.Peerings[at].StateDetails = details
			gcpComputeNetworks.Put(key, network)
			sim.WriteJSON(w, http.StatusOK, operation(r, key, verb))
		})
	}
	peeringRequest("requestRemovePeering", "removal requested by this network")
	peeringRequest("cancelRequestRemovePeering", "removal request withdrawn")

	srv.HandleFunc("POST "+base+"/{network}/switchToCustomMode", func(w http.ResponseWriter, r *http.Request) {
		key, network, ok := load(w, r)
		if !ok {
			return
		}
		if !network.AutoCreateSubnetworks {
			sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
				"network %q is already in custom subnet mode", network.Name)
			return
		}
		network.AutoCreateSubnetworks = false
		gcpComputeNetworks.Put(key, network)
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "switchToCustomMode"))
	})

	srv.HandleFunc("GET "+base+"/{network}/listPeeringRoutes", func(w http.ResponseWriter, r *http.Request) {
		_, network, ok := load(w, r)
		if !ok {
			return
		}
		// The routes a peering exchanges are the peer's subnet ranges, which
		// the simulator holds, rather than a list nothing derived.
		wanted := r.URL.Query().Get("peeringName")
		routes := []any{}
		for _, peering := range network.Peerings {
			if wanted != "" && peering.Name != wanted {
				continue
			}
			peer, held := gcpComputeNetworks.Get(peering.Network)
			if !held {
				continue
			}
			for _, subnet := range gcpComputeSubnetworks.Filter(func(s ComputeSubnetwork) bool {
				return s.Network == peer.SelfLink
			}) {
				routes = append(routes, map[string]any{
					"destRange": subnet.IpCidrRange, "type": "SUBNET_PEERING_ROUTE",
					"nextHopRegion": subnet.Region, "priority": 0,
					"imported": true, "peeringName": peering.Name,
				})
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#exchangedPeeringRoutesList", "items": routes,
		})
	})

	srv.HandleFunc("GET "+base+"/{network}/getEffectiveFirewalls", func(w http.ResponseWriter, r *http.Request) {
		key, _, ok := load(w, r)
		if !ok {
			return
		}
		rules := []any{}
		if gcpFirewalls != nil {
			for _, firewall := range gcpFirewalls.List() {
				if firewall.Network == key {
					rules = append(rules, firewall)
				}
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"firewalls": rules})
	})
}

// registerComputeNodeGroupNodes serves the nodes of a sole-tenant node group.
func registerComputeNodeGroupNodes(srv *sim.Server, groups, nodes sim.Store[map[string]any]) {
	const base = "/compute/v1/projects/{project}/zones/{zone}/nodeGroups"

	load := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		project, zone, name := sim.PathParam(r, "project"), sim.PathParam(r, "zone"), sim.PathParam(r, "nodeGroup")
		key := "projects/" + project + "/zones/" + zone + "/nodeGroups/" + name
		if _, ok := groups.Get(key); !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "nodeGroup %q not found", name)
			return "", false
		}
		return key, true
	}
	operation := func(r *http.Request, key, verb string) map[string]any {
		return newComputeOpWithType(sim.PathParam(r, "project"),
			"zones/"+sim.PathParam(r, "zone"), computeSelfLink(key), verb)
	}

	srv.HandleFunc("POST "+base+"/{nodeGroup}/addNodes", func(w http.ResponseWriter, r *http.Request) {
		key, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			AdditionalNodeCount int `json:"additionalNodeCount"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if req.AdditionalNodeCount <= 0 {
			sim.GCPError(w, http.StatusBadRequest,
				"additionalNodeCount must be greater than zero", "INVALID_ARGUMENT")
			return
		}
		existing := nodes.Filter(func(n map[string]any) bool { return n["group"] == key })
		for i := 0; i < req.AdditionalNodeCount; i++ {
			name := sim.PathParam(r, "nodeGroup") + "-node-" + computeNumericID()
			nodes.Put(key+"\x00"+name, map[string]any{
				"group": key, "name": name, "status": "READY",
				"nodeType": "n1-node-96-624", "instances": []any{},
			})
		}
		_ = existing
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "addNodes"))
	})

	srv.HandleFunc("POST "+base+"/{nodeGroup}/deleteNodes", func(w http.ResponseWriter, r *http.Request) {
		key, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			Nodes []string `json:"nodes"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		for _, name := range req.Nodes {
			if !nodes.Delete(key + "\x00" + name) {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"the group holds no node named %q", name)
				return
			}
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "deleteNodes"))
	})

	srv.HandleFunc("POST "+base+"/{nodeGroup}/listNodes", func(w http.ResponseWriter, r *http.Request) {
		key, ok := load(w, r)
		if !ok {
			return
		}
		held := nodes.Filter(func(n map[string]any) bool { return n["group"] == key })
		// The group a node belongs to is how the store finds it, not something
		// NodeGroupNode carries — the schema has no such member.
		items := make([]map[string]any, 0, len(held))
		for _, node := range held {
			reported := map[string]any{}
			for field, value := range node {
				if field == "group" {
					continue
				}
				reported[field] = value
			}
			items = append(items, reported)
		}
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i]["name"].(string)
			b, _ := items[j]["name"].(string)
			return a < b
		})
		if items == nil {
			items = []map[string]any{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#nodeGroupsListNodes", "items": items,
		})
	})

	srv.HandleFunc("POST "+base+"/{nodeGroup}/setNodeTemplate", func(w http.ResponseWriter, r *http.Request) {
		key, ok := load(w, r)
		if !ok {
			return
		}
		var req struct {
			NodeTemplate string `json:"nodeTemplate"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		groups.Update(key, func(g *map[string]any) { (*g)["nodeTemplate"] = req.NodeTemplate })
		sim.WriteJSON(w, http.StatusOK, operation(r, key, "setNodeTemplate"))
	})

	for _, verb := range []string{"performMaintenance", "simulateMaintenanceEvent"} {
		verb := verb
		srv.HandleFunc("POST "+base+"/{nodeGroup}/"+verb, func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				Nodes []string `json:"nodes"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			for _, name := range req.Nodes {
				nodeKey := key + "\x00" + name
				node, held := nodes.Get(nodeKey)
				if !held {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
						"the group holds no node named %q", name)
					return
				}
				node["status"] = "REPAIRING"
				nodes.Put(nodeKey, node)
			}
			sim.WriteJSON(w, http.StatusOK, operation(r, key, verb))
		})
	}
}
