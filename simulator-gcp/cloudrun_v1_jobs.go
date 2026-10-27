package main

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/listq"
)

// The Cloud Run Admin v1 (Knative) jobs, executions and tasks address the
// records the v2 handlers in cloudrunjobs.go own (crjJobs, crjExecutions,
// crjTasks) through cloudrun_v1_jobs_projection.go; there is no v1 store.

// CRJobList mirrors the Discovery ListJobsResponse schema.
type CRJobList struct {
	APIVersion  string      `json:"apiVersion"`
	Kind        string      `json:"kind"`
	Metadata    *CRListMeta `json:"metadata,omitempty"`
	Items       []CRJob     `json:"items"`
	Unreachable []string    `json:"unreachable,omitempty"`
}

// CRRunJobRequest mirrors the Discovery RunJobRequest schema.
type CRRunJobRequest struct {
	Overrides *CROverrides `json:"overrides,omitempty"`
}

// CROverrides mirrors the Discovery Overrides schema.
type CROverrides struct {
	ContainerOverrides []CRContainerOverride `json:"containerOverrides,omitempty"`
	TaskCount          int32                 `json:"taskCount,omitempty"`
	TimeoutSeconds     int32                 `json:"timeoutSeconds,omitempty"`
}

// CRContainerOverride mirrors the Discovery ContainerOverride schema.
type CRContainerOverride struct {
	Name      string     `json:"name,omitempty"`
	Args      []string   `json:"args,omitempty"`
	Env       []CREnvVar `json:"env,omitempty"`
	ClearArgs bool       `json:"clearArgs,omitempty"`
}

// CRExecutionList mirrors the Discovery ListExecutionsResponse schema.
type CRExecutionList struct {
	APIVersion  string        `json:"apiVersion"`
	Kind        string        `json:"kind"`
	Metadata    *CRListMeta   `json:"metadata,omitempty"`
	Items       []CRExecution `json:"items"`
	Unreachable []string      `json:"unreachable,omitempty"`
}

// CRTaskList mirrors the Discovery ListTasksResponse schema.
type CRTaskList struct {
	APIVersion  string      `json:"apiVersion"`
	Kind        string      `json:"kind"`
	Metadata    *CRListMeta `json:"metadata,omitempty"`
	Items       []CRTask    `json:"items"`
	Unreachable []string    `json:"unreachable,omitempty"`
}

// CRListMeta mirrors the Discovery ListMeta schema — the Knative list
// envelope's paging cursor.
type CRListMeta struct {
	ResourceVersion string `json:"resourceVersion,omitempty"`
	Continue        string `json:"continue,omitempty"`
	SelfLink        string `json:"selfLink,omitempty"`
}

// knativeLabelSelectorMatches reports whether a resource's labels satisfy the
// `labelSelector` query parameter Cloud Run's Knative list methods accept.
// Cloud Run supports the Kubernetes equality-based form: comma-separated
// `key=value` / `key==value` / `key!=value` terms, and a bare `key` requiring
// the label to be present. An empty selector matches everything.
func knativeLabelSelectorMatches(selector string, labels map[string]string) bool {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		return true
	}
	for _, term := range strings.Split(selector, ",") {
		term = strings.TrimSpace(term)
		if term == "" {
			continue
		}
		switch {
		case strings.Contains(term, "!="):
			key, want, _ := strings.Cut(term, "!=")
			if labels[strings.TrimSpace(key)] == strings.TrimSpace(want) {
				return false
			}
		case strings.Contains(term, "="):
			key, want, _ := strings.Cut(term, "=")
			key = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(key), "="))
			want = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(want), "="))
			if labels[key] != want {
				return false
			}
		default:
			if _, present := labels[term]; !present {
				return false
			}
		}
	}
	return true
}

// knativeListPage applies the `limit` / `continue` cursor a Knative list
// method pages with — the serving.knative.dev analogue of pageSize/pageToken —
// in the order first and then rest give, the last term naming each item
// uniquely, and returns the page plus the continue token for the next one. A
// malformed cursor is rejected rather than silently reset.
func knativeListPage[T any](w http.ResponseWriter, r *http.Request, items []T, first gcpSortKey[T], rest ...gcpSortKey[T]) ([]T, string, bool) {
	size, ok := gcpPageSizeParam(w, r, "limit")
	if !ok {
		return nil, "", false
	}
	token := r.URL.Query().Get("continue")
	page, next, err := gcpKeyPageOf(items, append([]gcpSortKey[T]{first}, rest...), token, size, 0, 0)
	switch {
	case errors.Is(err, listq.ErrBadToken):
		GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid continue token %q", token)
		return nil, "", false
	case err != nil:
		GCPError(w, http.StatusInternalServerError, err.Error(), "INTERNAL")
		return nil, "", false
	}
	return page, next, true
}

// knativeListMeta returns the list envelope's metadata, which carries the
// continue token of a page that is not the last. Every list carries it:
// `gcloud run jobs executions tasks list` reads metadata.continue off the
// response without checking for the metadata first.
func knativeListMeta(next string) *CRListMeta {
	return &CRListMeta{Continue: next}
}

// cloudRunV1JobsPrefix is the v2 resource-name prefix the Knative namespace
// `namespace` addresses. The v1 jobs surface is regional on real Cloud Run
// (`{region}-run.googleapis.com`) and the simulator serves one origin, so the
// namespaces surface addresses cloudRunDefaultLocation.
func cloudRunV1JobsPrefix(namespace string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/", namespace, cloudRunDefaultLocation)
}

// cloudRunV1FindExecution resolves a Knative execution name (an execution id
// within a namespace, with no parent job in the path) to the v2 execution
// record that carries it.
func cloudRunV1FindExecution(namespace, executionID string) (Execution, bool) {
	prefix := cloudRunV1JobsPrefix(namespace)
	suffix := "/executions/" + executionID
	for _, execution := range crjExecutions.Filter(func(e Execution) bool {
		return strings.HasPrefix(e.Name, prefix) && strings.HasSuffix(e.Name, suffix)
	}) {
		return execution, true
	}
	return Execution{}, false
}

// cloudRunV1FindTask resolves a Knative task name to the v2 task record.
func cloudRunV1FindTask(namespace, taskID string) (Task, bool) {
	prefix := cloudRunV1JobsPrefix(namespace)
	suffix := "/tasks/" + taskID
	for _, task := range crjTasks.Filter(func(t Task) bool {
		return strings.HasPrefix(t.Name, prefix) && strings.HasSuffix(t.Name, suffix)
	}) {
		return task, true
	}
	return Task{}, false
}

func registerCloudRunV1Jobs(srv *sim.Server) {

	srv.HandleFunc("POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		var body CRJob
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid job body: %v", err)
			return
		}
		if body.Metadata.Name == "" {
			GCPError(w, http.StatusBadRequest, "metadata.name is required", "INVALID_ARGUMENT")
			return
		}
		name := cloudRunV1JobsPrefix(namespace) + body.Metadata.Name
		if _, exists := crjJobs.Get(name); exists {
			GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS",
				"job %q already exists in namespace %q", body.Metadata.Name, namespace)
			return
		}
		now := nowTimestamp()
		job := cloudRunV1JobToV2(body)
		if !cloudRunJobTemplateValid(w, job.Template, true) || !cloudRunJobExecutionTokenValid(w, body.Metadata.Name, job) {
			return
		}
		job.Name = name
		job.UID = sim.NewUUID()
		job.Generation = 1
		job.CreateTime = now
		job.UpdateTime = now
		job.LaunchStage = "GA"
		job.TerminalCondition = &Condition{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now}
		job.Conditions = []Condition{
			{Type: "ConfigurationsReady", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
		}
		cloudRunJobTemplateDefaults(job.Template)
		job.Etag = sim.NewUUID()
		holdCloudRunJobForExecutionToken(&job, now)
		if !dryRun {
			crjJobs.Put(name, job)
			job = startCloudRunJobTokenExecution(namespace, cloudRunDefaultLocation, body.Metadata.Name, job)
		}
		writeCloudRunV1Job(w, job)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		job, ok := crjJobs.Get(cloudRunV1JobsPrefix(namespace) + id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"job %q not found in namespace %q", id, namespace)
			return
		}
		writeCloudRunV1Job(w, job)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs", func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeProjectedPage(w, r, crjJobs, cloudRunV1JobsPrefix(sim.PathParam(r, "namespace")),
			func(j Job) string { return j.Name }, cloudRunV2JobToV1,
			func(v CRJob) map[string]string { return v.Metadata.Labels }, func(v CRJob) string { return v.Metadata.Name })
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRJobList{
			APIVersion: "run.googleapis.com/v1", Kind: "JobList",
			Metadata: knativeListMeta(next), Items: page,
		})
	})

	srv.HandleFunc("PUT /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		name := cloudRunV1JobsPrefix(namespace) + id
		existing, found := crjJobs.Get(name)
		if !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"job %q not found in namespace %q", id, namespace)
			return
		}
		var body CRJob
		if err := sim.ReadJSON(r, &body); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid job body: %v", err)
			return
		}
		if !knativeReplaceAllowed(w, "job", id, namespace,
			knativeResourceVersion(existing.Generation), body.Metadata.ResourceVersion) {
			return
		}
		// ReplaceJob replaces the whole mutable resource; identity, launch
		// stage and the execution history carry over from the stored record.
		update := cloudRunV1JobToV2(body)
		if !cloudRunJobTemplateValid(w, update.Template, true) || !cloudRunJobExecutionTokenValid(w, id, update) {
			return
		}
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.LaunchStage = existing.LaunchStage
		update.ExecutionCount = existing.ExecutionCount
		update.LatestCreatedExecution = existing.LatestCreatedExecution
		update.Generation = existing.Generation + 1
		update.UpdateTime = nowTimestamp()
		update.TerminalCondition = &Condition{
			Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime,
		}
		update.Conditions = []Condition{
			{Type: "ConfigurationsReady", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
		}
		cloudRunJobTemplateDefaults(update.Template)
		update.Etag = sim.NewUUID()
		holdCloudRunJobForExecutionToken(&update, update.UpdateTime)
		if !dryRun {
			crjJobs.Put(name, update)
			update = startCloudRunJobTokenExecution(namespace, cloudRunDefaultLocation, id, update)
		}
		writeCloudRunV1Job(w, update)
	})

	srv.HandleFunc("DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id := sim.PathParam(r, "name")
		dryRun, ok := knativeDryRun(w, r)
		if !ok {
			return
		}
		name := cloudRunV1JobsPrefix(namespace) + id
		if _, found := crjJobs.Get(name); !found {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"job %q not found in namespace %q", id, namespace)
			return
		}
		if !dryRun {
			deleteCloudRunJobCascade(name)
		}
		knativeDeleteStatus(w)
	})

	// The Knative RunJob answers with the Execution it started, where v2 wraps the
	// same Execution in an operation.
	srv.HandleFunc("POST /apis/run.googleapis.com/v1/namespaces/{namespace}/jobs/{nameAction}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id, action, found := strings.Cut(sim.PathParam(r, "nameAction"), ":")
		if !found || action != "run" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on job %q", action, id)
			return
		}
		job, ok := crjJobs.Get(cloudRunV1JobsPrefix(namespace) + id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"job %q not found in namespace %q", id, namespace)
			return
		}
		var request CRRunJobRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid run request body: %v", err)
			return
		}
		execution := runCloudRunJob(namespace, cloudRunDefaultLocation, id, job,
			cloudRunV1OverridesToV2(request.Overrides), sim.NewUUID())
		projected, ok := cloudRunV2ExecutionToV1(execution)
		if !ok {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
				"execution %q has an unreadable resource name", execution.Name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, projected)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		execution, ok := cloudRunV1FindExecution(namespace, name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"execution %q not found in namespace %q", name, namespace)
			return
		}
		projected, ok := cloudRunV2ExecutionToV1(execution)
		if !ok {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
				"execution %q has an unreadable resource name", execution.Name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, projected)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/executions", func(w http.ResponseWriter, r *http.Request) {
		page, next, ok := knativeProjectedPage(w, r, crjExecutions, cloudRunV1JobsPrefix(sim.PathParam(r, "namespace")),
			func(e Execution) string { return e.Name }, cloudRunV2ExecutionToV1,
			func(v CRExecution) map[string]string { return v.Metadata.Labels }, func(v CRExecution) string { return v.Metadata.Name })
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRExecutionList{
			APIVersion: "run.googleapis.com/v1", Kind: "ExecutionList",
			Metadata: knativeListMeta(next), Items: page,
		})
	})

	srv.HandleFunc("DELETE /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		execution, ok := cloudRunV1FindExecution(namespace, name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"execution %q not found in namespace %q", name, namespace)
			return
		}
		deleteCloudRunExecutionCascade(execution.Name)
		knativeDeleteStatus(w)
	})

	// CancelExecution takes the shared cancel path, so the execution's containers
	// stop rather than only its record changing.
	srv.HandleFunc("POST /apis/run.googleapis.com/v1/namespaces/{namespace}/executions/{nameAction}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		id, action, found := strings.Cut(sim.PathParam(r, "nameAction"), ":")
		if !found || action != "cancel" {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on execution %q", action, id)
			return
		}
		execution, ok := cloudRunV1FindExecution(namespace, id)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"execution %q not found in namespace %q", id, namespace)
			return
		}
		_, _, jobID, executionID, ok := parseCloudRunV2ExecutionName(execution.Name)
		if !ok {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
				"execution %q has an unreadable resource name", execution.Name)
			return
		}
		cancelled, ok := cancelCloudRunExecution(namespace, cloudRunDefaultLocation, jobID, executionID)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"execution %q not found in namespace %q", id, namespace)
			return
		}
		projected, ok := cloudRunV2ExecutionToV1(cancelled)
		if !ok {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
				"execution %q has an unreadable resource name", cancelled.Name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, projected)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks/{name}", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		name := sim.PathParam(r, "name")
		task, ok := cloudRunV1FindTask(namespace, name)
		if !ok {
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"task %q not found in namespace %q", name, namespace)
			return
		}
		projected, ok := cloudRunV2TaskToV1(task)
		if !ok {
			GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
				"task %q has an unreadable resource name", task.Name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, projected)
	})

	srv.HandleFunc("GET /apis/run.googleapis.com/v1/namespaces/{namespace}/tasks", func(w http.ResponseWriter, r *http.Request) {
		namespace := sim.PathParam(r, "namespace")
		prefix := cloudRunV1JobsPrefix(namespace)
		selector := r.URL.Query().Get("labelSelector")
		items := make([]CRTask, 0)
		for _, task := range crjTasks.Filter(func(t Task) bool {
			return strings.HasPrefix(t.Name, prefix)
		}) {
			projected, ok := cloudRunV2TaskToV1(task)
			if !ok || !knativeLabelSelectorMatches(selector, projected.Metadata.Labels) {
				continue
			}
			items = append(items, projected)
		}
		page, next, ok := knativeListPage(w, r, items,
			gcpAscNumber(func(v CRTask) string { return strconv.Itoa(int(v.Status.Index)) }),
			gcpAsc(func(v CRTask) string { return v.Metadata.Name }))
		if !ok {
			return
		}
		sim.WriteJSON(w, http.StatusOK, CRTaskList{
			APIVersion: "run.googleapis.com/v1", Kind: "TaskList",
			Metadata: knativeListMeta(next), Items: page,
		})
	})

	// `gcloud run jobs get-iam-policy` reads a job's policy at this global v1
	// path. The policy is stored under the v2 resource name, so either API version
	// reads what the other wrote.
	srv.HandleFunc("GET /v1/projects/{project}/locations/{location}/jobs/{jobAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		id, action, found := strings.Cut(sim.PathParam(r, "jobAction"), ":")
		if !found && gcpServiceFromHost(r) != gcpServiceCloudRun {
			// Cloud Scheduler's jobs.get shares the URI; it has no GET verb.
			handleCSGetJob(w, r, id)
			return
		}
		if !found || action != "getIamPolicy" {
			// getIamPolicy is the only GET verb run.googleapis.com publishes on this path.
			GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on job %q", action, id)
			return
		}
		cloudRunV1JobIAM(w, r, project, location, id, action)
	})
}

func writeCloudRunV1Job(w http.ResponseWriter, job Job) {
	projected, ok := cloudRunV2JobToV1(job)
	if !ok {
		GCPErrorf(w, http.StatusInternalServerError, "INTERNAL",
			"job %q has an unreadable resource name", job.Name)
		return
	}
	sim.WriteJSON(w, http.StatusOK, projected)
}

// deleteCloudRunJobCascade removes a job together with the executions and
// tasks it owns and stops the executions still running, which is what deleting
// a job does on either API version.
func deleteCloudRunJobCascade(name string) {
	crjJobs.Delete(name)
	prefix := name + "/executions/"
	for _, execution := range crjExecutions.Filter(func(e Execution) bool {
		return strings.HasPrefix(e.Name, prefix)
	}) {
		crjExecutions.Delete(execution.Name)
		stopCloudRunExecutionRun(execution.Name)
	}
	for _, task := range crjTasks.Filter(func(t Task) bool { return strings.HasPrefix(t.Name, prefix) }) {
		crjTasks.Delete(task.Name)
	}
}

// deleteCloudRunExecutionCascade removes an execution together with its tasks
// and stops it if it still runs.
func deleteCloudRunExecutionCascade(name string) {
	crjExecutions.Delete(name)
	stopCloudRunExecutionRun(name)
	taskPrefix := name + "/tasks/"
	for _, task := range crjTasks.Filter(func(t Task) bool { return strings.HasPrefix(t.Name, taskPrefix) }) {
		crjTasks.Delete(task.Name)
	}
}

// cloudRunV1JobIAM serves an AIP-141 IAM verb against a Cloud Run job, keyed
// on the v2 resource name. A job that does not exist is NOT_FOUND, never an
// empty policy.
func cloudRunV1JobIAM(w http.ResponseWriter, r *http.Request, project, location, id, action string) {
	name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, id)
	if _, ok := crjJobs.Get(name); !ok {
		GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", name)
		return
	}
	handleResourceIAM(w, r, gcpResourceIAMStore(), name, action)
}

// knativeProjectedPage pages the resources of store named under prefix, each
// projected onto its Knative form and kept when the request's labelSelector
// matches its labels, in the order name gives.
func knativeProjectedPage[S, V any](w http.ResponseWriter, r *http.Request, store sim.Store[S], prefix string, storedName func(S) string,
	project func(S) (V, bool), labels func(V) map[string]string, name func(V) string) ([]V, string, bool) {
	selector := r.URL.Query().Get("labelSelector")
	items := make([]V, 0)
	for _, stored := range store.Filter(func(v S) bool { return strings.HasPrefix(storedName(v), prefix) }) {
		projected, ok := project(stored)
		if !ok || !knativeLabelSelectorMatches(selector, labels(projected)) {
			continue
		}
		items = append(items, projected)
	}
	return knativeListPage(w, r, items, gcpAsc(name))
}
