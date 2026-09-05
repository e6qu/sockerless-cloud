package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A regional instance group is not created: it is the group a regional
// instance group manager keeps, so Compute Engine declares only get, list,
// listInstances and setNamedPorts for it. Deriving it from the manager is what
// keeps the two from disagreeing — a resize is visible through the group
// without anything having to copy it there.
func registerComputeRegionInstanceGroups(srv *sim.Server, managers sim.Store[map[string]any]) {
	const base = "/compute/v1/projects/{project}/regions/{region}/instanceGroups"
	namedPorts := sim.MakeStore[map[string]any](srv.DB(), "compute_region_instance_group_named_ports")

	managerKey := func(r *http.Request, name string) string {
		return fmt.Sprintf("projects/%s/regions/%s/instanceGroupManagers/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "region"), name)
	}
	groupLink := func(r *http.Request, name string) string {
		return fmt.Sprintf("projects/%s/regions/%s/instanceGroups/%s",
			sim.PathParam(r, "project"), sim.PathParam(r, "region"), name)
	}

	// The group as the API reports it: the manager's identity, the size it is
	// currently at, and whatever named ports were set on the group itself.
	group := func(r *http.Request, name string, manager map[string]any) map[string]any {
		link := groupLink(r, name)
		size := computeStoredInt(manager["targetSize"])
		reported := map[string]any{
			"kind":     "compute#instanceGroup",
			"name":     name,
			"selfLink": link,
			"region": fmt.Sprintf("projects/%s/regions/%s",
				sim.PathParam(r, "project"), sim.PathParam(r, "region")),
			"size":              size,
			"creationTimestamp": manager["creationTimestamp"],
			"id":                manager["id"],
		}
		if ports, ok := namedPorts.Get(link); ok {
			reported["namedPorts"] = ports["namedPorts"]
			reported["fingerprint"] = ports["fingerprint"]
		}
		return reported
	}

	load := func(w http.ResponseWriter, r *http.Request, name string) (map[string]any, bool) {
		manager, ok := managers.Get(managerKey(r, name))
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance group %q not found", name)
			return nil, false
		}
		return group(r, name, manager), true
	}

	srv.HandleFunc("GET "+base+"/{instanceGroup}", func(w http.ResponseWriter, r *http.Request) {
		reported, ok := load(w, r, sim.PathParam(r, "instanceGroup"))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, reported)
	})

	srv.HandleFunc("GET "+base, func(w http.ResponseWriter, r *http.Request) {
		// The managers store stamps the absolute self-link, so the prefix a
		// list narrows by has to be spelled the same way.
		prefix := computeSelfLink(fmt.Sprintf("projects/%s/regions/%s/instanceGroupManagers/",
			sim.PathParam(r, "project"), sim.PathParam(r, "region")))
		var items []any
		for _, manager := range managers.Filter(func(m map[string]any) bool {
			link, _ := m["selfLink"].(string)
			return strings.HasPrefix(link, prefix)
		}) {
			name, _ := manager["name"].(string)
			items = append(items, group(r, name, manager))
		}
		sort.Slice(items, func(i, j int) bool {
			a, _ := items[i].(map[string]any)["name"].(string)
			b, _ := items[j].(map[string]any)["name"].(string)
			return a < b
		})
		if items == nil {
			items = []any{}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#regionInstanceGroupList", "items": items,
		})
	})

	// The instances the group holds are the manager's managed instances, which
	// is where a resize shows up.
	srv.HandleFunc("POST "+base+"/{instanceGroup}/listInstances", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "instanceGroup")
		if _, ok := load(w, r, name); !ok {
			return
		}
		var req struct {
			InstanceState string `json:"instanceState"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		managed := computeManagedInstances.Filter(func(i ComputeManagedInstance) bool {
			return i.Group == managerKey(r, name)
		})
		sort.Slice(managed, func(i, j int) bool { return managed[i].Name < managed[j].Name })
		items := []any{}
		for _, instance := range managed {
			status := instance.InstanceStatus
			if status == "" {
				status = "RUNNING"
			}
			// ALL is every instance; RUNNING narrows to the ones that are up.
			if strings.EqualFold(req.InstanceState, "RUNNING") && status != "RUNNING" {
				continue
			}
			items = append(items, map[string]any{
				"instance": instance.Instance, "status": status,
			})
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"kind": "compute#regionInstanceGroupsListInstances", "items": items,
		})
	})

	srv.HandleFunc("POST "+base+"/{instanceGroup}/setNamedPorts", func(w http.ResponseWriter, r *http.Request) {
		name := sim.PathParam(r, "instanceGroup")
		if _, ok := load(w, r, name); !ok {
			return
		}
		var req struct {
			NamedPorts  []map[string]any `json:"namedPorts"`
			Fingerprint string           `json:"fingerprint"`
		}
		if err := sim.ReadJSON(r, &req); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		link := groupLink(r, name)
		// The fingerprint is optimistic concurrency: a client that read the
		// group before someone else wrote it is told so rather than winning.
		if held, ok := namedPorts.Get(link); ok {
			current, _ := held["fingerprint"].(string)
			if req.Fingerprint != "" && req.Fingerprint != current {
				GCPErrorf(w, http.StatusPreconditionFailed, "CONDITION_NOT_MET",
					"the named ports were changed since they were read")
				return
			}
		}
		ports := req.NamedPorts
		if ports == nil {
			ports = []map[string]any{}
		}
		namedPorts.Put(link, map[string]any{
			"namedPorts": ports, "fingerprint": computeFingerprint(),
		})
		sim.WriteJSON(w, http.StatusOK, newComputeOpWithType(sim.PathParam(r, "project"),
			"regions/"+sim.PathParam(r, "region"), link, "setNamedPorts"))
	})
}
