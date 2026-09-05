package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/google/uuid"
)

// AWS Batch — REST/JSON protocol with operation-specific POST paths (/v1/<opname>).
// All operations are POST. Container jobs execute through the shared workload runner.

type BatchComputeEnvironment struct {
	ComputeEnvironmentName string            `json:"computeEnvironmentName"`
	ComputeEnvironmentArn  string            `json:"computeEnvironmentArn"`
	EcsClusterArn          string            `json:"ecsClusterArn"`
	State                  string            `json:"state"`
	Status                 string            `json:"status"`
	StatusReason           string            `json:"statusReason,omitempty"`
	Type                   string            `json:"type"`
	ComputeResources       map[string]any    `json:"computeResources,omitempty"`
	ServiceRole            string            `json:"serviceRole,omitempty"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

type BatchJobQueue struct {
	JobQueueName            string            `json:"jobQueueName"`
	JobQueueArn             string            `json:"jobQueueArn"`
	State                   string            `json:"state"`
	Status                  string            `json:"status"`
	StatusReason            string            `json:"statusReason,omitempty"`
	Priority                int               `json:"priority"`
	SchedulingPolicyArn     string            `json:"schedulingPolicyArn,omitempty"`
	ComputeEnvironmentOrder []map[string]any  `json:"computeEnvironmentOrder"`
	Tags                    map[string]string `json:"tags,omitempty"`
}

type BatchJobDefinition struct {
	JobDefinitionName   string            `json:"jobDefinitionName"`
	JobDefinitionArn    string            `json:"jobDefinitionArn"`
	Revision            int               `json:"revision"`
	Status              string            `json:"status"`
	Type                string            `json:"type"`
	ContainerProperties map[string]any    `json:"containerProperties,omitempty"`
	RetryStrategy       map[string]any    `json:"retryStrategy,omitempty"`
	Timeout             map[string]any    `json:"timeout,omitempty"`
	Tags                map[string]string `json:"tags,omitempty"`
}

type BatchSchedulingPolicy struct {
	Name             string            `json:"name"`
	Arn              string            `json:"arn"`
	FairsharePolicy  map[string]any    `json:"fairsharePolicy,omitempty"`
	QuotaSharePolicy map[string]any    `json:"quotaSharePolicy,omitempty"`
	Tags             map[string]string `json:"tags,omitempty"`
}

type BatchJob struct {
	JobID           string               `json:"jobId"`
	JobArn          string               `json:"jobArn,omitempty"`
	JobName         string               `json:"jobName"`
	JobQueue        string               `json:"jobQueue"`
	Status          string               `json:"status"`
	StatusReason    string               `json:"statusReason,omitempty"`
	JobDefinition   string               `json:"jobDefinition"`
	CreatedAt       int64                `json:"createdAt"`
	StartedAt       int64                `json:"startedAt"`
	StoppedAt       int64                `json:"stoppedAt"`
	Container       map[string]any       `json:"container,omitempty"`
	Tags            map[string]string    `json:"tags,omitempty"`
	ExecutionConfig *sim.ContainerConfig `json:"-"`
}

// BatchConsumableResource models a Batch consumable resource (an ARN plus a
// total/in-use/available capacity that jobs draw against).
type BatchConsumableResource struct {
	ConsumableResourceName string            `json:"consumableResourceName"`
	ConsumableResourceArn  string            `json:"consumableResourceArn"`
	TotalQuantity          int64             `json:"totalQuantity"`
	InUseQuantity          int64             `json:"inUseQuantity"`
	ResourceType           string            `json:"resourceType"`
	CreatedAt              int64             `json:"createdAt"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

// BatchServiceEnvironment models a Batch service environment (an ARN, a state,
// a status, and a list of capacity limits).
type BatchServiceEnvironment struct {
	ServiceEnvironmentName string            `json:"serviceEnvironmentName"`
	ServiceEnvironmentArn  string            `json:"serviceEnvironmentArn"`
	ServiceEnvironmentType string            `json:"serviceEnvironmentType"`
	State                  string            `json:"state"`
	Status                 string            `json:"status"`
	CapacityLimits         []map[string]any  `json:"capacityLimits"`
	Tags                   map[string]string `json:"tags,omitempty"`
}

// BatchServiceJob models a Batch service job. Like a regular Batch job it
// settles to SUCCEEDED; service jobs are payload-driven (no container) so the
// lifecycle is a state walk, not a container run.
type BatchServiceJob struct {
	JobID                 string            `json:"jobId"`
	JobArn                string            `json:"jobArn"`
	JobName               string            `json:"jobName"`
	JobQueue              string            `json:"jobQueue"`
	ServiceJobType        string            `json:"serviceJobType"`
	Status                string            `json:"status"`
	StatusReason          string            `json:"statusReason,omitempty"`
	ServiceRequestPayload string            `json:"serviceRequestPayload,omitempty"`
	ShareIdentifier       string            `json:"shareIdentifier,omitempty"`
	QuotaShareName        string            `json:"quotaShareName,omitempty"`
	SchedulingPriority    int               `json:"schedulingPriority"`
	IsTerminated          bool              `json:"isTerminated"`
	CreatedAt             int64             `json:"createdAt"`
	ScheduledAt           int64             `json:"scheduledAt"`
	StartedAt             int64             `json:"startedAt"`
	StoppedAt             int64             `json:"stoppedAt"`
	Tags                  map[string]string `json:"tags,omitempty"`
}

// BatchQuotaShare models a Batch quota share scoped to a job queue.
type BatchQuotaShare struct {
	QuotaShareName               string            `json:"quotaShareName"`
	QuotaShareArn                string            `json:"quotaShareArn"`
	JobQueueArn                  string            `json:"jobQueueArn"`
	CapacityLimits               []map[string]any  `json:"capacityLimits"`
	ResourceSharingConfiguration map[string]any    `json:"resourceSharingConfiguration,omitempty"`
	PreemptionConfiguration      map[string]any    `json:"preemptionConfiguration,omitempty"`
	State                        string            `json:"state"`
	Status                       string            `json:"status"`
	Tags                         map[string]string `json:"tags,omitempty"`
}

var (
	batchComputeEnvs   sim.Store[BatchComputeEnvironment]
	batchJobQueues     sim.Store[BatchJobQueue]
	batchJobDefs       sim.Store[BatchJobDefinition]
	batchJobs          sim.Store[BatchJob]
	batchSchedPols     sim.Store[BatchSchedulingPolicy]
	batchJobRevisions  sim.Store[int]
	batchConsumableRes sim.Store[BatchConsumableResource]
	batchServiceEnvs   sim.Store[BatchServiceEnvironment]
	batchServiceJobs   sim.Store[BatchServiceJob]
	batchQuotaShares   sim.Store[BatchQuotaShare]
	batchJobHandles    sync.Map
	batchMu            sync.Mutex
)

func registerBatch(srv *sim.Server) {
	batchComputeEnvs = sim.MakeStore[BatchComputeEnvironment](srv.DB(), "batch_compute_envs")
	batchJobQueues = sim.MakeStore[BatchJobQueue](srv.DB(), "batch_job_queues")
	batchJobDefs = sim.MakeStore[BatchJobDefinition](srv.DB(), "batch_job_definitions")
	batchJobs = sim.MakeStore[BatchJob](srv.DB(), "batch_jobs")
	batchSchedPols = sim.MakeStore[BatchSchedulingPolicy](srv.DB(), "batch_scheduling_policies")
	batchJobRevisions = sim.MakeStore[int](srv.DB(), "batch_job_revisions")
	batchConsumableRes = sim.MakeStore[BatchConsumableResource](srv.DB(), "batch_consumable_resources")
	batchServiceEnvs = sim.MakeStore[BatchServiceEnvironment](srv.DB(), "batch_service_environments")
	batchServiceJobs = sim.MakeStore[BatchServiceJob](srv.DB(), "batch_service_jobs")
	batchQuotaShares = sim.MakeStore[BatchQuotaShare](srv.DB(), "batch_quota_shares")

	batchResource := cloudTrailRESTResource("AWS::Batch::Resource", "resourceArn")
	// All Batch ops are POST to /v1/<lowercaseopname>
	srv.HandleFunc("POST /v1/createcomputeenvironment", cloudTrailRecordedREST("CreateComputeEnvironment", "batch.amazonaws.com", nil, handleBatchCreateComputeEnvironment))
	srv.HandleFunc("POST /v1/describecomputeenvironments", cloudTrailRecordedREST("DescribeComputeEnvironments", "batch.amazonaws.com", nil, handleBatchDescribeComputeEnvironments))
	srv.HandleFunc("POST /v1/updatecomputeenvironment", cloudTrailRecordedREST("UpdateComputeEnvironment", "batch.amazonaws.com", nil, handleBatchUpdateComputeEnvironment))
	srv.HandleFunc("POST /v1/deletecomputeenvironment", cloudTrailRecordedREST("DeleteComputeEnvironment", "batch.amazonaws.com", nil, handleBatchDeleteComputeEnvironment))

	srv.HandleFunc("POST /v1/createjobqueue", cloudTrailRecordedREST("CreateJobQueue", "batch.amazonaws.com", nil, handleBatchCreateJobQueue))
	srv.HandleFunc("POST /v1/describejobqueues", cloudTrailRecordedREST("DescribeJobQueues", "batch.amazonaws.com", nil, handleBatchDescribeJobQueues))
	srv.HandleFunc("POST /v1/updatejobqueue", cloudTrailRecordedREST("UpdateJobQueue", "batch.amazonaws.com", nil, handleBatchUpdateJobQueue))
	srv.HandleFunc("POST /v1/deletejobqueue", cloudTrailRecordedREST("DeleteJobQueue", "batch.amazonaws.com", nil, handleBatchDeleteJobQueue))

	srv.HandleFunc("POST /v1/registerjobdefinition", cloudTrailRecordedREST("RegisterJobDefinition", "batch.amazonaws.com", nil, handleBatchRegisterJobDefinition))
	srv.HandleFunc("POST /v1/describejobdefinitions", cloudTrailRecordedREST("DescribeJobDefinitions", "batch.amazonaws.com", nil, handleBatchDescribeJobDefinitions))
	srv.HandleFunc("POST /v1/deregisterjobdefinition", cloudTrailRecordedREST("DeregisterJobDefinition", "batch.amazonaws.com", nil, handleBatchDeregisterJobDefinition))

	srv.HandleFunc("POST /v1/submitjob", cloudTrailRecordedREST("SubmitJob", "batch.amazonaws.com", nil, handleBatchSubmitJob))
	srv.HandleFunc("POST /v1/describejobs", cloudTrailRecordedREST("DescribeJobs", "batch.amazonaws.com", nil, handleBatchDescribeJobs))
	srv.HandleFunc("POST /v1/listjobs", cloudTrailRecordedREST("ListJobs", "batch.amazonaws.com", nil, handleBatchListJobs))
	srv.HandleFunc("POST /v1/canceljob", cloudTrailRecordedREST("CancelJob", "batch.amazonaws.com", nil, handleBatchCancelJob))
	srv.HandleFunc("POST /v1/terminatejob", cloudTrailRecordedREST("TerminateJob", "batch.amazonaws.com", nil, handleBatchTerminateJob))

	srv.HandleFunc("POST /v1/createschedulingpolicy", cloudTrailRecordedREST("CreateSchedulingPolicy", "batch.amazonaws.com", nil, handleBatchCreateSchedulingPolicy))
	srv.HandleFunc("POST /v1/describeschedulingpolicies", cloudTrailRecordedREST("DescribeSchedulingPolicies", "batch.amazonaws.com", nil, handleBatchDescribeSchedulingPolicies))
	srv.HandleFunc("POST /v1/listschedulingpolicies", cloudTrailRecordedREST("ListSchedulingPolicies", "batch.amazonaws.com", nil, handleBatchListSchedulingPolicies))
	srv.HandleFunc("POST /v1/updateschedulingpolicy", cloudTrailRecordedREST("UpdateSchedulingPolicy", "batch.amazonaws.com", nil, handleBatchUpdateSchedulingPolicy))
	srv.HandleFunc("POST /v1/deleteschedulingpolicy", cloudTrailRecordedREST("DeleteSchedulingPolicy", "batch.amazonaws.com", nil, handleBatchDeleteSchedulingPolicy))

	// Consumable resources
	srv.HandleFunc("POST /v1/createconsumableresource", cloudTrailRecordedREST("CreateConsumableResource", "batch.amazonaws.com", nil, handleBatchCreateConsumableResource))
	srv.HandleFunc("POST /v1/describeconsumableresource", cloudTrailRecordedREST("DescribeConsumableResource", "batch.amazonaws.com", nil, handleBatchDescribeConsumableResource))
	srv.HandleFunc("POST /v1/listconsumableresources", cloudTrailRecordedREST("ListConsumableResources", "batch.amazonaws.com", nil, handleBatchListConsumableResources))
	srv.HandleFunc("POST /v1/updateconsumableresource", cloudTrailRecordedREST("UpdateConsumableResource", "batch.amazonaws.com", nil, handleBatchUpdateConsumableResource))
	srv.HandleFunc("POST /v1/deleteconsumableresource", cloudTrailRecordedREST("DeleteConsumableResource", "batch.amazonaws.com", nil, handleBatchDeleteConsumableResource))
	srv.HandleFunc("POST /v1/listjobsbyconsumableresource", cloudTrailRecordedREST("ListJobsByConsumableResource", "batch.amazonaws.com", nil, handleBatchListJobsByConsumableResource))

	// Service environments
	srv.HandleFunc("POST /v1/createserviceenvironment", cloudTrailRecordedREST("CreateServiceEnvironment", "batch.amazonaws.com", nil, handleBatchCreateServiceEnvironment))
	srv.HandleFunc("POST /v1/describeserviceenvironments", cloudTrailRecordedREST("DescribeServiceEnvironments", "batch.amazonaws.com", nil, handleBatchDescribeServiceEnvironments))
	srv.HandleFunc("POST /v1/updateserviceenvironment", cloudTrailRecordedREST("UpdateServiceEnvironment", "batch.amazonaws.com", nil, handleBatchUpdateServiceEnvironment))
	srv.HandleFunc("POST /v1/deleteserviceenvironment", cloudTrailRecordedREST("DeleteServiceEnvironment", "batch.amazonaws.com", nil, handleBatchDeleteServiceEnvironment))

	// Service jobs
	srv.HandleFunc("POST /v1/submitservicejob", cloudTrailRecordedREST("SubmitServiceJob", "batch.amazonaws.com", nil, handleBatchSubmitServiceJob))
	srv.HandleFunc("POST /v1/describeservicejob", cloudTrailRecordedREST("DescribeServiceJob", "batch.amazonaws.com", nil, handleBatchDescribeServiceJob))
	srv.HandleFunc("POST /v1/listservicejobs", cloudTrailRecordedREST("ListServiceJobs", "batch.amazonaws.com", nil, handleBatchListServiceJobs))
	srv.HandleFunc("POST /v1/terminateservicejob", cloudTrailRecordedREST("TerminateServiceJob", "batch.amazonaws.com", nil, handleBatchTerminateServiceJob))
	srv.HandleFunc("POST /v1/updateservicejob", cloudTrailRecordedREST("UpdateServiceJob", "batch.amazonaws.com", nil, handleBatchUpdateServiceJob))

	// Quota shares
	srv.HandleFunc("POST /v1/createquotashare", cloudTrailRecordedREST("CreateQuotaShare", "batch.amazonaws.com", nil, handleBatchCreateQuotaShare))
	srv.HandleFunc("POST /v1/describequotashare", cloudTrailRecordedREST("DescribeQuotaShare", "batch.amazonaws.com", nil, handleBatchDescribeQuotaShare))
	srv.HandleFunc("POST /v1/listquotashares", cloudTrailRecordedREST("ListQuotaShares", "batch.amazonaws.com", nil, handleBatchListQuotaShares))
	srv.HandleFunc("POST /v1/updatequotashare", cloudTrailRecordedREST("UpdateQuotaShare", "batch.amazonaws.com", nil, handleBatchUpdateQuotaShare))
	srv.HandleFunc("POST /v1/deletequotashare", cloudTrailRecordedREST("DeleteQuotaShare", "batch.amazonaws.com", nil, handleBatchDeleteQuotaShare))

	// Job queue snapshot
	srv.HandleFunc("POST /v1/getjobqueuesnapshot", cloudTrailRecordedREST("GetJobQueueSnapshot", "batch.amazonaws.com", nil, handleBatchGetJobQueueSnapshot))

	// Resource-level tags
	srv.HandleFunc("GET /v1/tags/{resourceArn}", cloudTrailRecordedREST("ListTagsForResource", "batch.amazonaws.com", batchResource, handleBatchListTagsForResource))
	srv.HandleFunc("POST /v1/tags/{resourceArn}", cloudTrailRecordedREST("TagResource", "batch.amazonaws.com", batchResource, handleBatchTagResource))
	srv.HandleFunc("DELETE /v1/tags/{resourceArn}", cloudTrailRecordedREST("UntagResource", "batch.amazonaws.com", batchResource, handleBatchUntagResource))
	if err := recoverBatchJobs(); err != nil {
		panic(fmt.Sprintf("restore AWS Batch jobs: %v", err))
	}
}

func recoverBatchJobs() error {
	for _, job := range batchJobs.List() {
		if batchTerminal(job.Status) {
			continue
		}
		existing, err := sim.FindExistingContainers(map[string]string{"aws-batch-job-id": job.JobID})
		if err != nil {
			return fmt.Errorf("find job %s container: %w", job.JobID, err)
		}
		if len(existing) > 1 {
			return fmt.Errorf("job %s has %d workload containers", job.JobID, len(existing))
		}
		if len(existing) == 1 {
			cfg := sim.ContainerConfig{}
			if job.ExecutionConfig != nil {
				cfg = *job.ExecutionConfig
				if cfg.Timeout > 0 {
					cfg.Timeout -= time.Since(time.UnixMilli(job.StartedAt))
					if cfg.Timeout <= 0 {
						cfg.Timeout = time.Nanosecond
					}
				}
			}
			handle, err := sim.AdoptContainer(existing[0].ID, cfg, sim.NoopSink{})
			if err != nil {
				return fmt.Errorf("adopt job %s container: %w", job.JobID, err)
			}
			batchJobs.Update(job.JobID, func(current *BatchJob) {
				current.Status = "RUNNING"
				if current.StartedAt == 0 {
					current.StartedAt = batchEpochMs()
				}
			})
			batchJobHandles.Store(job.JobID, handle)
			go batchWaitForJob(job.JobID, handle)
			continue
		}
		if job.ExecutionConfig == nil {
			return fmt.Errorf("job %s has neither a workload container nor a persisted execution configuration", job.JobID)
		}
		handle, err := sim.StartContainerSync(*job.ExecutionConfig, sim.NoopSink{})
		if err != nil {
			return fmt.Errorf("resume job %s before container start: %w", job.JobID, err)
		}
		batchJobHandles.Store(job.JobID, handle)
		go batchRunJobLifecycle(job.JobID, handle)
	}
	return nil
}

func batchARN(resource string) string {
	return fmt.Sprintf("arn:aws:batch:us-east-1:123456789012:%s", resource)
}

func batchEpochMs() int64 {
	return time.Now().UnixMilli()
}

func batchWriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func batchWriteError(w http.ResponseWriter, status int, msg string) {
	code := "ClientException"
	if status >= 500 {
		code = "ServerException"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Amzn-Errortype", code)
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"__type":  code,
		"message": msg,
	})
}

func handleBatchCreateComputeEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComputeEnvironmentName string            `json:"computeEnvironmentName"`
		Type                   string            `json:"type"`
		State                  string            `json:"state"`
		ComputeResources       map[string]any    `json:"computeResources"`
		ServiceRole            string            `json:"serviceRole"`
		Tags                   map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ComputeEnvironmentName == "" {
		batchWriteError(w, http.StatusBadRequest, "computeEnvironmentName is required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if _, ok := batchComputeEnvs.Get(req.ComputeEnvironmentName); ok {
		batchWriteError(w, http.StatusBadRequest, "Compute environment already exists: "+req.ComputeEnvironmentName)
		return
	}
	state := req.State
	if state == "" {
		state = "ENABLED"
	}
	ceType := req.Type
	if ceType == "" {
		ceType = "MANAGED"
	}
	ce := BatchComputeEnvironment{
		ComputeEnvironmentName: req.ComputeEnvironmentName,
		ComputeEnvironmentArn:  batchARN("compute-environment/" + req.ComputeEnvironmentName),
		EcsClusterArn:          batchARN("cluster/" + req.ComputeEnvironmentName),
		State:                  state,
		Status:                 "VALID",
		Type:                   ceType,
		ComputeResources:       req.ComputeResources,
		ServiceRole:            req.ServiceRole,
		Tags:                   req.Tags,
	}
	batchComputeEnvs.Put(req.ComputeEnvironmentName, ce)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"computeEnvironmentArn":  ce.ComputeEnvironmentArn,
		"computeEnvironmentName": ce.ComputeEnvironmentName,
	})
}

func handleBatchDescribeComputeEnvironments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComputeEnvironments []string `json:"computeEnvironments"`
		MaxResults          *int32   `json:"maxResults"`
		NextToken           string   `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var result []BatchComputeEnvironment
	if len(req.ComputeEnvironments) > 0 {
		for _, nameOrARN := range req.ComputeEnvironments {
			name := batchNameFromARN(nameOrARN)
			if ce, ok := batchComputeEnvs.Get(name); ok {
				result = append(result, ce)
			}
		}
	} else {
		result = batchComputeEnvs.List()
		sort.Slice(result, func(i, j int) bool { return result[i].ComputeEnvironmentName < result[j].ComputeEnvironmentName })
	}
	if result == nil {
		result = []BatchComputeEnvironment{}
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"computeEnvironments": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchUpdateComputeEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComputeEnvironment string         `json:"computeEnvironment"`
		State              string         `json:"state"`
		ComputeResources   map[string]any `json:"computeResources"`
		ServiceRole        string         `json:"serviceRole"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	name := batchNameFromARN(req.ComputeEnvironment)
	ce, ok := batchComputeEnvs.Get(name)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Compute environment not found: "+name)
		return
	}
	if req.State != "" {
		ce.State = req.State
	}
	if req.ComputeResources != nil {
		ce.ComputeResources = req.ComputeResources
	}
	if req.ServiceRole != "" {
		ce.ServiceRole = req.ServiceRole
	}
	batchComputeEnvs.Put(name, ce)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"computeEnvironmentArn":  ce.ComputeEnvironmentArn,
		"computeEnvironmentName": ce.ComputeEnvironmentName,
	})
}

func handleBatchDeleteComputeEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ComputeEnvironment string `json:"computeEnvironment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	batchComputeEnvs.Delete(batchNameFromARN(req.ComputeEnvironment))
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchCreateJobQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueueName            string            `json:"jobQueueName"`
		State                   string            `json:"state"`
		Priority                int               `json:"priority"`
		SchedulingPolicyArn     string            `json:"schedulingPolicyArn"`
		ComputeEnvironmentOrder []map[string]any  `json:"computeEnvironmentOrder"`
		Tags                    map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.JobQueueName == "" {
		batchWriteError(w, http.StatusBadRequest, "jobQueueName is required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if _, ok := batchJobQueues.Get(req.JobQueueName); ok {
		batchWriteError(w, http.StatusBadRequest, "Job queue already exists: "+req.JobQueueName)
		return
	}
	state := req.State
	if state == "" {
		state = "ENABLED"
	}
	ceOrder := req.ComputeEnvironmentOrder
	if ceOrder == nil {
		ceOrder = []map[string]any{}
	}
	q := BatchJobQueue{
		JobQueueName:            req.JobQueueName,
		JobQueueArn:             batchARN("job-queue/" + req.JobQueueName),
		State:                   state,
		Status:                  "VALID",
		Priority:                req.Priority,
		SchedulingPolicyArn:     req.SchedulingPolicyArn,
		ComputeEnvironmentOrder: ceOrder,
		Tags:                    req.Tags,
	}
	batchJobQueues.Put(req.JobQueueName, q)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"jobQueueArn":  q.JobQueueArn,
		"jobQueueName": q.JobQueueName,
	})
}

func handleBatchDescribeJobQueues(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueues  []string `json:"jobQueues"`
		MaxResults *int32   `json:"maxResults"`
		NextToken  string   `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	var result []BatchJobQueue
	if len(req.JobQueues) > 0 {
		for _, nameOrARN := range req.JobQueues {
			name := batchNameFromARN(nameOrARN)
			if q, ok := batchJobQueues.Get(name); ok {
				result = append(result, q)
			}
		}
	} else {
		result = batchJobQueues.List()
		sort.Slice(result, func(i, j int) bool { return result[i].JobQueueName < result[j].JobQueueName })
	}
	if result == nil {
		result = []BatchJobQueue{}
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"jobQueues": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchUpdateJobQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueue                string           `json:"jobQueue"`
		State                   string           `json:"state"`
		Priority                *int             `json:"priority"`
		SchedulingPolicyArn     string           `json:"schedulingPolicyArn"`
		ComputeEnvironmentOrder []map[string]any `json:"computeEnvironmentOrder"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	name := batchNameFromARN(req.JobQueue)
	q, ok := batchJobQueues.Get(name)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Job queue not found: "+req.JobQueue)
		return
	}
	if req.State != "" {
		q.State = req.State
	}
	if req.Priority != nil {
		q.Priority = *req.Priority
	}
	if req.SchedulingPolicyArn != "" {
		q.SchedulingPolicyArn = req.SchedulingPolicyArn
	}
	if req.ComputeEnvironmentOrder != nil {
		q.ComputeEnvironmentOrder = req.ComputeEnvironmentOrder
	}
	batchJobQueues.Put(name, q)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"jobQueueArn":  q.JobQueueArn,
		"jobQueueName": q.JobQueueName,
	})
}

func handleBatchDeleteJobQueue(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueue string `json:"jobQueue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	batchJobQueues.Delete(batchNameFromARN(req.JobQueue))
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchRegisterJobDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobDefinitionName   string            `json:"jobDefinitionName"`
		Type                string            `json:"type"`
		ContainerProperties map[string]any    `json:"containerProperties"`
		RetryStrategy       map[string]any    `json:"retryStrategy"`
		Timeout             map[string]any    `json:"timeout"`
		Tags                map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.JobDefinitionName == "" {
		batchWriteError(w, http.StatusBadRequest, "jobDefinitionName is required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	rev, _ := batchJobRevisions.Get(req.JobDefinitionName)
	rev++
	batchJobRevisions.Put(req.JobDefinitionName, rev)

	jobType := req.Type
	if jobType == "" {
		jobType = "container"
	}
	key := fmt.Sprintf("%s:%d", req.JobDefinitionName, rev)
	jd := BatchJobDefinition{
		JobDefinitionName:   req.JobDefinitionName,
		JobDefinitionArn:    batchARN(fmt.Sprintf("job-definition/%s:%d", req.JobDefinitionName, rev)),
		Revision:            rev,
		Status:              "ACTIVE",
		Type:                jobType,
		ContainerProperties: req.ContainerProperties,
		RetryStrategy:       req.RetryStrategy,
		Timeout:             req.Timeout,
		Tags:                req.Tags,
	}
	batchJobDefs.Put(key, jd)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"jobDefinitionArn":  jd.JobDefinitionArn,
		"jobDefinitionName": jd.JobDefinitionName,
		"revision":          jd.Revision,
	})
}

func handleBatchDescribeJobDefinitions(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobDefinitionName string   `json:"jobDefinitionName"`
		JobDefinitions    []string `json:"jobDefinitions"`
		Status            string   `json:"status"`
		MaxResults        *int32   `json:"maxResults"`
		NextToken         string   `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := batchJobDefs.List()
	var result []BatchJobDefinition
	for _, jd := range all {
		if req.JobDefinitionName != "" && jd.JobDefinitionName != req.JobDefinitionName {
			continue
		}
		if req.Status != "" && jd.Status != req.Status {
			continue
		}
		if len(req.JobDefinitions) > 0 {
			nameRev := fmt.Sprintf("%s:%d", jd.JobDefinitionName, jd.Revision)
			matched := false
			for _, want := range req.JobDefinitions {
				if want == jd.JobDefinitionArn || want == nameRev || want == jd.JobDefinitionName {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		result = append(result, jd)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].JobDefinitionArn < result[j].JobDefinitionArn })
	if result == nil {
		result = []BatchJobDefinition{}
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"jobDefinitions": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchDeregisterJobDefinition(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobDefinition string `json:"jobDefinition"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	// req.JobDefinition may be "name:rev" or an ARN — look up by key suffix
	key := batchJobDefKey(req.JobDefinition)
	if jd, ok := batchJobDefs.Get(key); ok {
		jd.Status = "INACTIVE"
		batchJobDefs.Put(key, jd)
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchSubmitJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName            string            `json:"jobName"`
		JobQueue           string            `json:"jobQueue"`
		JobDefinition      string            `json:"jobDefinition"`
		ContainerOverrides map[string]any    `json:"containerOverrides"`
		Tags               map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.JobName == "" || req.JobQueue == "" || req.JobDefinition == "" {
		batchWriteError(w, http.StatusBadRequest, "jobName, jobQueue, and jobDefinition are required")
		return
	}

	queueName := batchNameFromARN(req.JobQueue)
	if _, ok := batchJobQueues.Get(queueName); !ok {
		batchWriteError(w, http.StatusBadRequest, "Job queue not found: "+req.JobQueue)
		return
	}
	jd, ok := batchLookupJobDefinition(req.JobDefinition)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Job definition not found: "+req.JobDefinition)
		return
	}
	if jd.Status != "ACTIVE" {
		batchWriteError(w, http.StatusBadRequest, "Job definition is not active: "+req.JobDefinition)
		return
	}
	cfg, containerMeta, err := batchContainerConfig(jd, req.ContainerOverrides)
	if err != nil {
		batchWriteError(w, http.StatusBadRequest, err.Error())
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	jobID := uuid.New().String()
	now := batchEpochMs()
	cfg.Name = "sockerless-batch-" + jobID
	cfg.Labels = map[string]string{"aws-batch-job-id": jobID}
	job := BatchJob{
		JobID:           jobID,
		JobArn:          batchARN("job/" + jobID),
		JobName:         req.JobName,
		JobQueue:        req.JobQueue,
		Status:          "SUBMITTED",
		JobDefinition:   jd.JobDefinitionArn,
		CreatedAt:       now,
		StartedAt:       now,
		Container:       containerMeta,
		Tags:            req.Tags,
		ExecutionConfig: &cfg,
	}
	batchJobs.Put(jobID, job)

	handle, err := sim.StartContainerSync(cfg, sim.NoopSink{})
	if err != nil {
		job.Status = "FAILED"
		job.StatusReason = err.Error()
		job.StoppedAt = batchEpochMs()
		job.Container["reason"] = err.Error()
		batchJobs.Put(jobID, job)
	} else {
		job.Container["containerInstanceArn"] = batchARN("container/" + handle.ContainerID)
		batchJobs.Put(jobID, job)
		batchJobHandles.Store(jobID, handle)
		go batchRunJobLifecycle(jobID, handle)
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"jobId":   jobID,
		"jobName": req.JobName,
		"jobArn":  batchARN("job/" + jobID),
	})
}

func batchTerminal(status string) bool {
	return status == "SUCCEEDED" || status == "FAILED"
}

// batchRunJobLifecycle drives the real Batch job state machine
// SUBMITTED→PENDING→RUNNABLE→STARTING→RUNNING (sub-second dwells, real states,
// no synthetic timer) before delegating to batchWaitForJob for the terminal
// transition driven by the real container exit. A job whose container has
// already finished is not regressed back into a running state.
func batchRunJobLifecycle(jobID string, handle *sim.ContainerHandle) {
	for _, st := range []string{"PENDING", "RUNNABLE", "STARTING", "RUNNING"} {
		time.Sleep(40 * time.Millisecond)
		batchMu.Lock()
		job, ok := batchJobs.Get(jobID)
		if !ok || batchTerminal(job.Status) {
			batchMu.Unlock()
			break
		}
		job.Status = st
		if st == "RUNNING" {
			job.StartedAt = batchEpochMs()
		}
		batchJobs.Put(jobID, job)
		batchMu.Unlock()
	}
	batchWaitForJob(jobID, handle)
}

func batchWaitForJob(jobID string, handle *sim.ContainerHandle) {
	result := handle.Wait()
	batchJobHandles.Delete(jobID)

	batchMu.Lock()
	defer batchMu.Unlock()

	job, ok := batchJobs.Get(jobID)
	if !ok {
		return
	}
	if job.Status == "FAILED" && job.StoppedAt > 0 {
		return
	}
	if result.ExitCode == 0 && result.Error == nil {
		job.Status = "SUCCEEDED"
	} else {
		job.Status = "FAILED"
		if result.Error != nil {
			job.StatusReason = result.Error.Error()
		} else {
			job.StatusReason = fmt.Sprintf("Container exited with status %d", result.ExitCode)
		}
	}
	job.StoppedAt = result.StoppedAt.UnixMilli()
	if job.Container == nil {
		job.Container = map[string]any{}
	}
	job.Container["exitCode"] = result.ExitCode
	if result.Error != nil {
		job.Container["reason"] = result.Error.Error()
	}
	batchJobs.Put(jobID, job)
}

func handleBatchDescribeJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Jobs []string `json:"jobs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var result []BatchJob
	for _, id := range req.Jobs {
		if job, ok := batchJobs.Get(id); ok {
			result = append(result, job)
		}
	}
	if result == nil {
		result = []BatchJob{}
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{"jobs": result})
}

func handleBatchListJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueue   string `json:"jobQueue"`
		JobStatus  string `json:"jobStatus"`
		MaxResults *int32 `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := batchJobs.List()
	var result []map[string]any
	for _, job := range all {
		if req.JobQueue != "" &&
			job.JobQueue != req.JobQueue &&
			batchNameFromARN(job.JobQueue) != batchNameFromARN(req.JobQueue) {
			continue
		}
		if req.JobStatus != "" && job.Status != req.JobStatus {
			continue
		}
		result = append(result, map[string]any{
			"jobId":   job.JobID,
			"jobName": job.JobName,
			"jobArn":  batchARN("job/" + job.JobID),
			"status":  job.Status,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		a, _ := result[i]["jobId"].(string)
		b, _ := result[j]["jobId"].(string)
		return a < b
	})
	if result == nil {
		result = []map[string]any{}
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"jobSummaryList": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchCancelJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID  string `json:"jobId"`
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	batchMu.Lock()
	defer batchMu.Unlock()

	job, ok := batchJobs.Get(req.JobID)
	if !ok {
		batchWriteJSON(w, http.StatusOK, map[string]any{})
		return
	}
	if job.Status != "SUCCEEDED" && job.Status != "FAILED" {
		if handleAny, ok := batchJobHandles.Load(req.JobID); ok {
			if handle, ok := handleAny.(*sim.ContainerHandle); ok {
				handle.Cancel()
			}
			batchJobHandles.Delete(req.JobID)
		}
		job.Status = "FAILED"
		job.StatusReason = req.Reason
		job.StoppedAt = batchEpochMs()
		batchJobs.Put(req.JobID, job)
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchTerminateJob(w http.ResponseWriter, r *http.Request) {
	// Same as cancel for simulator purposes
	handleBatchCancelJob(w, r)
}

func handleBatchCreateSchedulingPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string            `json:"name"`
		FairsharePolicy  map[string]any    `json:"fairsharePolicy"`
		QuotaSharePolicy map[string]any    `json:"quotaSharePolicy"`
		Tags             map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.Name == "" {
		batchWriteError(w, http.StatusBadRequest, "name is required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if _, ok := batchSchedPols.Get(req.Name); ok {
		batchWriteError(w, http.StatusBadRequest, "Scheduling policy already exists: "+req.Name)
		return
	}
	sp := BatchSchedulingPolicy{
		Name:             req.Name,
		Arn:              batchARN("scheduling-policy/" + req.Name),
		FairsharePolicy:  req.FairsharePolicy,
		QuotaSharePolicy: req.QuotaSharePolicy,
		Tags:             req.Tags,
	}
	batchSchedPols.Put(req.Name, sp)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"name": sp.Name,
		"arn":  sp.Arn,
	})
}

func handleBatchDescribeSchedulingPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arns []string `json:"arns"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	result := []BatchSchedulingPolicy{}
	for _, arn := range req.Arns {
		name := batchNameFromARN(arn)
		if sp, ok := batchSchedPols.Get(name); ok {
			result = append(result, sp)
		}
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{"schedulingPolicies": result})
}

func handleBatchListSchedulingPolicies(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int32 `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := batchSchedPols.List()
	sort.Slice(all, func(i, j int) bool { return all[i].Arn < all[j].Arn })
	result := make([]map[string]any, 0, len(all))
	for _, sp := range all {
		result = append(result, map[string]any{"arn": sp.Arn})
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"schedulingPolicies": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchUpdateSchedulingPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn              string         `json:"arn"`
		FairsharePolicy  map[string]any `json:"fairsharePolicy"`
		QuotaSharePolicy map[string]any `json:"quotaSharePolicy"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	name := batchNameFromARN(req.Arn)
	sp, ok := batchSchedPols.Get(name)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Scheduling policy not found: "+req.Arn)
		return
	}
	if req.FairsharePolicy != nil {
		sp.FairsharePolicy = req.FairsharePolicy
	}
	if req.QuotaSharePolicy != nil {
		sp.QuotaSharePolicy = req.QuotaSharePolicy
	}
	batchSchedPols.Put(name, sp)
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchDeleteSchedulingPolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Arn string `json:"arn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	batchSchedPols.Delete(batchNameFromARN(req.Arn))
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchListTagsForResource(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("resourceArn")
	tags := batchTagsForARN(arn)
	batchWriteJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func handleBatchTagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("resourceArn")
	var req struct {
		Tags map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	batchMu.Lock()
	defer batchMu.Unlock()
	batchApplyTags(arn, req.Tags)
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchUntagResource(w http.ResponseWriter, r *http.Request) {
	arn := r.PathValue("resourceArn")
	keys := r.URL.Query()["tagKeys"]
	batchMu.Lock()
	defer batchMu.Unlock()
	batchRemoveTags(arn, keys)
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func batchTagsForARN(arn string) map[string]string {
	if strings.Contains(arn, ":compute-environment/") {
		name := batchNameFromARN(arn)
		if ce, ok := batchComputeEnvs.Get(name); ok && ce.Tags != nil {
			return ce.Tags
		}
	} else if strings.Contains(arn, ":job-queue/") {
		name := batchNameFromARN(arn)
		if q, ok := batchJobQueues.Get(name); ok && q.Tags != nil {
			return q.Tags
		}
	} else if strings.Contains(arn, ":scheduling-policy/") {
		name := batchNameFromARN(arn)
		if sp, ok := batchSchedPols.Get(name); ok && sp.Tags != nil {
			return sp.Tags
		}
	}
	return map[string]string{}
}

func batchApplyTags(arn string, tags map[string]string) {
	if strings.Contains(arn, ":compute-environment/") {
		name := batchNameFromARN(arn)
		if ce, ok := batchComputeEnvs.Get(name); ok {
			if ce.Tags == nil {
				ce.Tags = make(map[string]string)
			}
			for k, v := range tags {
				ce.Tags[k] = v
			}
			batchComputeEnvs.Put(name, ce)
		}
	} else if strings.Contains(arn, ":job-queue/") {
		name := batchNameFromARN(arn)
		if q, ok := batchJobQueues.Get(name); ok {
			if q.Tags == nil {
				q.Tags = make(map[string]string)
			}
			for k, v := range tags {
				q.Tags[k] = v
			}
			batchJobQueues.Put(name, q)
		}
	} else if strings.Contains(arn, ":scheduling-policy/") {
		name := batchNameFromARN(arn)
		if sp, ok := batchSchedPols.Get(name); ok {
			if sp.Tags == nil {
				sp.Tags = make(map[string]string)
			}
			for k, v := range tags {
				sp.Tags[k] = v
			}
			batchSchedPols.Put(name, sp)
		}
	}
}

func batchRemoveTags(arn string, keys []string) {
	if strings.Contains(arn, ":compute-environment/") {
		name := batchNameFromARN(arn)
		if ce, ok := batchComputeEnvs.Get(name); ok {
			for _, k := range keys {
				delete(ce.Tags, k)
			}
			batchComputeEnvs.Put(name, ce)
		}
	} else if strings.Contains(arn, ":job-queue/") {
		name := batchNameFromARN(arn)
		if q, ok := batchJobQueues.Get(name); ok {
			for _, k := range keys {
				delete(q.Tags, k)
			}
			batchJobQueues.Put(name, q)
		}
	} else if strings.Contains(arn, ":scheduling-policy/") {
		name := batchNameFromARN(arn)
		if sp, ok := batchSchedPols.Get(name); ok {
			for _, k := range keys {
				delete(sp.Tags, k)
			}
			batchSchedPols.Put(name, sp)
		}
	}
}

func handleBatchCreateConsumableResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumableResourceName string            `json:"consumableResourceName"`
		TotalQuantity          int64             `json:"totalQuantity"`
		ResourceType           string            `json:"resourceType"`
		Tags                   map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ConsumableResourceName == "" {
		batchWriteError(w, http.StatusBadRequest, "consumableResourceName is required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if _, ok := batchConsumableRes.Get(req.ConsumableResourceName); ok {
		batchWriteError(w, http.StatusBadRequest, "Consumable resource already exists: "+req.ConsumableResourceName)
		return
	}
	resourceType := req.ResourceType
	if resourceType == "" {
		resourceType = "REPLENISHABLE"
	}
	cr := BatchConsumableResource{
		ConsumableResourceName: req.ConsumableResourceName,
		ConsumableResourceArn:  batchARN("consumable-resource/" + req.ConsumableResourceName),
		TotalQuantity:          req.TotalQuantity,
		InUseQuantity:          0,
		ResourceType:           resourceType,
		CreatedAt:              batchEpochMs(),
		Tags:                   req.Tags,
	}
	batchConsumableRes.Put(req.ConsumableResourceName, cr)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"consumableResourceName": cr.ConsumableResourceName,
		"consumableResourceArn":  cr.ConsumableResourceArn,
	})
}

func handleBatchDescribeConsumableResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumableResource string `json:"consumableResource"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	cr, ok := batchConsumableRes.Get(batchNameFromARN(req.ConsumableResource))
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Consumable resource not found: "+req.ConsumableResource)
		return
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"consumableResourceName": cr.ConsumableResourceName,
		"consumableResourceArn":  cr.ConsumableResourceArn,
		"totalQuantity":          cr.TotalQuantity,
		"inUseQuantity":          cr.InUseQuantity,
		"availableQuantity":      cr.TotalQuantity - cr.InUseQuantity,
		"resourceType":           cr.ResourceType,
		"createdAt":              cr.CreatedAt,
		"tags":                   cr.Tags,
	})
}

func handleBatchListConsumableResources(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MaxResults *int32 `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := batchConsumableRes.List()
	sort.Slice(all, func(i, j int) bool { return all[i].ConsumableResourceName < all[j].ConsumableResourceName })
	result := make([]map[string]any, 0, len(all))
	for _, cr := range all {
		result = append(result, map[string]any{
			"consumableResourceArn":  cr.ConsumableResourceArn,
			"consumableResourceName": cr.ConsumableResourceName,
			"totalQuantity":          cr.TotalQuantity,
			"inUseQuantity":          cr.InUseQuantity,
			"resourceType":           cr.ResourceType,
		})
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"consumableResources": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchUpdateConsumableResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumableResource string `json:"consumableResource"`
		Operation          string `json:"operation"`
		Quantity           int64  `json:"quantity"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	name := batchNameFromARN(req.ConsumableResource)
	cr, ok := batchConsumableRes.Get(name)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Consumable resource not found: "+req.ConsumableResource)
		return
	}
	// operation defaults to SET; ADD/REMOVE adjust the total quantity.
	switch strings.ToUpper(req.Operation) {
	case "ADD":
		cr.TotalQuantity += req.Quantity
	case "REMOVE":
		cr.TotalQuantity -= req.Quantity
	default:
		cr.TotalQuantity = req.Quantity
	}
	batchConsumableRes.Put(name, cr)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"consumableResourceName": cr.ConsumableResourceName,
		"consumableResourceArn":  cr.ConsumableResourceArn,
		"totalQuantity":          cr.TotalQuantity,
	})
}

func handleBatchDeleteConsumableResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumableResource string `json:"consumableResource"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	batchConsumableRes.Delete(batchNameFromARN(req.ConsumableResource))
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchListJobsByConsumableResource(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ConsumableResource string `json:"consumableResource"`
		MaxResults         *int32 `json:"maxResults"`
		NextToken          string `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ConsumableResource == "" {
		batchWriteError(w, http.StatusBadRequest, "consumableResource is required")
		return
	}

	// A faithful list of jobs that reference this consumable resource. The
	// simulator's container jobs do not draw against named consumable
	// resources, so this returns an empty (but well-formed) page.
	result := []map[string]any{}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"jobs": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchCreateServiceEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceEnvironmentName string            `json:"serviceEnvironmentName"`
		ServiceEnvironmentType string            `json:"serviceEnvironmentType"`
		State                  string            `json:"state"`
		CapacityLimits         []map[string]any  `json:"capacityLimits"`
		Tags                   map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.ServiceEnvironmentName == "" {
		batchWriteError(w, http.StatusBadRequest, "serviceEnvironmentName is required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if _, ok := batchServiceEnvs.Get(req.ServiceEnvironmentName); ok {
		batchWriteError(w, http.StatusBadRequest, "Service environment already exists: "+req.ServiceEnvironmentName)
		return
	}
	state := req.State
	if state == "" {
		state = "ENABLED"
	}
	seType := req.ServiceEnvironmentType
	if seType == "" {
		seType = "SAGEMAKER_TRAINING"
	}
	limits := req.CapacityLimits
	if limits == nil {
		limits = []map[string]any{}
	}
	se := BatchServiceEnvironment{
		ServiceEnvironmentName: req.ServiceEnvironmentName,
		ServiceEnvironmentArn:  batchARN("service-environment/" + req.ServiceEnvironmentName),
		ServiceEnvironmentType: seType,
		State:                  state,
		Status:                 "VALID",
		CapacityLimits:         limits,
		Tags:                   req.Tags,
	}
	batchServiceEnvs.Put(req.ServiceEnvironmentName, se)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"serviceEnvironmentName": se.ServiceEnvironmentName,
		"serviceEnvironmentArn":  se.ServiceEnvironmentArn,
	})
}

func handleBatchDescribeServiceEnvironments(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceEnvironments []string `json:"serviceEnvironments"`
		MaxResults          *int32   `json:"maxResults"`
		NextToken           string   `json:"nextToken"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && err != io.EOF {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var result []BatchServiceEnvironment
	if len(req.ServiceEnvironments) > 0 {
		for _, nameOrARN := range req.ServiceEnvironments {
			if se, ok := batchServiceEnvs.Get(batchNameFromARN(nameOrARN)); ok {
				result = append(result, se)
			}
		}
	} else {
		result = batchServiceEnvs.List()
		sort.Slice(result, func(i, j int) bool { return result[i].ServiceEnvironmentName < result[j].ServiceEnvironmentName })
	}
	if result == nil {
		result = []BatchServiceEnvironment{}
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"serviceEnvironments": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchUpdateServiceEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceEnvironment string           `json:"serviceEnvironment"`
		State              string           `json:"state"`
		CapacityLimits     []map[string]any `json:"capacityLimits"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	name := batchNameFromARN(req.ServiceEnvironment)
	se, ok := batchServiceEnvs.Get(name)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Service environment not found: "+req.ServiceEnvironment)
		return
	}
	if req.State != "" {
		se.State = req.State
	}
	if req.CapacityLimits != nil {
		se.CapacityLimits = req.CapacityLimits
	}
	batchServiceEnvs.Put(name, se)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"serviceEnvironmentName": se.ServiceEnvironmentName,
		"serviceEnvironmentArn":  se.ServiceEnvironmentArn,
	})
}

func handleBatchDeleteServiceEnvironment(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ServiceEnvironment string `json:"serviceEnvironment"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	batchServiceEnvs.Delete(batchNameFromARN(req.ServiceEnvironment))
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchSubmitServiceJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName               string            `json:"jobName"`
		JobQueue              string            `json:"jobQueue"`
		ServiceJobType        string            `json:"serviceJobType"`
		ServiceRequestPayload string            `json:"serviceRequestPayload"`
		ShareIdentifier       string            `json:"shareIdentifier"`
		QuotaShareName        string            `json:"quotaShareName"`
		SchedulingPriority    int               `json:"schedulingPriority"`
		Tags                  map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.JobName == "" || req.JobQueue == "" {
		batchWriteError(w, http.StatusBadRequest, "jobName and jobQueue are required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	jobID := uuid.New().String()
	now := batchEpochMs()
	seType := req.ServiceJobType
	if seType == "" {
		seType = "SAGEMAKER_TRAINING"
	}
	job := BatchServiceJob{
		JobID:                 jobID,
		JobArn:                batchARN("service-job/" + jobID),
		JobName:               req.JobName,
		JobQueue:              req.JobQueue,
		ServiceJobType:        seType,
		Status:                "SUBMITTED",
		ServiceRequestPayload: req.ServiceRequestPayload,
		ShareIdentifier:       req.ShareIdentifier,
		QuotaShareName:        req.QuotaShareName,
		SchedulingPriority:    req.SchedulingPriority,
		CreatedAt:             now,
		ScheduledAt:           now,
		StartedAt:             now,
		Tags:                  req.Tags,
	}
	batchServiceJobs.Put(jobID, job)
	go batchRunServiceJobLifecycle(jobID)

	batchWriteJSON(w, http.StatusOK, map[string]any{
		"jobId":   jobID,
		"jobName": req.JobName,
		"jobArn":  job.JobArn,
	})
}

// batchRunServiceJobLifecycle walks a service job through the real Batch
// service-job state machine and settles it to SUCCEEDED. Service jobs are
// payload-driven (no container), so the terminal transition is the scheduler
// completing the request, not a container exit. A terminated job is not
// regressed back into a running state.
func batchRunServiceJobLifecycle(jobID string) {
	for _, st := range []string{"PENDING", "RUNNABLE", "SCHEDULED", "STARTING", "RUNNING", "SUCCEEDED"} {
		time.Sleep(40 * time.Millisecond)
		batchMu.Lock()
		job, ok := batchServiceJobs.Get(jobID)
		if !ok || job.IsTerminated || job.Status == "SUCCEEDED" || job.Status == "FAILED" {
			batchMu.Unlock()
			return
		}
		job.Status = st
		if st == "RUNNING" {
			job.StartedAt = batchEpochMs()
		}
		if st == "SUCCEEDED" {
			job.StoppedAt = batchEpochMs()
		}
		batchServiceJobs.Put(jobID, job)
		batchMu.Unlock()
	}
}

func handleBatchDescribeServiceJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID string `json:"jobId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	job, ok := batchServiceJobs.Get(req.JobID)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Service job not found: "+req.JobID)
		return
	}
	out := map[string]any{
		"jobId":          job.JobID,
		"jobArn":         job.JobArn,
		"jobName":        job.JobName,
		"jobQueue":       job.JobQueue,
		"serviceJobType": job.ServiceJobType,
		"status":         job.Status,
		"createdAt":      job.CreatedAt,
		"scheduledAt":    job.ScheduledAt,
		"startedAt":      job.StartedAt,
		"isTerminated":   job.IsTerminated,
	}
	if job.StoppedAt > 0 {
		out["stoppedAt"] = job.StoppedAt
	}
	if job.StatusReason != "" {
		out["statusReason"] = job.StatusReason
	}
	if job.ServiceRequestPayload != "" {
		out["serviceRequestPayload"] = job.ServiceRequestPayload
	}
	if job.ShareIdentifier != "" {
		out["shareIdentifier"] = job.ShareIdentifier
	}
	if job.QuotaShareName != "" {
		out["quotaShareName"] = job.QuotaShareName
	}
	if job.Tags != nil {
		out["tags"] = job.Tags
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchListServiceJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueue   string `json:"jobQueue"`
		JobStatus  string `json:"jobStatus"`
		MaxResults *int32 `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := batchServiceJobs.List()
	var result []map[string]any
	for _, job := range all {
		if req.JobQueue != "" && job.JobQueue != req.JobQueue && batchNameFromARN(req.JobQueue) != batchNameFromARN(job.JobQueue) {
			continue
		}
		if req.JobStatus != "" && job.Status != req.JobStatus {
			continue
		}
		result = append(result, map[string]any{
			"jobId":          job.JobID,
			"jobArn":         job.JobArn,
			"jobName":        job.JobName,
			"serviceJobType": job.ServiceJobType,
			"status":         job.Status,
			"createdAt":      job.CreatedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		a, _ := result[i]["jobId"].(string)
		b, _ := result[j]["jobId"].(string)
		return a < b
	})
	if result == nil {
		result = []map[string]any{}
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"jobSummaryList": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchTerminateServiceJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID  string `json:"jobId"`
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if job, ok := batchServiceJobs.Get(req.JobID); ok {
		if job.Status != "SUCCEEDED" && job.Status != "FAILED" {
			job.Status = "FAILED"
			job.StatusReason = req.Reason
			job.IsTerminated = true
			job.StoppedAt = batchEpochMs()
			batchServiceJobs.Put(req.JobID, job)
		}
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchUpdateServiceJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobID              string `json:"jobId"`
		SchedulingPriority *int   `json:"schedulingPriority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	job, ok := batchServiceJobs.Get(req.JobID)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Service job not found: "+req.JobID)
		return
	}
	if req.SchedulingPriority != nil {
		job.SchedulingPriority = *req.SchedulingPriority
	}
	batchServiceJobs.Put(req.JobID, job)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"jobId":   job.JobID,
		"jobArn":  job.JobArn,
		"jobName": job.JobName,
	})
}

func handleBatchCreateQuotaShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuotaShareName               string            `json:"quotaShareName"`
		JobQueue                     string            `json:"jobQueue"`
		CapacityLimits               []map[string]any  `json:"capacityLimits"`
		ResourceSharingConfiguration map[string]any    `json:"resourceSharingConfiguration"`
		PreemptionConfiguration      map[string]any    `json:"preemptionConfiguration"`
		State                        string            `json:"state"`
		Tags                         map[string]string `json:"tags"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.QuotaShareName == "" || req.JobQueue == "" {
		batchWriteError(w, http.StatusBadRequest, "quotaShareName and jobQueue are required")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	if _, ok := batchQuotaShares.Get(req.QuotaShareName); ok {
		batchWriteError(w, http.StatusBadRequest, "Quota share already exists: "+req.QuotaShareName)
		return
	}
	state := req.State
	if state == "" {
		state = "ENABLED"
	}
	limits := req.CapacityLimits
	if limits == nil {
		limits = []map[string]any{}
	}
	qs := BatchQuotaShare{
		QuotaShareName:               req.QuotaShareName,
		QuotaShareArn:                batchARN("quota-share/" + req.QuotaShareName),
		JobQueueArn:                  batchARN("job-queue/" + batchNameFromARN(req.JobQueue)),
		CapacityLimits:               limits,
		ResourceSharingConfiguration: req.ResourceSharingConfiguration,
		PreemptionConfiguration:      req.PreemptionConfiguration,
		State:                        state,
		Status:                       "VALID",
		Tags:                         req.Tags,
	}
	batchQuotaShares.Put(req.QuotaShareName, qs)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"quotaShareName": qs.QuotaShareName,
		"quotaShareArn":  qs.QuotaShareArn,
	})
}

func handleBatchDescribeQuotaShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuotaShareArn string `json:"quotaShareArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	qs, ok := batchQuotaShares.Get(batchNameFromARN(req.QuotaShareArn))
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Quota share not found: "+req.QuotaShareArn)
		return
	}
	out := map[string]any{
		"quotaShareName": qs.QuotaShareName,
		"quotaShareArn":  qs.QuotaShareArn,
		"jobQueueArn":    qs.JobQueueArn,
		"capacityLimits": qs.CapacityLimits,
		"state":          qs.State,
		"status":         qs.Status,
	}
	if qs.ResourceSharingConfiguration != nil {
		out["resourceSharingConfiguration"] = qs.ResourceSharingConfiguration
	}
	if qs.PreemptionConfiguration != nil {
		out["preemptionConfiguration"] = qs.PreemptionConfiguration
	}
	if qs.Tags != nil {
		out["tags"] = qs.Tags
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchListQuotaShares(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueue   string `json:"jobQueue"`
		MaxResults *int32 `json:"maxResults"`
		NextToken  string `json:"nextToken"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)

	all := batchQuotaShares.List()
	sort.Slice(all, func(i, j int) bool { return all[i].QuotaShareName < all[j].QuotaShareName })
	result := make([]map[string]any, 0, len(all))
	for _, qs := range all {
		if req.JobQueue != "" && qs.JobQueueArn != batchARN("job-queue/"+batchNameFromARN(req.JobQueue)) {
			continue
		}
		entry := map[string]any{
			"quotaShareName": qs.QuotaShareName,
			"quotaShareArn":  qs.QuotaShareArn,
			"jobQueueArn":    qs.JobQueueArn,
			"capacityLimits": qs.CapacityLimits,
			"state":          qs.State,
			"status":         qs.Status,
		}
		if qs.ResourceSharingConfiguration != nil {
			entry["resourceSharingConfiguration"] = qs.ResourceSharingConfiguration
		}
		if qs.PreemptionConfiguration != nil {
			entry["preemptionConfiguration"] = qs.PreemptionConfiguration
		}
		result = append(result, entry)
	}
	page, next := awsPageExplicit(result, req.NextToken, awsMaxResults(req.MaxResults))
	out := map[string]any{"quotaShares": page}
	if next != "" {
		out["nextToken"] = next
	}
	batchWriteJSON(w, http.StatusOK, out)
}

func handleBatchUpdateQuotaShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuotaShareArn                string           `json:"quotaShareArn"`
		CapacityLimits               []map[string]any `json:"capacityLimits"`
		ResourceSharingConfiguration map[string]any   `json:"resourceSharingConfiguration"`
		PreemptionConfiguration      map[string]any   `json:"preemptionConfiguration"`
		State                        string           `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	name := batchNameFromARN(req.QuotaShareArn)
	qs, ok := batchQuotaShares.Get(name)
	if !ok {
		batchWriteError(w, http.StatusBadRequest, "Quota share not found: "+req.QuotaShareArn)
		return
	}
	if req.CapacityLimits != nil {
		qs.CapacityLimits = req.CapacityLimits
	}
	if req.ResourceSharingConfiguration != nil {
		qs.ResourceSharingConfiguration = req.ResourceSharingConfiguration
	}
	if req.PreemptionConfiguration != nil {
		qs.PreemptionConfiguration = req.PreemptionConfiguration
	}
	if req.State != "" {
		qs.State = req.State
	}
	batchQuotaShares.Put(name, qs)
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"quotaShareName": qs.QuotaShareName,
		"quotaShareArn":  qs.QuotaShareArn,
	})
}

func handleBatchDeleteQuotaShare(w http.ResponseWriter, r *http.Request) {
	var req struct {
		QuotaShareArn string `json:"quotaShareArn"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	batchMu.Lock()
	defer batchMu.Unlock()

	batchQuotaShares.Delete(batchNameFromARN(req.QuotaShareArn))
	batchWriteJSON(w, http.StatusOK, map[string]any{})
}

func handleBatchGetJobQueueSnapshot(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobQueue string `json:"jobQueue"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		batchWriteError(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if req.JobQueue == "" {
		batchWriteError(w, http.StatusBadRequest, "jobQueue is required")
		return
	}

	queueName := batchNameFromARN(req.JobQueue)
	if _, ok := batchJobQueues.Get(queueName); !ok {
		batchWriteError(w, http.StatusBadRequest, "Job queue not found: "+req.JobQueue)
		return
	}

	// The front of the queue: the runnable/submitted jobs on this queue,
	// ordered by creation time (the dispatch order), each with the earliest
	// time it reached its current position.
	type frontJob struct {
		arn       string
		createdAt int64
	}
	var front []frontJob
	for _, job := range batchJobs.List() {
		if batchNameFromARN(job.JobQueue) != queueName {
			continue
		}
		if batchTerminal(job.Status) {
			continue
		}
		front = append(front, frontJob{arn: job.JobArn, createdAt: job.CreatedAt})
	}
	sort.Slice(front, func(i, j int) bool { return front[i].createdAt < front[j].createdAt })

	jobs := make([]map[string]any, 0, len(front))
	for _, f := range front {
		jobs = append(jobs, map[string]any{
			"jobArn":                 f.arn,
			"earliestTimeAtPosition": f.createdAt,
		})
	}
	batchWriteJSON(w, http.StatusOK, map[string]any{
		"frontOfQueue": map[string]any{
			"jobs":          jobs,
			"lastUpdatedAt": batchEpochMs(),
		},
	})
}

func batchNameFromARN(arn string) string {
	parts := strings.Split(arn, "/")
	if len(parts) >= 2 {
		return parts[len(parts)-1]
	}
	return arn
}

func batchJobDefKey(nameOrARN string) string {
	// Accept "name:rev" or ARN ending with "name:rev"
	parts := strings.Split(nameOrARN, "/")
	return parts[len(parts)-1]
}

func batchLookupJobDefinition(nameOrARN string) (BatchJobDefinition, bool) {
	key := batchJobDefKey(nameOrARN)
	if strings.Contains(key, ":") {
		return batchJobDefs.Get(key)
	}
	rev, ok := batchJobRevisions.Get(key)
	if !ok {
		return BatchJobDefinition{}, false
	}
	for rev > 0 {
		if jd, ok := batchJobDefs.Get(fmt.Sprintf("%s:%d", key, rev)); ok && jd.Status == "ACTIVE" {
			return jd, true
		}
		rev--
	}
	return BatchJobDefinition{}, false
}

func batchContainerConfig(jd BatchJobDefinition, overrides map[string]any) (sim.ContainerConfig, map[string]any, error) {
	image := batchString(jd.ContainerProperties["image"])
	if image == "" {
		return sim.ContainerConfig{}, nil, fmt.Errorf("containerProperties.image is required")
	}
	command := batchStringSlice(jd.ContainerProperties["command"])
	env := batchEnvironment(jd.ContainerProperties["environment"])
	if overrides != nil {
		if overrideCommand := batchStringSlice(overrides["command"]); len(overrideCommand) > 0 {
			command = overrideCommand
		}
		for k, v := range batchEnvironment(overrides["environment"]) {
			env[k] = v
		}
	}
	timeout := batchTimeout(jd.Timeout)
	meta := map[string]any{
		"image": image,
	}
	if len(command) > 0 {
		meta["command"] = command
	}
	if len(env) > 0 {
		meta["environment"] = batchEnvironmentList(env)
	}
	return sim.ContainerConfig{
		Image:        sim.ResolveLocalImage(image),
		Architecture: "linux/" + runtime.GOARCH,
		Args:         command,
		Env:          env,
		Timeout:      timeout,
		Sandbox:      SandboxFargate,
	}, meta, nil
}

func batchString(v any) string {
	s, _ := v.(string)
	return s
}

func batchStringSlice(v any) []string {
	switch values := v.(type) {
	case []string:
		return values
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if s, ok := value.(string); ok {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func batchEnvironment(v any) map[string]string {
	env := map[string]string{}
	values, ok := v.([]any)
	if !ok {
		return env
	}
	for _, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			continue
		}
		name := batchString(item["name"])
		if name == "" {
			name = batchString(item["Name"])
		}
		if name == "" {
			continue
		}
		env[name] = batchString(item["value"])
		if env[name] == "" {
			env[name] = batchString(item["Value"])
		}
	}
	return env
}

func batchEnvironmentList(env map[string]string) []map[string]string {
	out := make([]map[string]string, 0, len(env))
	for k, v := range env {
		out = append(out, map[string]string{"name": k, "value": v})
	}
	return out
}

func batchTimeout(timeout map[string]any) time.Duration {
	seconds, ok := timeout["attemptDurationSeconds"].(float64)
	if !ok || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}
