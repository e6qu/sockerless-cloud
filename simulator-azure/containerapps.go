package main

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	sim "github.com/e6qu/sockerless-cloud/simulator-azure/shared"
	"github.com/gorilla/websocket"
	dockerclient "github.com/moby/moby/client"
)

// Container handle tracker for Container Apps Jobs real execution
var acaProcessHandles sync.Map // map[execID]*acaExecutionProcesses

type acaExecutionProcesses struct {
	Main     *sim.ContainerHandle
	Sidecars []*sim.ContainerHandle
}

func stopACAExecutionProcesses(p *acaExecutionProcesses) {
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

// ContainerAppJob represents an Azure Container Apps Job resource.
type ContainerAppJob struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Properties JobProperties     `json:"properties"`
	SystemData *SystemData       `json:"systemData,omitempty"`
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

// JobSecret holds a secret reference for a Container Apps Job.
//
// KeyVaultURL is an operator-supplied reference to an Azure Key Vault
// secret (`https://<vault>.vault.azure.net/secrets/<name>[/<version>]`).
// The sim stores and round-trips the string but does not auto-resolve
// it on Job execution — real ACA reads the KV secret at job-start
// time using the supplied managed identity. The sim's KV data plane
// (in keyvault.go) accepts the same URL shape when followed by a
// data-plane client, but the ACA Job runtime itself doesn't do the
// resolution step today. Operator-supplied external URL.
type JobSecret struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Identity    string `json:"identity,omitempty"`
	KeyVaultURL string `json:"keyVaultUrl,omitempty"` // external (operator-supplied): KV secret reference; sim KV data plane accepts the URL shape but ACA Job runtime doesn't auto-resolve
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
// carries no volumeMounts, so an execution never echoes the volumes or mounts
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
	acaJobs = jobs

	const basePath = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App"

	// PUT - Create or update job
	srv.HandleFunc("PUT "+basePath+"/jobs/{jobName}", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		var req ContainerAppJob
		if err := sim.ReadJSON(r, &req); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}

		if req.Location == "" {
			sim.AzureError(w, "InvalidRequestContent", "The 'location' property is required.", http.StatusBadRequest)
			return
		}

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)

		existing, exists := jobs.Get(resourceID)
		if exists && existing.Properties.ProvisioningState == "Deleting" {
			sim.AzureErrorf(w, "Conflict", http.StatusConflict,
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

		job := ContainerAppJob{
			ID:       resourceID,
			Name:     name,
			Type:     "Microsoft.App/jobs",
			Location: req.Location,
			Tags:     req.Tags,
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

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)

		job, ok := jobs.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		sim.WriteJSON(w, http.StatusOK, job)
	})

	// GET - List jobs
	srv.HandleFunc("GET "+basePath+"/jobs", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		prefix := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/", sub, rg)

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
		prefix := fmt.Sprintf("/subscriptions/%s/", sub)
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)
		job, ok := jobs.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}
		patch, err := io.ReadAll(r.Body)
		if err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to read request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		prior := job
		if err := applyARMMergePatch(&job, patch); err != nil {
			sim.AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		// Identity and server-owned fields are not client-writable.
		job.ID, job.Name, job.Type, job.Location = prior.ID, prior.Name, prior.Type, prior.Location
		job.SystemData = prior.SystemData
		job.Properties.ProvisioningState = prior.Properties.ProvisioningState
		if job.Properties.Configuration != nil && job.Properties.Configuration.TriggerType == "" {
			job.Properties.Configuration.TriggerType = "Manual"
		}
		if job.SystemData != nil {
			job.SystemData.LastModifiedAt = time.Now().UTC().Format(time.RFC3339Nano)
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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)
		job, ok := jobs.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)

		job, ok := jobs.Get(resourceID)
		if !ok {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		job.Properties.ProvisioningState = "Deleting"
		jobs.Put(resourceID, job)
		opID := issueAzureAsyncOperation(func() {
			jobs.Delete(resourceID)
			// Also delete associated executions
			execs := executions.Filter(func(e JobExecution) bool {
				return strings.HasPrefix(e.ID, resourceID+"/executions/")
			})
			for _, e := range execs {
				executions.Delete(e.ID)
			}
		})
		acaAsyncOpHeaders(w, r, sub, job.Location, opID)
		w.WriteHeader(http.StatusAccepted)
	})

	// POST - Start execution
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/start", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)

		job, ok := jobs.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		// Read optional override template
		var override struct {
			Containers     []JobContainer `json:"containers,omitempty"`
			InitContainers []JobContainer `json:"initContainers,omitempty"`
		}
		if err := sim.ReadJSON(r, &override); err != nil {
			sim.AzureErrorf(w, "BadRequest", http.StatusBadRequest, "invalid request body: %v", err)
			return
		}

		execName := fmt.Sprintf("%s-%s", name, randomSuffix(7))
		execID := fmt.Sprintf("%s/executions/%s", resourceID, execName)

		template := job.Properties.Template
		if len(override.Containers) > 0 {
			template = &JobTemplate{
				Containers:     override.Containers,
				InitContainers: override.InitContainers,
			}
		}

		exec := JobExecution{
			ID:   execID,
			Name: execName,
			Type: "Microsoft.App/jobs/executions",
			Properties: JobExecutionProperties{
				Status:    "Running",
				StartTime: time.Now().UTC().Format(time.RFC3339),
				Template:  jobExecutionTemplate(template),
			},
		}

		executions.Put(execID, exec)

		// Inject log entry for execution start
		injectContainerAppLog(name, "Container started")

		// Auto-stop execution after replica timeout or process exit
		replicaTimeout := 0
		if job.Properties.Configuration != nil {
			replicaTimeout = job.Properties.Configuration.ReplicaTimeout
		}
		go func(id, jobShortName, envID string, replicaTimeout int, tmpl *JobTemplate) {
			timeout := 1800 * time.Second // Azure default
			if replicaTimeout > 0 {
				timeout = time.Duration(replicaTimeout) * time.Second
			}

			succeeded := true
			if tmpl != nil && len(tmpl.Containers) > 0 {
				// Container execution
				shortExecID := id
				if idx := strings.LastIndex(id, "/"); idx >= 0 {
					shortExecID = id[idx+1:]
				}

				// Resolve the env's Docker network and connect the
				// container with the job short name as DNS alias.
				// Other jobs in the same env resolve each other via
				// Docker's embedded DNS.
				var netName string
				var netAliases []string
				if envID != "" {
					if env, ok := acaEnvironments.Get(envID); ok && env.DockerNetworkName != "" {
						netName = env.DockerNetworkName
						netAliases = []string{jobShortName}
					}
				}

				sink := &acaLogSink{jobName: jobShortName}
				handle, sidecars, err := startACAJobContainers(context.Background(), id, shortExecID, tmpl, envID, timeout, netName, netAliases, sink)
				if err != nil {
					succeeded = false
				} else {
					acaProcessHandles.Store(id, &acaExecutionProcesses{Main: handle, Sidecars: sidecars})
					result := handle.Wait()
					acaProcessHandles.Delete(id)
					for _, h := range sidecars {
						h.Cancel()
					}
					succeeded = result.ExitCode == 0
				}
			} else {
				// No image — no-op (template has no containers)
				succeeded = true
			}

			completed := false
			executions.Update(id, func(e *JobExecution) {
				if e.Properties.Status != "Running" {
					return
				}
				completed = true
				if succeeded {
					e.Properties.Status = "Succeeded"
				} else {
					e.Properties.Status = "Failed"
				}
				e.Properties.EndTime = time.Now().UTC().Format(time.RFC3339)
			})
			if completed {
				// Match the actual outcome (the previous behaviour
				// always injected "Execution completed successfully"
				// regardless of `succeeded`, masking failed jobs as
				// fake-success in the log stream and breaking tests
				// like TestACAArithmeticInvalid that assert on the
				// failure marker).
				if succeeded {
					injectContainerAppLog(jobShortName, "Execution completed successfully")
				} else {
					injectContainerAppLog(jobShortName, "Execution failed")
				}
			}
		}(execID, name, job.Properties.EnvironmentID, replicaTimeout, template)

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
		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)
		if _, ok := jobs.Get(resourceID); !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The Resource 'Microsoft.App/jobs/%s' under resource group '%s' was not found.", name, rg)
			return
		}

		running := executions.Filter(func(e JobExecution) bool {
			return strings.HasPrefix(e.ID, resourceID+"/executions/") && e.Properties.Status == "Running"
		})
		stopped := make([]JobExecution, 0, len(running))
		for _, e := range running {
			if v, ok := acaProcessHandles.LoadAndDelete(e.ID); ok {
				if procs, ok := v.(*acaExecutionProcesses); ok {
					stopACAExecutionProcesses(procs)
				}
			}
			executions.Update(e.ID, func(ex *JobExecution) {
				ex.Properties.Status = "Stopped"
				ex.Properties.EndTime = time.Now().UTC().Format(time.RFC3339)
			})
			if updated, ok := executions.Get(e.ID); ok {
				stopped = append(stopped, updated)
			}
		}
		injectContainerAppLog(name, "Executions stopped")
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": stopped})
	})

	// GET - List executions
	srv.HandleFunc("GET "+basePath+"/jobs/{jobName}/executions", func(w http.ResponseWriter, r *http.Request) {
		sub := sim.PathParam(r, "subscriptionId")
		rg := sim.PathParam(r, "resourceGroupName")
		name := sim.PathParam(r, "jobName")

		resourceID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s", sub, rg, name)

		_, ok := jobs.Get(resourceID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		execID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s/executions/%s",
			sub, rg, jobName, execName)

		exec, ok := executions.Get(execID)
		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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

		execID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s/executions/%s",
			sub, rg, jobName, execName)

		// Cancel running container if any
		if v, ok := acaProcessHandles.LoadAndDelete(execID); ok {
			if procs, ok := v.(*acaExecutionProcesses); ok {
				stopACAExecutionProcesses(procs)
			}
		}

		ok := executions.Update(execID, func(e *JobExecution) {
			e.Properties.Status = "Stopped"
			e.Properties.EndTime = time.Now().UTC().Format(time.RFC3339)
		})
		if ok {
			injectContainerAppLog(jobName, "Execution stopped")
		}

		if !ok {
			sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
				"The execution '%s' for job '%s' was not found.", execName, jobName)
			return
		}

		w.WriteHeader(http.StatusOK)
	})

	// POST - Console exec WebSocket. Backend's aca/exec_cloud.go dials
	// this with the user command in the `command` query parameter.
	// The handler upgrades to WebSocket and bridges to a `docker exec`
	// against the running execution container — same pattern as
	// simulator-aws/ecs.go's SSM ExecuteCommand handler, simpler
	// frame format (raw binary in/out, no SSM AgentMessage wrapping).
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/executions/{execName}/exec", handleACAJobExec)
}

func startACAJobContainers(ctx context.Context, execID, shortExecID string, tmpl *JobTemplate, envID string, timeout time.Duration, netName string, netAliases []string, sink sim.LogSink) (*sim.ContainerHandle, []*sim.ContainerHandle, error) {
	if tmpl == nil || len(tmpl.Containers) == 0 {
		return nil, nil, fmt.Errorf("execution has no containers")
	}

	volByName := make(map[string]JobVolume, len(tmpl.Volumes))
	for _, v := range tmpl.Volumes {
		volByName[v.Name] = v
	}
	bindsFor := func(c JobContainer) []string {
		var binds []string
		for _, mp := range c.VolumeMounts {
			v, ok := volByName[mp.VolumeName]
			if !ok || !strings.EqualFold(v.StorageType, "AzureFile") || v.StorageName == "" {
				continue
			}
			acct, share, found := LookupEnvStorageBinding(envID, v.StorageName)
			if !found {
				continue
			}
			binds = append(binds, FileShareHostDir(acct, share)+":"+mp.MountPath)
		}
		return binds
	}
	envFor := func(c JobContainer) map[string]string {
		cmdEnv := make(map[string]string, len(c.Env))
		for _, ev := range c.Env {
			cmdEnv[ev.Name] = ev.Value
		}
		return mergeEnv(cmdEnv, hostMetadataEnv())
	}

	main := tmpl.Containers[0]
	mainImage := sim.ResolveLocalImage(main.Image)
	mainPlatform, err := localImagePlatform(ctx, mainImage)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect main container %q image platform: %w", main.Name, err)
	}
	mainHandle, err := sim.StartContainerSync(sim.ContainerConfig{
		Image:        mainImage,
		Architecture: mainPlatform,
		Command:      main.Command,
		Args:         main.Args,
		Env:          envFor(main),
		Timeout:      timeout,
		Name:         fmt.Sprintf("sockerless-sim-azure-execution-%s", shortExecID),
		Labels: map[string]string{
			"sockerless-sim-type":                "aca-job-execution",
			"sockerless-exec-id":                 execID,
			"sockerless-sim-execution-container": main.Name,
		},
		Network:        netName,
		NetworkAliases: netAliases,
		Binds:          bindsFor(main),
		ExtraHosts:     hostMetadataExtraHosts(),
		Sandbox:        sim.SandboxACA,
	}, sink)
	if err != nil {
		return nil, nil, fmt.Errorf("start main container %q: %w", main.Name, err)
	}

	var sidecars []*sim.ContainerHandle
	for i, c := range tmpl.Containers[1:] {
		sidecarImage := sim.ResolveLocalImage(c.Image)
		sidecarPlatform, err := localImagePlatform(ctx, sidecarImage)
		if err != nil {
			mainHandle.Cancel()
			for _, h := range sidecars {
				h.Cancel()
			}
			return nil, nil, fmt.Errorf("inspect sidecar container %q image platform: %w", c.Name, err)
		}
		handle, err := sim.StartContainerSync(sim.ContainerConfig{
			Image:        sidecarImage,
			Architecture: sidecarPlatform,
			Command:      c.Command,
			Args:         c.Args,
			Env:          envFor(c),
			Timeout:      timeout,
			Name:         fmt.Sprintf("sockerless-sim-azure-execution-%s-sidecar-%d", shortExecID, i),
			Labels: map[string]string{
				"sockerless-sim-type":                "aca-job-execution",
				"sockerless-exec-id":                 execID,
				"sockerless-sim-execution-container": c.Name,
			},
			NetworkMode: "container:" + mainHandle.ContainerID,
			Binds:       bindsFor(c),
			Sandbox:     sim.SandboxACA,
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

// handleACAJobExec serves the ACA jobs-exec WebSocket. The user
// command arrives as `?command=<urlencoded>`; the running execution
// container is looked up in acaProcessHandles; the WebSocket is then
// bridged to a docker exec session against that container.
func handleACAJobExec(w http.ResponseWriter, r *http.Request) {
	sub := sim.PathParam(r, "subscriptionId")
	rg := sim.PathParam(r, "resourceGroupName")
	jobName := sim.PathParam(r, "jobName")
	execName := sim.PathParam(r, "execName")
	command := r.URL.Query().Get("command")
	if command == "" {
		sim.AzureError(w, "BadRequest", "command query parameter is required", http.StatusBadRequest)
		return
	}

	execID := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.App/jobs/%s/executions/%s",
		sub, rg, jobName, execName)
	v, ok := acaProcessHandles.Load(execID)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"No running execution container for '%s/%s'", jobName, execName)
		return
	}
	procs, ok := v.(*acaExecutionProcesses)
	if !ok {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
			"No running execution container for '%s/%s'", jobName, execName)
		return
	}
	handle := procs.Main
	if handle == nil {
		sim.AzureErrorf(w, "ResourceNotFound", http.StatusNotFound,
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
	execResp, err := cli.ExecCreate(ctx, handle.ContainerID, execCfg)
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

// acaLogSink implements sim.LogSink and writes log lines to Log Analytics.
type acaLogSink struct {
	jobName string
}

func (s *acaLogSink) WriteLog(line sim.LogLine) {
	injectContainerAppLog(s.jobName, line.Text)
}
