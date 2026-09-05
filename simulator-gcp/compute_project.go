package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// The Compute Engine project resource and the verbs that write it.
//
// A project is not created through Compute Engine — it exists because the
// caller addressed it — so a read answers for any project, with the defaults
// Compute applies until something is set. The verbs beside it each write one
// part of it: the common instance metadata every VM inherits, where usage
// reports are delivered, the network tier new resources default to, and the
// Cloud Armor tier the project is on.
//
// Shared VPC lives here too. A host project lends its network to service
// projects, so the association is a property of the projects rather than of any
// resource in them: enabling a host records it, attaching a service project
// records the pair, and the reads report both directions.
func registerComputeProject(srv *sim.Server) {
	projects := sim.MakeStore[map[string]any](srv.DB(), "compute_projects")
	// The service projects attached to each host, keyed by host project.
	xpnResources := sim.MakeStore[map[string]any](srv.DB(), "compute_xpn_resources")

	load := func(project string) map[string]any {
		held, ok := projects.Get(project)
		if !ok {
			held = map[string]any{}
		}
		held["kind"] = "compute#project"
		held["name"] = project
		held["selfLink"] = computeSelfLink("projects/" + project)
		if _, set := held["defaultNetworkTier"]; !set {
			held["defaultNetworkTier"] = "PREMIUM"
		}
		if _, set := held["cloudArmorTier"]; !set {
			held["cloudArmorTier"] = "CA_STANDARD"
		}
		if _, set := held["defaultServiceAccount"]; !set {
			held["defaultServiceAccount"] = project + "-compute@developer.gserviceaccount.com"
		}
		return held
	}
	write := func(project string, apply func(map[string]any)) {
		held := load(project)
		apply(held)
		projects.Put(project, held)
	}
	operation := func(r *http.Request, verb string) map[string]any {
		project := sim.PathParam(r, "project")
		return newComputeOpWithType(project, "global", "projects/"+project, verb)
	}

	srv.HandleFunc("GET /compute/v1/projects/{project}", func(w http.ResponseWriter, r *http.Request) {
		sim.WriteJSON(w, http.StatusOK, load(sim.PathParam(r, "project")))
	})

	// Each verb reads one member from its request body and writes it onto the
	// project, which is what the read beside it then reports. Mounted at
	// literal paths so the surface tables can see them.
	setMember := func(verb, member, into string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			var body map[string]any
			if err := sim.ReadJSON(r, &body); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			value, sent := body[member]
			if !sent {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
					"%s needs %s in its request body", verb, member)
				return
			}
			write(sim.PathParam(r, "project"), func(m map[string]any) { m[into] = value })
			sim.WriteJSON(w, http.StatusOK, operation(r, verb))
		}
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/setDefaultNetworkTier",
		setMember("setDefaultNetworkTier", "networkTier", "defaultNetworkTier"))
	srv.HandleFunc("POST /compute/v1/projects/{project}/setCloudArmorTier",
		setMember("setCloudArmorTier", "cloudArmorTier", "cloudArmorTier"))

	// The metadata every instance in the project inherits. It carries a
	// fingerprint for optimistic concurrency, the way an instance's own
	// metadata does.
	srv.HandleFunc("POST /compute/v1/projects/{project}/setCommonInstanceMetadata", func(w http.ResponseWriter, r *http.Request) {
		var metadata map[string]any
		if err := sim.ReadJSON(r, &metadata); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		metadata["kind"] = "compute#metadata"
		metadata["fingerprint"] = computeFingerprint()
		write(sim.PathParam(r, "project"), func(m map[string]any) { m["commonInstanceMetadata"] = metadata })
		sim.WriteJSON(w, http.StatusOK, operation(r, "setCommonInstanceMetadata"))
	})

	// Where Compute Engine delivers the project's usage reports. An empty
	// body turns reporting off, which is how the API expresses it.
	srv.HandleFunc("POST /compute/v1/projects/{project}/setUsageExportBucket", func(w http.ResponseWriter, r *http.Request) {
		var location map[string]any
		if err := sim.ReadJSON(r, &location); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		write(sim.PathParam(r, "project"), func(m map[string]any) {
			if bucket, _ := location["bucketName"].(string); bucket == "" {
				delete(m, "usageExportLocation")
				return
			}
			m["usageExportLocation"] = location
		})
		sim.WriteJSON(w, http.StatusOK, operation(r, "setUsageExportBucket"))
	})

	// ── Shared VPC ──────────────────────────────────────────────────────

	// A project's own host status, and the host it is attached to.
	xpnStatus := func(project string) (isHost bool, host string) {
		held := load(project)
		status, _ := held["xpnProjectStatus"].(string)
		for _, entry := range xpnResources.List() {
			hostProject, _ := entry["host"].(string)
			for _, res := range computeXpnResourceIDs(entry) {
				if res == project {
					return status == "HOST", hostProject
				}
			}
		}
		return status == "HOST", ""
	}

	srv.HandleFunc("POST /compute/v1/projects/{project}/enableXpnHost", func(w http.ResponseWriter, r *http.Request) {
		write(sim.PathParam(r, "project"), func(m map[string]any) { m["xpnProjectStatus"] = "HOST" })
		sim.WriteJSON(w, http.StatusOK, operation(r, "enableXpnHost"))
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/disableXpnHost", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		write(project, func(m map[string]any) { m["xpnProjectStatus"] = "UNSPECIFIED_XPN_PROJECT_STATUS" })
		// A host that is no longer a host lends its network to nobody.
		xpnResources.Delete(project)
		sim.WriteJSON(w, http.StatusOK, operation(r, "disableXpnHost"))
	})

	// getXpnHost answers with the host project a service project is attached
	// to — the whole Project resource, as the method's response declares.
	srv.HandleFunc("GET /compute/v1/projects/{project}/getXpnHost", func(w http.ResponseWriter, r *http.Request) {
		_, host := xpnStatus(sim.PathParam(r, "project"))
		if host == "" {
			// Not attached to one: the response carries no project.
			sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#project"})
			return
		}
		sim.WriteJSON(w, http.StatusOK, load(host))
	})

	attach := func(w http.ResponseWriter, r *http.Request, joining bool) {
		host := sim.PathParam(r, "project")
		var req struct {
			XpnResource *struct {
				ID   string `json:"id"`
				Type string `json:"type"`
			} `json:"xpnResource"`
		}
		if err := sim.ReadJSON(r, &req); err != nil || req.XpnResource == nil || req.XpnResource.ID == "" {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"the request needs an xpnResource naming the service project")
			return
		}
		if isHost, _ := xpnStatus(host); !isHost {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"project %q is not a Shared VPC host", host)
			return
		}
		entry, ok := xpnResources.Get(host)
		if !ok {
			entry = map[string]any{"host": host, "resources": []any{}}
		}
		held := computeXpnResourceIDs(entry)
		kept := []any{}
		for _, id := range held {
			if id == req.XpnResource.ID {
				continue
			}
			kept = append(kept, map[string]any{"id": id, "type": "PROJECT"})
		}
		if joining {
			resourceType := req.XpnResource.Type
			if resourceType == "" {
				resourceType = "PROJECT"
			}
			kept = append(kept, map[string]any{"id": req.XpnResource.ID, "type": resourceType})
		} else if len(kept) == len(held) {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
				"project %q is not attached to host %q", req.XpnResource.ID, host)
			return
		}
		entry["resources"] = kept
		xpnResources.Put(host, entry)
		verb := "disableXpnResource"
		if joining {
			verb = "enableXpnResource"
		}
		sim.WriteJSON(w, http.StatusOK, operation(r, verb))
	}
	srv.HandleFunc("POST /compute/v1/projects/{project}/enableXpnResource", func(w http.ResponseWriter, r *http.Request) {
		attach(w, r, true)
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/disableXpnResource", func(w http.ResponseWriter, r *http.Request) {
		attach(w, r, false)
	})

	srv.HandleFunc("GET /compute/v1/projects/{project}/getXpnResources", func(w http.ResponseWriter, r *http.Request) {
		entry, ok := xpnResources.Get(sim.PathParam(r, "project"))
		resources := []any{}
		if ok {
			for _, id := range computeXpnResourceIDs(entry) {
				resources = append(resources, map[string]any{"id": id, "type": "PROJECT"})
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#projectsGetXpnResources", "resources": resources,
		})
	})

	// The hosts an organization has. Every project recorded as a host is one,
	// which is the same set enableXpnHost writes.
	srv.HandleFunc("POST /compute/v1/projects/{project}/listXpnHosts", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Organization string `json:"organization"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		hosts := []any{}
		for _, held := range projects.List() {
			if status, _ := held["xpnProjectStatus"].(string); status != "HOST" {
				continue
			}
			name, _ := held["name"].(string)
			hosts = append(hosts, load(name))
		}
		sort.Slice(hosts, func(i, j int) bool {
			a, _ := hosts[i].(map[string]any)["name"].(string)
			b, _ := hosts[j].(map[string]any)["name"].(string)
			return a < b
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{"kind": "compute#xpnHostList", "items": hosts})
	})

	// ── Moving a resource between zones ─────────────────────────────────

	// moveDisk and moveInstance re-home a resource onto another zone. The
	// resource keeps its name and its contents; only the zone in its key
	// changes, which is what makes the move visible to a read in the new zone
	// and a not-found in the old one.
	srv.HandleFunc("POST /compute/v1/projects/{project}/moveDisk", func(w http.ResponseWriter, r *http.Request) {
		computeMoveZonalResource(w, r, "disks", "targetDisk", gcpComputeZoneDisks)
	})
	srv.HandleFunc("POST /compute/v1/projects/{project}/moveInstance", func(w http.ResponseWriter, r *http.Request) {
		computeMoveZonalResource(w, r, "instances", "targetInstance", gcpInstances)
	})
}

// computeXpnResourceIDs reads the service-project ids out of a stored host
// entry.
func computeXpnResourceIDs(entry map[string]any) []string {
	held, _ := entry["resources"].([]any)
	ids := make([]string, 0, len(held))
	for _, res := range held {
		item, ok := res.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := item["id"].(string); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// computeMoveZonalResource re-homes one zonal resource onto the zone the
// request names. The resource is the same resource afterwards — same name,
// same contents — so it moves under a new key rather than being recreated.
func computeMoveZonalResource[T any](w http.ResponseWriter, r *http.Request, collection, member string, store sim.Store[T]) {
	project := sim.PathParam(r, "project")
	var req map[string]any
	if err := sim.ReadJSON(r, &req); err != nil {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	source, _ := req[member].(string)
	destination, _ := req["destinationZone"].(string)
	if source == "" || destination == "" {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"the request needs %s and destinationZone", member)
		return
	}
	// Both are resource URLs or paths; the last segment names the thing.
	name := source[strings.LastIndex(source, "/")+1:]
	zone := destination[strings.LastIndex(destination, "/")+1:]
	sourceKey := strings.TrimPrefix(strings.TrimPrefix(source, "https://www.googleapis.com/compute/v1/"), "/")
	if !strings.HasPrefix(sourceKey, "projects/") {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"%s must name the resource by its path or URL", member)
		return
	}
	held, ok := store.Get(sourceKey)
	if !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "%s %q not found", collection, name)
		return
	}
	target := fmt.Sprintf("projects/%s/zones/%s/%s/%s", project, zone, collection, name)
	if target == sourceKey {
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"%s %q is already in zone %q", collection, name, zone)
		return
	}
	store.Put(target, held)
	store.Delete(sourceKey)
	sim.WriteJSON(w, http.StatusOK,
		newComputeOpWithType(project, "zones/"+zone, target, "move"))
}
