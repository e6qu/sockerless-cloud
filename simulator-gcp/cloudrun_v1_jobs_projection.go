package main

import (
	"fmt"
	"strconv"
	"strings"
)

// Knative (Cloud Run Admin v1) shapes for the Cloud Run Jobs family, and the
// projection that renders the v2-owned Job / Execution / Task records in them.
//
// A Cloud Run job, its executions and its tasks are ONE resource each,
// addressable through two API versions: the v2 resource-oriented surface
// (`/v2/projects/{p}/locations/{l}/jobs/…`) and the v1 Knative surface
// (`/apis/run.googleapis.com/v1/namespaces/{ns}/…`). The simulator stores the
// resource once, in the v2 stores (crjJobs / crjExecutions / crjTasks), and
// renders the Knative shape on read — the same rule the services projection in
// cloudrun_service_projection.go follows, and the reason a job created through
// either version is visible through the other.
//
// The Knative namespace is the project: `namespaces/{project}`. The v1 jobs
// surface is regional on real Cloud Run — a client reaches it at
// `{region}-run.googleapis.com` — and the simulator serves one origin, so the
// namespaces surface addresses cloudRunDefaultLocation.
//
// Shapes per the vendored cloudrun-v1 Discovery document's Job / JobSpec /
// JobStatus / Execution / ExecutionSpec / ExecutionStatus / Task / TaskSpec /
// TaskStatus schemas.

// CRExecution is the Knative rendering of a Cloud Run job execution.
type CRExecution struct {
	APIVersion string             `json:"apiVersion"`
	Kind       string             `json:"kind"`
	Metadata   CRServiceMetadata  `json:"metadata"`
	Spec       *CRExecutionSpec   `json:"spec,omitempty"`
	Status     *CRExecutionStatus `json:"status,omitempty"`
}

// CRExecutionSpec mirrors the Discovery ExecutionSpec schema.
type CRExecutionSpec struct {
	Parallelism int32               `json:"parallelism,omitempty"`
	TaskCount   int32               `json:"taskCount,omitempty"`
	Template    *CRTaskTemplateSpec `json:"template,omitempty"`
}

// CRTaskTemplateSpec mirrors the Discovery TaskTemplateSpec schema.
type CRTaskTemplateSpec struct {
	Spec *CRTaskSpec `json:"spec,omitempty"`
}

// CRTaskSpec mirrors the Discovery TaskSpec schema. timeoutSeconds is an
// int64-as-string per proto-JSON, where the v2 TaskTemplate carries a
// google-duration ("600s").
type CRTaskSpec struct {
	Containers         []CRContainer `json:"containers,omitempty"`
	Volumes            []CRVolume    `json:"volumes,omitempty"`
	MaxRetries         int32         `json:"maxRetries,omitempty"`
	TimeoutSeconds     string        `json:"timeoutSeconds,omitempty"`
	ServiceAccountName string        `json:"serviceAccountName,omitempty"`
}

// CRExecutionStatus mirrors the Discovery ExecutionStatus schema.
type CRExecutionStatus struct {
	ObservedGeneration int64         `json:"observedGeneration,omitempty"`
	StartTime          string        `json:"startTime,omitempty"`
	CompletionTime     string        `json:"completionTime,omitempty"`
	RunningCount       int32         `json:"runningCount,omitempty"`
	SucceededCount     int32         `json:"succeededCount,omitempty"`
	FailedCount        int32         `json:"failedCount,omitempty"`
	CancelledCount     int32         `json:"cancelledCount,omitempty"`
	RetriedCount       int32         `json:"retriedCount,omitempty"`
	Conditions         []CRCondition `json:"conditions,omitempty"`
}

// CRTask is the Knative rendering of one Cloud Run task.
type CRTask struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   CRServiceMetadata `json:"metadata"`
	Spec       *CRTaskSpec       `json:"spec,omitempty"`
	Status     *CRTaskStatus     `json:"status,omitempty"`
}

// CRTaskStatus mirrors the Discovery TaskStatus schema.
type CRTaskStatus struct {
	ObservedGeneration int64                `json:"observedGeneration,omitempty"`
	Index              int32                `json:"index"`
	StartTime          string               `json:"startTime,omitempty"`
	CompletionTime     string               `json:"completionTime,omitempty"`
	Retried            int32                `json:"retried,omitempty"`
	LastAttemptResult  *CRTaskAttemptResult `json:"lastAttemptResult,omitempty"`
	Conditions         []CRCondition        `json:"conditions,omitempty"`
}

// CRTaskAttemptResult mirrors the Discovery TaskAttemptResult schema.
type CRTaskAttemptResult struct {
	Status   *CRRPCStatus `json:"status,omitempty"`
	ExitCode int32        `json:"exitCode,omitempty"`
}

// CRRPCStatus mirrors the Discovery GoogleRpcStatus schema.
type CRRPCStatus struct {
	Code    int32  `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// CRVolume mirrors the Discovery Volume schema. Cloud Run's v1 API expresses a
// Cloud Storage volume as a CSI volume on the `gcsfuse.run.googleapis.com`
// driver, where v2 carries a dedicated `gcs` member; the two spell the same
// mount and convert into each other below.
type CRVolume struct {
	Name     string                  `json:"name"`
	Csi      *CRCSIVolumeSource      `json:"csi,omitempty"`
	EmptyDir *CREmptyDirVolumeSource `json:"emptyDir,omitempty"`
	Nfs      *CRNFSVolumeSource      `json:"nfs,omitempty"`
	Secret   *CRSecretVolumeSource   `json:"secret,omitempty"`
}

// CRCSIVolumeSource mirrors the Discovery CSIVolumeSource schema.
type CRCSIVolumeSource struct {
	Driver           string            `json:"driver,omitempty"`
	VolumeAttributes map[string]string `json:"volumeAttributes,omitempty"`
	ReadOnly         bool              `json:"readOnly,omitempty"`
}

// CREmptyDirVolumeSource mirrors the Discovery EmptyDirVolumeSource schema.
type CREmptyDirVolumeSource struct {
	Medium    string `json:"medium,omitempty"`
	SizeLimit string `json:"sizeLimit,omitempty"`
}

// CRNFSVolumeSource mirrors the Discovery NFSVolumeSource schema.
type CRNFSVolumeSource struct {
	Server   string `json:"server,omitempty"`
	Path     string `json:"path,omitempty"`
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// CRSecretVolumeSource mirrors the Discovery SecretVolumeSource schema.
type CRSecretVolumeSource struct {
	SecretName  string        `json:"secretName,omitempty"`
	DefaultMode int32         `json:"defaultMode,omitempty"`
	Items       []CRKeyToPath `json:"items,omitempty"`
}

// CRKeyToPath mirrors the Discovery KeyToPath schema: `key` is the secret
// version where v2's VersionToPath calls it `version`.
type CRKeyToPath struct {
	Key  string `json:"key,omitempty"`
	Path string `json:"path,omitempty"`
	Mode int32  `json:"mode,omitempty"`
}

// CRVolumeMount mirrors the Discovery VolumeMount schema.
type CRVolumeMount struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
	SubPath   string `json:"subPath,omitempty"`
}

// CRResourceRequirements is the limits member of the Discovery
// ResourceRequirements schema. Cloud Run's v2 model of the same container
// carries only limits — there is no `requests` member for one to project into
// or out of — so limits is the whole of what round-trips between the two API
// versions.
type CRResourceRequirements struct {
	Limits map[string]string `json:"limits,omitempty"`
}

// cloudRunGCSFuseCSIDriver is the CSI driver name Cloud Run's v1 API uses for
// a Cloud Storage volume; v2 models the same mount as Volume.gcs.
const cloudRunGCSFuseCSIDriver = "gcsfuse.run.googleapis.com"

// cloudRunGCSFuseBucketAttribute is the CSI volume attribute carrying the
// bucket of a Cloud Storage volume.
const cloudRunGCSFuseBucketAttribute = "bucketName"

// cloudRunJobLabel is the label Cloud Run stamps on an execution to name the
// job that owns it; cloudRunExecutionLabel does the same for a task's
// execution. The Knative surface lists executions and tasks per namespace
// rather than per parent, so these labels are how a client narrows a list to
// one job (`labelSelector=run.googleapis.com/job=<job>`).
const (
	cloudRunJobLabel       = "run.googleapis.com/job"
	cloudRunExecutionLabel = "run.googleapis.com/execution"
)

// parseCloudRunV2ExecutionName splits a v2 execution resource name into its
// parts.
func parseCloudRunV2ExecutionName(name string) (project, location, job, execution string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 8 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "jobs" || parts[6] != "executions" {
		return "", "", "", "", false
	}
	return parts[1], parts[3], parts[5], parts[7], true
}

// parseCloudRunV2TaskName splits a v2 task resource name into its parts.
func parseCloudRunV2TaskName(name string) (project, location, job, execution, task string, ok bool) {
	parts := strings.Split(name, "/")
	if len(parts) != 10 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "jobs" ||
		parts[6] != "executions" || parts[8] != "tasks" {
		return "", "", "", "", "", false
	}
	return parts[1], parts[3], parts[5], parts[7], parts[9], true
}

// cloudRunV2ConditionToV1 renders a v2 Condition in the Knative shape: v2
// carries a CONDITION_* state enum where Knative carries a True/False/Unknown
// status string.
func cloudRunV2ConditionToV1(condition Condition) CRCondition {
	status := "Unknown"
	switch condition.State {
	case "CONDITION_SUCCEEDED":
		status = "True"
	case "CONDITION_FAILED":
		status = "False"
	}
	return CRCondition{
		Type:               condition.Type,
		Status:             status,
		Reason:             condition.Reason,
		LastTransitionTime: condition.LastTransitionTime,
	}
}

func cloudRunV2ConditionsToV1(conditions []Condition) []CRCondition {
	if len(conditions) == 0 {
		return nil
	}
	out := make([]CRCondition, 0, len(conditions))
	for _, condition := range conditions {
		out = append(out, cloudRunV2ConditionToV1(condition))
	}
	return out
}

// cloudRunV2VolumeToV1 renders a v2 Volume in the Knative shape.
func cloudRunV2VolumeToV1(volume Volume) CRVolume {
	out := CRVolume{Name: volume.Name}
	switch {
	case volume.Gcs != nil:
		out.Csi = &CRCSIVolumeSource{
			Driver:           cloudRunGCSFuseCSIDriver,
			VolumeAttributes: map[string]string{cloudRunGCSFuseBucketAttribute: volume.Gcs.Bucket},
			ReadOnly:         volume.Gcs.ReadOnly,
		}
	case volume.EmptyDir != nil:
		out.EmptyDir = &CREmptyDirVolumeSource{Medium: volume.EmptyDir.Medium, SizeLimit: volume.EmptyDir.SizeLimit}
	case volume.Nfs != nil:
		out.Nfs = &CRNFSVolumeSource{Server: volume.Nfs.Server, Path: volume.Nfs.Path, ReadOnly: volume.Nfs.ReadOnly}
	case volume.Secret != nil:
		items := make([]CRKeyToPath, 0, len(volume.Secret.Items))
		for _, item := range volume.Secret.Items {
			items = append(items, CRKeyToPath{Key: item.Version, Path: item.Path, Mode: item.Mode})
		}
		out.Secret = &CRSecretVolumeSource{
			SecretName:  volume.Secret.Secret,
			DefaultMode: volume.Secret.DefaultMode,
			Items:       items,
		}
	}
	return out
}

// cloudRunV1VolumeToV2 is cloudRunV2VolumeToV1's inverse.
func cloudRunV1VolumeToV2(volume CRVolume) Volume {
	out := Volume{Name: volume.Name}
	switch {
	case volume.Csi != nil && volume.Csi.Driver == cloudRunGCSFuseCSIDriver:
		out.Gcs = &GcsVolumeSource{
			Bucket:   volume.Csi.VolumeAttributes[cloudRunGCSFuseBucketAttribute],
			ReadOnly: volume.Csi.ReadOnly,
		}
	case volume.EmptyDir != nil:
		out.EmptyDir = &EmptyDirVolumeSource{Medium: volume.EmptyDir.Medium, SizeLimit: volume.EmptyDir.SizeLimit}
	case volume.Nfs != nil:
		out.Nfs = &NfsVolumeSource{Server: volume.Nfs.Server, Path: volume.Nfs.Path, ReadOnly: volume.Nfs.ReadOnly}
	case volume.Secret != nil:
		items := make([]VersionToPath, 0, len(volume.Secret.Items))
		for _, item := range volume.Secret.Items {
			items = append(items, VersionToPath{Version: item.Key, Path: item.Path, Mode: item.Mode})
		}
		out.Secret = &SecretVolumeSource{
			Secret:      volume.Secret.SecretName,
			DefaultMode: volume.Secret.DefaultMode,
			Items:       items,
		}
	}
	return out
}

func cloudRunV2VolumesToV1(volumes []Volume) []CRVolume {
	if len(volumes) == 0 {
		return nil
	}
	out := make([]CRVolume, 0, len(volumes))
	for _, volume := range volumes {
		out = append(out, cloudRunV2VolumeToV1(volume))
	}
	return out
}

func cloudRunV1VolumesToV2(volumes []CRVolume) []Volume {
	if len(volumes) == 0 {
		return nil
	}
	out := make([]Volume, 0, len(volumes))
	for _, volume := range volumes {
		out = append(out, cloudRunV1VolumeToV2(volume))
	}
	return out
}

// cloudRunV2TaskTemplateToV1 renders a v2 TaskTemplate as the Knative TaskSpec
// a v1 Execution and Task both carry.
func cloudRunV2TaskTemplateToV1(template *TaskTemplate) *CRTaskSpec {
	if template == nil {
		return nil
	}
	containers := make([]CRContainer, 0, len(template.Containers))
	for _, container := range template.Containers {
		containers = append(containers, cloudRunV2ContainerToV1(container))
	}
	return &CRTaskSpec{
		Containers:         containers,
		Volumes:            cloudRunV2VolumesToV1(template.Volumes),
		MaxRetries:         template.MaxRetries,
		TimeoutSeconds:     cloudRunDurationToSeconds(template.Timeout),
		ServiceAccountName: template.ServiceAccount,
	}
}

// cloudRunV1TaskSpecToV2 is cloudRunV2TaskTemplateToV1's inverse.
func cloudRunV1TaskSpecToV2(spec *CRTaskSpec) *TaskTemplate {
	if spec == nil {
		return nil
	}
	containers := make([]Container, 0, len(spec.Containers))
	for _, container := range spec.Containers {
		containers = append(containers, cloudRunV1ContainerToV2(container))
	}
	return &TaskTemplate{
		Containers:     containers,
		Volumes:        cloudRunV1VolumesToV2(spec.Volumes),
		MaxRetries:     spec.MaxRetries,
		Timeout:        cloudRunSecondsToDuration(spec.TimeoutSeconds),
		ServiceAccount: spec.ServiceAccountName,
	}
}

// cloudRunDurationToSeconds converts a proto-JSON google-duration ("600s") to
// the int64-as-string seconds count the Knative TaskSpec carries. A duration
// the simulator did not itself write, and cannot read, yields "" rather than a
// made-up number.
func cloudRunDurationToSeconds(duration string) string {
	if duration == "" {
		return ""
	}
	seconds, err := strconv.ParseFloat(strings.TrimSuffix(duration, "s"), 64)
	if err != nil || !strings.HasSuffix(duration, "s") {
		return ""
	}
	return strconv.FormatInt(int64(seconds), 10)
}

// cloudRunSecondsToDuration is cloudRunDurationToSeconds' inverse.
func cloudRunSecondsToDuration(seconds string) string {
	if seconds == "" {
		return ""
	}
	value, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return ""
	}
	return strconv.FormatInt(value, 10) + "s"
}

// cloudRunV2ExecutionToV1 renders a v2 Execution in the Knative shape.
func cloudRunV2ExecutionToV1(execution Execution) (CRExecution, bool) {
	project, _, job, executionID, ok := parseCloudRunV2ExecutionName(execution.Name)
	if !ok {
		return CRExecution{}, false
	}
	labels := map[string]string{cloudRunJobLabel: job}
	for key, value := range execution.Labels {
		labels[key] = value
	}
	return CRExecution{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "Execution",
		Metadata: CRServiceMetadata{
			Name:              executionID,
			Namespace:         project,
			UID:               execution.UID,
			Generation:        execution.Generation,
			ResourceVersion:   fmt.Sprintf("%d", execution.Generation),
			Labels:            labels,
			CreationTimestamp: execution.CreateTime,
		},
		Spec: &CRExecutionSpec{
			Parallelism: execution.Parallelism,
			TaskCount:   execution.TaskCount,
			Template:    &CRTaskTemplateSpec{Spec: cloudRunV2TaskTemplateToV1(execution.Template)},
		},
		Status: &CRExecutionStatus{
			ObservedGeneration: execution.Generation,
			StartTime:          execution.StartTime,
			CompletionTime:     execution.CompletionTime,
			RunningCount:       execution.RunningCount,
			SucceededCount:     execution.SucceededCount,
			FailedCount:        execution.FailedCount,
			CancelledCount:     execution.CancelledCount,
			Conditions:         cloudRunV2ConditionsToV1(execution.Conditions),
		},
	}, true
}

// cloudRunV2TaskToV1 renders a v2 Task in the Knative shape.
func cloudRunV2TaskToV1(task Task) (CRTask, bool) {
	project, _, job, execution, taskID, ok := parseCloudRunV2TaskName(task.Name)
	if !ok {
		return CRTask{}, false
	}
	labels := map[string]string{cloudRunJobLabel: job, cloudRunExecutionLabel: execution}
	for key, value := range task.Labels {
		labels[key] = value
	}
	containers := make([]CRContainer, 0, len(task.Containers))
	for _, container := range task.Containers {
		containers = append(containers, cloudRunV2ContainerToV1(container))
	}
	status := &CRTaskStatus{
		ObservedGeneration: task.Generation,
		Index:              task.Index,
		StartTime:          task.StartTime,
		CompletionTime:     task.CompletionTime,
		Retried:            task.Retried,
		Conditions:         cloudRunV2ConditionsToV1(task.Conditions),
	}
	if task.LastAttemptResult != nil {
		status.LastAttemptResult = &CRTaskAttemptResult{ExitCode: task.LastAttemptResult.ExitCode}
		if task.LastAttemptResult.Status != nil {
			status.LastAttemptResult.Status = &CRRPCStatus{
				Code:    task.LastAttemptResult.Status.Code,
				Message: task.LastAttemptResult.Status.Message,
			}
		}
	}
	return CRTask{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "Task",
		Metadata: CRServiceMetadata{
			Name:              taskID,
			Namespace:         project,
			UID:               task.UID,
			Generation:        task.Generation,
			ResourceVersion:   fmt.Sprintf("%d", task.Generation),
			Labels:            labels,
			CreationTimestamp: task.CreateTime,
		},
		Spec: &CRTaskSpec{
			Containers:         containers,
			Volumes:            cloudRunV2VolumesToV1(task.Volumes),
			MaxRetries:         task.MaxRetries,
			TimeoutSeconds:     cloudRunDurationToSeconds(task.Timeout),
			ServiceAccountName: task.ServiceAccount,
		},
		Status: status,
	}, true
}

// CRJob is the Knative rendering of a Cloud Run job.
type CRJob struct {
	APIVersion string            `json:"apiVersion"`
	Kind       string            `json:"kind"`
	Metadata   CRServiceMetadata `json:"metadata"`
	Spec       *CRJobSpec        `json:"spec,omitempty"`
	Status     *CRJobStatus      `json:"status,omitempty"`
}

// CRJobSpec mirrors the Discovery JobSpec schema.
type CRJobSpec struct {
	Template *CRExecutionTemplateSpec `json:"template,omitempty"`
}

// CRExecutionTemplateSpec mirrors the Discovery ExecutionTemplateSpec schema.
type CRExecutionTemplateSpec struct {
	Metadata *CRServiceMetadata `json:"metadata,omitempty"`
	Spec     *CRExecutionSpec   `json:"spec,omitempty"`
}

// CRJobStatus mirrors the Discovery JobStatus schema.
type CRJobStatus struct {
	ObservedGeneration     int64                 `json:"observedGeneration,omitempty"`
	ExecutionCount         int32                 `json:"executionCount,omitempty"`
	LatestCreatedExecution *CRExecutionReference `json:"latestCreatedExecution,omitempty"`
	Conditions             []CRCondition         `json:"conditions,omitempty"`
}

// CRExecutionReference mirrors the Discovery ExecutionReference schema.
type CRExecutionReference struct {
	Name                string `json:"name,omitempty"`
	CreationTimestamp   string `json:"creationTimestamp,omitempty"`
	CompletionTimestamp string `json:"completionTimestamp,omitempty"`
	CompletionStatus    string `json:"completionStatus,omitempty"`
}

// cloudRunExecutionCompletionStatus reports an execution's outcome in the
// ExecutionReference.completionStatus enum the Knative Job status carries. It
// reads the execution record itself — the same record the v2 collection
// serves — rather than inferring an outcome from the reference's timestamps.
func cloudRunExecutionCompletionStatus(executionName string) string {
	execution, ok := crjExecutions.Get(executionName)
	if !ok {
		return ""
	}
	switch {
	case execution.CancelledCount > 0:
		return "EXECUTION_CANCELLED"
	case execution.FailedCount > 0:
		return "EXECUTION_FAILED"
	case execution.RunningCount > 0:
		return "EXECUTION_RUNNING"
	case execution.SucceededCount > 0:
		return "EXECUTION_SUCCEEDED"
	default:
		return "EXECUTION_PENDING"
	}
}

// cloudRunV2JobToV1 renders a v2 Job in the Knative shape.
func cloudRunV2JobToV1(job Job) (CRJob, bool) {
	parts := strings.Split(job.Name, "/")
	if len(parts) != 6 || parts[0] != "projects" || parts[2] != "locations" || parts[4] != "jobs" {
		return CRJob{}, false
	}
	var template *CRExecutionTemplateSpec
	if job.Template != nil {
		template = &CRExecutionTemplateSpec{
			Metadata: &CRServiceMetadata{
				Name:        parts[5],
				Namespace:   parts[1],
				Labels:      job.Template.Labels,
				Annotations: job.Template.Annotations,
			},
			Spec: &CRExecutionSpec{
				Parallelism: job.Template.Parallelism,
				TaskCount:   job.Template.TaskCount,
				Template:    &CRTaskTemplateSpec{Spec: cloudRunV2TaskTemplateToV1(job.Template.Template)},
			},
		}
	}
	conditions := cloudRunV2ConditionsToV1(job.Conditions)
	if job.TerminalCondition != nil {
		conditions = append([]CRCondition{cloudRunV2ConditionToV1(*job.TerminalCondition)}, conditions...)
	}
	status := &CRJobStatus{
		ObservedGeneration: job.Generation,
		ExecutionCount:     job.ExecutionCount,
		Conditions:         conditions,
	}
	if job.LatestCreatedExecution != nil {
		status.LatestCreatedExecution = &CRExecutionReference{
			Name:                job.LatestCreatedExecution.Name[strings.LastIndex(job.LatestCreatedExecution.Name, "/")+1:],
			CreationTimestamp:   job.LatestCreatedExecution.CreateTime,
			CompletionTimestamp: job.LatestCreatedExecution.CompletionTime,
			CompletionStatus:    cloudRunExecutionCompletionStatus(job.LatestCreatedExecution.Name),
		}
	}
	return CRJob{
		APIVersion: "run.googleapis.com/v1",
		Kind:       "Job",
		Metadata: CRServiceMetadata{
			Name:              parts[5],
			Namespace:         parts[1],
			UID:               job.UID,
			Generation:        job.Generation,
			ResourceVersion:   fmt.Sprintf("%d", job.Generation),
			Labels:            job.Labels,
			Annotations:       job.Annotations,
			CreationTimestamp: job.CreateTime,
		},
		Spec:   &CRJobSpec{Template: template},
		Status: status,
	}, true
}

// cloudRunV1JobToV2 folds a Knative Job body onto the v2 resource the
// simulator stores. Server-owned members (name, uid, timestamps, conditions,
// execution counters) are seeded by the caller.
func cloudRunV1JobToV2(job CRJob) Job {
	converted := Job{
		Labels:      job.Metadata.Labels,
		Annotations: job.Metadata.Annotations,
	}
	if job.Spec == nil || job.Spec.Template == nil {
		return converted
	}
	template := &ExecutionTemplate{}
	if job.Spec.Template.Metadata != nil {
		template.Labels = job.Spec.Template.Metadata.Labels
		template.Annotations = job.Spec.Template.Metadata.Annotations
	}
	if spec := job.Spec.Template.Spec; spec != nil {
		template.Parallelism = spec.Parallelism
		template.TaskCount = spec.TaskCount
		if spec.Template != nil {
			template.Template = cloudRunV1TaskSpecToV2(spec.Template.Spec)
		}
	}
	converted.Template = template
	return converted
}

// cloudRunV1OverridesToV2 renders the Knative RunJobRequest overrides in the
// v2 shape the shared run path takes. Knative spells the per-run timeout as an
// int32 seconds count where v2 carries a google-duration.
func cloudRunV1OverridesToV2(overrides *CROverrides) *Overrides {
	if overrides == nil {
		return nil
	}
	converted := &Overrides{TaskCount: overrides.TaskCount}
	if overrides.TimeoutSeconds > 0 {
		converted.Timeout = fmt.Sprintf("%ds", overrides.TimeoutSeconds)
	}
	for _, override := range overrides.ContainerOverrides {
		env := make([]EnvVar, 0, len(override.Env))
		for _, entry := range override.Env {
			converted := EnvVar{Name: entry.Name, Value: entry.Value}
			if entry.ValueFrom != nil && entry.ValueFrom.SecretKeyRef != nil {
				converted.Value = ""
				converted.ValueSource = &EnvVarSource{SecretKeyRef: &SecretKeySelector{
					Secret:  entry.ValueFrom.SecretKeyRef.Name,
					Version: entry.ValueFrom.SecretKeyRef.Key,
				}}
			}
			env = append(env, converted)
		}
		converted.ContainerOverrides = append(converted.ContainerOverrides, ContainerOverride{
			Name:      override.Name,
			Args:      override.Args,
			Env:       env,
			ClearArgs: override.ClearArgs,
		})
	}
	return converted
}
