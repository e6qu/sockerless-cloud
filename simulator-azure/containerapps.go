package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/gorilla/websocket"
	dockerclient "github.com/moby/moby/client"
)

// ContainerAppJob represents an Azure Container Apps Job resource.
type ContainerAppJob struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   *SiteIdentity     `json:"identity,omitempty"`
	Properties JobProperties     `json:"properties"`
	SystemData *SystemData       `json:"systemData,omitempty"`
	// SystemIdentityClientID is the application ID of the job's
	// system-assigned identity, which the directory reads and the identity's
	// wire shape does not carry.
	SystemIdentityClientID string `json:"-"`
}

// SystemData holds Azure Resource Manager metadata. Real ARM stamps
// `createdAt` once on resource creation and updates `lastModifiedAt` on
// every write — the sim must preserve `createdAt` across PATCH/PUT
// updates instead of restamping it.
type SystemData struct {
	CreatedAt      string `json:"createdAt,omitempty"`
	LastModifiedAt string `json:"lastModifiedAt,omitempty"`
}

// JobProperties holds the properties of a Container Apps Job.
type JobProperties struct {
	ProvisioningState   string            `json:"provisioningState"`
	EnvironmentID       string            `json:"environmentId,omitempty"`
	WorkloadProfileName string            `json:"workloadProfileName,omitempty"`
	Configuration       *JobConfiguration `json:"configuration,omitempty"`
	Template            *JobTemplate      `json:"template,omitempty"`
}

// JobConfiguration holds the configuration of a Container Apps Job.
type JobConfiguration struct {
	ReplicaTimeout    int              `json:"replicaTimeout,omitempty"`
	ReplicaRetryLimit int              `json:"replicaRetryLimit,omitempty"`
	TriggerType       string           `json:"triggerType,omitempty"`
	Secrets           []JobSecret      `json:"secrets,omitempty"`
	Registries        []JobRegistry    `json:"registries,omitempty"`
	ManualTrigger     *ManualTrigger   `json:"manualTriggerConfig,omitempty"`
	ScheduleTrigger   *ScheduleTrigger `json:"scheduleTriggerConfig,omitempty"`
	EventTrigger      *EventTrigger    `json:"eventTriggerConfig,omitempty"`
}

type ManualTrigger struct {
	Parallelism            int `json:"parallelism,omitempty"`
	ReplicaCompletionCount int `json:"replicaCompletionCount,omitempty"`
}

type ScheduleTrigger struct {
	CronExpression         string `json:"cronExpression,omitempty"`
	Parallelism            int    `json:"parallelism,omitempty"`
	ReplicaCompletionCount int    `json:"replicaCompletionCount,omitempty"`
}

type EventTrigger struct {
	Parallelism            int `json:"parallelism,omitempty"`
	ReplicaCompletionCount int `json:"replicaCompletionCount,omitempty"`
}

// JobSecret holds a secret of a Container Apps Job: a value, or the Key Vault
// secret KeyVaultURL names, read as the managed identity Identity names
// (containerapps_identity.go).
type JobSecret struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Identity    string `json:"identity,omitempty"`
	KeyVaultURL string `json:"keyVaultUrl,omitempty"`
}

// JobRegistry holds a container registry reference for a Container Apps Job.
type JobRegistry struct {
	Server            string `json:"server"`
	Username          string `json:"username,omitempty"`
	PasswordSecretRef string `json:"passwordSecretRef,omitempty"`
	Identity          string `json:"identity,omitempty"`
}

// JobTemplate holds the template of a Container Apps Job.
type JobTemplate struct {
	Containers     []JobContainer `json:"containers,omitempty"`
	InitContainers []JobContainer `json:"initContainers,omitempty"`
	Volumes        []JobVolume    `json:"volumes,omitempty"`
}

// jobExecutionTemplate narrows a job's template to the JobExecutionTemplate an
// execution reports. Azure models the two separately: JobExecutionTemplate
// declares only containers and initContainers, and its JobExecutionContainer
// carries no volumeMounts or probes, so an execution never echoes the volumes,
// mounts or probes
// the job template configures. Returning the job template unchanged would put
// fields on the wire that the real service does not send.
func jobExecutionTemplate(t *JobTemplate) *JobTemplate {
	if t == nil {
		return nil
	}
	strip := func(in []JobContainer) []JobContainer {
		if in == nil {
			return nil
		}
		out := make([]JobContainer, len(in))
		for i, c := range in {
			c.VolumeMounts = nil
			c.Probes = nil
			out[i] = c
		}
		return out
	}
	return &JobTemplate{
		Containers:     strip(t.Containers),
		InitContainers: strip(t.InitContainers),
	}
}

// JobContainer holds a container definition for a Container Apps Job.
type JobContainer struct {
	Name         string                `json:"name"`
	Image        string                `json:"image"`
	Command      []string              `json:"command,omitempty"`
	Args         []string              `json:"args,omitempty"`
	Env          []EnvVar              `json:"env,omitempty"`
	Resources    *ResourceRequirements `json:"resources,omitempty"`
	VolumeMounts []VolumeMount         `json:"volumeMounts,omitempty"`
	Probes       []ContainerAppProbe   `json:"probes,omitempty"`
}

// ContainerAppProbe is a health check the platform runs against a
// container, with Kubernetes' probe semantics.
type ContainerAppProbe struct {
	Type                          string                      `json:"type,omitempty"`
	HTTPGet                       *ContainerAppProbeHTTPGet   `json:"httpGet,omitempty"`
	TCPSocket                     *ContainerAppProbeTCPSocket `json:"tcpSocket,omitempty"`
	InitialDelaySeconds           *int32                      `json:"initialDelaySeconds,omitempty"`
	PeriodSeconds                 *int32                      `json:"periodSeconds,omitempty"`
	TimeoutSeconds                *int32                      `json:"timeoutSeconds,omitempty"`
	FailureThreshold              *int32                      `json:"failureThreshold,omitempty"`
	SuccessThreshold              *int32                      `json:"successThreshold,omitempty"`
	TerminationGracePeriodSeconds *int64                      `json:"terminationGracePeriodSeconds,omitempty"`
}

type ContainerAppProbeHTTPGet struct {
	Port        int32                     `json:"port"`
	Path        string                    `json:"path,omitempty"`
	Host        string                    `json:"host,omitempty"`
	Scheme      string                    `json:"scheme,omitempty"`
	HTTPHeaders []ContainerAppProbeHeader `json:"httpHeaders,omitempty"`
}

type ContainerAppProbeHeader struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ContainerAppProbeTCPSocket struct {
	Port int32  `json:"port"`
	Host string `json:"host,omitempty"`
}

// EnvVar holds an environment variable.
type EnvVar struct {
	Name      string `json:"name"`
	Value     string `json:"value,omitempty"`
	SecretRef string `json:"secretRef,omitempty"`
}

type ResourceRequirements struct {
	CPU              float64 `json:"cpu,omitempty"`
	Memory           string  `json:"memory,omitempty"`
	EphemeralStorage string  `json:"ephemeralStorage,omitempty"`
}

type VolumeMount struct {
	VolumeName string `json:"volumeName"`
	MountPath  string `json:"mountPath"`
	SubPath    string `json:"subPath,omitempty"`
}

// JobVolume holds a volume definition for a Container Apps Job.
type JobVolume struct {
	Name        string `json:"name"`
	StorageType string `json:"storageType,omitempty"`
	StorageName string `json:"storageName,omitempty"`
}

// JobExecution represents a running or completed execution of a Container
// Apps Job. The wire shape nests status/startTime/endTime/template under
// properties, matching the Microsoft.App jobs spec and the armappcontainers
// v3 deserializer.
type JobExecution struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type,omitempty"`
	Properties JobExecutionProperties `json:"properties"`
}

// JobExecutionProperties holds the execution state of a JobExecution.
type JobExecutionProperties struct {
	Status    string       `json:"status"`
	StartTime string       `json:"startTime"`
	EndTime   string       `json:"endTime,omitempty"`
	Template  *JobTemplate `json:"template,omitempty"`
}

// Package-level stores for dashboard access.
var acaJobs sim.Store[ContainerAppJob]

func registerContainerApps(srv *sim.Server) {
	jobs := sim.MakeStore[ContainerAppJob](srv.DB(), "aca_jobs")
	executions := sim.MakeStore[JobExecution](srv.DB(), "aca_executions")
	acaJobs, acaExecutions = jobs, executions
	acaRuns = sim.MakeStore[acaExecutionRun](srv.DB(), "aca_execution_runs")

	const basePath = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App"

	// PUT - Create or update job
	srv.HandleFunc("PUT "+basePath+"/jobs/{jobName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		var req ContainerAppJob
		if err := sim.ReadJSON(r, &req); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)

		existing, exists := jobs.Get(resourceID)
		if exists && existing.Properties.ProvisioningState == "Deleting" {
			AzureErrorf(w, "Conflict", http.StatusConflict,
				"Container app job '%s' is being deleted and cannot be updated until the delete operation completes.", name)
			return
		}

		// Real ACA: Jobs go through Creating → Succeeded just like Apps.
		// Mirror the Azure-AsyncOperation polling flow: store the resource
		// with Succeeded directly + emit the shared ARM LRO headers so SDK
		// pollers exercise the real path.
		// Real ARM stamps SystemData.createdAt once and only updates
		// lastModifiedAt on subsequent writes — preserve CreatedAt on update.
		nowStamp := time.Now().UTC().Format(time.RFC3339Nano)
		systemData := &SystemData{
			CreatedAt:      nowStamp,
			LastModifiedAt: nowStamp,
		}
		if exists && existing.SystemData != nil && existing.SystemData.CreatedAt != "" {
			systemData.CreatedAt = existing.SystemData.CreatedAt
		}

		identity, identityClientID, err := acaApplyIdentity(req.Identity, existing.Identity, existing.SystemIdentityClientID)
		if err != nil {
			AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
			return
		}
		job := ContainerAppJob{
			ID:                     resourceID,
			Name:                   name,
			Type:                   "Microsoft.App/jobs",
			Location:               req.Location,
			Tags:                   req.Tags,
			Identity:               identity,
			SystemIdentityClientID: identityClientID,
			Properties: JobProperties{
				ProvisioningState:   "Succeeded",
				EnvironmentID:       req.Properties.EnvironmentID,
				WorkloadProfileName: req.Properties.WorkloadProfileName,
				Configuration:       req.Properties.Configuration,
				Template:            req.Properties.Template,
			},
			SystemData: systemData,
		}

		if job.Properties.Configuration != nil && job.Properties.Configuration.TriggerType == "" {
			job.Properties.Configuration.TriggerType = "Manual"
		}
		if config := job.Properties.Configuration; config != nil && strings.EqualFold(config.TriggerType, "Schedule") {
			if _, err := acaJobCron(config); err != nil {
				AzureError(w, "InvalidRequestContent", "Invalid scheduleTriggerConfig.cronExpression: "+err.Error(), http.StatusBadRequest)
				return
			}
		}

		if !acaSettleJobIdentity(w, job, existing) {
			return
		}
		jobs.Put(resourceID, job)

		opID := issueAzureAsyncOperation(nil)
		acaAsyncOpHeaders(w, r, sub, req.Location, opID)

		if exists {
			sim.WriteJSON(w, http.StatusOK, job)
		} else {
			sim.WriteJSON(w, http.StatusCreated, job)
		}
	})

	// GET - Get job
	srv.HandleFunc("GET "+basePath+"/jobs/{jobName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)

		job, ok := jobs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, job)
	})

	// GET - List jobs
	srv.HandleFunc("GET "+basePath+"/jobs", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := armResourceID(sub, rg, "Microsoft.App", "jobs") + "/"

		filtered := jobs.Filter(func(j ContainerAppJob) bool {
			return strings.HasPrefix(j.ID, prefix)
		})

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": filtered,
		})
	})

	// GET - List jobs by subscription.
	srv.HandleFunc("GET /subscriptions/{subscriptionId}/providers/Microsoft.App/jobs", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		prefix := armSubscriptionID(sub) + "/"
		filtered := jobs.Filter(func(j ContainerAppJob) bool {
			return strings.HasPrefix(j.ID, prefix) && strings.Contains(j.ID, "/providers/Microsoft.App/jobs/")
		})
		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": filtered,
		})
	})

	// PATCH - Update job. Real ACA models this as a long-running
	// operation that settles to provisioningState=Succeeded; the SDK's
	// body poller reads the Succeeded state off the 200 body.
	srv.HandleFunc("PATCH "+basePath+"/jobs/{jobName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)
		job, ok := jobs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		patch, err := io.ReadAll(r.Body)
		if err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		prior := job
		var patchIdentity struct {
			Identity *SiteIdentity `json:"identity"`
		}
		if err := json.Unmarshal(patch, &patchIdentity); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := applyARMMergePatch(&job, patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		job.Identity, job.SystemIdentityClientID, err = acaApplyIdentity(patchIdentity.Identity, prior.Identity, prior.SystemIdentityClientID)
		if err != nil {
			AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
			return
		}
		// Identity and server-owned fields are not client-writable.
		job.ID, job.Name, job.Type, job.Location = prior.ID, prior.Name, prior.Type, prior.Location
		job.SystemData = prior.SystemData
		job.Properties.ProvisioningState = prior.Properties.ProvisioningState
		if job.Properties.Configuration != nil && job.Properties.Configuration.TriggerType == "" {
			job.Properties.Configuration.TriggerType = "Manual"
		}
		if config := job.Properties.Configuration; config != nil && strings.EqualFold(config.TriggerType, "Schedule") {
			if _, err := acaJobCron(config); err != nil {
				AzureError(w, "InvalidRequestContent", "Invalid scheduleTriggerConfig.cronExpression: "+err.Error(), http.StatusBadRequest)
				return
			}
		}
		if job.SystemData != nil {
			job.SystemData.LastModifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
		}
		if !acaSettleJobIdentity(w, job, prior) {
			return
		}
		jobs.Put(resourceID, job)
		sim.WriteJSON(w, http.StatusOK, job)
	})

	// POST /jobs/{jobName}/listsecrets — same shape as the containerApps
	// listsecrets handler: secrets aren't returned on GET; the dedicated
	// POST returns them. Single lowercase registration; the middleware
	// canonicalizes any client casing to lowercase before dispatch.
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/listsecrets", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)
		job, ok := jobs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		secrets := []JobSecret{}
		// SecretsCollection.value is a REQUIRED array — keep [] (not null) when a
		// job has a configuration but no secrets.
		if job.Properties.Configuration != nil && job.Properties.Configuration.Secrets != nil {
			secrets = job.Properties.Configuration.Secrets
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": secrets})
	})

	// DELETE - Delete job. Real Azure ARM returns 202 Accepted with the LRO
	// headers and an empty body for an existing job (the resource stays in
	// provisioningState=Deleting until the operation completes) and 204 No
	// Content when the job does not exist.
	srv.HandleFunc("DELETE "+basePath+"/jobs/{jobName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)

		job, ok := jobs.Get(resourceID)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		job.Properties.ProvisioningState = "Deleting"
		jobs.Put(resourceID, job)
		opID := issueAzureAsyncOperation(func() {
			// Deleting a job stops its running executions and removes them.
			execs := executions.Filter(func(e JobExecution) bool {
				return strings.HasPrefix(e.ID, resourceID+"/executions/")
			})
			for _, e := range execs {
				stopACAJobExecution(job, e.ID)
				executions.Delete(e.ID)
			}
			jobs.Delete(resourceID)
			syncManagedIdentityPrincipal(job.Identity, nil, "", "")
		})
		acaAsyncOpHeaders(w, r, sub, job.Location, opID)
		w.WriteHeader(http.StatusAccepted)
	})

	// POST - Start execution
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/start", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)

		job, ok := jobs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		// Read optional override template
		var override struct {
			Containers     []JobContainer `json:"containers,omitempty"`
			InitContainers []JobContainer `json:"initContainers,omitempty"`
		}
		if err := sim.ReadJSON(r, &override); err != nil {
			AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}

		template := job.Properties.Template
		if len(override.Containers) > 0 {
			template = &JobTemplate{
				Containers:     override.Containers,
				InitContainers: override.InitContainers,
			}
		}
		exec := acaStartJobExecution(executions, job, template)
		execName, execID := exec.Name, exec.ID

		// Return 202 with Location header for LRO polling.
		// The Azure SDK's BeginStart uses FinalStateViaLocation,
		// so it polls the Location URL to get the final result.
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		execURL := fmt.Sprintf("%s://%s%s?api-version=%s",
			scheme, r.Host, execID, r.URL.Query().Get("api-version"))
		w.Header().Set("Location", execURL)
		sim.WriteJSON(w, http.StatusAccepted, map[string]string{
			"name": execName,
			"id":   execID,
		})
	})

	// POST - Stop all executions of a job. Returns the collection of
	// executions that were requested to be stopped (ContainerAppJobExecutions).
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/stop", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")
		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)
		job, ok := jobs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		running := executions.Filter(func(e JobExecution) bool {
			return strings.HasPrefix(e.ID, resourceID+"/executions/") && e.Properties.Status == "Running"
		})
		stopped := make([]JobExecution, 0, len(running))
		for _, e := range running {
			stopACAJobExecution(job, e.ID)
			if updated, ok := executions.Get(e.ID); ok {
				stopped = append(stopped, updated)
			}
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": stopped})
	})

	// GET - List executions
	srv.HandleFunc("GET "+basePath+"/jobs/{jobName}/executions", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		resourceID := armResourceID(sub, rg, "Microsoft.App", "jobs", name)

		_, ok := jobs.Get(resourceID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		filtered := executions.Filter(func(e JobExecution) bool {
			return strings.HasPrefix(e.ID, resourceID+"/executions/")
		})

		sim.WriteJSON(w, http.StatusOK, map[string]any{
			"value": filtered,
		})
	})

	// GET - Get execution
	srv.HandleFunc("GET "+basePath+"/jobs/{jobName}/executions/{execName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		jobName := sim.PathParam(r, "jobName")
		execName := sim.PathParam(r, "execName")

		execID := armResourceID(sub, rg, "Microsoft.App", "jobs", jobName, "executions", execName)

		exec, ok := executions.Get(execID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The execution '%s' for job '%s' was not found.", execName, jobName)
			return
		}

		sim.WriteJSON(w, http.StatusOK, exec)
	})

	// POST - Stop execution
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/executions/{execName}/stop", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		jobName := sim.PathParam(r, "jobName")
		execName := sim.PathParam(r, "execName")

		execID := armResourceID(sub, rg, "Microsoft.App", "jobs", jobName, "executions", execName)

		job, _ := jobs.Get(armResourceID(sub, rg, "Microsoft.App", "jobs", jobName))
		ok := stopACAJobExecution(job, execID)
		if !ok {
			AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The execution '%s' for job '%s' was not found.", execName, jobName)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	startACAJobSchedules(srv, jobs, executions)

	// POST - Console exec WebSocket. Backend's aca/exec_cloud.go dials
	// this with the user command in the `command` query parameter.
	// The handler upgrades to WebSocket and bridges to a `docker exec`
	// against the running execution container — same pattern as
	// simulator-aws/ecs.go's SSM ExecuteCommand handler, simpler
	// frame format (raw binary in/out, no SSM AgentMessage wrapping).
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/executions/{execName}/exec", handleACAJobExec)
}

// acaSettleJobIdentity validates a job's secrets against its identity and
// brings the directory in step with its system-assigned identity, answering
// the request and restoring the directory when the secrets do not resolve.
func acaSettleJobIdentity(w http.ResponseWriter, job, prev ContainerAppJob) bool {
	secrets := acaJobSecrets(job)
	var containers []JobContainer
	if job.Properties.Template != nil {
		containers = append(append(containers, job.Properties.Template.InitContainers...), job.Properties.Template.Containers...)
	}
	if err := acaValidateSecretRefs(containers, secrets); err != nil {
		AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return false
	}
	syncManagedIdentityPrincipal(prev.Identity, job.Identity, job.SystemIdentityClientID, job.Name)
	if _, err := acaResolveSecrets(job.Identity, secrets); err != nil {
		syncManagedIdentityPrincipal(job.Identity, prev.Identity, prev.SystemIdentityClientID, prev.Name)
		AzureError(w, "InvalidParameterValue", err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

// handleACAJobExec serves the ACA jobs-exec WebSocket. The user
// command arrives as `?command=<urlencoded>`; the main container of the
// execution's first running replica is looked up; the WebSocket is then
// bridged to a docker exec session against that container.
func handleACAJobExec(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	jobName := sim.PathParam(r, "jobName")
	execName := sim.PathParam(r, "execName")
	command := r.URL.Query().Get("command")
	if command == "" {
		AzureError(w, "BadRequest", "command query parameter is required", http.StatusBadRequest)
		return
	}

	execID := armResourceID(sub, rg, "Microsoft.App", "jobs", jobName, "executions", execName)
	var handle placement.Handle
	if v, ok := acaJobRuns.Load(execID); ok {
		if run, ok := v.(*acaJobRun); ok {
			handle = run.mainContainer()
		}
	}
	if handle == nil {
		AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"No running execution container for '%s/%s'", jobName, execName)
		return
	}

	conn, err := acaWSUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close() //nolint:errcheck

	cli := sim.DockerClient()
	if cli == nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, "docker client not initialised"))
		return
	}

	// Run the command via `sh -c "<command>"` to match what real ACA's
	// console exec does and to support arbitrary shell expressions.
	execCfg := dockerclient.ExecCreateOptions{
		Cmd:          []string{"sh", "-c", command},
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	}
	ctx := r.Context()
	execResp, err := cli.ExecCreate(ctx, handle.ID(), execCfg)
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	attach, err := cli.ExecAttach(ctx, execResp.ID, dockerclient.ExecAttachOptions{})
	if err != nil {
		_ = conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseInternalServerErr, err.Error()))
		return
	}
	defer attach.Close()

	// Bridge: WebSocket binary frames → exec stdin; exec stdout+stderr
	// → WebSocket binary frames. ACA's real protocol does not split
	// stdout/stderr at the wire level, so we let stdcopy demux upstream
	// in the backend / matching client (or just merge to one stream).
	done := make(chan struct{})

	go func() {
		defer close(done)
		buf := make([]byte, 4096)
		for {
			n, rerr := attach.Reader.Read(buf)
			if n > 0 {
				if werr := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if rerr != nil {
				return
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			break
		}
		if _, werr := attach.Conn.Write(msg); werr != nil {
			break
		}
	}
	_ = attach.CloseWrite()
	<-done

	_ = conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(5*time.Second),
	)
}

var acaWSUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// randomSuffix generates a random alphanumeric suffix of the given length.
func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

// acaLogSink writes a job container's output to the job environment's
// ContainerAppConsoleLogs_CL.
type acaLogSink struct {
	job ContainerAppJob
}

func (s *acaLogSink) WriteLog(line sim.LogLine) {
	acaConsoleLog(s.job.Properties.EnvironmentID, s.job.ID, line, monitorLogRow{"ContainerGroupName_s": s.job.Name})
}
