package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Cloud Run v2 Worker Pools slice. Worker pools are the v2 resource for
// pull-based (non-request-serving) workloads — the SDK uses
// run.NewWorkerPoolsRESTClient and the v2 REST surface
// `/v2/projects/{project}/locations/{location}/workerPools`. terraform's
// `google_cloud_run_v2_worker_pool` and `gcloud run worker-pools` wrap the
// same endpoints.
//
// Real API: https://cloud.google.com/run/docs/reference/rest/v2/projects.locations.workerPools

// WorkerPoolV2 mirrors google.cloud.run.v2.WorkerPool (proto-JSON shape).
// Generation is encoded as a JSON string per proto-JSON int64 rules.
type WorkerPoolV2 struct {
	Name                  string                      `json:"name"`
	UID                   string                      `json:"uid,omitempty"`
	Generation            int64                       `json:"generation,string,omitempty"`
	Labels                map[string]string           `json:"labels,omitempty"`
	Annotations           map[string]string           `json:"annotations,omitempty"`
	Description           string                      `json:"description,omitempty"`
	CreateTime            string                      `json:"createTime,omitempty"`
	UpdateTime            string                      `json:"updateTime,omitempty"`
	LaunchStage           enumString                  `json:"launchStage,omitempty"`
	Client                string                      `json:"client,omitempty"`
	ClientVersion         string                      `json:"clientVersion,omitempty"`
	CustomAudiences       []string                    `json:"customAudiences,omitempty"`
	BinaryAuthorization   *BinaryAuthorization        `json:"binaryAuthorization,omitempty"`
	Template              *WorkerPoolRevisionTemplate `json:"template,omitempty"`
	Scaling               *WorkerPoolScaling          `json:"scaling,omitempty"`
	InstanceSplits        []InstanceSplit             `json:"instanceSplits,omitempty"`
	InstanceSplitStatuses []InstanceSplit             `json:"instanceSplitStatuses,omitempty"`
	TerminalCondition     *Condition                  `json:"terminalCondition,omitempty"`
	Conditions            []Condition                 `json:"conditions,omitempty"`
	LatestReadyRevision   string                      `json:"latestReadyRevision,omitempty"`
	LatestCreatedRevision string                      `json:"latestCreatedRevision,omitempty"`
	ObservedGeneration    int64                       `json:"observedGeneration,string,omitempty"`
	Etag                  string                      `json:"etag,omitempty"`
	Reconciling           bool                        `json:"reconciling,omitempty"`
}

// WorkerPoolRevisionTemplate mirrors
// google.cloud.run.v2.WorkerPoolRevisionTemplate — the per-revision spec
// for a worker pool. Unlike a Service's RevisionTemplate there is no
// per-revision scaling (worker-pool scaling is manual, at the pool level).
type WorkerPoolRevisionTemplate struct {
	Labels                        map[string]string `json:"labels,omitempty"`
	Annotations                   map[string]string `json:"annotations,omitempty"`
	Revision                      string            `json:"revision,omitempty"`
	Containers                    []Container       `json:"containers,omitempty"`
	Volumes                       []Volume          `json:"volumes,omitempty"`
	VpcAccess                     *VpcAccess        `json:"vpcAccess,omitempty"`
	ServiceAccount                string            `json:"serviceAccount,omitempty"`
	NodeSelector                  *NodeSelector     `json:"nodeSelector,omitempty"`
	ServiceMesh                   *ServiceMesh      `json:"serviceMesh,omitempty"`
	Client                        string            `json:"client,omitempty"`
	ClientVersion                 string            `json:"clientVersion,omitempty"`
	EncryptionKey                 string            `json:"encryptionKey,omitempty"`
	EncryptionKeyRevocationAction enumString        `json:"encryptionKeyRevocationAction,omitempty"`
	EncryptionKeyShutdownDuration string            `json:"encryptionKeyShutdownDuration,omitempty"`
	GpuZonalRedundancyDisabled    bool              `json:"gpuZonalRedundancyDisabled,omitempty"`
}

// NodeSelector mirrors google.cloud.run.v2.NodeSelector — the GPU
// accelerator selector a worker-pool revision can request.
type NodeSelector struct {
	Accelerator string `json:"accelerator,omitempty"`
}

// WorkerPoolScaling mirrors google.cloud.run.v2.WorkerPoolScaling — how many
// instances a worker pool runs, either pinned (MANUAL) or autoscaled between a
// floor and a ceiling (AUTOMATIC).
//
// The pinned Cloud Run Discovery document publishes only manualInstanceCount:
// Google has not yet released the automatic-scaling members into the public
// Discovery/REST reference, even though the API serves them and the official
// google Terraform provider sends and reads them back as scaling.scalingMode,
// scaling.minInstanceCount and scaling.maxInstanceCount. Dropping them would
// make every worker pool configured for automatic scaling drift on the next
// plan, so they are modelled from the wire the official clients speak. See
// specs/SIM_SURFACE_TABLES/gcp-cloudrun.md for the revision mismatch.
type WorkerPoolScaling struct {
	ScalingMode         enumString `json:"scalingMode,omitempty"`
	MinInstanceCount    int32      `json:"minInstanceCount,omitempty"`
	MaxInstanceCount    int32      `json:"maxInstanceCount,omitempty"`
	ManualInstanceCount int32      `json:"manualInstanceCount,omitempty"`
}

// InstanceSplit mirrors google.cloud.run.v2.InstanceSplit (and the
// identical InstanceSplitStatus). Worker pools split instances across
// revisions much as a Service splits request traffic.
type InstanceSplit struct {
	Type     string `json:"type,omitempty"`
	Revision string `json:"revision,omitempty"`
	Percent  int32  `json:"percent,omitempty"`
}

// crv2WorkerPools and crv2WorkerPoolRevisions are the Cloud Run worker-pool
// stores. A worker pool is one resource addressable through two API versions —
// the v2 resource-oriented collection and the v1 Knative `workerpools`
// collection — so the stores are package-scoped rather than closed over by the
// v2 handlers alone.
var (
	crv2WorkerPools         sim.Store[WorkerPoolV2]
	crv2WorkerPoolRevisions sim.Store[RevisionV2]
)

func registerCloudRunWorkerPoolsV2(srv *sim.Server) {
	pools := sim.MakeStore[WorkerPoolV2](srv.DB(), "crv2_workerpools")
	revisions := sim.MakeStore[RevisionV2](srv.DB(), "crv2_workerpool_revisions")
	crv2WorkerPools, crv2WorkerPoolRevisions = pools, revisions
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}

	const wpType = "type.googleapis.com/google.cloud.run.v2.WorkerPool"

	// CreateWorkerPool: POST .../workerPools?workerPoolId=<id>
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/workerPools", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolID := r.URL.Query().Get("workerPoolId")
		if poolID == "" {
			GCPError(w, http.StatusBadRequest, "workerPoolId query parameter is required", "INVALID_ARGUMENT")
			return
		}
		var pool WorkerPoolV2
		if err := sim.ReadJSON(r, &pool); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, poolID)
		if _, exists := pools.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "worker pool %q already exists", name)
			return
		}
		pool = seedWorkerPoolV2Defaults(pool, project, location, poolID)
		pool.Etag = generateUUID()
		pools.Put(name, pool)
		reconcileWorkerPoolRevision(revisions, name, poolID+"-00001-abc", pool)
		lro := newLRO(project, location, pool, wpType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// GetWorkerPool (the {workerPool} wildcard also carries the GET-side
	// `{workerPool}:getIamPolicy` verb).
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolParam := sim.PathParam(r, "workerPool")
		if id, action, found := strings.Cut(poolParam, ":"); found {
			if action == "getIamPolicy" {
				workerPoolIAM(w, r, pools, project, location, id, action)
				return
			}
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on worker pool %q", action, id)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, poolParam)
		pool, ok := pools.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "worker pool %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, pool)
	})

	// ListWorkerPools
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/workerPools", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/workerPools/", project, location)
		result := pools.Filter(func(p WorkerPoolV2) bool { return strings.HasPrefix(p.Name, prefix) })
		if result == nil {
			result = []WorkerPoolV2{}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		resp := map[string]any{"workerPools": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// UpdateWorkerPool (PATCH, honoring updateMask).
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/workerPools/{workerPool}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolID := sim.PathParam(r, "workerPool")
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, poolID)
		existing, ok := pools.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "worker pool %q not found", name)
			return
		}
		var update WorkerPoolV2
		if err := sim.ReadJSON(r, &update); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		// The etag is read off the request as sent, before any mask merge
		// rewrites the body.
		if !cloudRunEtagOK(w, "worker pool", existing.Name, existing.Etag, update.Etag) {
			return
		}
		// A mask naming an unknown or output-only field is rejected with
		// 400 INVALID_ARGUMENT, matching real Cloud Run v2.
		if mask := r.URL.Query().Get("updateMask"); mask != "" {
			merged, err := applyWorkerPoolUpdateMask(existing, update, mask)
			if err != nil {
				GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "%v", err)
				return
			}
			update = merged
		}
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.Generation = existing.Generation + 1
		update.UpdateTime = nowTimestamp()
		if update.LaunchStage == "" {
			update.LaunchStage = existing.LaunchStage
		}
		update.TerminalCondition = &Condition{
			Type:               "Ready",
			State:              "CONDITION_SUCCEEDED",
			LastTransitionTime: update.UpdateTime,
		}
		revName := fmt.Sprintf("%s-%05d-abc", poolID, update.Generation)
		update.LatestCreatedRevision = fmt.Sprintf("%s/revisions/%s", name, revName)
		update.LatestReadyRevision = update.LatestCreatedRevision
		update.Etag = generateUUID()
		pools.Put(name, update)
		reconcileWorkerPoolRevision(revisions, name, revName, update)
		lro := newLRO(project, location, update, wpType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// DeleteWorkerPool
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolID := sim.PathParam(r, "workerPool")
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, poolID)
		pool, ok := pools.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "worker pool %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "worker pool", pool.Name, pool.Etag, r.URL.Query().Get("etag")) {
			return
		}
		pools.Delete(name)
		revPrefix := name + "/revisions/"
		for _, rev := range revisions.Filter(func(rv RevisionV2) bool { return strings.HasPrefix(rv.Name, revPrefix) }) {
			revisions.Delete(rev.Name)
		}
		lro := newLRO(project, location, pool, wpType)
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolID := sim.PathParam(r, "workerPool")
		revisionID := sim.PathParam(r, "revision")
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s/revisions/%s", project, location, poolID, revisionID)
		rev, ok := revisions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "revision %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, rev)
	})
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolID := sim.PathParam(r, "workerPool")
		prefix := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s/revisions/", project, location, poolID)
		result := revisions.Filter(func(rev RevisionV2) bool { return strings.HasPrefix(rev.Name, prefix) })
		if result == nil {
			result = []RevisionV2{}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		resp := map[string]any{"revisions": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/workerPools/{workerPool}/revisions/{revision}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolID := sim.PathParam(r, "workerPool")
		revisionID := sim.PathParam(r, "revision")
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s/revisions/%s", project, location, poolID, revisionID)
		rev, ok := revisions.Get(name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "revision %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "revision", rev.Name, rev.Etag, r.URL.Query().Get("etag")) {
			return
		}
		revisions.Delete(name)
		lro := newLRO(project, location, rev, "type.googleapis.com/google.cloud.run.v2.Revision")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// getIamPolicy rides the GET handler's colon-split above.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/workerPools/{workerPoolAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		poolAction := sim.PathParam(r, "workerPoolAction")
		id, action, found := strings.Cut(poolAction, ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on worker pool %q", poolAction)
			return
		}
		switch action {
		case "setIamPolicy", "testIamPermissions":
			workerPoolIAM(w, r, pools, project, location, id, action)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on worker pool %q", action, id)
		}
	})

	// The Cloud Run Admin v1 API exposes the same worker pool resource's IAM
	// policy under a lowercase `workerpools` collection
	// (run.projects.locations.workerpools.{getIamPolicy,setIamPolicy,
	// testIamPermissions}). It is the surface the gcloud CLI's
	// `gcloud run worker-pools {get,set}-iam-policy` and
	// `{add,remove}-iam-policy-binding` commands call. Both API versions
	// address one resource, so the policy is stored under the v2 resource
	// name and a policy written through either version reads back through
	// the other.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/workerpools/{workerPool}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		id, action, found := strings.Cut(sim.PathParam(r, "workerPool"), ":")
		if !found || action != "getIamPolicy" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on worker pool %q", action, id)
			return
		}
		workerPoolIAM(w, r, pools, project, location, id, action)
	})
	srv.HandleFunc("POST /v1/projects/{project}/locations/{location}/workerpools/{workerPoolAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		id, action, found := strings.Cut(sim.PathParam(r, "workerPoolAction"), ":")
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on worker pool %q", id)
			return
		}
		switch action {
		case "setIamPolicy", "testIamPermissions":
			workerPoolIAM(w, r, pools, project, location, id, action)
		default:
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on worker pool %q", action, id)
		}
	})
}

// workerPoolIAM serves an AIP-141 IAM verb against a worker pool. The policy
// is keyed on the v2 resource name so the v1 (`workerpools`) and v2
// (`workerPools`) collections address one resource's single policy. An IAM
// verb on a worker pool that does not exist is NOT_FOUND, as on a real Cloud
// Run project — never an empty policy for a name that was never deployed.
func workerPoolIAM(w http.ResponseWriter, r *http.Request, pools sim.Store[WorkerPoolV2], project, location, id, action string) {
	name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, id)
	if _, ok := pools.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "worker pool %q not found", name)
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), name, action)
}

func seedWorkerPoolV2Defaults(pool WorkerPoolV2, project, location, poolID string) WorkerPoolV2 {
	now := nowTimestamp()
	pool.Name = fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", project, location, poolID)
	pool.UID = generateUUID()
	pool.Generation = 1
	pool.ObservedGeneration = 1
	pool.CreateTime = now
	pool.UpdateTime = now
	if pool.LaunchStage == "" {
		pool.LaunchStage = "GA"
	}
	pool.TerminalCondition = &Condition{
		Type:               "Ready",
		State:              "CONDITION_SUCCEEDED",
		LastTransitionTime: now,
	}
	pool.Conditions = []Condition{
		{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
	}
	revName := fmt.Sprintf("%s-00001-abc", poolID)
	pool.LatestReadyRevision = fmt.Sprintf("%s/revisions/%s", pool.Name, revName)
	pool.LatestCreatedRevision = pool.LatestReadyRevision
	pool.InstanceSplitStatuses = []InstanceSplit{
		{Type: "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST", Percent: 100, Revision: revName},
	}
	return pool
}

// reconcileWorkerPoolRevision materializes the immutable Revision a worker
// pool deploy produces, mirroring reconcileServiceRevision for services.
func reconcileWorkerPoolRevision(store sim.Store[RevisionV2], poolName, revName string, pool WorkerPoolV2) {
	now := nowTimestamp()
	full := poolName + "/revisions/" + revName
	rev := RevisionV2{
		Name:        full,
		UID:         generateUUID(),
		Generation:  pool.Generation,
		CreateTime:  now,
		UpdateTime:  now,
		LaunchStage: pool.LaunchStage,
		Conditions: []Condition{
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
		},
		Etag: generateUUID(),
	}
	if pool.Template != nil {
		rev.Labels = pool.Template.Labels
		rev.Annotations = pool.Template.Annotations
		rev.Containers = pool.Template.Containers
		rev.Volumes = pool.Template.Volumes
		rev.VpcAccess = pool.Template.VpcAccess
	}
	store.Put(full, rev)
}
