package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Run v2 Instances slice. An Instance is a single long-lived Cloud
// Run instance (the v2 resource the SDK reaches via
// run.NewInstancesRESTClient and `/v2/projects/{project}/locations/{location}/instances`).
//
// Real API: https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.instances

// InstanceV2 mirrors google.cloud.run.v2.Instance (proto-JSON shape).
type InstanceV2 struct {
	Name                          string               `json:"name"`
	UID                           string               `json:"uid,omitempty"`
	Generation                    int64                `json:"generation,string,omitempty"`
	Labels                        map[string]string    `json:"labels,omitempty"`
	Annotations                   map[string]string    `json:"annotations,omitempty"`
	Description                   string               `json:"description,omitempty"`
	CreateTime                    string               `json:"createTime,omitempty"`
	UpdateTime                    string               `json:"updateTime,omitempty"`
	LaunchStage                   enumString           `json:"launchStage,omitempty"`
	Ingress                       ingressString        `json:"ingress,omitempty"`
	DefaultUriDisabled            bool                 `json:"defaultUriDisabled,omitempty"`
	Containers                    []Container          `json:"containers,omitempty"`
	Volumes                       []Volume             `json:"volumes,omitempty"`
	VpcAccess                     *VpcAccess           `json:"vpcAccess,omitempty"`
	ServiceAccount                string               `json:"serviceAccount,omitempty"`
	NodeSelector                  *NodeSelector        `json:"nodeSelector,omitempty"`
	RestartPolicy                 enumString           `json:"restartPolicy,omitempty"`
	Client                        string               `json:"client,omitempty"`
	ClientVersion                 string               `json:"clientVersion,omitempty"`
	BinaryAuthorization           *BinaryAuthorization `json:"binaryAuthorization,omitempty"`
	EncryptionKey                 string               `json:"encryptionKey,omitempty"`
	EncryptionKeyRevocationAction enumString           `json:"encryptionKeyRevocationAction,omitempty"`
	EncryptionKeyShutdownDuration string               `json:"encryptionKeyShutdownDuration,omitempty"`
	GpuZonalRedundancyDisabled    bool                 `json:"gpuZonalRedundancyDisabled,omitempty"`
	IapEnabled                    bool                 `json:"iapEnabled,omitempty"`
	InvokerIamDisabled            bool                 `json:"invokerIamDisabled,omitempty"`
	URLs                          []string             `json:"urls,omitempty"`
	TerminalCondition             *Condition           `json:"terminalCondition,omitempty"`
	Conditions                    []Condition          `json:"conditions,omitempty"`
	ObservedGeneration            int64                `json:"observedGeneration,string,omitempty"`
	Etag                          string               `json:"etag,omitempty"`
	Reconciling                   bool                 `json:"reconciling,omitempty"`
}

// InstanceLifecycleRequest is the body of instances.start and instances.stop.
// google.cloud.run.v2.StartInstanceRequest and StopInstanceRequest declare the
// identical two members, so one shape decodes both.
type InstanceLifecycleRequest struct {
	Etag         string `json:"etag,omitempty"`
	ValidateOnly bool   `json:"validateOnly,omitempty"`
}

// crv2Instances is the Cloud Run instances store. The Cloud Run Admin v1 IAM
// aliases address the same resource under a different API version and reach it
// from the shared `/v1/projects/{p}/locations/{l}/instances/…` prefix, so the
// store is package-scoped rather than closed over by the v2 handlers alone.
var crv2Instances sim.Store[InstanceV2]

func registerCloudRunInstancesV2(srv *sim.Server) {
	instances := sim.MakeStore[InstanceV2](srv.DB(), "crv2_instances")
	crv2Instances = instances
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	const instType = "type.googleapis.com/google.cloud.run.v2.Instance"

	// CreateInstance: POST .../instances?instanceId=<id>
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		instanceID := r.URL.Query().Get("instanceId")
		if instanceID == "" {
			GCPError(w, http.StatusBadRequest, "instanceId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var inst InstanceV2
		if err := sim.ReadJSON(r, &inst); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, instanceID)
		if _, exists := instances.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "instance %q already exists", name)
			return
		}
		inst = seedInstanceV2Defaults(inst, r.Host, project, location, instanceID)
		inst.Etag = generateUUID()
		instances.Put(name, inst)
		lro := newLRO(project, location, inst, instType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// GetInstance (the {instance} wildcard also carries the GET-side
	// `{instance}:getIamPolicy` verb).
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/instances/{instance}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		instParam := sim.PathParam(r, "instance")
		if id, action, found := strings.Cut(instParam, ":"); found {
			if action == "getIamPolicy" {
				instanceIAM(w, r, instances, project, location, id, action)
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on instance %q", action, id)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, instParam)
		inst, ok := instances.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, inst)
	})

	// ListInstances
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/instances", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/instances/", project, location)
		result := instances.Filter(func(i InstanceV2) bool { return strings.HasPrefix(i.Name, prefix) })
		if result == nil {
			result = []InstanceV2{}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		resp := map[string]any{"instances": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// UpdateInstance (PATCH, honoring updateMask).
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/instances/{instance}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		instanceID := sim.PathParam(r, "instance")
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, instanceID)
		existing, ok := instances.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
			return
		}
		var update InstanceV2
		if err := sim.ReadJSON(r, &update); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// The etag is read off the request as sent, before any mask merge
		// rewrites the body.
		if !cloudRunEtagOK(w, "instance", existing.Name, existing.Etag, update.Etag) {
			return
		}
		// A mask naming an unknown or output-only field is rejected with
		// 400 INVALID_ARGUMENT, matching real Cloud Run v2.
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			merged, err := applyInstanceUpdateMask(existing, update, mask)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			update = merged
		}
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.URLs = existing.URLs
		update.Generation = existing.Generation + 1
		update.ObservedGeneration = update.Generation
		update.UpdateTime = nowTimestamp()
		if update.LaunchStage == "" {
			update.LaunchStage = existing.LaunchStage
		}
		update.TerminalCondition = &Condition{
			Type:               "Ready",
			State:              "CONDITION_SUCCEEDED",
			LastTransitionTime: update.UpdateTime,
		}
		update.Conditions = []Condition{
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
		}
		update.Etag = generateUUID()
		instances.Put(name, update)
		lro := newLRO(project, location, update, instType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// DeleteInstance
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/instances/{instance}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		instanceID := sim.PathParam(r, "instance")
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, instanceID)
		inst, ok := instances.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "instance", inst.Name, inst.Etag, r.URL.Query().Get("etag")) {
			return
		}
		instances.Delete(name)
		lro := newLRO(project, location, inst, instType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// StartInstance / StopInstance / setIamPolicy / testIamPermissions all
	// arrive as POST .../instances/{instance}:<verb>.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/instances/{instanceAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		instanceAction := sim.PathParam(r, "instanceAction")
		id, action, found := strings.Cut(instanceAction, ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on instance %q", instanceAction)
			return
		}
		switch action {
		case "setIamPolicy", "testIamPermissions":
			instanceIAM(w, r, instances, project, location, id, action)
			return
		case "start", "stop":
			name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, id)
			// StartInstanceRequest and StopInstanceRequest carry the same two
			// members, so one shape decodes both.
			var request InstanceLifecycleRequest
			if err := sim.ReadJSON(r, &request); err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
				return
			}
			existing, ok := instances.Get(name)
			if !ok {
				GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
				return
			}
			if !cloudRunEtagOK(w, "instance", existing.Name, existing.Etag, request.Etag) {
				return
			}
			if request.ValidateOnly {
				// The request validated and nothing changed, so the operation
				// carries no resource.
				lro := newLRO(project, location, nil, instType)
				sim.WriteJSON(w, http.StatusOK, lro)
				return
			}
			now := nowTimestamp()
			state := enumString("CONDITION_SUCCEEDED")
			reason := ""
			if action == "stop" {
				state = "CONDITION_PENDING"
				reason = "Stopped"
			}
			instances.Update(name, func(i *InstanceV2) {
				i.UpdateTime = now
				i.TerminalCondition = &Condition{Type: "Ready", State: state, LastTransitionTime: now, Reason: reason}
				i.Etag = generateUUID()
			})
			inst, _ := instances.Get(name)
			lro := newLRO(project, location, inst, instType)
			sim.WriteJSON(w, http.StatusOK, lro)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on instance %q", action, id)
		}
	})
}

// cloudRunAdminV1InstanceIAM serves the Cloud Run Admin v1 instances IAM
// aliases (run.projects.locations.instances.{get,set,test}IamPolicy). They ride
// the `/v1/projects/{p}/locations/{l}/instances/{id}:{verb}` path that
// Memorystore for Redis also publishes, so the shared prefix resolves the
// owning service first (see endpoint_hosts.go). Both API versions address one
// resource, so the policy is stored under the v2 resource name and a policy
// written through either version reads back through the other — the same rule
// the worker-pool v1/v2 aliases follow.
func cloudRunAdminV1InstanceIAM(w http.ResponseWriter, r *http.Request, id, action string) {
	if !cloudRunAdminV1InstanceIAMVerbs[r.Method][action] {
		// The IAM triple is the whole of the Cloud Run Admin v1 instances
		// collection: anything else on that path is a method run.googleapis.com
		// does not publish.
		GCPError(w, http.StatusNotFound, "Method not found.", "NOT_FOUND")
		return
	}
	instanceIAM(w, r, crv2Instances, sim.PathParam(r, "project"), sim.PathParam(r, "location"), id, action)
}

// instanceIAM serves an AIP-141 IAM verb against a Cloud Run instance. An IAM
// verb on an instance that does not exist is NOT_FOUND, as on a real Cloud Run
// project — never an empty policy for a name that was never created.
func instanceIAM(w http.ResponseWriter, r *http.Request, instances sim.Store[InstanceV2], project, location, id, action string) {
	name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, id)
	if _, ok := instances.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "instance %q not found", name)
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), name, action)
}

func seedInstanceV2Defaults(inst InstanceV2, host, project, location, instanceID string) InstanceV2 {
	now := nowTimestamp()
	inst.Name = fmt.Sprintf("projects/%s/locations/%s/instances/%s", project, location, instanceID)
	inst.UID = generateUUID()
	inst.Generation = 1
	inst.ObservedGeneration = 1
	inst.CreateTime = now
	inst.UpdateTime = now
	if inst.LaunchStage == "" {
		inst.LaunchStage = "GA"
	}
	inst.TerminalCondition = &Condition{
		Type:               "Ready",
		State:              "CONDITION_SUCCEEDED",
		LastTransitionTime: now,
	}
	inst.Conditions = []Condition{
		{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
	}
	if !inst.DefaultUriDisabled {
		inst.URLs = []string{fmt.Sprintf("http://%s/v2-services-invoke/%s/%s/%s", host, project, location, instanceID)}
	}
	return inst
}
