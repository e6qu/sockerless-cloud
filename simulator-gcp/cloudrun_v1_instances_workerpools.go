package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Run Admin v1 (Knative) surface for instances and worker pools.
//
// Both collections address the SAME resources the v2 handlers in
// cloudruninstances.go and cloudrunworkerpools.go own — the records in
// crv2Instances, crv2WorkerPools and crv2WorkerPoolRevisions — rendered in the
// Knative shape. There is no v1 store: an instance deployed through either API
// version is visible, mutable and deletable through the other.
//
// Real API:
//   https://cloud.google.com/run/docs/reference/rest/v1/namespaces.instances
//   https://cloud.google.com/run/docs/reference/rest/v1/namespaces.workerpools

// CRInstance is the Knative rendering of a Cloud Run instance.
type CRInstance struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   CRServiceMetadata `json:"metadata"`
	Spec       *CRInstanceSpec   `json:"spec,omitempty"`
	Status     *CRInstanceStatus `json:"status,omitempty"`
}

// CRInstanceSpec mirrors the Discovery InstanceSpec schema.
type CRInstanceSpec struct {
	Containers         []CRContainer     `json:"containers,omitempty"`
	Volumes            []CRVolume        `json:"volumes,omitempty"`
	NodeSelector       map[string]string `json:"nodeSelector,omitempty"`
	RestartPolicy      string            `json:"restartPolicy,omitempty"`
	ServiceAccountName string            `json:"serviceAccountName,omitempty"`
}

// CRInstanceStatus mirrors the Discovery InstanceStatus schema.
type CRInstanceStatus struct {
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
	URLs               []string      `json:"urls,omitempty"`
	Conditions         []CRCondition `json:"conditions,omitempty"`
}

// CRInstanceList mirrors the Discovery ListInstancesResponse schema.
type CRInstanceList struct {
	APIVersion  string       `json:"apiVersion"`
	Kind        string       `json:"kind"`
	Metadata    *CRListMeta  `json:"metadata,omitempty"`
	Items       []CRInstance `json:"items"`
	Unreachable []string     `json:"unreachable,omitempty"`
}

// CRWorkerPool is the Knative rendering of a Cloud Run worker pool.
type CRWorkerPool struct {
	APIVersion string              `json:"apiVersion"`
	Kind       string              `json:"kind"`
	Metadata   CRServiceMetadata   `json:"metadata"`
	Spec       *CRWorkerPoolSpec   `json:"spec,omitempty"`
	Status     *CRWorkerPoolStatus `json:"status,omitempty"`
}

// CRWorkerPoolSpec mirrors the Discovery WorkerPoolSpec schema.
type CRWorkerPoolSpec struct {
	Template       *CRServiceTemplate `json:"template,omitempty"`
	InstanceSplits []CRInstanceSplit  `json:"instanceSplits,omitempty"`
}

// CRWorkerPoolStatus mirrors the Discovery WorkerPoolStatus schema.
type CRWorkerPoolStatus struct {
	ObservedGeneration        int64             `json:"observedGeneration,omitempty"`
	LatestReadyRevisionName   string            `json:"latestReadyRevisionName,omitempty"`
	LatestCreatedRevisionName string            `json:"latestCreatedRevisionName,omitempty"`
	InstanceSplits            []CRInstanceSplit `json:"instanceSplits,omitempty"`
	Conditions                []CRCondition     `json:"conditions,omitempty"`
}

// CRInstanceSplit mirrors the Discovery InstanceSplit schema. Knative names
// the latest revision with a boolean where v2 carries an allocation-type enum.
type CRInstanceSplit struct {
	RevisionName   string `json:"revisionName,omitempty"`
	Percent        int32  `json:"percent,omitempty"`
	LatestRevision bool   `json:"latestRevision,omitempty"`
}

// CRWorkerPoolList mirrors the Discovery ListWorkerPoolsResponse schema.
type CRWorkerPoolList struct {
	APIVersion  string         `json:"apiVersion"`
	Kind        string         `json:"kind"`
	Metadata    *CRListMeta    `json:"metadata,omitempty"`
	Items       []CRWorkerPool `json:"items"`
	Unreachable []string       `json:"unreachable,omitempty"`
}

// cloudRunAcceleratorNodeSelectorKey is the nodeSelector key Cloud Run's v1 API
// carries the GPU accelerator under; v2 models the same choice as the typed
// NodeSelector.accelerator member.
const cloudRunAcceleratorNodeSelectorKey = "run.googleapis.com/accelerator"

func cloudRunV2NodeSelectorToV1(selector *NodeSelector) map[string]string {
	if selector == nil || selector.Accelerator == "" {
		return nil
	}
	return map[string]string{cloudRunAcceleratorNodeSelectorKey: selector.Accelerator}
}

func cloudRunV1NodeSelectorToV2(selector map[string]string) *NodeSelector {
	if accelerator := selector[cloudRunAcceleratorNodeSelectorKey]; accelerator != "" {
		return &NodeSelector{Accelerator: accelerator}
	}
	return nil
}

func cloudRunV2ContainersToV1(containers []Container) []CRContainer {
	if len(containers) == 0 {
		return nil
	}
	out := make([]CRContainer, 0, len(containers))
	for _, container := range containers {
		out = append(out, cloudRunV2ContainerToV1(container))
	}
	return out
}

func cloudRunV1ContainersToV2(containers []CRContainer) []Container {
	if len(containers) == 0 {
		return nil
	}
	out := make([]Container, 0, len(containers))
	for _, container := range containers {
		out = append(out, cloudRunV1ContainerToV2(container))
	}
	return out
}

// cloudRunV2InstanceToV1 renders a v2 Instance in the Knative shape.
func cloudRunV2InstanceToV1(instance InstanceV2) (CRInstance, bool) {
	parts := strings.Split(instance.Name, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "instances" {
		return CRInstance{}, false
	}
	conditions := cloudRunV2ConditionsToV1(instance.Conditions)
	if instance.TerminalCondition != nil {
		conditions = append([]CRCondition{cloudRunV2ConditionToV1(*instance.TerminalCondition)}, conditions...)
	}
	return CRInstance{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "Instance",
		Metadata: CRServiceMetadata{
			Name:              parts[5],
			Namespace:         parts[1],
			UID:               instance.UID,
			Generation:        instance.Generation,
			ResourceVersion:   fmt.Sprintf("%d", instance.Generation),
			Labels:            instance.Labels,
			Annotations:       instance.Annotations,
			CreationTimestamp: instance.CreateTime,
		},
		Spec: &CRInstanceSpec{
			Containers:         cloudRunV2ContainersToV1(instance.Containers),
			Volumes:            cloudRunV2VolumesToV1(instance.Volumes),
			NodeSelector:       cloudRunV2NodeSelectorToV1(instance.NodeSelector),
			RestartPolicy:      string(instance.RestartPolicy),
			ServiceAccountName: instance.ServiceAccount,
		},
		Status: &CRInstanceStatus{
			ObservedGeneration: instance.ObservedGeneration,
			URLs:               instance.URLs,
			Conditions:         conditions,
		},
	}, true
}

// cloudRunV1InstanceToV2 folds a Knative Instance body onto the v2 resource the
// simulator stores. Server-owned members (name, uid, timestamps, conditions,
// urls) are seeded by the caller through seedInstanceV2Defaults, so only the
// client-settable spec crosses here.
func cloudRunV1InstanceToV2(instance CRInstance) InstanceV2 {
	converted := InstanceV2{
		Labels:      instance.Metadata.Labels,
		Annotations: instance.Metadata.Annotations,
	}
	if instance.Spec != nil {
		converted.Containers = cloudRunV1ContainersToV2(instance.Spec.Containers)
		converted.Volumes = cloudRunV1VolumesToV2(instance.Spec.Volumes)
		converted.NodeSelector = cloudRunV1NodeSelectorToV2(instance.Spec.NodeSelector)
		converted.RestartPolicy = enumString(instance.Spec.RestartPolicy)
		converted.ServiceAccount = instance.Spec.ServiceAccountName
	}
	return converted
}

func cloudRunV2InstanceSplitsToV1(splits []InstanceSplit) []CRInstanceSplit {
	if len(splits) == 0 {
		return nil
	}
	out := make([]CRInstanceSplit, 0, len(splits))
	for _, split := range splits {
		converted := CRInstanceSplit{Percent: split.Percent}
		if split.Type == "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST" {
			converted.LatestRevision = true
		} else {
			converted.RevisionName = split.Revision
		}
		out = append(out, converted)
	}
	return out
}

func cloudRunV1InstanceSplitsToV2(splits []CRInstanceSplit) []InstanceSplit {
	if len(splits) == 0 {
		return nil
	}
	out := make([]InstanceSplit, 0, len(splits))
	for _, split := range splits {
		converted := InstanceSplit{Percent: split.Percent}
		if split.LatestRevision {
			converted.Type = "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST"
		} else {
			converted.Type = "INSTANCE_SPLIT_ALLOCATION_TYPE_REVISION"
			converted.Revision = split.RevisionName
		}
		out = append(out, converted)
	}
	return out
}

// cloudRunRevisionID strips the collection prefix from a fully-qualified
// revision name; Knative status members carry the bare revision id.
func cloudRunRevisionID(name string) string {
	if name == "" {
		return ""
	}
	return name[strings.LastIndex(name, "/")+1:]
}

// cloudRunV2WorkerPoolToV1 renders a v2 WorkerPool in the Knative shape.
func cloudRunV2WorkerPoolToV1(pool WorkerPoolV2) (CRWorkerPool, bool) {
	parts := strings.Split(pool.Name, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "workerPools" {
		return CRWorkerPool{}, false
	}
	var template *CRServiceTemplate
	if pool.Template != nil {
		template = &CRServiceTemplate{
			Metadata: &CRServiceMetadata{
				Name:        pool.Template.Revision,
				Namespace:   parts[1],
				Labels:      pool.Template.Labels,
				Annotations: pool.Template.Annotations,
			},
			Spec: &CRTemplateSpec{
				Containers:         cloudRunV2ContainersToV1(pool.Template.Containers),
				Volumes:            cloudRunV2VolumesToV1(pool.Template.Volumes),
				NodeSelector:       cloudRunV2NodeSelectorToV1(pool.Template.NodeSelector),
				ServiceAccountName: pool.Template.ServiceAccount,
			},
		}
	}
	conditions := cloudRunV2ConditionsToV1(pool.Conditions)
	if pool.TerminalCondition != nil {
		conditions = append([]CRCondition{cloudRunV2ConditionToV1(*pool.TerminalCondition)}, conditions...)
	}
	return CRWorkerPool{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "WorkerPool",
		Metadata: CRServiceMetadata{
			Name:              parts[5],
			Namespace:         parts[1],
			UID:               pool.UID,
			Generation:        pool.Generation,
			ResourceVersion:   fmt.Sprintf("%d", pool.Generation),
			Labels:            pool.Labels,
			Annotations:       pool.Annotations,
			CreationTimestamp: pool.CreateTime,
		},
		Spec: &CRWorkerPoolSpec{
			Template:       template,
			InstanceSplits: cloudRunV2InstanceSplitsToV1(pool.InstanceSplits),
		},
		Status: &CRWorkerPoolStatus{
			ObservedGeneration:        pool.ObservedGeneration,
			LatestReadyRevisionName:   cloudRunRevisionID(pool.LatestReadyRevision),
			LatestCreatedRevisionName: cloudRunRevisionID(pool.LatestCreatedRevision),
			InstanceSplits:            cloudRunV2InstanceSplitsToV1(pool.InstanceSplitStatuses),
			Conditions:                conditions,
		},
	}, true
}

// cloudRunV1WorkerPoolToV2 folds a Knative WorkerPool body onto the v2
// resource; server-owned members are seeded by seedWorkerPoolV2Defaults.
func cloudRunV1WorkerPoolToV2(pool CRWorkerPool) WorkerPoolV2 {
	converted := WorkerPoolV2{
		Labels:      pool.Metadata.Labels,
		Annotations: pool.Metadata.Annotations,
	}
	if pool.Spec == nil {
		return converted
	}
	converted.InstanceSplits = cloudRunV1InstanceSplitsToV2(pool.Spec.InstanceSplits)
	if pool.Spec.Template == nil {
		return converted
	}
	template := &WorkerPoolRevisionTemplate{}
	if pool.Spec.Template.Metadata != nil {
		template.Revision = pool.Spec.Template.Metadata.Name
		template.Labels = pool.Spec.Template.Metadata.Labels
		template.Annotations = pool.Spec.Template.Metadata.Annotations
	}
	if pool.Spec.Template.Spec != nil {
		template.Containers = cloudRunV1ContainersToV2(pool.Spec.Template.Spec.Containers)
		template.Volumes = cloudRunV1VolumesToV2(pool.Spec.Template.Spec.Volumes)
		template.NodeSelector = cloudRunV1NodeSelectorToV2(pool.Spec.Template.Spec.NodeSelector)
		template.ServiceAccount = pool.Spec.Template.Spec.ServiceAccountName
	}
	converted.Template = template
	return converted
}

// knativeDryRun reports whether the request asked for a validate-only write.
// Cloud Run's Knative methods accept `dryRun=all`, meaning "validate the
// request and populate defaults without persisting it"; any other value is
// rejected rather than silently treated as a real write.
func knativeDryRun(w http.ResponseWriter, r *http.Request) (dryRun bool, ok bool) {
	switch value := r.URL.Query().Get("dryRun"); value {
	case "":
		return false, true
	case "all":
		return true, true
	default:
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT",
			"invalid dryRun %q: the only supported value is \"all\"", value)
		return false, false
	}
}

// knativeDeleteStatus is the Knative Status object a DELETE returns. The
// cloudrun-v1 Discovery Status schema declares no apiVersion/kind members.
func knativeDeleteStatus(w http.ResponseWriter) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"status": "Success"})
}

func registerCloudRunV1InstancesWorkerPools(srv *sim.Server) {
	// --- namespaces.instances ---

	srv.HandleFunc("POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		var body CRInstance
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid instance body: %v", err)
			return
		}
		if body.Metadata.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "metadata.name is required", "INVALID_ARGUMENT")
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", namespace, cloudRunDefaultLocation, body.Metadata.Name)
		if _, exists := crv2Instances.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"instance %q already exists in namespace %q", body.Metadata.Name, namespace)
			return
		}
		instance := seedInstanceV2Defaults(cloudRunV1InstanceToV2(body), r.Host, namespace, cloudRunDefaultLocation, body.Metadata.Name)
		instance.Etag = generateUUID()
		if !dryRun {
			crv2Instances.Put(name, instance)
		}
		writeCloudRunV1Instance(w, instance)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		instance, ok := crv2Instances.Get(fmt.Sprintf("projects/%s/locations/%s/instances/%s", namespace, cloudRunDefaultLocation, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"instance %q not found in namespace %q", id, namespace)
			return
		}
		writeCloudRunV1Instance(w, instance)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/instances", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		prefix := fmt.Sprintf("projects/%s/locations/%s/instances/", namespace, cloudRunDefaultLocation)
		selector := r.URL.Query().Get("labelSelector")
		items := make([]CRInstance, 0)
		for _, instance := range crv2Instances.Filter(func(i InstanceV2) bool { return strings.HasPrefix(i.Name, prefix) }) {
			projected, ok := cloudRunV2InstanceToV1(instance)
			if !ok || !knativeLabelSelectorMatches(selector, projected.Metadata.Labels) {
				continue
			}
			items = append(items, projected)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Metadata.Name < items[j].Metadata.Name })
		page, next, ok := knativeListPage(w, r, items)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRInstanceList{
			APIVersion: "run.googleapis.com/v1", Kind: "InstanceList",
			Metadata: knativeListMeta(next), Items: page,
		})
	})

	srv.HandleFunc("PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", namespace, cloudRunDefaultLocation, id)
		existing, found := crv2Instances.Get(name)
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"instance %q not found in namespace %q", id, namespace)
			return
		}
		var body CRInstance
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid instance body: %v", err)
			return
		}
		// ReplaceInstance replaces the whole mutable resource; identity and
		// server-owned members carry over from the stored record.
		update := cloudRunV1InstanceToV2(body)
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.URLs = existing.URLs
		update.LaunchStage = existing.LaunchStage
		update.Generation = existing.Generation + 1
		update.ObservedGeneration = update.Generation
		update.UpdateTime = nowTimestamp()
		update.TerminalCondition = &Condition{
			Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime,
		}
		update.Conditions = []Condition{
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
		}
		update.Etag = generateUUID()
		if !dryRun {
			crv2Instances.Put(name, update)
		}
		writeCloudRunV1Instance(w, update)
	})

	srv.HandleFunc("DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", namespace, cloudRunDefaultLocation, id)
		if _, found := crv2Instances.Get(name); !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"instance %q not found in namespace %q", id, namespace)
			return
		}
		if !dryRun {
			crv2Instances.Delete(name)
		}
		knativeDeleteStatus(w)
	})

	// StartInstance / StopInstance arrive as POST .../instances/{id}:{verb}.
	// They do exactly what the v2 collection's start/stop do — flip the
	// instance's terminal condition — and touch no container: Cloud Run's
	// instance lifecycle is not an execution surface on either API version,
	// and inventing one here would make the two versions disagree about the
	// same resource.
	srv.HandleFunc("POST /apis/run.googleapis.com/v1/namespaces/{namespace}/instances/{nameAction}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id, action, found := strings.Cut(sim.PathParam(r, "nameAction"), ":")
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on instance %q", id)
			return
		}
		switch action {
		case "start", "stop":
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on instance %q", action, id)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/instances/%s", namespace, cloudRunDefaultLocation, id)
		if _, exists := crv2Instances.Get(name); !exists {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"instance %q not found in namespace %q", id, namespace)
			return
		}
		now := nowTimestamp()
		state := enumString("CONDITION_SUCCEEDED")
		reason := ""
		if action == "stop" {
			state = "CONDITION_PENDING"
			reason = "Stopped"
		}
		crv2Instances.Update(name, func(i *InstanceV2) {
			i.UpdateTime = now
			i.TerminalCondition = &Condition{Type: "Ready", State: state, LastTransitionTime: now, Reason: reason}
			i.Etag = generateUUID()
		})
		instance, _ := crv2Instances.Get(name)
		writeCloudRunV1Instance(w, instance)
	})

	// --- namespaces.workerpools ---

	srv.HandleFunc("POST /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		var body CRWorkerPool
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid worker pool body: %v", err)
			return
		}
		if body.Metadata.Name == "" {
			sim.GCPError(w, http.StatusBadRequest, "metadata.name is required", "INVALID_ARGUMENT")
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", namespace, cloudRunDefaultLocation, body.Metadata.Name)
		if _, exists := crv2WorkerPools.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"worker pool %q already exists in namespace %q", body.Metadata.Name, namespace)
			return
		}
		pool := seedWorkerPoolV2Defaults(cloudRunV1WorkerPoolToV2(body), namespace, cloudRunDefaultLocation, body.Metadata.Name)
		pool.Etag = generateUUID()
		if !dryRun {
			crv2WorkerPools.Put(name, pool)
			reconcileWorkerPoolRevision(crv2WorkerPoolRevisions, name, body.Metadata.Name+"-00001-abc", pool)
		}
		writeCloudRunV1WorkerPool(w, pool)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		pool, ok := crv2WorkerPools.Get(fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", namespace, cloudRunDefaultLocation, id))
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"worker pool %q not found in namespace %q", id, namespace)
			return
		}
		writeCloudRunV1WorkerPool(w, pool)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		prefix := fmt.Sprintf("projects/%s/locations/%s/workerPools/", namespace, cloudRunDefaultLocation)
		selector := r.URL.Query().Get("labelSelector")
		items := make([]CRWorkerPool, 0)
		for _, pool := range crv2WorkerPools.Filter(func(p WorkerPoolV2) bool { return strings.HasPrefix(p.Name, prefix) }) {
			projected, ok := cloudRunV2WorkerPoolToV1(pool)
			if !ok || !knativeLabelSelectorMatches(selector, projected.Metadata.Labels) {
				continue
			}
			items = append(items, projected)
		}
		sort.Slice(items, func(i, j int) bool { return items[i].Metadata.Name < items[j].Metadata.Name })
		page, next, ok := knativeListPage(w, r, items)
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRWorkerPoolList{
			APIVersion: "run.googleapis.com/v1", Kind: "WorkerPoolList",
			Metadata: knativeListMeta(next), Items: page,
		})
	})

	srv.HandleFunc("PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", namespace, cloudRunDefaultLocation, id)
		existing, found := crv2WorkerPools.Get(name)
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"worker pool %q not found in namespace %q", id, namespace)
			return
		}
		var body CRWorkerPool
		if err := sim.ReadJSON(r, &body); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid worker pool body: %v", err)
			return
		}
		update := cloudRunV1WorkerPoolToV2(body)
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.LaunchStage = existing.LaunchStage
		update.Scaling = existing.Scaling
		update.Generation = existing.Generation + 1
		update.ObservedGeneration = update.Generation
		update.UpdateTime = nowTimestamp()
		update.TerminalCondition = &Condition{
			Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime,
		}
		update.Conditions = []Condition{
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
		}
		revName := fmt.Sprintf("%s-%05d-abc", id, update.Generation)
		update.LatestCreatedRevision = fmt.Sprintf("%s/revisions/%s", name, revName)
		update.LatestReadyRevision = update.LatestCreatedRevision
		update.InstanceSplitStatuses = []InstanceSplit{
			{Type: "INSTANCE_SPLIT_ALLOCATION_TYPE_LATEST", Percent: 100, Revision: revName},
		}
		update.Etag = generateUUID()
		if !dryRun {
			crv2WorkerPools.Put(name, update)
			reconcileWorkerPoolRevision(crv2WorkerPoolRevisions, name, revName, update)
		}
		writeCloudRunV1WorkerPool(w, update)
	})

	srv.HandleFunc("DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/workerpools/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/workerPools/%s", namespace, cloudRunDefaultLocation, id)
		if _, found := crv2WorkerPools.Get(name); !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"worker pool %q not found in namespace %q", id, namespace)
			return
		}
		if !dryRun {
			crv2WorkerPools.Delete(name)
			revPrefix := name + "/revisions/"
			for _, rev := range crv2WorkerPoolRevisions.Filter(func(rv RevisionV2) bool {
				return strings.HasPrefix(rv.Name, revPrefix)
			}) {
				crv2WorkerPoolRevisions.Delete(rev.Name)
			}
		}
		knativeDeleteStatus(w)
	})
}

func writeCloudRunV1Instance(w http.ResponseWriter, instance InstanceV2) {
	projected, ok := cloudRunV2InstanceToV1(instance)
	if !ok {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
			"instance %q has an unreadable resource name", instance.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, projected)
}

func writeCloudRunV1WorkerPool(w http.ResponseWriter, pool WorkerPoolV2) {
	projected, ok := cloudRunV2WorkerPoolToV1(pool)
	if !ok {
		sim.GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
			"worker pool %q has an unreadable resource name", pool.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, projected)
}
