package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// A managed instance group's instances, per-instance configurations and resize
// requests.
//
// The group used to be metadata alone, and listManagedInstances answered with
// an empty list whatever the group's target size — which is the shape a stub
// has. The verbs beside it only mean something against instances that exist, so
// the group owns them here: createInstances names them, the lifecycle verbs
// move them between states, deleteInstances and abandonInstances remove them,
// and listManagedInstances reports what is actually there.
//
// The instances are records rather than running virtual machines. That is the
// honest scope of this collection: Compute Engine's own managed-instance list
// reports each instance's name, its current action and its status, and those
// are exactly what a client reads back here.

// ComputeManagedInstance is one instance a group manages.
type ComputeManagedInstance struct {
	Group          string `json:"-"`
	Name           string `json:"name"`
	Instance       string `json:"instance"`
	CurrentAction  string `json:"currentAction"`
	InstanceStatus string `json:"instanceStatus,omitempty"`
	ID             string `json:"id,omitempty"`
}

// ComputeInstanceGroupPerInstanceConfig is a configuration held for one named
// instance, whether or not that instance exists yet.
type ComputeInstanceGroupPerInstanceConfig struct {
	Group          string         `json:"-"`
	Name           string         `json:"name"`
	PreservedState map[string]any `json:"preservedState,omitempty"`
	Status         string         `json:"status,omitempty"`
	Fingerprint    string         `json:"fingerprint,omitempty"`
}

// ComputeInstanceGroupResizeRequest is a request to add capacity at a time the
// group chooses.
type ComputeInstanceGroupResizeRequest struct {
	Group       string `json:"-"`
	Name        string `json:"name"`
	SelfLink    string `json:"selfLink,omitempty"`
	Kind        string `json:"kind,omitempty"`
	State       string `json:"state,omitempty"`
	ResizeBy    int    `json:"resizeBy,omitempty"`
	Description string `json:"description,omitempty"`
}

var (
	computeManagedInstances   sim.Store[ComputeManagedInstance]
	computePerInstanceConfigs sim.Store[ComputeInstanceGroupPerInstanceConfig]
	computeResizeRequests     sim.Store[ComputeInstanceGroupResizeRequest]
)

func computeManagedInstanceKey(group, name string) string { return group + "\x00" + name }

// computeInstanceURL is the self link a managed instance reports, which is the
// zonal instance the group would have created.
func computeInstanceURL(project, zone, name string) string {
	return computeSelfLink(fmt.Sprintf("projects/%s/zones/%s/instances/%s", project, zone, name))
}

// computeInstanceNameFromRef reads the instance name out of the URL or bare
// name a caller addresses an instance by.
func computeInstanceNameFromRef(ref string) string {
	if at := strings.LastIndex(ref, "/"); at >= 0 {
		return ref[at+1:]
	}
	return ref
}

func registerComputeInstanceGroupInstances(srv *sim.Server, store sim.Store[map[string]any]) {
	computeManagedInstances = sim.MakeStore[ComputeManagedInstance](srv.DB(), "compute_managed_instances")
	computePerInstanceConfigs = sim.MakeStore[ComputeInstanceGroupPerInstanceConfig](srv.DB(), "compute_per_instance_configs")
	computeResizeRequests = sim.MakeStore[ComputeInstanceGroupResizeRequest](srv.DB(), "compute_igm_resize_requests")

	for _, scope := range []computeScopeKind{cScopeZone, cScopeRegion} {
		scope := scope
		base := computeScopeMux(scope, "instanceGroupManagers")

		groupKey := func(r *http.Request) string {
			return fmt.Sprintf("projects/%s/%s/instanceGroupManagers/%s",
				sim.PathParam(r, "project"), computeScopeSegment(scope, r), sim.PathParam(r, "name"))
		}
		load := func(w http.ResponseWriter, r *http.Request) (string, bool) {
			key := groupKey(r)
			if _, ok := store.Get(key); !ok {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"instanceGroupManager %q not found", sim.PathParam(r, "name"))
				return "", false
			}
			return key, true
		}
		operation := func(r *http.Request, key, opType string) map[string]any {
			return newComputeOpWithType(sim.PathParam(r, "project"), computeScopeSegment(scope, r),
				computeSelfLink(key), opType)
		}
		// A regional group's instances still live in a zone; the group's own
		// region names it well enough for a record.
		instanceZone := func(r *http.Request) string {
			if zone := sim.PathParam(r, "zone"); zone != "" {
				return zone
			}
			return sim.PathParam(r, "region") + "-a"
		}

		// The instances the group manages.
		srv.HandleFunc("POST "+base+"/{name}/createInstances", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				Instances []struct {
					Name           string         `json:"name"`
					PreservedState map[string]any `json:"preservedState"`
				} `json:"instances"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			project, zone := sim.PathParam(r, "project"), instanceZone(r)
			for _, wanted := range req.Instances {
				if wanted.Name == "" {
					sim.GCPError(w, http.StatusBadRequest,
						"each instance must be named to be created", "INVALID_ARGUMENT")
					return
				}
				computeManagedInstances.Put(computeManagedInstanceKey(key, wanted.Name), ComputeManagedInstance{
					Group:          key,
					Name:           wanted.Name,
					Instance:       computeInstanceURL(project, zone, wanted.Name),
					CurrentAction:  "NONE",
					InstanceStatus: "RUNNING",
					ID:             computeNumericID(),
				})
				if wanted.PreservedState != nil {
					computePerInstanceConfigs.Put(computeManagedInstanceKey(key, wanted.Name),
						ComputeInstanceGroupPerInstanceConfig{
							Group: key, Name: wanted.Name,
							PreservedState: wanted.PreservedState, Status: "EFFECTIVE",
							Fingerprint: computeFingerprint(),
						})
				}
			}
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "createInstances"))
		})

		// The verbs that act on instances the group already manages. Each takes
		// the same request shape and differs only in what it does to the
		// record, so they are registered from one table.
		type instanceVerb struct {
			verb string
			// apply reports the instance's state after the verb, and whether
			// the instance survives it.
			apply func(*ComputeManagedInstance) (keep bool)
		}
		verbs := []instanceVerb{
			{"deleteInstances", func(*ComputeManagedInstance) bool { return false }},
			{"abandonInstances", func(*ComputeManagedInstance) bool { return false }},
			{"recreateInstances", func(i *ComputeManagedInstance) bool {
				i.CurrentAction, i.InstanceStatus = "NONE", "RUNNING"
				i.ID = computeNumericID()
				return true
			}},
			{"applyUpdatesToInstances", func(i *ComputeManagedInstance) bool {
				i.CurrentAction = "NONE"
				return true
			}},
			{"startInstances", func(i *ComputeManagedInstance) bool {
				i.InstanceStatus = "RUNNING"
				return true
			}},
			{"stopInstances", func(i *ComputeManagedInstance) bool {
				i.InstanceStatus = "TERMINATED"
				return true
			}},
			{"suspendInstances", func(i *ComputeManagedInstance) bool {
				i.InstanceStatus = "SUSPENDED"
				return true
			}},
			{"resumeInstances", func(i *ComputeManagedInstance) bool {
				i.InstanceStatus = "RUNNING"
				return true
			}},
		}
		for _, verb := range verbs {
			verb := verb
			srv.HandleFunc("POST "+base+"/{name}/"+verb.verb, func(w http.ResponseWriter, r *http.Request) {
				key, ok := load(w, r)
				if !ok {
					return
				}
				var req struct {
					Instances []string `json:"instances"`
				}
				if err := sim.ReadJSON(r, &req); err != nil {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
					return
				}
				for _, ref := range req.Instances {
					name := computeInstanceNameFromRef(ref)
					recordKey := computeManagedInstanceKey(key, name)
					instance, held := computeManagedInstances.Get(recordKey)
					if !held {
						sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
							"the group manages no instance named %q", name)
						return
					}
					if verb.apply(&instance) {
						computeManagedInstances.Put(recordKey, instance)
						continue
					}
					computeManagedInstances.Delete(recordKey)
				}
				sim.WriteJSON(w, http.StatusOK, operation(r, key, verb.verb))
			})
		}

		srv.HandleFunc("POST "+base+"/{name}/setTargetPools", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				TargetPools []string `json:"targetPools"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			store.Update(key, func(m *map[string]any) { (*m)["targetPools"] = req.TargetPools })
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "setTargetPools"))
		})

		// The errors a group recorded while trying to reach its target size.
		// Nothing here fails a creation, so the list is empty — and it is empty
		// because nothing failed, not because nothing looked.
		srv.HandleFunc("GET "+base+"/{name}/listErrors", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := load(w, r); !ok {
				return
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"items": []any{}})
		})

		// Per-instance configurations.
		srv.HandleFunc("POST "+base+"/{name}/listPerInstanceConfigs", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			items := computePerInstanceConfigs.Filter(func(c ComputeInstanceGroupPerInstanceConfig) bool {
				return c.Group == key
			})
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			if items == nil {
				items = []ComputeInstanceGroupPerInstanceConfig{}
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{"items": items})
		})

		upsertConfigs := func(opType string) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				key, ok := load(w, r)
				if !ok {
					return
				}
				var req struct {
					PerInstanceConfigs []struct {
						Name           string         `json:"name"`
						PreservedState map[string]any `json:"preservedState"`
					} `json:"perInstanceConfigs"`
				}
				if err := sim.ReadJSON(r, &req); err != nil {
					sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
					return
				}
				for _, wanted := range req.PerInstanceConfigs {
					if wanted.Name == "" {
						sim.GCPError(w, http.StatusBadRequest,
							"each per-instance configuration must name its instance", "INVALID_ARGUMENT")
						return
					}
					recordKey := computeManagedInstanceKey(key, wanted.Name)
					config, held := computePerInstanceConfigs.Get(recordKey)
					if !held {
						config = ComputeInstanceGroupPerInstanceConfig{Group: key, Name: wanted.Name}
					}
					// An update replaces the preserved state; a patch merges
					// into it, which is the whole difference between the two.
					if opType == "updatePerInstanceConfigs" || config.PreservedState == nil {
						config.PreservedState = wanted.PreservedState
					} else {
						for field, value := range wanted.PreservedState {
							config.PreservedState[field] = value
						}
					}
					config.Status = "EFFECTIVE"
					config.Fingerprint = computeFingerprint()
					computePerInstanceConfigs.Put(recordKey, config)
				}
				sim.WriteJSON(w, http.StatusOK, operation(r, key, opType))
			}
		}
		srv.HandleFunc("POST "+base+"/{name}/updatePerInstanceConfigs", upsertConfigs("updatePerInstanceConfigs"))
		srv.HandleFunc("POST "+base+"/{name}/patchPerInstanceConfigs", upsertConfigs("patchPerInstanceConfigs"))

		srv.HandleFunc("POST "+base+"/{name}/deletePerInstanceConfigs", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			var req struct {
				Names []string `json:"names"`
			}
			if err := sim.ReadJSON(r, &req); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			for _, name := range req.Names {
				if !computePerInstanceConfigs.Delete(computeManagedInstanceKey(key, name)) {
					sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
						"the group holds no configuration for %q", name)
					return
				}
			}
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "deletePerInstanceConfigs"))
		})

		// Resize requests, a sub-collection of the group.
		requestKey := func(r *http.Request) string {
			return computeManagedInstanceKey(groupKey(r), sim.PathParam(r, "resizeRequest"))
		}
		srv.HandleFunc("POST "+base+"/{name}/resizeRequests", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			var body ComputeInstanceGroupResizeRequest
			if err := sim.ReadJSON(r, &body); err != nil {
				sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			if body.Name == "" {
				sim.GCPError(w, http.StatusBadRequest, "name is required", "INVALID_ARGUMENT")
				return
			}
			body.Group = key
			body.Kind = "compute#instanceGroupManagerResizeRequest"
			body.State = "ACCEPTED"
			body.SelfLink = computeSelfLink(key + "/resizeRequests/" + body.Name)
			computeResizeRequests.Put(computeManagedInstanceKey(key, body.Name), body)
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "insert"))
		})
		srv.HandleFunc("GET "+base+"/{name}/resizeRequests", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			items := computeResizeRequests.Filter(func(q ComputeInstanceGroupResizeRequest) bool {
				return q.Group == key
			})
			sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
			if items == nil {
				items = []ComputeInstanceGroupResizeRequest{}
			}
			sim.WriteJSON(w, http.StatusOK, map[string]any{
				"kind": "compute#instanceGroupManagerResizeRequestList", "items": items,
			})
		})
		srv.HandleFunc("GET "+base+"/{name}/resizeRequests/{resizeRequest}", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := load(w, r); !ok {
				return
			}
			request, held := computeResizeRequests.Get(requestKey(r))
			if !held {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"resize request %q not found", sim.PathParam(r, "resizeRequest"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, request)
		})
		srv.HandleFunc("DELETE "+base+"/{name}/resizeRequests/{resizeRequest}", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			if !computeResizeRequests.Delete(requestKey(r)) {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"resize request %q not found", sim.PathParam(r, "resizeRequest"))
				return
			}
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "delete"))
		})
		srv.HandleFunc("POST "+base+"/{name}/resizeRequests/{resizeRequest}/cancel", func(w http.ResponseWriter, r *http.Request) {
			key, ok := load(w, r)
			if !ok {
				return
			}
			request, held := computeResizeRequests.Get(requestKey(r))
			if !held {
				sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
					"resize request %q not found", sim.PathParam(r, "resizeRequest"))
				return
			}
			if request.State == "CANCELLED" {
				sim.GCPErrorf(w, http.StatusBadRequest, "FAILED_PRECONDITION",
					"resize request %q is already cancelled", request.Name)
				return
			}
			request.State = "CANCELLED"
			computeResizeRequests.Put(requestKey(r), request)
			sim.WriteJSON(w, http.StatusOK, operation(r, key, "cancel"))
		})
	}
}

// computeListManagedInstances reports the instances a group actually manages,
// which is what the lifecycle verbs above have left it holding.
// computeReconcileManagedInstances brings the instances a managed group holds
// to the size it is set to: growing creates them, shrinking removes the ones
// added last, and deleting the manager removes them all. Compute Engine's
// managed group does this itself, so a target size the group does not actually
// manage is a state the real service never reports — listManagedInstances, the
// group's size and its members all read the same reconciled set.
func computeReconcileManagedInstances(managers sim.Store[map[string]any], key string) {
	held := computeManagedInstances.Filter(func(i ComputeManagedInstance) bool { return i.Group == key })
	sort.Slice(held, func(i, j int) bool { return held[i].Name < held[j].Name })

	manager, ok := managers.Get(key)
	if !ok {
		// The manager is gone, and so are the instances it managed.
		for _, instance := range held {
			computeManagedInstances.Delete(computeManagedInstanceKey(key, instance.Name))
			computePerInstanceConfigs.Delete(computeManagedInstanceKey(key, instance.Name))
		}
		return
	}

	target := computeStoredInt(manager["targetSize"])
	if target < 0 {
		target = 0
	}

	for len(held) > target {
		last := held[len(held)-1]
		computeManagedInstances.Delete(computeManagedInstanceKey(key, last.Name))
		computePerInstanceConfigs.Delete(computeManagedInstanceKey(key, last.Name))
		held = held[:len(held)-1]
	}
	if len(held) >= target {
		return
	}

	base, _ := manager["baseInstanceName"].(string)
	if base == "" {
		base, _ = manager["name"].(string)
	}
	project, scope := computeManagerScope(key)
	zones := computeManagerZones(manager, scope)
	taken := map[string]bool{}
	for _, instance := range held {
		taken[instance.Name] = true
	}
	for i := len(held); i < target; i++ {
		name := fmt.Sprintf("%s-%s", base, computeInstanceSuffix())
		for taken[name] {
			name = fmt.Sprintf("%s-%s", base, computeInstanceSuffix())
		}
		taken[name] = true
		zone := zones[i%len(zones)]
		computeManagedInstances.Put(computeManagedInstanceKey(key, name), ComputeManagedInstance{
			Group:          key,
			Name:           name,
			Instance:       computeInstanceURL(project, zone, name),
			CurrentAction:  "NONE",
			InstanceStatus: "RUNNING",
			ID:             computeNumericID(),
		})
	}
}

// computeStoredInt reads a number a client sent. A store that round-trips
// through JSON hands back a float64 and one that holds the value in memory
// hands back what was parsed, so a reader that knows only one of those shapes
// silently reads zero.
func computeStoredInt(value any) int {
	switch n := value.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		parsed, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(parsed)
	}
	return 0
}

// computeManagerScope splits a manager's key into the project it belongs to and
// the scope segment it is managed at ("zones/us-central1-a", "regions/us-central1").
func computeManagerScope(key string) (project, scope string) {
	parts := strings.Split(key, "/")
	if len(parts) < 4 {
		return "", ""
	}
	return parts[1], parts[2] + "/" + parts[3]
}

// computeManagerZones is where a manager places the instances it creates: the
// zone it is in for a zonal manager, and the zones of its distribution policy
// for a regional one — which defaults to the region's first zone when the
// policy names none.
func computeManagerZones(manager map[string]any, scope string) []string {
	kind, name, _ := strings.Cut(scope, "/")
	if kind == "zones" {
		return []string{name}
	}
	var zones []string
	if policy, ok := manager["distributionPolicy"].(map[string]any); ok {
		if declared, ok := policy["zones"].([]any); ok {
			for _, entry := range declared {
				held, ok := entry.(map[string]any)
				if !ok {
					continue
				}
				zone, _ := held["zone"].(string)
				if i := strings.LastIndex(zone, "/"); i >= 0 {
					zone = zone[i+1:]
				}
				if zone != "" {
					zones = append(zones, zone)
				}
			}
		}
	}
	if len(zones) == 0 {
		zones = []string{name + "-a"}
	}
	return zones
}

// computeInstanceSuffix is the four-character tail Compute Engine appends to a
// managed group's base instance name.
func computeInstanceSuffix() string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	drawn := make([]byte, 4)
	_, _ = rand.Read(drawn)
	suffix := make([]byte, len(drawn))
	for i, b := range drawn {
		suffix[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(suffix)
}

func computeListManagedInstances(w http.ResponseWriter, group string) {
	items := computeManagedInstances.Filter(func(i ComputeManagedInstance) bool { return i.Group == group })
	sort.Slice(items, func(i, j int) bool { return items[i].Name < items[j].Name })
	if items == nil {
		items = []ComputeManagedInstance{}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"managedInstances": items})
}
