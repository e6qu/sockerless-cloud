package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-gcp/shared"
)

// Cloud Run Jobs v2 types

// Job represents a Cloud Run Job resource.
type Job struct {
	Name                   string              `json:"name"`
	UID                    string              `json:"uid"`
	Generation             int64               `json:"generation,string"`
	Labels                 map[string]string   `json:"labels,omitempty"`
	Annotations            map[string]string   `json:"annotations,omitempty"`
	CreateTime             string              `json:"createTime"`
	UpdateTime             string              `json:"updateTime"`
	LaunchStage            enumString          `json:"launchStage,omitempty"`
	Template               *ExecutionTemplate  `json:"template"`
	TerminalCondition      *Condition          `json:"terminalCondition,omitempty"`
	Conditions             []Condition         `json:"conditions,omitempty"`
	LatestCreatedExecution *ExecutionReference `json:"latestCreatedExecution,omitempty"`
	ExecutionCount         int32               `json:"executionCount"`
	Etag                   string              `json:"etag,omitempty"`
	Reconciling            bool                `json:"reconciling"`
}

// ExecutionReference holds a reference to the latest execution of a job.
type ExecutionReference struct {
	Name           string `json:"name"`
	CreateTime     string `json:"createTime"`
	CompletionTime string `json:"completionTime,omitempty"`
}

// RunJobRequest mirrors google.cloud.run.v2.RunJobRequest — the body of
// projects.locations.jobs.run.
type RunJobRequest struct {
	Etag         string     `json:"etag,omitempty"`
	Overrides    *Overrides `json:"overrides,omitempty"`
	ValidateOnly bool       `json:"validateOnly,omitempty"`
}

// CancelExecutionRequest mirrors google.cloud.run.v2.CancelExecutionRequest —
// the body of projects.locations.jobs.executions.cancel.
type CancelExecutionRequest struct {
	Etag         string `json:"etag,omitempty"`
	ValidateOnly bool   `json:"validateOnly,omitempty"`
}

// cloudRunEtagOK enforces the optimistic concurrency an etag-bearing request
// asks for, on any Cloud Run v2 resource that carries one. Every resource whose
// schema declares an etag mints a fresh fingerprint at each store write, so
// `current` is the fingerprint of the version the store holds now. An omitted
// etag acts unconditionally — the member is optional on every request that
// accepts one — a supplied one must match, and a stale one is refused with the
// ABORTED status Cloud Run answers a modification conflict with. `kind` names
// the resource in the message, so a conflict says what moved under the caller.
func cloudRunEtagOK(w http.ResponseWriter, kind, name, current, supplied string) bool {
	if supplied == "" || supplied == current {
		return true
	}
	sim.GCPErrorf(w, http.StatusConflict, "ABORTED",
		"etag %q does not match the current etag of %s %q", supplied, kind, name)
	return false
}

// cloudRunJobEtagOK is cloudRunEtagOK bound to a Job, which several handlers
// check the same way.
func cloudRunJobEtagOK(w http.ResponseWriter, job Job, etag string) bool {
	return cloudRunEtagOK(w, "job", job.Name, job.Etag, etag)
}

// Overrides mirrors google.cloud.run.v2.Overrides — the per-run replacements a
// client applies on top of the job's execution template.
type Overrides struct {
	ContainerOverrides []ContainerOverride `json:"containerOverrides,omitempty"`
	TaskCount          int32               `json:"taskCount,omitempty"`
	Timeout            string              `json:"timeout,omitempty"`
}

// ContainerOverride mirrors google.cloud.run.v2.ContainerOverride. Args replace
// the container's args, env is merged into the container's env, and clearArgs
// empties the args list.
type ContainerOverride struct {
	Name      string   `json:"name,omitempty"`
	Args      []string `json:"args,omitempty"`
	Env       []EnvVar `json:"env,omitempty"`
	ClearArgs bool     `json:"clearArgs,omitempty"`
}

type ExecutionTemplate struct {
	Labels      map[string]string `json:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty"`
	Parallelism int32             `json:"parallelism"`
	TaskCount   int32             `json:"taskCount"`
	Template    *TaskTemplate     `json:"template"`
}

// TaskTemplate holds the template for creating tasks within an execution.
type TaskTemplate struct {
	Containers     []Container `json:"containers,omitempty"`
	Volumes        []Volume    `json:"volumes,omitempty"`
	MaxRetries     int32       `json:"maxRetries"`
	Timeout        string      `json:"timeout,omitempty"`
	ServiceAccount string      `json:"serviceAccount,omitempty"`
}

// Container mirrors google.cloud.run.v2.Container — one container of a
// revision, execution or instance.
type Container struct {
	Name           string                `json:"name,omitempty"`
	Image          string                `json:"image"`
	Command        []string              `json:"command,omitempty"`
	Args           []string              `json:"args,omitempty"`
	Env            []EnvVar              `json:"env,omitempty"`
	Resources      *ResourceRequirements `json:"resources,omitempty"`
	Ports          []ContainerPort       `json:"ports,omitempty"`
	VolumeMounts   []VolumeMount         `json:"volumeMounts,omitempty"`
	WorkingDir     string                `json:"workingDir,omitempty"`
	LivenessProbe  *Probe                `json:"livenessProbe,omitempty"`
	StartupProbe   *Probe                `json:"startupProbe,omitempty"`
	ReadinessProbe *Probe                `json:"readinessProbe,omitempty"`
	DependsOn      []string              `json:"dependsOn,omitempty"`
	BaseImageURI   string                `json:"baseImageUri,omitempty"`
}

// Probe mirrors google.cloud.run.v2.Probe — a health check performed against a
// container to decide whether it is alive or ready to receive traffic. Exactly
// one of HTTPGet, TCPSocket or GRPC carries the action.
type Probe struct {
	InitialDelaySeconds int32            `json:"initialDelaySeconds,omitempty"`
	TimeoutSeconds      int32            `json:"timeoutSeconds,omitempty"`
	PeriodSeconds       int32            `json:"periodSeconds,omitempty"`
	FailureThreshold    int32            `json:"failureThreshold,omitempty"`
	HTTPGet             *HTTPGetAction   `json:"httpGet,omitempty"`
	TCPSocket           *TCPSocketAction `json:"tcpSocket,omitempty"`
	GRPC                *GRPCAction      `json:"grpc,omitempty"`
}

// HTTPGetAction mirrors google.cloud.run.v2.HTTPGetAction.
type HTTPGetAction struct {
	Path        string       `json:"path,omitempty"`
	HTTPHeaders []HTTPHeader `json:"httpHeaders,omitempty"`
	Port        int32        `json:"port,omitempty"`
}

// HTTPHeader mirrors google.cloud.run.v2.HTTPHeader — one custom header sent
// with an HTTP probe.
type HTTPHeader struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// TCPSocketAction mirrors google.cloud.run.v2.TCPSocketAction.
type TCPSocketAction struct {
	Port int32 `json:"port,omitempty"`
}

// GRPCAction mirrors google.cloud.run.v2.GRPCAction.
type GRPCAction struct {
	Port    int32  `json:"port,omitempty"`
	Service string `json:"service,omitempty"`
}

// EnvVar represents an environment variable. Either a literal Value or a
// ValueSource is set; the API models them as alternatives on the same member.
type EnvVar struct {
	Name        string        `json:"name"`
	Value       string        `json:"value"`
	ValueSource *EnvVarSource `json:"valueSource,omitempty"`
}

// EnvVarSource mirrors google.cloud.run.v2.EnvVarSource — where an environment
// variable's value comes from when it is not a literal.
type EnvVarSource struct {
	SecretKeyRef *SecretKeySelector `json:"secretKeyRef,omitempty"`
}

// SecretKeySelector mirrors google.cloud.run.v2.SecretKeySelector — a Cloud
// Secret Manager secret and the version to read.
type SecretKeySelector struct {
	Secret  string `json:"secret"`
	Version string `json:"version,omitempty"`
}

// MarshalJSON writes the proto-JSON form of google.cloud.run.v2.EnvVar's
// `values` oneof: only the member that is set. An environment variable sourced
// from Secret Manager carries `valueSource` and no `value`; a literal one
// carries `value` — including the empty string, which is a literal a client can
// set explicitly and which the oneof therefore still reports.
func (e EnvVar) MarshalJSON() ([]byte, error) {
	wire := struct {
		Name        string        `json:"name"`
		Value       *string       `json:"value,omitempty"`
		ValueSource *EnvVarSource `json:"valueSource,omitempty"`
	}{Name: e.Name}
	if e.ValueSource != nil {
		wire.ValueSource = e.ValueSource
	} else {
		value := e.Value
		wire.Value = &value
	}
	return json.Marshal(wire)
}

func (e *EnvVar) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name        string        `json:"name"`
		Value       string        `json:"value"`
		ValueSource *EnvVarSource `json:"valueSource"`
		Values      *struct {
			Value string `json:"value"`
		} `json:"values"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	e.Name = raw.Name
	e.Value = raw.Value
	e.ValueSource = raw.ValueSource
	if e.Value == "" && raw.Values != nil {
		e.Value = raw.Values.Value
	}
	return nil
}

// ResourceRequirements mirrors google.cloud.run.v2.ResourceRequirements.
type ResourceRequirements struct {
	Limits          map[string]string `json:"limits,omitempty"`
	CPUIdle         bool              `json:"cpuIdle,omitempty"`
	StartupCPUBoost bool              `json:"startupCpuBoost,omitempty"`
}

type ContainerPort struct {
	Name          string `json:"name,omitempty"`
	ContainerPort int32  `json:"containerPort"`
}

// Volume mirrors google.cloud.run.v2.Volume — a volume available to the
// containers of a revision, execution or instance. Exactly one source is set;
// each of the five the API defines round-trips.
type Volume struct {
	Name             string                `json:"name"`
	CloudSQLInstance *CloudSQLInstance     `json:"cloudSqlInstance,omitempty"`
	EmptyDir         *EmptyDirVolumeSource `json:"emptyDir,omitempty"`
	Gcs              *GcsVolumeSource      `json:"gcs,omitempty"`
	Nfs              *NfsVolumeSource      `json:"nfs,omitempty"`
	Secret           *SecretVolumeSource   `json:"secret,omitempty"`
}

// EmptyDirVolumeSource mirrors google.cloud.run.v2.EmptyDirVolumeSource —
// an in-memory or on-disk scratch volume shared by a revision's containers.
type EmptyDirVolumeSource struct {
	Medium    string `json:"medium,omitempty"`
	SizeLimit string `json:"sizeLimit,omitempty"`
}

// CloudSQLInstance mirrors google.cloud.run.v2.CloudSqlInstance — the Cloud
// SQL connections mounted into a revision.
type CloudSQLInstance struct {
	Instances []string `json:"instances,omitempty"`
}

// GcsVolumeSource mirrors google.cloud.run.v2.GCSVolumeSource.
type GcsVolumeSource struct {
	Bucket       string   `json:"bucket"`
	ReadOnly     bool     `json:"readOnly,omitempty"`
	MountOptions []string `json:"mountOptions,omitempty"`
}

// NfsVolumeSource mirrors google.cloud.run.v2.NFSVolumeSource.
type NfsVolumeSource struct {
	Server   string `json:"server"`
	Path     string `json:"path"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// SecretVolumeSource mirrors google.cloud.run.v2.SecretVolumeSource.
type SecretVolumeSource struct {
	Secret      string          `json:"secret"`
	DefaultMode int32           `json:"defaultMode,omitempty"`
	Items       []VersionToPath `json:"items,omitempty"`
}

// VersionToPath mirrors google.cloud.run.v2.VersionToPath — one secret
// version projected onto a path inside a secret volume.
type VersionToPath struct {
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	Mode    int32  `json:"mode,omitempty"`
}

// VolumeMount mirrors google.cloud.run.v2.VolumeMount.
type VolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath,omitempty"`
}

// BinaryAuthorization mirrors google.cloud.run.v2.BinaryAuthorization — the
// deploy-time image-attestation settings a Cloud Run resource carries.
type BinaryAuthorization struct {
	BreakglassJustification string `json:"breakglassJustification,omitempty"`
	Policy                  string `json:"policy,omitempty"`
	UseDefault              bool   `json:"useDefault,omitempty"`
}

// ServiceMesh mirrors google.cloud.run.v2.ServiceMesh — the Cloud Service Mesh
// a revision joins.
type ServiceMesh struct {
	Mesh string `json:"mesh,omitempty"`
}

// Execution represents a Cloud Run Job execution.
type Execution struct {
	Name           string            `json:"name"`
	UID            string            `json:"uid"`
	Generation     int64             `json:"generation,string"`
	Labels         map[string]string `json:"labels,omitempty"`
	Parallelism    int32             `json:"parallelism"`
	CreateTime     string            `json:"createTime"`
	StartTime      string            `json:"startTime,omitempty"`
	CompletionTime string            `json:"completionTime,omitempty"`
	RunningCount   int32             `json:"runningCount"`
	SucceededCount int32             `json:"succeededCount"`
	FailedCount    int32             `json:"failedCount"`
	CancelledCount int32             `json:"cancelledCount"`
	Conditions     []Condition       `json:"conditions,omitempty"`
	TaskCount      int32             `json:"taskCount"`
	Template       *TaskTemplate     `json:"template,omitempty"`
	Etag           string            `json:"etag,omitempty"`
	Reconciling    bool              `json:"reconciling"`
}

// Task represents a single google.cloud.run.v2.Task — one attempt of one
// unit of work within an Execution. Cloud Run materializes TaskCount tasks
// per execution; the sim records them so the
// jobs.executions.tasks get/list endpoints return faithful data.
type Task struct {
	Name              string             `json:"name"`
	UID               string             `json:"uid"`
	Generation        int64              `json:"generation,string"`
	Labels            map[string]string  `json:"labels,omitempty"`
	CreateTime        string             `json:"createTime"`
	ScheduledTime     string             `json:"scheduledTime,omitempty"`
	StartTime         string             `json:"startTime,omitempty"`
	CompletionTime    string             `json:"completionTime,omitempty"`
	Job               string             `json:"job,omitempty"`
	Execution         string             `json:"execution,omitempty"`
	Containers        []Container        `json:"containers,omitempty"`
	Volumes           []Volume           `json:"volumes,omitempty"`
	MaxRetries        int32              `json:"maxRetries"`
	Timeout           string             `json:"timeout,omitempty"`
	ServiceAccount    string             `json:"serviceAccount,omitempty"`
	Index             int32              `json:"index"`
	Retried           int32              `json:"retried"`
	LastAttemptResult *TaskAttemptResult `json:"lastAttemptResult,omitempty"`
	Conditions        []Condition        `json:"conditions,omitempty"`
	Etag              string             `json:"etag,omitempty"`
	Reconciling       bool               `json:"reconciling"`
}

// TaskAttemptResult mirrors google.cloud.run.v2.TaskAttemptResult.
type TaskAttemptResult struct {
	Status   *RPCStatus `json:"status,omitempty"`
	ExitCode int32      `json:"exitCode"`
}

// RPCStatus mirrors google.rpc.Status as proto-JSON.
type RPCStatus struct {
	Code    int32  `json:"code"`
	Message string `json:"message,omitempty"`
}

// Condition represents a status condition on a resource. State is
// proto-JSON: real run/apiv2 REST clients serialize the enum as a
// number on PATCH (e.g. `"state": 2` for CONDITION_SUCCEEDED), so
// enumString accepts both forms.
type Condition struct {
	Type               string     `json:"type"`
	State              enumString `json:"state"`
	Message            string     `json:"message,omitempty"`
	LastTransitionTime string     `json:"lastTransitionTime,omitempty"`
	Reason             string     `json:"reason,omitempty"`
}

// Operation represents a long-running operation. Kind and SelfLink are the two
// members Cloud Storage adds to google.longrunning.Operation in its own
// GoogleLongrunningOperation schema ("storage#operation" and the link to the
// record); every other service's document declares neither, so they are omitted
// there.
type Operation struct {
	Name     string          `json:"name"`
	Metadata map[string]any  `json:"metadata,omitempty"`
	Done     bool            `json:"done"`
	Response any             `json:"response,omitempty"`
	Error    *OperationError `json:"error,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	SelfLink string          `json:"selfLink,omitempty"`
}

// OperationError represents an error from a long-running operation.
type OperationError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func generateUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// newLRO creates a completed Long-Running Operation and persists it so
// subsequent GET /operations/{op} polls return the same record. The sim does
// no asynchronous work, so the operation is always returned with `done=true`
// and the embedded response — that matches what real Cloud Run returns once
// the underlying resource has settled.
func newLRO(project, location string, resource any, typeName string) Operation {
	opID := generateUUID()
	// Convert resource to a map and add @type for protobuf Any compatibility.
	// GCP REST clients expect the response field to be a google.protobuf.Any
	// which requires @type in the JSON representation.
	//
	// resource is always an in-process struct we declared; Marshal can only
	// fail on unsupported types (chan/func/unsafe.Pointer). Surface that
	// loudly rather than silently emitting an empty Operation — a regression
	// here would be invisible in production and break every consuming SDK.
	var responseMap map[string]any
	if resource != nil {
		data, err := json.Marshal(resource)
		if err != nil {
			panic(fmt.Errorf("newLRO: marshal %s resource: %w", typeName, err))
		}
		if err := json.Unmarshal(data, &responseMap); err != nil {
			panic(fmt.Errorf("newLRO: unmarshal %s response to map: %w", typeName, err))
		}
		if responseMap == nil {
			responseMap = map[string]any{}
		}
		responseMap["@type"] = typeName
	} else {
		responseMap = map[string]any{"@type": typeName}
	}

	// Derive target from the resource's name field if available
	var target string
	if n, ok := responseMap["name"].(string); ok {
		target = n
	}

	metadataMap := map[string]any{
		"@type":      gcpOperationMetadataType(typeName),
		"createTime": nowTimestamp(),
		"target":     target,
	}
	if strings.HasPrefix(typeName, "type.googleapis.com/google.cloud.run.v2.") {
		metadataMap = cloneAnyMap(responseMap)
	}

	op := Operation{
		Name:     fmt.Sprintf("projects/%s/locations/%s/operations/%s", project, location, opID),
		Metadata: metadataMap,
		Done:     true,
		Response: responseMap,
	}
	if crOperations != nil {
		crOperations.Put(op.Name, op)
	}
	return op
}

func renameGCPOperation(op Operation, collection string) Operation {
	oldName := op.Name
	opID := oldName[strings.LastIndex(oldName, "/")+1:]
	op.Name = strings.TrimRight(collection, "/") + "/" + opID
	if crOperations != nil {
		crOperations.Delete(oldName)
		crOperations.Put(op.Name, op)
	}
	return op
}

func cloneAnyMap(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

// Container handle tracker for Cloud Run Jobs real execution
var crjProcessHandles sync.Map // map[execName]*cloudRunJobProcesses

type cloudRunJobProcesses struct {
	Main     *sim.ContainerHandle
	Sidecars []*sim.ContainerHandle
}

func stopCloudRunJobProcesses(p *cloudRunJobProcesses) {
	if p == nil {
		return
	}
	if p.Main != nil {
		sim.StopContainer(p.Main.ContainerID)
		p.Main.Cancel()
	}
	for _, h := range p.Sidecars {
		if h != nil {
			h.Cancel()
		}
	}
}

// crOperations holds long-running Operation records so the SDK can
// `GetOperation` against the LRO returned by Create/Run/Delete, matching
// real Cloud Run. Unknown ops 404 instead of synthetic done=true.
var crOperations sim.Store[Operation]

// recoverCloudRunJobExecutions settles persisted executions that still claim
// running tasks but whose workload process handles are absent. The processes a
// Cloud Run Job execution runs die with the simulator process, so an execution
// that was running before a control-plane restart can never complete on its
// own; leaving it with runningCount > 0 would make clients poll forever.
// Cloud Run reports such an execution as completed-failed (failedCount set,
// completionTime stamped, terminal Completed condition CONDITION_FAILED), and
// the sim does the same, with an honest message about the lost workload.
func recoverCloudRunJobExecutions(jobs sim.Store[Job], executions sim.Store[Execution], tasks sim.Store[Task]) {
	const message = "Workload containers not found after control-plane restart"
	for _, exec := range executions.List() {
		if exec.RunningCount == 0 {
			continue
		}
		if _, ok := crjProcessHandles.Load(exec.Name); ok {
			continue
		}
		completionTime := nowTimestamp()
		transitioned := false
		executions.Update(exec.Name, func(e *Execution) {
			if e.RunningCount == 0 {
				return
			}
			transitioned = true
			e.CompletionTime = completionTime
			e.FailedCount += e.RunningCount
			e.RunningCount = 0
			e.Conditions = []Condition{
				{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: completionTime, Message: message},
				{Type: "Completed", State: "CONDITION_FAILED", LastTransitionTime: completionTime, Message: message},
			}
			e.Reconciling = false
			e.Etag = generateUUID()
		})
		if !transitioned {
			continue
		}
		taskPrefix := exec.Name + "/tasks/"
		for _, tk := range tasks.Filter(func(t Task) bool {
			return strings.HasPrefix(t.Name, taskPrefix) && t.CompletionTime == ""
		}) {
			tasks.Update(tk.Name, func(t *Task) {
				t.CompletionTime = completionTime
				t.Conditions = []Condition{
					{Type: "Started", State: "CONDITION_SUCCEEDED", LastTransitionTime: completionTime},
					{Type: "Completed", State: "CONDITION_FAILED", LastTransitionTime: completionTime, Message: message},
				}
				t.Reconciling = false
				t.Etag = generateUUID()
			})
		}
		if jobName, _, ok := strings.Cut(exec.Name, "/executions/"); ok {
			jobs.Update(jobName, func(j *Job) {
				if j.LatestCreatedExecution != nil && j.LatestCreatedExecution.Name == exec.Name && j.LatestCreatedExecution.CompletionTime == "" {
					j.LatestCreatedExecution.CompletionTime = completionTime
					j.Etag = generateUUID()
				}
			})
		}
		fmt.Fprintf(os.Stderr, "[sim-cloudrun] execution %s: workload processes not found after control-plane restart; transitioned to failed\n", exec.Name)
	}
}

// crjJobs, crjExecutions and crjTasks are the Cloud Run jobs stores. A job,
// its executions and its tasks are each one resource addressable through two
// API versions — the v2 resource-oriented surface and the v1 Knative
// `namespaces` surface — so the stores are package-scoped rather than closed
// over by the v2 handlers alone, and the v1 handlers project the same records
// rather than keeping their own.
var (
	crjJobs       sim.Store[Job]
	crjExecutions sim.Store[Execution]
	crjTasks      sim.Store[Task]
)

func registerCloudRunJobs(srv *sim.Server) {
	jobs := sim.MakeStore[Job](srv.DB(), "crj_jobs")
	executions := sim.MakeStore[Execution](srv.DB(), "crj_executions")
	tasks := sim.MakeStore[Task](srv.DB(), "crj_tasks")
	crjJobs, crjExecutions, crjTasks = jobs, executions, tasks
	if crOperations == nil {
		crOperations = sim.MakeStore[Operation](srv.DB(), "operations")
	}
	recoverCloudRunJobExecutions(jobs, executions, tasks)

	// Create job
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/jobs", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := r.URL.Query().Get("jobId")
		if jobID == "" {
			sim.GCPError(w, http.StatusBadRequest, "jobId query parameter is required", "INVALID_ARGUMENT")
			return
		}

		var job Job
		if err := sim.ReadJSON(r, &job); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
		if _, exists := jobs.Get(name); exists {
			sim.GCPErrorf(w, http.StatusConflict, "ALREADY_EXISTS", "job %q already exists", name)
			return
		}

		now := nowTimestamp()
		job.Name = name
		job.UID = generateUUID()
		job.Generation = 1
		job.CreateTime = now
		job.UpdateTime = now
		if job.LaunchStage == "" {
			job.LaunchStage = "GA"
		}
		// The sim has no real reconciliation to do (no image pull, no
		// IAM check, no infra plumbing) — so the resource settles to
		// CONDITION_SUCCEEDED the moment it is stored. Real Cloud Run
		// transitions through CONDITION_RECONCILING only because actual
		// work takes time; injecting a synthetic delay just to mimic the
		// shape would be exactly the kind of fake behaviour this audit
		// is about removing.
		job.TerminalCondition = &Condition{
			Type:               "Ready",
			State:              "CONDITION_SUCCEEDED",
			LastTransitionTime: now,
		}
		job.Conditions = []Condition{
			{Type: "ConfigurationsReady", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: now},
		}
		job.Reconciling = false

		// Set defaults on template
		if job.Template != nil {
			if job.Template.Parallelism == 0 {
				job.Template.Parallelism = 1
			}
			if job.Template.TaskCount == 0 {
				job.Template.TaskCount = 1
			}
		}
		// The etag is the fingerprint of this version of the resource, so a
		// fresh one is minted for every version the store holds.
		job.Etag = generateUUID()

		jobs.Put(name, job)

		lro := newLRO(project, location, job, "type.googleapis.com/google.cloud.run.v2.Job")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// Get job
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs/{job}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")

		// Reject if the path has extra segments (executions)
		if strings.Contains(r.URL.Path, "/executions") {
			return // let the executions handler deal with it
		}

		// The {job} wildcard also carries the GET-side IAM verb
		// `{job}:getIamPolicy` (Go's mux can't spell `{id}:verb`); split
		// on the colon and dispatch to the shared IAM handler.
		if id, action, found := strings.Cut(jobID, ":"); found {
			if action == "getIamPolicy" {
				handleResourceIAM(w, r, gcpResourceIAMStore(),
					fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, id), action)
				return
			}
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on job %q", action, id)
			return
		}

		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
		job, ok := jobs.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, job)
	})

	// List jobs
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		prefix := fmt.Sprintf("projects/%s/locations/%s/jobs/", project, location)

		result := jobs.Filter(func(j Job) bool {
			return strings.HasPrefix(j.Name, prefix)
		})
		if result == nil {
			result = []Job{}
		}
		sortCloudRunJobs(result)
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}

		resp := map[string]any{"jobs": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// Delete job
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/jobs/{job}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)

		job, ok := jobs.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", name)
			return
		}
		if !cloudRunJobEtagOK(w, job, r.URL.Query().Get("etag")) {
			return
		}

		jobs.Delete(name)

		// Also delete associated executions
		execs := executions.Filter(func(e Execution) bool {
			return strings.HasPrefix(e.Name, name+"/executions/")
		})
		for _, e := range execs {
			executions.Delete(e.Name)
		}
		// Also delete tasks belonging to those executions.
		for _, t := range tasks.Filter(func(t Task) bool {
			return strings.HasPrefix(t.Name, name+"/executions/")
		}) {
			tasks.Delete(t.Name)
		}

		lro := newLRO(project, location, job, "type.googleapis.com/google.cloud.run.v2.Job")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// RunJob and the POST-side IAM verbs all arrive as
	// POST .../jobs/{job}:<verb>; the verb selects the method.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/jobs/{jobAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobAction := sim.PathParam(r, "jobAction")
		jobID, action, found := strings.Cut(jobAction, ":")
		if !found {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action on job %q", jobAction)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)

		switch action {
		case "setIamPolicy", "testIamPermissions":
			cloudRunV1JobIAM(w, r, project, location, jobID, action)
			return
		case "run":
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "unknown action %q on job %q", action, jobID)
			return
		}

		job, ok := jobs.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", name)
			return
		}

		var request RunJobRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !cloudRunJobEtagOK(w, job, request.Etag) {
			return
		}
		if request.ValidateOnly {
			// The request validated and nothing was created, so the operation
			// carries no resource: reporting an Execution that does not exist
			// would be the fake this simulator refuses to serve.
			lro := newLRO(project, location, nil, "type.googleapis.com/google.cloud.run.v2.Execution")
			sim.WriteJSON(w, http.StatusOK, lro)
			return
		}

		exec := runCloudRunJob(project, location, jobID, job, request.Overrides)
		lro := newLRO(project, location, exec, "type.googleapis.com/google.cloud.run.v2.Execution")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// Get execution
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		execID := sim.PathParam(r, "execution")
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s", project, location, jobID, execID)

		exec, ok := executions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "execution %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, exec)
	})

	// List executions
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		prefix := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/", project, location, jobID)

		result := executions.Filter(func(e Execution) bool {
			return strings.HasPrefix(e.Name, prefix)
		})
		if result == nil {
			result = []Execution{}
		}
		sortCloudRunExecutions(result)
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}

		resp := map[string]any{"executions": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})

	// The custom methods the executions collection publishes. Cloud Run v2
	// declares exactly one — executions.cancel — so every other verb reaching
	// this fan-in names a method the service does not serve and is refused as
	// such rather than silently cancelling the execution.
	srv.HandleFunc("POST /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execAction}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		execID, action, _ := strings.Cut(sim.PathParam(r, "execAction"), ":")
		switch action {
		case "cancel":
		default:
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND",
				"unknown action %q on execution %q", action, execID)
			return
		}
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s", project, location, jobID, execID)

		var request CancelExecutionRequest
		if err := sim.ReadJSON(r, &request); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		existing, ok := executions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "execution %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "execution", existing.Name, existing.Etag, request.Etag) {
			return
		}
		if request.ValidateOnly {
			// The request validated and nothing was cancelled, so the
			// operation carries no resource.
			lro := newLRO(project, location, nil, "type.googleapis.com/google.cloud.run.v2.Execution")
			sim.WriteJSON(w, http.StatusOK, lro)
			return
		}

		exec, ok := cancelCloudRunExecution(project, location, jobID, execID)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "execution %q not found", name)
			return
		}
		lro := newLRO(project, location, exec, "type.googleapis.com/google.cloud.run.v2.Execution")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// UpdateJob: PATCH /v2/projects/{project}/locations/{location}/jobs/{job}
	srv.HandleFunc("PATCH /v2/projects/{project}/locations/{location}/jobs/{job}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
		existing, ok := jobs.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "job %q not found", name)
			return
		}
		var update Job
		if err := sim.ReadJSON(r, &update); err != nil {
			sim.GCPErrorf(w, http.StatusBadRequest, "INVALID_ARGUMENT", "invalid request body: %v", err)
			return
		}
		if !cloudRunJobEtagOK(w, existing, update.Etag) {
			return
		}
		// UpdateJob has no updateMask parameter — the full mutable resource is
		// replaced. Preserve identity + server-owned fields.
		update.Name = existing.Name
		update.UID = existing.UID
		update.CreateTime = existing.CreateTime
		update.Generation = existing.Generation + 1
		update.UpdateTime = nowTimestamp()
		update.ExecutionCount = existing.ExecutionCount
		update.LatestCreatedExecution = existing.LatestCreatedExecution
		if update.LaunchStage == "" {
			update.LaunchStage = existing.LaunchStage
		}
		if update.Template != nil {
			if update.Template.Parallelism == 0 {
				update.Template.Parallelism = 1
			}
			if update.Template.TaskCount == 0 {
				update.Template.TaskCount = 1
			}
		}
		update.TerminalCondition = &Condition{
			Type:               "Ready",
			State:              "CONDITION_SUCCEEDED",
			LastTransitionTime: update.UpdateTime,
		}
		update.Conditions = []Condition{
			{Type: "ConfigurationsReady", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
			{Type: "Ready", State: "CONDITION_SUCCEEDED", LastTransitionTime: update.UpdateTime},
		}
		update.Reconciling = false
		update.Etag = generateUUID()
		jobs.Put(name, update)
		lro := newLRO(project, location, update, "type.googleapis.com/google.cloud.run.v2.Job")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// DeleteExecution: DELETE /v2/.../jobs/{job}/executions/{execution}
	srv.HandleFunc("DELETE /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		execID := sim.PathParam(r, "execution")
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s", project, location, jobID, execID)
		exec, ok := executions.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "execution %q not found", name)
			return
		}
		if !cloudRunEtagOK(w, "execution", exec.Name, exec.Etag, r.URL.Query().Get("etag")) {
			return
		}
		executions.Delete(name)
		taskPrefix := name + "/tasks/"
		for _, tk := range tasks.Filter(func(t Task) bool { return strings.HasPrefix(t.Name, taskPrefix) }) {
			tasks.Delete(tk.Name)
		}
		lro := newLRO(project, location, exec, "type.googleapis.com/google.cloud.run.v2.Execution")
		sim.WriteJSON(w, http.StatusOK, lro)
	})

	// GetTask: GET /v2/.../jobs/{job}/executions/{execution}/tasks/{task}
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks/{task}", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		execID := sim.PathParam(r, "execution")
		taskID := sim.PathParam(r, "task")
		name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s/tasks/%s", project, location, jobID, execID, taskID)
		task, ok := tasks.Get(name)
		if !ok {
			sim.GCPErrorf(w, http.StatusNotFound, "NOT_FOUND", "task %q not found", name)
			return
		}
		sim.WriteJSON(w, http.StatusOK, task)
	})

	// ListTasks: GET /v2/.../jobs/{job}/executions/{execution}/tasks
	srv.HandleFunc("GET /v2/projects/{project}/locations/{location}/jobs/{job}/executions/{execution}/tasks", func(w http.ResponseWriter, r *http.Request) {
		project := sim.PathParam(r, "project")
		location := sim.PathParam(r, "location")
		jobID := sim.PathParam(r, "job")
		execID := sim.PathParam(r, "execution")
		prefix := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s/tasks/", project, location, jobID, execID)
		result := tasks.Filter(func(t Task) bool { return strings.HasPrefix(t.Name, prefix) })
		if result == nil {
			result = []Task{}
		}
		sort.Slice(result, func(i, j int) bool { return result[i].Index < result[j].Index })
		page, next, ok := paginateList(w, r, result)
		if !ok {
			return
		}
		resp := map[string]any{"tasks": page}
		if next != "" {
			resp["nextPageToken"] = next
		}
		sim.WriteJSON(w, http.StatusOK, resp)
	})
}

// runCloudRunJob starts one execution of a Cloud Run job: it materializes the
// Execution and the TaskCount Tasks the execution owns, launches the workload
// containers, and settles the execution and its tasks from the real container
// outcome. It returns the Execution as it exists the moment the run was
// accepted — running, with the workload in flight — which is what both API
// versions' run methods report.
//
// The Execution and Task records it writes are the ones every reader sees:
// the v2 collection reads them directly and the v1 Knative surface projects
// them, so there is exactly one execution lifecycle behind both spellings.
func runCloudRunJob(project, location, jobID string, job Job, overrides *Overrides) Execution {
	name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
	now := nowTimestamp()
	execName := fmt.Sprintf("%s/executions/%s", name, generateUUID())

	var taskCount int32 = 1
	var parallelism int32 = 1
	var tmpl *TaskTemplate
	if job.Template != nil {
		taskCount = job.Template.TaskCount
		if taskCount == 0 {
			taskCount = 1
		}
		parallelism = job.Template.Parallelism
		if parallelism == 0 {
			parallelism = 1
		}
		tmpl = job.Template.Template
	}
	if overrides != nil {
		if overrides.TaskCount > 0 {
			taskCount = overrides.TaskCount
		}
		tmpl = applyCloudRunJobOverrides(tmpl, overrides)
	}

	exec := Execution{
		Name:         execName,
		UID:          generateUUID(),
		Generation:   1,
		Labels:       job.Labels,
		Parallelism:  parallelism,
		CreateTime:   now,
		StartTime:    now,
		RunningCount: taskCount,
		TaskCount:    taskCount,
		Template:     tmpl,
		Conditions: []Condition{
			{Type: "Ready", State: "CONDITION_PENDING", LastTransitionTime: now},
		},
		Etag:        generateUUID(),
		Reconciling: true,
	}
	crjExecutions.Put(execName, exec)

	// Materialize one Task per index, mirroring how Cloud Run creates
	// TaskCount tasks when an execution starts.
	for i := int32(0); i < taskCount; i++ {
		taskName := fmt.Sprintf("%s/tasks/%s", execName, generateUUID())
		task := Task{
			Name:       taskName,
			UID:        generateUUID(),
			Generation: 1,
			Labels:     job.Labels,
			CreateTime: now,
			StartTime:  now,
			Job:        name,
			Execution:  execName,
			Index:      i,
			Conditions: []Condition{
				{Type: "Started", State: "CONDITION_PENDING", LastTransitionTime: now},
			},
			Etag:        generateUUID(),
			Reconciling: true,
		}
		if tmpl != nil {
			task.Containers = tmpl.Containers
			task.Volumes = tmpl.Volumes
			task.MaxRetries = tmpl.MaxRetries
			task.Timeout = tmpl.Timeout
			task.ServiceAccount = tmpl.ServiceAccount
		}
		crjTasks.Put(taskName, task)
	}

	injectCloudRunJobLog(project, jobID, "Container started")
	go settleCloudRunJobExecution(execName, taskCount, project, jobID, tmpl)

	crjJobs.Update(name, func(j *Job) {
		j.ExecutionCount++
		j.UpdateTime = now
		j.LatestCreatedExecution = &ExecutionReference{
			Name:       execName,
			CreateTime: now,
		}
		j.Etag = generateUUID()
	})
	return exec
}

// applyCloudRunJobOverrides returns the task template one run should use: the
// job's own template with the request's per-run replacements applied. The job
// resource is untouched — an override lasts for the execution it was sent
// with, which is what makes it an override rather than an update.
//
// Per google.cloud.run.v2.ContainerOverride: args replace the container's
// args, clearArgs empties them, and env is merged into the container's env
// (a name present in both takes the override's value). A container override
// with no name addresses the job's first container, the way a single-container
// job is addressed everywhere else in the API.
func applyCloudRunJobOverrides(template *TaskTemplate, overrides *Overrides) *TaskTemplate {
	if template == nil || overrides == nil {
		return template
	}
	merged := *template
	merged.Containers = append([]Container(nil), template.Containers...)
	if overrides.Timeout != "" {
		merged.Timeout = overrides.Timeout
	}
	for _, override := range overrides.ContainerOverrides {
		index := -1
		for i, container := range merged.Containers {
			if container.Name == override.Name {
				index = i
				break
			}
		}
		if index < 0 && override.Name == "" && len(merged.Containers) > 0 {
			index = 0
		}
		if index < 0 {
			continue
		}
		container := merged.Containers[index]
		switch {
		case override.ClearArgs:
			container.Args = nil
		case len(override.Args) > 0:
			container.Args = append([]string(nil), override.Args...)
		}
		if len(override.Env) > 0 {
			env := append([]EnvVar(nil), container.Env...)
			for _, entry := range override.Env {
				replaced := false
				for i := range env {
					if env[i].Name == entry.Name {
						env[i] = entry
						replaced = true
						break
					}
				}
				if !replaced {
					env = append(env, entry)
				}
			}
			container.Env = env
		}
		merged.Containers[index] = container
	}
	return &merged
}

// settleCloudRunJobExecution runs the execution's workload containers to
// completion and transitions the execution, its tasks and the owning job's
// latest-execution reference from the real container outcome.
func settleCloudRunJobExecution(execName string, taskCount int32, project, jobID string, taskTmpl *TaskTemplate) {
	timeout := 600 * time.Second // GCP default
	if taskTmpl != nil && taskTmpl.Timeout != "" {
		if d, err := time.ParseDuration(taskTmpl.Timeout); err == nil {
			timeout = d
		}
	}

	succeeded := true
	if taskTmpl != nil && len(taskTmpl.Containers) > 0 {
		sink := &crjLogSink{project: project, jobName: jobID}
		execShort := execName
		if parts := strings.Split(execName, "/"); len(parts) > 0 {
			last := parts[len(parts)-1]
			if len(last) > 12 {
				execShort = last[:12]
			} else {
				execShort = last
			}
		}
		handle, sidecars, err := startCloudRunJobContainers(execName, execShort, taskTmpl, timeout, sink)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ERROR: failed to start containers for execution: err=%v\n", err)
			succeeded = false
		} else {
			crjProcessHandles.Store(execName, &cloudRunJobProcesses{Main: handle, Sidecars: sidecars})
			result := handle.Wait()
			crjProcessHandles.Delete(execName)
			for _, h := range sidecars {
				h.Cancel()
			}
			succeeded = result.ExitCode == 0
		}
	}

	completed := false
	crjExecutions.Update(execName, func(e *Execution) {
		if e.RunningCount == 0 {
			return
		}
		completed = true
		completionTime := nowTimestamp()
		e.CompletionTime = completionTime
		e.RunningCount = 0
		if succeeded {
			e.SucceededCount = taskCount
		} else {
			e.FailedCount = taskCount
		}
		state := "CONDITION_SUCCEEDED"
		reason := ""
		if !succeeded {
			state = "CONDITION_FAILED"
			reason = "NonZeroExitCode"
		}
		e.Conditions = []Condition{
			{Type: "Ready", State: enumString(state), LastTransitionTime: completionTime, Reason: reason},
			{Type: "Completed", State: enumString(state), LastTransitionTime: completionTime, Reason: reason},
		}
		e.Reconciling = false
		e.Etag = generateUUID()
	})
	if !completed {
		return
	}

	// Settle each task to match the execution outcome.
	taskState := "CONDITION_SUCCEEDED"
	taskReason := ""
	var exitCode int32
	if !succeeded {
		taskState = "CONDITION_FAILED"
		taskReason = "NonZeroExitCode"
		exitCode = 1
	}
	completionTime := nowTimestamp()
	taskPrefix := execName + "/tasks/"
	for _, tk := range crjTasks.Filter(func(t Task) bool { return strings.HasPrefix(t.Name, taskPrefix) }) {
		crjTasks.Update(tk.Name, func(t *Task) {
			t.CompletionTime = completionTime
			t.Conditions = []Condition{
				{Type: "Started", State: "CONDITION_SUCCEEDED", LastTransitionTime: completionTime},
				{Type: "Completed", State: enumString(taskState), LastTransitionTime: completionTime, Reason: taskReason},
			}
			t.LastAttemptResult = &TaskAttemptResult{
				Status:   &RPCStatus{Code: exitCode},
				ExitCode: exitCode,
			}
			t.Reconciling = false
			t.Etag = generateUUID()
		})
	}
	if jobKey, _, ok := strings.Cut(execName, "/executions/"); ok {
		crjJobs.Update(jobKey, func(j *Job) {
			if j.LatestCreatedExecution != nil && j.LatestCreatedExecution.Name == execName {
				j.LatestCreatedExecution.CompletionTime = nowTimestamp()
				j.Etag = generateUUID()
			}
		})
	}
	// The log line matches the actual outcome: a failed execution must not
	// report success in the log stream a client tails.
	if succeeded {
		injectCloudRunJobLog(project, jobID, "Execution completed successfully")
	} else {
		injectCloudRunJobLog(project, jobID, "Execution failed")
	}
}

// cancelCloudRunExecution cancels a running execution: it stops the workload
// containers the execution owns and settles the execution record and the
// owning job's latest-execution reference. Stopping the containers is the
// point — a cancel that only rewrote the record would leave a container
// running behind an execution both API versions report as cancelled.
func cancelCloudRunExecution(project, location, jobID, execID string) (Execution, bool) {
	name := fmt.Sprintf("projects/%s/locations/%s/jobs/%s/executions/%s", project, location, jobID, execID)

	if v, ok := crjProcessHandles.LoadAndDelete(name); ok {
		if procs, ok := v.(*cloudRunJobProcesses); ok {
			stopCloudRunJobProcesses(procs)
		}
	}

	ok := crjExecutions.Update(name, func(e *Execution) {
		now := nowTimestamp()
		e.CompletionTime = now
		e.CancelledCount = e.RunningCount
		e.RunningCount = 0
		// A cancelled execution did not complete successfully: its terminal
		// condition is the failed one, carrying the reason that says why. A
		// client polling the cancel — `gcloud run jobs executions cancel`
		// does exactly this — reads a succeeded terminal condition as "the
		// execution finished before the cancel landed".
		e.Conditions = []Condition{
			{Type: "Ready", State: "CONDITION_FAILED", LastTransitionTime: now, Reason: "Cancelled"},
			{Type: "Completed", State: "CONDITION_FAILED", LastTransitionTime: now, Reason: "Cancelled"},
		}
		e.Reconciling = false
		e.Etag = generateUUID()
	})
	if !ok {
		return Execution{}, false
	}

	jobName := fmt.Sprintf("projects/%s/locations/%s/jobs/%s", project, location, jobID)
	crjJobs.Update(jobName, func(j *Job) {
		if j.LatestCreatedExecution != nil && j.LatestCreatedExecution.Name == name {
			j.LatestCreatedExecution.CompletionTime = nowTimestamp()
			j.Etag = generateUUID()
		}
	})
	injectCloudRunJobLog(project, jobID, "Execution cancelled")

	exec, _ := crjExecutions.Get(name)
	return exec, true
}

func startCloudRunJobContainers(execID, execShort string, taskTmpl *TaskTemplate, timeout time.Duration, sink sim.LogSink) (*sim.ContainerHandle, []*sim.ContainerHandle, error) {
	if taskTmpl == nil || len(taskTmpl.Containers) == 0 {
		return nil, nil, fmt.Errorf("execution has no containers")
	}

	volByName := make(map[string]Volume)
	for _, v := range taskTmpl.Volumes {
		volByName[v.Name] = v
	}
	bindsFor := func(c Container) []string {
		var binds []string
		for _, mp := range c.VolumeMounts {
			v, ok := volByName[mp.Name]
			if !ok || v.Gcs == nil || v.Gcs.Bucket == "" {
				continue
			}
			bind := GCSBucketHostDir(v.Gcs.Bucket) + ":" + mp.MountPath
			if v.Gcs.ReadOnly {
				bind += ":ro"
			}
			binds = append(binds, bind)
		}
		return binds
	}
	envFor := func(c Container) map[string]string {
		cmdEnv := make(map[string]string, len(c.Env))
		for _, ev := range c.Env {
			cmdEnv[ev.Name] = ev.Value
		}
		return mergeEnv(cmdEnv, hostMetadataEnv())
	}

	main := taskTmpl.Containers[0]
	mainImage := sim.ResolveLocalImage(main.Image)
	mainPlatform, err := localImagePlatform(context.Background(), mainImage)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve main container %q image platform: %w", main.Name, err)
	}
	mainHandle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        mainImage,
		Architecture: mainPlatform,
		Command:      main.Command,
		Args:         main.Args,
		Env:          envFor(main),
		Timeout:      timeout,
		Name:         fmt.Sprintf("sockerless-sim-gcp-job-%s", execShort),
		Labels: map[string]string{
			"sockerless-sim-execution":           execID,
			"sockerless-sim-execution-container": main.Name,
		},
		Binds:      bindsFor(main),
		ExtraHosts: hostMetadataExtraHosts(),
		Sandbox:    sim.SandboxCloudRun,
	}, sink)
	if err != nil {
		return nil, nil, fmt.Errorf("start main container %q: %w", main.Name, err)
	}

	var sidecars []*sim.ContainerHandle
	for i, c := range taskTmpl.Containers[1:] {
		sidecarImage := sim.ResolveLocalImage(c.Image)
		sidecarPlatform, err := localImagePlatform(context.Background(), sidecarImage)
		if err != nil {
			mainHandle.Cancel()
			for _, h := range sidecars {
				h.Cancel()
			}
			return nil, nil, fmt.Errorf("resolve sidecar container %q image platform: %w", c.Name, err)
		}
		handle, err := sim.StartContainerSync(sim.ContainerConfig{
			Image:        sidecarImage,
			Architecture: sidecarPlatform,
			Command:      c.Command,
			Args:         c.Args,
			Env:          envFor(c),
			Timeout:      timeout,
			Name:         fmt.Sprintf("sockerless-sim-gcp-job-%s-sidecar-%d", execShort, i),
			Labels: map[string]string{
				"sockerless-sim-execution":           execID,
				"sockerless-sim-execution-container": c.Name,
			},
			NetworkMode: "container:" + mainHandle.ContainerID,
			Binds:       bindsFor(c),
			Sandbox:     sim.SandboxCloudRun,
		}, sink)
		if err != nil {
			mainHandle.Cancel()
			for _, h := range sidecars {
				h.Cancel()
			}
			return nil, nil, fmt.Errorf("start sidecar container %q: %w", c.Name, err)
		}
		sidecars = append(sidecars, handle)
	}
	return mainHandle, sidecars, nil
}

// injectCloudRunJobLog writes a log entry to the Cloud Logging store for a
// Cloud Run Job execution, using the same resource type and labels that the
// backend's log filter expects.
func injectCloudRunJobLog(project, jobName, text string) {
	logName := fmt.Sprintf("projects/%s/logs/run.googleapis.com%%2Fstdout", project)
	writeLogEntries(logName, &MonitoredResource{
		Type:   "cloud_run_job",
		Labels: map[string]string{"job_name": jobName},
	}, nil, []LogEntry{{TextPayload: text}})
}

// crjLogSink implements sim.LogSink and writes log lines to Cloud Logging.
type crjLogSink struct {
	project string
	jobName string
}

func (s *crjLogSink) WriteLog(line sim.LogLine) {
	injectCloudRunJobLog(s.project, s.jobName, line.Text)
}
