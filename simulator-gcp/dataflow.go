package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// dataflowJob mirrors the fields of the Cloud Dataflow v1b3 `Job` resource
// the simulator round-trips. The Discovery schema names every field; the
// objects (`environment`, `steps`, `transformNameMapping`, `labels`) are
// carried verbatim from the client so a read-back returns what was written.
type dataflowJob struct {
	ID                    string            `json:"id,omitempty"`
	ProjectID             string            `json:"projectId,omitempty"`
	Location              string            `json:"location,omitempty"`
	Name                  string            `json:"name,omitempty"`
	Type                  string            `json:"type,omitempty"`
	CurrentState          string            `json:"currentState,omitempty"`
	CurrentStateTime      string            `json:"currentStateTime,omitempty"`
	RequestedState        string            `json:"requestedState,omitempty"`
	CreateTime            string            `json:"createTime,omitempty"`
	StartTime             string            `json:"startTime,omitempty"`
	ClientRequestID       string            `json:"clientRequestId,omitempty"`
	CreatedFromSnapshotID string            `json:"createdFromSnapshotId,omitempty"`
	ReplaceJobID          string            `json:"replaceJobId,omitempty"`
	ReplacedByJobID       string            `json:"replacedByJobId,omitempty"`
	Steps                 []map[string]any  `json:"steps,omitempty"`
	StepsLocation         string            `json:"stepsLocation,omitempty"`
	Environment           map[string]any    `json:"environment,omitempty"`
	TransformNameMapping  map[string]string `json:"transformNameMapping,omitempty"`
	Labels                map[string]string `json:"labels,omitempty"`
}

// dataflowSnapshot mirrors the Cloud Dataflow v1b3 `Snapshot` resource.
type dataflowSnapshot struct {
	ID            string `json:"id,omitempty"`
	ProjectID     string `json:"projectId,omitempty"`
	SourceJobID   string `json:"sourceJobId,omitempty"`
	CreationTime  string `json:"creationTime,omitempty"`
	TTL           string `json:"ttl,omitempty"`
	State         string `json:"state,omitempty"`
	Description   string `json:"description,omitempty"`
	DiskSizeBytes string `json:"diskSizeBytes,omitempty"`
	Region        string `json:"region,omitempty"`
}

var (
	dataflowJobs      sim.Store[dataflowJob]
	dataflowSnapshots sim.Store[dataflowSnapshot]
)

// registerDataflow mounts the Cloud Dataflow v1b3 slice. Dataflow exposes
// most of its surface under both a global (`projects/{p}/...`) and a regional
// (`projects/{p}/locations/{loc}/...`) path form; both are mounted so a client
// reaches the same handlers either way. The regional form carries an explicit
// location; the global form defaults the job's location to the value the
// client supplied in the body (or empty).
func registerDataflow(srv *sim.Server) {
	dataflowJobs = sim.MakeStore[dataflowJob](srv.DB(), "dataflow_jobs")
	dataflowSnapshots = sim.MakeStore[dataflowSnapshot](srv.DB(), "dataflow_snapshots")

	// ----- jobs (global) -----
	srv.HandleFunc("POST /v1b3/projects/{project}/jobs", handleDataflowCreateJobGlobal)
	srv.HandleFunc("GET /v1b3/projects/{project}/jobs", handleDataflowListJobsGlobal)
	srv.HandleFunc("GET /v1b3/projects/{project}/jobs:aggregated", handleDataflowAggregatedJobs)
	srv.HandleFunc("GET /v1b3/projects/{project}/jobs/{jobAction}", handleDataflowGetJobGlobal)
	srv.HandleFunc("PUT /v1b3/projects/{project}/jobs/{job}", handleDataflowUpdateJobGlobal)
	srv.HandleFunc("POST /v1b3/projects/{project}/jobs/{jobAction}", handleDataflowJobPostActionGlobal)
	srv.HandleFunc("GET /v1b3/projects/{project}/jobs/{job}/metrics", handleDataflowGetMetricsGlobal)
	srv.HandleFunc("GET /v1b3/projects/{project}/jobs/{job}/messages", handleDataflowListMessagesGlobal)
	srv.HandleFunc("POST /v1b3/projects/{project}/jobs/{job}/debug/getConfig", handleDataflowGetDebugConfig)
	srv.HandleFunc("POST /v1b3/projects/{project}/jobs/{job}/debug/sendCapture", handleDataflowSendDebugCapture)
	srv.HandleFunc("POST /v1b3/projects/{project}/jobs/{job}/workItems:lease", handleDataflowLeaseWorkItems)
	srv.HandleFunc("POST /v1b3/projects/{project}/jobs/{job}/workItems:reportStatus", handleDataflowReportWorkItemStatus)

	// ----- jobs (regional) -----
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs", handleDataflowCreateJob)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs", handleDataflowListJobs)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}", handleDataflowGetJob)
	srv.HandleFunc("PUT /v1b3/projects/{project}/locations/{location}/jobs/{job}", handleDataflowUpdateJob)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs/{jobAction}", handleDataflowJobPostAction)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/metrics", handleDataflowGetMetrics)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/messages", handleDataflowListMessages)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/executionDetails", handleDataflowGetExecutionDetails)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/snapshots", handleDataflowListJobSnapshots)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/jobs/{job}/stages/{stage}/executionDetails", handleDataflowGetStageExecutionDetails)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/getConfig", handleDataflowGetDebugConfig)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/sendCapture", handleDataflowSendDebugCapture)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/debug/getWorkerStacktraces", handleDataflowGetWorkerStacktraces)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/workItems:lease", handleDataflowLeaseWorkItems)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/jobs/{job}/workItems:reportStatus", handleDataflowReportWorkItemStatus)

	// ----- templates (global) -----
	srv.HandleFunc("POST /v1b3/projects/{project}/templates", handleDataflowCreateJobFromTemplateGlobal)
	srv.HandleFunc("GET /v1b3/projects/{project}/templates:get", handleDataflowGetTemplate)
	srv.HandleFunc("POST /v1b3/projects/{project}/templates:launch", handleDataflowLaunchTemplateGlobal)

	// ----- templates (regional) + flexTemplates -----
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/templates", handleDataflowCreateJobFromTemplate)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/templates:get", handleDataflowGetTemplate)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/templates:launch", handleDataflowLaunchTemplate)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/flexTemplates:launch", handleDataflowLaunchFlexTemplate)

	// ----- snapshots (global) -----
	srv.HandleFunc("GET /v1b3/projects/{project}/snapshots", handleDataflowListSnapshotsGlobal)
	srv.HandleFunc("DELETE /v1b3/projects/{project}/snapshots", handleDataflowDeleteSnapshotsGlobal)
	srv.HandleFunc("GET /v1b3/projects/{project}/snapshots/{snapshot}", handleDataflowGetSnapshotGlobal)

	// ----- snapshots (regional) -----
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/snapshots", handleDataflowListSnapshots)
	srv.HandleFunc("GET /v1b3/projects/{project}/locations/{location}/snapshots/{snapshot}", handleDataflowGetSnapshot)
	srv.HandleFunc("DELETE /v1b3/projects/{project}/locations/{location}/snapshots/{snapshot}", handleDataflowDeleteSnapshot)

	// ----- workerMessages -----
	srv.HandleFunc("POST /v1b3/projects/{project}/WorkerMessages", handleDataflowWorkerMessages)
	srv.HandleFunc("POST /v1b3/projects/{project}/locations/{location}/WorkerMessages", handleDataflowWorkerMessages)
}

func dataflowJobKey(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, id)
}

func dataflowSnapshotKey(project, location, id string) string {
	return fmt.Sprintf("projects/%s/locations/%s/snapshots/%s", project, location, id)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// createDataflowJob applies the shared create semantics: a job is assigned an
// id, recorded under (project, location), and enters JOB_STATE_RUNNING.
func createDataflowJob(w http.ResponseWriter, project, location string, req dataflowJob) {
	if req.Name == "" {
		sim.GCPError(w, http.StatusBadRequest, "job name is required", "INVALID_ARGUMENT")
		return
	}
	if req.ID == "" {
		req.ID = generateUUID()
	}
	now := nowRFC3339()
	req.ProjectID = project
	req.Location = location
	if req.Type == "" {
		req.Type = "JOB_TYPE_BATCH"
	}
	req.CurrentState = "JOB_STATE_RUNNING"
	req.CreateTime = now
	req.StartTime = now
	req.CurrentStateTime = now
	dataflowJobs.Put(dataflowJobKey(project, location, req.ID), req)
	sim.WriteJSON(w, http.StatusOK, req)
}

func handleDataflowCreateJob(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	var req dataflowJob
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	createDataflowJob(w, project, location, req)
}

func handleDataflowCreateJobGlobal(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	var req dataflowJob
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	createDataflowJob(w, project, req.Location, req)
}

// listDataflowJobs returns every job recorded under (project, location); when
// location is empty (global list), every job in the project is returned.
func listDataflowJobs(w http.ResponseWriter, r *http.Request, project, location string) {
	out := dataflowJobs.Filter(func(job dataflowJob) bool {
		if job.ProjectID != project {
			return false
		}
		return location == "" || job.Location == location
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	page, next, ok := paginateList(w, r, out)
	if !ok {
		return
	}
	resp := map[string]any{"jobs": page}
	if next != "" {
		resp["nextPageToken"] = next
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleDataflowListJobs(w http.ResponseWriter, r *http.Request) {
	listDataflowJobs(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

func handleDataflowListJobsGlobal(w http.ResponseWriter, r *http.Request) {
	listDataflowJobs(w, r, sim.PathParam(r, "project"), "")
}

// handleDataflowAggregatedJobs serves jobs.aggregated — the cross-region list
// for a project. Same ListJobsResponse shape as a plain list.
func handleDataflowAggregatedJobs(w http.ResponseWriter, r *http.Request) {
	listDataflowJobs(w, r, sim.PathParam(r, "project"), "")
}

// findDataflowJob locates a job by id within a project, optionally constrained
// to a location (regional path forms).
func findDataflowJob(project, location, id string) (dataflowJob, string, bool) {
	if location != "" {
		key := dataflowJobKey(project, location, id)
		job, ok := dataflowJobs.Get(key)
		return job, key, ok
	}
	var found dataflowJob
	var key string
	ok := false
	dataflowJobs.Filter(func(job dataflowJob) bool {
		if !ok && job.ProjectID == project && job.ID == id {
			found, key, ok = job, dataflowJobKey(project, job.Location, job.ID), true
		}
		return false
	})
	return found, key, ok
}

func handleDataflowGetJob(w http.ResponseWriter, r *http.Request) {
	getDataflowJob(w, sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "job"))
}

// handleDataflowGetJobGlobal serves the global GET, which fans in GetJob (plain
// "{job}") and the job-snapshot colon-verb is POST-only, so a GET here is
// always a plain get.
func handleDataflowGetJobGlobal(w http.ResponseWriter, r *http.Request) {
	getDataflowJob(w, sim.PathParam(r, "project"), "", sim.PathParam(r, "jobAction"))
}

func getDataflowJob(w http.ResponseWriter, project, location, id string) {
	job, _, ok := findDataflowJob(project, location, id)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, job)
}

func handleDataflowUpdateJob(w http.ResponseWriter, r *http.Request) {
	updateDataflowJob(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "job"))
}

func handleDataflowUpdateJobGlobal(w http.ResponseWriter, r *http.Request) {
	updateDataflowJob(w, r, sim.PathParam(r, "project"), "", sim.PathParam(r, "job"))
}

func updateDataflowJob(w http.ResponseWriter, r *http.Request, project, location, id string) {
	var req dataflowJob
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	_, key, ok := findDataflowJob(project, location, id)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", id)
		return
	}
	dataflowJobs.Update(key, func(job *dataflowJob) {
		// A Dataflow job update primarily drives a state transition via
		// requestedState (e.g. JOB_STATE_DRAINING / JOB_STATE_CANCELLED).
		if req.RequestedState != "" {
			job.RequestedState = req.RequestedState
			job.CurrentState = req.RequestedState
			job.CurrentStateTime = nowRFC3339()
		}
		if req.Labels != nil {
			job.Labels = req.Labels
		}
		if req.TransformNameMapping != nil {
			job.TransformNameMapping = req.TransformNameMapping
		}
	})
	job, _ := dataflowJobs.Get(key)
	sim.WriteJSON(w, http.StatusOK, job)
}

// handleDataflowJobPostAction fans in the POST colon-verb on a job resource:
// "{job}:snapshot". Go's ServeMux cannot spell "{job}:verb" as a pattern, so
// the handler splits the trailing ":verb" suffix itself.
func handleDataflowJobPostAction(w http.ResponseWriter, r *http.Request) {
	dataflowJobPostAction(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "jobAction"))
}

func handleDataflowJobPostActionGlobal(w http.ResponseWriter, r *http.Request) {
	dataflowJobPostAction(w, r, sim.PathParam(r, "project"), "", sim.PathParam(r, "jobAction"))
}

func dataflowJobPostAction(w http.ResponseWriter, r *http.Request, project, location, jobAction string) {
	jobID, action, found := strings.Cut(jobAction, ":")
	if !found {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown job action %q", jobAction)
		return
	}
	if action != "snapshot" {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown job action %q", action)
		return
	}
	job, _, ok := findDataflowJob(project, location, jobID)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", jobID)
		return
	}
	var req struct {
		Description string `json:"description"`
		Location    string `json:"location"`
		TTL         string `json:"ttl"`
	}
	_ = sim.ReadJSON(r, &req)
	region := job.Location
	if req.Location != "" {
		region = req.Location
	}
	snap := dataflowSnapshot{
		ID:           generateUUID(),
		ProjectID:    project,
		SourceJobID:  job.ID,
		CreationTime: nowRFC3339(),
		TTL:          req.TTL,
		State:        "READY",
		Description:  req.Description,
		Region:       region,
	}
	dataflowSnapshots.Put(dataflowSnapshotKey(project, region, snap.ID), snap)
	sim.WriteJSON(w, http.StatusOK, snap)
}

func handleDataflowGetMetrics(w http.ResponseWriter, r *http.Request) {
	dataflowGetMetrics(w, sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "job"))
}

func handleDataflowGetMetricsGlobal(w http.ResponseWriter, r *http.Request) {
	dataflowGetMetrics(w, sim.PathParam(r, "project"), "", sim.PathParam(r, "job"))
}

func dataflowGetMetrics(w http.ResponseWriter, project, location, id string) {
	if _, _, ok := findDataflowJob(project, location, id); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", id)
		return
	}
	// A representative JobMetrics: one scalar MetricUpdate. The MetricUpdate
	// `scalar` field is typed "any" in the Discovery schema, so a JSON number
	// is valid.
	resp := map[string]any{
		"metricTime": nowRFC3339(),
		"metrics": []map[string]any{
			{
				"name":       map[string]any{"name": "ElementCount", "origin": "dataflow/v1b3"},
				"kind":       "sum",
				"cumulative": true,
				"scalar":     0,
				"updateTime": nowRFC3339(),
			},
		},
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleDataflowListMessages(w http.ResponseWriter, r *http.Request) {
	dataflowListMessages(w, sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "job"))
}

func handleDataflowListMessagesGlobal(w http.ResponseWriter, r *http.Request) {
	dataflowListMessages(w, sim.PathParam(r, "project"), "", sim.PathParam(r, "job"))
}

func dataflowListMessages(w http.ResponseWriter, project, location, id string) {
	job, _, ok := findDataflowJob(project, location, id)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", id)
		return
	}
	resp := map[string]any{
		"jobMessages": []map[string]any{
			{
				"id":                job.ID + "-msg-0",
				"time":              job.CreateTime,
				"messageText":       "Worker pool started.",
				"messageImportance": "JOB_MESSAGE_BASIC",
			},
		},
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleDataflowGetExecutionDetails(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "job")
	if _, _, ok := findDataflowJob(project, location, id); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", id)
		return
	}
	resp := map[string]any{
		"stages": []map[string]any{
			{
				"stageId": "s1",
				"state":   "EXECUTION_STATE_SUCCEEDED",
			},
		},
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleDataflowGetStageExecutionDetails(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "job")
	if _, _, ok := findDataflowJob(project, location, id); !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", id)
		return
	}
	resp := map[string]any{
		"workers": []map[string]any{
			{"workerName": "worker-0"},
		},
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

func handleDataflowGetDebugConfig(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"config": "{}"})
}

func handleDataflowSendDebugCapture(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

func handleDataflowGetWorkerStacktraces(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"sdks": []any{}})
}

func handleDataflowLeaseWorkItems(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"workItems": []any{}})
}

func handleDataflowReportWorkItemStatus(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"workItemServiceStates": []any{}})
}

func handleDataflowWorkerMessages(w http.ResponseWriter, r *http.Request) {
	sim.WriteJSON(w, http.StatusOK, map[string]any{"workerMessageResponses": []any{}})
}

// ----- templates -----

// createJobFromTemplate (templates.create) launches a classic template, which
// in the real service creates a Dataflow job. The response is a Job.
func dataflowCreateJobFromTemplate(w http.ResponseWriter, r *http.Request, project, location string) {
	var req struct {
		JobName     string            `json:"jobName"`
		GcsPath     string            `json:"gcsPath"`
		Location    string            `json:"location"`
		Environment map[string]any    `json:"environment"`
		Parameters  map[string]string `json:"parameters"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
		return
	}
	loc := location
	if loc == "" {
		loc = req.Location
	}
	createDataflowJob(w, project, loc, dataflowJob{Name: req.JobName, Type: "JOB_TYPE_BATCH", Environment: req.Environment})
}

func handleDataflowCreateJobFromTemplate(w http.ResponseWriter, r *http.Request) {
	dataflowCreateJobFromTemplate(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

func handleDataflowCreateJobFromTemplateGlobal(w http.ResponseWriter, r *http.Request) {
	dataflowCreateJobFromTemplate(w, r, sim.PathParam(r, "project"), "")
}

// dataflowLaunchTemplate (templates.launch) launches a classic template; the
// response is a LaunchTemplateResponse wrapping the created Job.
func dataflowLaunchTemplate(w http.ResponseWriter, r *http.Request, project, location string) {
	var req struct {
		JobName     string         `json:"jobName"`
		Environment map[string]any `json:"environment"`
	}
	_ = sim.ReadJSON(r, &req)
	name := req.JobName
	if name == "" {
		name = "template-launch"
	}
	if req.JobName == "" {
		req.JobName = name
	}
	loc := location
	job := dataflowJob{Name: name, Type: "JOB_TYPE_BATCH", Environment: req.Environment}
	if job.Name == "" {
		sim.GCPError(w, http.StatusBadRequest, "job name is required", "INVALID_ARGUMENT")
		return
	}
	job.ID = generateUUID()
	now := nowRFC3339()
	job.ProjectID = project
	job.Location = loc
	job.CurrentState = "JOB_STATE_RUNNING"
	job.CreateTime = now
	job.StartTime = now
	job.CurrentStateTime = now
	dataflowJobs.Put(dataflowJobKey(project, loc, job.ID), job)
	sim.WriteJSON(w, http.StatusOK, map[string]any{"job": job})
}

func handleDataflowLaunchTemplate(w http.ResponseWriter, r *http.Request) {
	dataflowLaunchTemplate(w, r, sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

func handleDataflowLaunchTemplateGlobal(w http.ResponseWriter, r *http.Request) {
	dataflowLaunchTemplate(w, r, sim.PathParam(r, "project"), "")
}

// handleDataflowLaunchFlexTemplate (flexTemplates.launch) launches a Flex
// Template; the response is a LaunchFlexTemplateResponse wrapping the Job.
func handleDataflowLaunchFlexTemplate(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	var req struct {
		LaunchParameter struct {
			JobName     string         `json:"jobName"`
			Environment map[string]any `json:"environment"`
		} `json:"launchParameter"`
		ValidateOnly bool `json:"validateOnly"`
	}
	_ = sim.ReadJSON(r, &req)
	name := req.LaunchParameter.JobName
	if name == "" {
		name = "flex-template-launch"
	}
	job := dataflowJob{
		ID:           generateUUID(),
		Name:         name,
		Type:         "JOB_TYPE_BATCH",
		ProjectID:    project,
		Location:     location,
		Environment:  req.LaunchParameter.Environment,
		CurrentState: "JOB_STATE_RUNNING",
		CreateTime:   nowRFC3339(),
		StartTime:    nowRFC3339(),
	}
	job.CurrentStateTime = job.CreateTime
	if !req.ValidateOnly {
		dataflowJobs.Put(dataflowJobKey(project, location, job.ID), job)
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{"job": job})
}

// handleDataflowGetTemplate (templates.get) returns template metadata.
func handleDataflowGetTemplate(w http.ResponseWriter, r *http.Request) {
	resp := map[string]any{
		"status":       map[string]any{"code": 0},
		"templateType": "LEGACY",
		"metadata": map[string]any{
			"name": "Word Count",
		},
	}
	sim.WriteJSON(w, http.StatusOK, resp)
}

// ----- snapshots -----

func listDataflowSnapshots(w http.ResponseWriter, project, location string) {
	out := dataflowSnapshots.Filter(func(s dataflowSnapshot) bool {
		if s.ProjectID != project {
			return false
		}
		return location == "" || s.Region == location
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func handleDataflowListSnapshots(w http.ResponseWriter, r *http.Request) {
	listDataflowSnapshots(w, sim.PathParam(r, "project"), sim.PathParam(r, "location"))
}

func handleDataflowListSnapshotsGlobal(w http.ResponseWriter, r *http.Request) {
	listDataflowSnapshots(w, sim.PathParam(r, "project"), "")
}

// handleDataflowListJobSnapshots (jobs.snapshots.list) lists snapshots created
// from one job.
func handleDataflowListJobSnapshots(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	id := sim.PathParam(r, "job")
	out := dataflowSnapshots.Filter(func(s dataflowSnapshot) bool {
		return s.ProjectID == project && s.SourceJobID == id
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	sim.WriteJSON(w, http.StatusOK, map[string]any{"snapshots": out})
}

func findDataflowSnapshot(project, location, id string) (dataflowSnapshot, string, bool) {
	if location != "" {
		key := dataflowSnapshotKey(project, location, id)
		s, ok := dataflowSnapshots.Get(key)
		return s, key, ok
	}
	var found dataflowSnapshot
	var key string
	ok := false
	dataflowSnapshots.Filter(func(s dataflowSnapshot) bool {
		if !ok && s.ProjectID == project && s.ID == id {
			found, key, ok = s, dataflowSnapshotKey(project, s.Region, s.ID), true
		}
		return false
	})
	return found, key, ok
}

func handleDataflowGetSnapshot(w http.ResponseWriter, r *http.Request) {
	getDataflowSnapshot(w, sim.PathParam(r, "project"), sim.PathParam(r, "location"), sim.PathParam(r, "snapshot"))
}

func handleDataflowGetSnapshotGlobal(w http.ResponseWriter, r *http.Request) {
	getDataflowSnapshot(w, sim.PathParam(r, "project"), "", sim.PathParam(r, "snapshot"))
}

func getDataflowSnapshot(w http.ResponseWriter, project, location, id string) {
	s, _, ok := findDataflowSnapshot(project, location, id)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "snapshot %q not found", id)
		return
	}
	sim.WriteJSON(w, http.StatusOK, s)
}

func handleDataflowDeleteSnapshot(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	location := sim.PathParam(r, "location")
	id := sim.PathParam(r, "snapshot")
	_, key, ok := findDataflowSnapshot(project, location, id)
	if !ok {
		sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "snapshot %q not found", id)
		return
	}
	dataflowSnapshots.Delete(key)
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}

// handleDataflowDeleteSnapshotsGlobal (projects.deleteSnapshots) deletes a
// snapshot named by the snapshotId query parameter.
func handleDataflowDeleteSnapshotsGlobal(w http.ResponseWriter, r *http.Request) {
	project := sim.PathParam(r, "project")
	id := r.URL.Query().Get("snapshotId")
	if id != "" {
		if _, key, ok := findDataflowSnapshot(project, "", id); ok {
			dataflowSnapshots.Delete(key)
		}
	}
	sim.WriteJSON(w, http.StatusOK, map[string]any{})
}
