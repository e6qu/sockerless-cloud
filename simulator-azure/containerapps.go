package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/gorilla/websocket"
	dockerclient "github.com/moby/moby/client"
)

type ContainerAppJob struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	Location   string            `json:"location"`
	Tags       map[string]string `json:"tags,omitempty"`
	Identity   *SiteIdentity     `json:"identity,omitempty"`
	Properties JobProperties     `json:"properties"`
	SystemData *SystemData       `json:"systemData,omitempty"`
	// SystemIdentityClientID is the system-assigned identity's application ID,
	// which the directory reads and the wire shape does not carry.
	SystemIdentityClientID string `json:"-"`
}

// Azure Resource Manager stamps createdAt once and lastModifiedAt on every
// write.
type SystemData struct {
	CreatedAt      string `json:"createdAt,omitempty"`
	LastModifiedAt string `json:"lastModifiedAt,omitempty"`
}

type JobProperties struct {
	ProvisioningState   string            `json:"provisioningState"`
	EnvironmentID       string            `json:"environmentId,omitempty"`
	WorkloadProfileName string            `json:"workloadProfileName,omitempty"`
	Configuration       *JobConfiguration `json:"configuration,omitempty"`
	Template            *JobTemplate      `json:"template,omitempty"`
}

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

// JobSecret is a value, or the Key Vault secret KeyVaultURL names, read as the
// managed identity Identity names (containerapps_identity.go).
type JobSecret struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	Identity    string `json:"identity,omitempty"`
	KeyVaultURL string `json:"keyVaultUrl,omitempty"`
}

type JobRegistry struct {
	Server            string `json:"server"`
	Username          string `json:"username,omitempty"`
	PasswordSecretRef string `json:"passwordSecretRef,omitempty"`
	Identity          string `json:"identity,omitempty"`
}

type JobTemplate struct {
	Containers     []JobContainer `json:"containers,omitempty"`
	InitContainers []JobContainer `json:"initContainers,omitempty"`
	Volumes        []JobVolume    `json:"volumes,omitempty"`
}

// jobExecutionTemplate narrows to JobExecutionTemplate: it declares only
// containers and initContainers, and JobExecutionContainer carries no
// volumeMounts or probes, so an execution never echoes them.
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

// ContainerAppProbe follows Kubernetes' probe semantics.
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

type JobVolume struct {
	Name        string `json:"name"`
	StorageType string `json:"storageType,omitempty"`
	StorageName string `json:"storageName,omitempty"`
}

// The wire shape nests status, startTime, endTime and template under
// properties, as the Microsoft.App jobs spec declares.
type JobExecution struct {
	ID         string                 `json:"id"`
	Name       string                 `json:"name"`
	Type       string                 `json:"type,omitempty"`
	Properties JobExecutionProperties `json:"properties"`
}

type JobExecutionProperties struct {
	Status    string       `json:"status"`
	StartTime string       `json:"startTime"`
	EndTime   string       `json:"endTime,omitempty"`
	Template  *JobTemplate `json:"template,omitempty"`
}

var acaJobs sim.Store[ContainerAppJob]

func registerContainerApps(srv *sim.Server) {
	jobs := sim.MakeStore[ContainerAppJob](srv.DB(), "aca_jobs")
	executions := sim.MakeStore[JobExecution](srv.DB(), "aca_executions")
	acaJobs, acaExecutions = jobs, executions
	acaRuns = sim.MakeStore[acaExecutionRun](srv.DB(), "aca_execution_runs")

	const basePath = "/subscriptions/{subscriptionId}/resourceGroups/{resourceGroupName}/providers/Microsoft.App"

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

		// Store the job Succeeded and emit the operation headers, so SDK pollers take
		// the real path. Keep CreatedAt on update.
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

	// The SDK's body poller reads Succeeded off the 200 body.
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
		var patch json.RawMessage
		if err := sim.ReadJSON(r, &patch); err != nil {
			AzureError(w, "InvalidRequestContent", "Failed to parse request body: "+err.Error(), http.StatusBadRequest)
			return
		}
		var err error
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

	// GET omits secrets; listsecrets returns them. The middleware lowercases the
	// action before dispatch.
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
		// SecretsCollection.value is required: keep [] rather than null.
		if job.Properties.Configuration != nil && job.Properties.Configuration.Secrets != nil {
			secrets = job.Properties.Configuration.Secrets
		}
		sim.WriteJSON(w, http.StatusOK, map[string]any{"value": secrets})
	})

	// Azure answers 202 with the operation headers and an empty body for an
	// existing job, which reads Deleting until the operation completes, and 204
	// for an absent one.
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

		// BeginStart uses FinalStateViaLocation, so it polls the Location URL for the
		// result.
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

	// Answers the executions it asked to stop (ContainerAppJobExecutions).
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

	// The exec WebSocket takes the command in the `command` query parameter and
	// bridges to a `docker exec` against the execution's container.
	srv.HandleFunc("POST "+basePath+"/jobs/{jobName}/executions/{execName}/exec", handleACAJobExec)
}

// acaSettleJobIdentity brings the directory in step with the job's
// system-assigned identity; when the secrets do not resolve against it, it
// answers the request and restores the directory.
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

// handleACAJobExec bridges to the main container of the execution's first
// running replica.
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

	// Run through `sh -c` so arbitrary shell expressions work.
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

	// The protocol does not split stdout and stderr on the wire, so the client
	// receives the engine's multiplexed stream.
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

func randomSuffix(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	_, _ = rand.Read(b)
	for i := range b {
		b[i] = chars[int(b[i])%len(chars)]
	}
	return string(b)
}

type acaLogSink struct {
	job ContainerAppJob
}

func (s *acaLogSink) WriteLog(line sim.LogLine) {
	acaConsoleLog(s.job.Properties.EnvironmentID, s.job.ID, line, monitorLogRow{"ContainerGroupName_s": s.job.Name})
}
