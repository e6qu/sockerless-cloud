package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/jobrun"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
)

const (
	acaDefaultReplicaTimeout = 1800 * time.Second

	acaJobReplicaIndexLabel   = "sockerless-sim-execution-replica"
	acaJobReplicaAttemptLabel = "sockerless-sim-execution-attempt"
	acaJobExecutionLabel      = "sockerless-exec-id"
)

// acaExecutionRun lets a restarted process resume an execution. The Azure
// Resource Manager surface reports only the status.
type acaExecutionRun struct {
	ExecutionID    string           `json:"executionId"`
	JobID          string           `json:"jobId"`
	Replicas       int              `json:"replicas"`
	Parallelism    int              `json:"parallelism"`
	Completions    int              `json:"completions"`
	RetryLimit     int              `json:"retryLimit"`
	TimeoutSeconds int              `json:"timeoutSeconds"`
	Template       *JobTemplate     `json:"template,omitempty"`
	Tasks          []acaReplicaTask `json:"tasks"`
}

type acaReplicaTask struct {
	Index        int       `json:"index"`
	Attempt      int       `json:"attempt"`
	Status       string    `json:"status"`
	FirstStarted time.Time `json:"firstStarted,omitzero"`
	Started      time.Time `json:"started,omitzero"`
	Ended        time.Time `json:"ended,omitzero"`
}

var (
	acaExecutions sim.Store[JobExecution]
	acaRuns       sim.Store[acaExecutionRun]
	acaJobRuns    sync.Map // map[execID]*acaJobRun
)

// acaJobRunSettings: parallelism and completions default to 1; timeout bounds
// a replica across its retries.
func acaJobRunSettings(job ContainerAppJob) (parallelism, completions, retryLimit int, timeout time.Duration) {
	parallelism, completions, timeout = 1, 1, acaDefaultReplicaTimeout
	config := job.Properties.Configuration
	if config == nil {
		return
	}
	p, c := 0, 0
	switch {
	case strings.EqualFold(config.TriggerType, "Schedule") && config.ScheduleTrigger != nil:
		p, c = config.ScheduleTrigger.Parallelism, config.ScheduleTrigger.ReplicaCompletionCount
	case strings.EqualFold(config.TriggerType, "Event") && config.EventTrigger != nil:
		p, c = config.EventTrigger.Parallelism, config.EventTrigger.ReplicaCompletionCount
	case config.ManualTrigger != nil:
		p, c = config.ManualTrigger.Parallelism, config.ManualTrigger.ReplicaCompletionCount
	}
	if p > 0 {
		parallelism = p
	}
	if c > 0 {
		completions = c
	}
	retryLimit = max(config.ReplicaRetryLimit, 0)
	if config.ReplicaTimeout > 0 {
		timeout = time.Duration(config.ReplicaTimeout) * time.Second
	}
	return
}

func (r acaExecutionRun) spec() jobrun.Spec {
	return jobrun.Spec{
		Tasks:       r.Replicas,
		Parallelism: r.Parallelism,
		MaxRetries:  r.RetryLimit,
		TaskTimeout: time.Duration(r.TimeoutSeconds) * time.Second,
		Policy:      jobrun.Completions(r.Completions),
	}
}

// acaStartJobExecution runs parallelism replicas, each its own container group,
// retries a failed one up to replicaRetryLimit within replicaTimeout, and
// succeeds once replicaCompletionCount have succeeded.
func acaStartJobExecution(executions sim.Store[JobExecution], job ContainerAppJob, template *JobTemplate) JobExecution {
	execName := fmt.Sprintf("%s-%s", job.Name, randomSuffix(7))
	execID := fmt.Sprintf("%s/executions/%s", job.ID, execName)
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
	acaJobSystemLog(job, execName, "Normal", "Container started")

	parallelism, completions, retryLimit, timeout := acaJobRunSettings(job)
	record := acaExecutionRun{
		ExecutionID: execID,
		JobID:       job.ID,
		// Run at least as many replicas as must complete.
		Replicas:       max(parallelism, completions),
		Parallelism:    parallelism,
		Completions:    completions,
		RetryLimit:     retryLimit,
		TimeoutSeconds: int(timeout / time.Second),
		Template:       template,
	}
	record.Tasks = make([]acaReplicaTask, record.Replicas)
	for i := range record.Tasks {
		record.Tasks[i] = acaReplicaTask{Index: i, Status: jobrun.Pending.String()}
	}
	acaRuns.Put(execID, record)
	run := &acaJobRun{record: record, live: map[int]*workload.Group{}}
	run.register(jobrun.Start(record.spec(), run.hooks()))
	return exec
}

type acaJobRun struct {
	record    acaExecutionRun
	adoptable map[[2]int][]placement.Existing
	adopted   map[[2]int]bool

	mu   sync.Mutex
	live map[int]*workload.Group
	done bool
	run  *jobrun.Execution
}

func (r *acaJobRun) hooks() jobrun.Hooks {
	return jobrun.Hooks{Start: r.start, Adopt: r.adopt, Changed: r.changed, Finished: r.finished}
}

func (r *acaJobRun) register(e *jobrun.Execution, err error) {
	if err != nil {
		log.Printf("Container Apps execution %s: %v", r.record.ExecutionID, err)
		r.finished(jobrun.Failure, jobrun.Counts{})
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.run = e
	if !r.done {
		acaJobRuns.Store(r.record.ExecutionID, r)
	}
}

func (r *acaJobRun) job() (ContainerAppJob, bool) {
	if acaJobs == nil {
		return ContainerAppJob{}, false
	}
	return acaJobs.Get(r.record.JobID)
}

func (r *acaJobRun) execName() string {
	id := r.record.ExecutionID
	return id[strings.LastIndex(id, "/")+1:]
}

// The job's short name is the replica's alias on the environment's network, so
// executions resolve each other as the environment's internal DNS does.
func (r *acaJobRun) start(ctx context.Context, t jobrun.Task) (jobrun.Workload, error) {
	job, ok := r.job()
	if !ok {
		return nil, fmt.Errorf("job %s no longer exists", r.record.JobID)
	}
	tmpl := r.record.Template
	if tmpl == nil || len(tmpl.Containers) == 0 {
		return nil, fmt.Errorf("execution has no containers")
	}
	network, subnetID, err := acaWorkloadNetwork(ctx, job.Properties.EnvironmentID)
	if err != nil {
		acaJobSystemLog(job, r.execName(), "Warning", err.Error())
		return nil, err
	}
	group, err := startACAJobContainers(ctx, r.record.ExecutionID, r.execName(), t.Index, t.Attempt, job, tmpl, network, subnetID, []string{job.Name}, &acaLogSink{job: job})
	if err != nil {
		acaJobSystemLog(job, r.execName(), "Warning", err.Error())
		return nil, err
	}
	return r.track(t.Index, group), nil
}

func (r *acaJobRun) track(index int, group *workload.Group) jobrun.Workload {
	r.mu.Lock()
	r.live[index] = group
	r.mu.Unlock()
	return &acaReplicaWorkload{run: r, index: index, group: group}
}

// mainContainer serves console exec: the lowest-numbered running replica's.
func (r *acaJobRun) mainContainer() placement.Handle {
	r.mu.Lock()
	defer r.mu.Unlock()
	indexes := make([]int, 0, len(r.live))
	for i := range r.live {
		indexes = append(indexes, i)
	}
	sort.Ints(indexes)
	for _, i := range indexes {
		if g := r.live[i]; g.Main != nil {
			return g.Main
		}
	}
	return nil
}

type acaReplicaWorkload struct {
	run   *acaJobRun
	index int
	group *workload.Group
}

func (w *acaReplicaWorkload) Wait() jobrun.Exit {
	result := w.group.Main.Wait()
	for _, h := range w.group.Sidecars {
		h.Cancel()
		h.Wait()
	}
	w.run.mu.Lock()
	if w.run.live[w.index] == w.group {
		delete(w.run.live, w.index)
	}
	w.run.mu.Unlock()
	return jobrun.Exit{Code: result.ExitCode, Err: result.Error, At: result.StoppedAt}
}

// Stop signals the main container, which the termination grace then kills.
func (w *acaReplicaWorkload) Stop() { w.group.Main.Stop() }

func (r *acaJobRun) changed(t jobrun.Task, _ jobrun.Counts) {
	acaRuns.Update(r.record.ExecutionID, func(rec *acaExecutionRun) {
		if t.Index >= len(rec.Tasks) {
			return
		}
		rec.Tasks[t.Index] = acaReplicaTask{
			Index:        t.Index,
			Attempt:      t.Attempt,
			Status:       t.Status.String(),
			FirstStarted: t.FirstStarted,
			Started:      t.Started,
			Ended:        t.Ended,
		}
	})
}

// Succeeded once enough replicas completed, Failed once too few can. A stop
// already recorded Stopped.
func (r *acaJobRun) finished(v jobrun.Verdict, _ jobrun.Counts) {
	r.mu.Lock()
	r.done = true
	acaJobRuns.Delete(r.record.ExecutionID)
	r.mu.Unlock()
	acaRuns.Delete(r.record.ExecutionID)
	completed := false
	acaExecutions.Update(r.record.ExecutionID, func(e *JobExecution) {
		if e.Properties.Status != "Running" {
			return
		}
		completed = true
		e.Properties.Status = "Failed"
		if v == jobrun.Success {
			e.Properties.Status = "Succeeded"
		}
		e.Properties.EndTime = time.Now().UTC().Format(time.RFC3339)
	})
	job, ok := r.job()
	if !completed || !ok {
		return
	}
	if v == jobrun.Success {
		acaJobSystemLog(job, r.execName(), "Normal", "Execution completed successfully")
	} else {
		acaJobSystemLog(job, r.execName(), "Warning", "Execution failed")
	}
}

// Replicas not yet started never start; running ones get the stop signal.
func stopACAJobExecution(job ContainerAppJob, id string) bool {
	stopped := false
	ok := acaExecutions.Update(id, func(e *JobExecution) {
		if e.Properties.Status != "Running" {
			return
		}
		stopped = true
		e.Properties.Status = "Stopped"
		e.Properties.EndTime = time.Now().UTC().Format(time.RFC3339)
	})
	if v, running := acaJobRuns.Load(id); running {
		if run, ok := v.(*acaJobRun); ok {
			run.mu.Lock()
			execution := run.run
			run.mu.Unlock()
			if execution != nil {
				execution.Cancel()
			}
		}
	}
	if stopped && job.ID != "" {
		acaJobSystemLog(job, id[strings.LastIndex(id, "/")+1:], "Normal", "Execution stopped")
	}
	return ok
}

// resumeACAJobExecutions adopts the containers an earlier process left Running
// for each replica's attempt; a replica whose containers are gone ends as lost
// and retries within the limits, and unstarted replicas start. An execution
// with no recorded run fails. Unclaimed containers are removed.
func resumeACAJobExecutions(ctx context.Context) {
	if acaExecutions == nil {
		return
	}
	found := acaJobContainersLeft(ctx)
	claimed := map[string]bool{}
	for _, exec := range acaExecutions.List() {
		if exec.Properties.Status != "Running" {
			continue
		}
		record, ok := acaRuns.Get(exec.ID)
		if !ok {
			jobID, _, _ := strings.Cut(exec.ID, "/executions/")
			record = acaExecutionRun{ExecutionID: exec.ID, JobID: jobID, Replicas: 1, Parallelism: 1, Completions: 1,
				Tasks: []acaReplicaTask{{Index: 0, Status: jobrun.Running.String()}}}
			acaRuns.Put(exec.ID, record)
		}
		tasks := make([]jobrun.Task, len(record.Tasks))
		for i, rt := range record.Tasks {
			tasks[i] = jobrun.Task{Index: rt.Index, Attempt: rt.Attempt, FirstStarted: rt.FirstStarted, Started: rt.Started, Ended: rt.Ended}
			for _, s := range []jobrun.Status{jobrun.Pending, jobrun.Running, jobrun.Succeeded, jobrun.Failed, jobrun.Cancelled} {
				if s.String() == rt.Status {
					tasks[i].Status = s
				}
			}
		}
		run := &acaJobRun{record: record, live: map[int]*workload.Group{}, adoptable: found[exec.ID], adopted: map[[2]int]bool{}}
		run.register(jobrun.Resume(record.spec(), run.hooks(), tasks))
		for key := range run.adopted {
			for _, c := range run.adoptable[key] {
				claimed[c.ID] = true
			}
		}
	}
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		return
	}
	for _, byReplica := range found {
		for _, group := range byReplica {
			var left []placement.Existing
			for _, c := range group {
				if !claimed[c.ID] {
					left = append(left, c)
				}
			}
			removeACAContainers(ctx, backend, left)
		}
	}
}

// Keyed by execution, then replica index and attempt.
func acaJobContainersLeft(ctx context.Context) map[string]map[[2]int][]placement.Existing {
	found := map[string]map[[2]int][]placement.Existing{}
	if !sim.HasPersistentWorkloadIdentity() {
		return found
	}
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		log.Printf("Container Apps jobs: find the replicas an earlier process left: %v", err)
		return found
	}
	containers, err := backend.Find(ctx, map[string]string{acaJobExecutionLabel: ""})
	if err != nil {
		log.Printf("Container Apps jobs: find the replicas an earlier process left: %v", err)
		return found
	}
	for _, c := range containers {
		id := c.Labels[acaJobExecutionLabel]
		index, indexErr := strconv.Atoi(c.Labels[acaJobReplicaIndexLabel])
		attempt, attemptErr := strconv.Atoi(c.Labels[acaJobReplicaAttemptLabel])
		if indexErr != nil || attemptErr != nil {
			index, attempt = -1, -1
		}
		if found[id] == nil {
			found[id] = map[[2]int][]placement.Existing{}
		}
		key := [2]int{index, attempt}
		found[id][key] = append(found[id][key], c)
	}
	return found
}

func (r *acaJobRun) adopt(t jobrun.Task) (jobrun.Workload, error) {
	key := [2]int{t.Index, t.Attempt}
	group := r.adoptable[key]
	job, ok := r.job()
	tmpl := r.record.Template
	if len(group) == 0 || !ok || tmpl == nil || len(group) != len(tmpl.Containers) {
		return nil, nil
	}
	byName := map[string]placement.Existing{}
	for _, c := range group {
		byName[c.Labels["sockerless-sim-execution-container"]] = c
	}
	backend, err := azurePlacementBackend(placementContainerApps)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	sink := &acaLogSink{job: job}
	handles := make([]placement.Handle, 0, len(tmpl.Containers))
	cancel := func() {
		for _, h := range handles {
			h.Cancel()
		}
	}
	for _, c := range tmpl.Containers {
		existing, ok := byName[c.Name]
		if !ok || !existing.Running {
			cancel()
			return nil, nil
		}
		member := workload.Container{Name: c.Name, Config: sim.ContainerConfig{CancelGracePeriod: acaDefaultTerminationGrace}}
		handle, err := backend.Adopt(ctx, existing.ID, member.Config, sink)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("adopt container %s: %w", existing.ID, err)
		}
		handles = append(handles, handle)
	}
	network, subnetID, err := acaWorkloadNetwork(ctx, job.Properties.EnvironmentID)
	if err != nil {
		cancel()
		return nil, err
	}
	address, err := handles[0].Address(ctx)
	if err != nil {
		cancel()
		return nil, err
	}
	release, err := azureAdoptWorkloadAddress(ctx, network, subnetID, acaJobReplicaContainerName(r.execName(), t.Index, t.Attempt), address)
	if err != nil {
		cancel()
		return nil, err
	}
	azureReleaseOnExit(handles[0].Exited(), release)
	r.adopted[key] = true
	return r.track(t.Index, &workload.Group{Main: handles[0], Sidecars: handles[1:]}), nil
}

func acaJobReplicaContainerName(execName string, index, attempt int) string {
	return fmt.Sprintf("sockerless-sim-azure-execution-%s-%d-%d", execName, index, attempt)
}

// The template's first container owns the network namespace; the others join
// it.
func startACAJobContainers(ctx context.Context, execID, execName string, index, attempt int, job ContainerAppJob, tmpl *JobTemplate, network azureWorkloadNetwork, subnetID string, netAliases []string, sink sim.LogSink) (*workload.Group, error) {
	envID := job.Properties.EnvironmentID
	volByName := make(map[string]JobVolume, len(tmpl.Volumes))
	for _, v := range tmpl.Volumes {
		volByName[v.Name] = v
	}
	metadataEnv, err := workloadMetadataEnv(job.ID, job.Identity, network.DNS)
	if err != nil {
		return nil, err
	}
	secrets, err := acaResolveSecrets(job.Identity, acaJobSecrets(job))
	if err != nil {
		return nil, err
	}
	registries := acaJobWorkloadRegistries(job.Properties.Configuration, secrets)
	member := func(c JobContainer, name string) workload.Container {
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
		return workload.Container{Name: c.Name, Config: sim.ContainerConfig{
			CancelGracePeriod: acaDefaultTerminationGrace,
			Image:             sim.ResolveLocalImage(c.Image),
			// The host pulls with the credential the job declared for the registry, as
			// Container Apps does.
			RegistryAuth: acrWorkloadRegistryAuth(c.Image, registries),
			Command:      c.Command,
			Args:         c.Args,
			Env:          workloadhost.MergeEnv(acaContainerEnv(c, secrets), metadataEnv),
			Name:         name,
			Labels: map[string]string{
				"sockerless-sim-type":                "aca-job-execution",
				acaJobExecutionLabel:                 execID,
				"sockerless-sim-execution-container": c.Name,
				acaJobReplicaIndexLabel:              strconv.Itoa(index),
				acaJobReplicaAttemptLabel:            strconv.Itoa(attempt),
			},
			Binds:   binds,
			Sandbox: SandboxACA,
		}}
	}

	base := acaJobReplicaContainerName(execName, index, attempt)
	main := member(tmpl.Containers[0], base)
	main.Config.Network = network.Network
	main.Config.NetworkAliases = netAliases
	main.Config.DNS = network.resolvers()
	main.Config.ExtraHosts = workloadhost.ExtraHosts()
	address, release, err := azureWorkloadAddress(ctx, network, subnetID, main.Config.Name)
	if err != nil {
		return nil, err
	}
	main.Config.IPv4Address = address
	sidecars := make([]workload.Container, 0, len(tmpl.Containers)-1)
	for i, c := range tmpl.Containers[1:] {
		sidecars = append(sidecars, member(c, fmt.Sprintf("%s-sidecar-%d", base, i)))
	}
	group, err := acaStartGroup(ctx, main, sidecars, sink)
	if err != nil {
		release()
		return nil, err
	}
	azureReleaseOnExit(group.Main.Exited(), release)
	return group, nil
}
