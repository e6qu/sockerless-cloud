package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/placement"
	"github.com/e6qu/sockerless-cloud/sim/workload"
	"github.com/e6qu/sockerless-cloud/sim/workloadhost"
	"github.com/google/uuid"
)

// glueRefusesRole writes refusal, as Glue's InvalidInputException, when role,
// named by ARN or by name, names no role or does not let Glue assume it for
// the Glue resource it configures.
func glueRefusesRole(w http.ResponseWriter, role, resource, refusal string) bool {
	if _, err := iamServiceTrustedRole(role, "glue.amazonaws.com", glueGlueArn(resource)); err != nil {
		glueWriteError(w, "InvalidInputException", refusal)
		return true
	}
	return false
}

const glueCrawlerRoleRefusal = "Service is unable to assume provided role. Please verify role's TrustPolicy"

// glueCanAssumeRole refuses a job, session or development endpoint role
// Glue cannot assume.
func glueCanAssumeRole(w http.ResponseWriter, role, resource string) bool {
	return !glueRefusesRole(w, role, resource, "Role "+role+" should be given assume role permissions for Glue Service.")
}

func handleGlueCreateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name             string            `json:"Name"`
		Description      string            `json:"Description"`
		Role             string            `json:"Role"`
		Command          map[string]any    `json:"Command"`
		DefaultArguments map[string]string `json:"DefaultArguments"`
		GlueVersion      string            `json:"GlueVersion"`
		MaxCapacity      *float64          `json:"MaxCapacity"`
		WorkerType       string            `json:"WorkerType"`
		NumberOfWorkers  *int              `json:"NumberOfWorkers"`
		Tags             map[string]string `json:"Tags"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Name == "" {
		glueWriteError(w, "InvalidInputException", "Name is required")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	if _, ok := glueJobs.Get(req.Name); ok {
		glueWriteError(w, "AlreadyExistsException", "Job already exists: "+req.Name)
		return
	}
	if !glueCanAssumeRole(w, req.Role, "job/"+req.Name) {
		return
	}
	now := glueEpochNow()
	job := GlueJob{
		Name:             req.Name,
		Description:      req.Description,
		Role:             req.Role,
		Command:          req.Command,
		DefaultArguments: req.DefaultArguments,
		GlueVersion:      req.GlueVersion,
		MaxCapacity:      req.MaxCapacity,
		WorkerType:       req.WorkerType,
		NumberOfWorkers:  req.NumberOfWorkers,
		CreatedOn:        now,
		LastModifiedOn:   now,
		Tags:             req.Tags,
	}
	glueJobs.Put(req.Name, job)
	glueWriteJSON(w, http.StatusOK, map[string]any{"Name": req.Name})
}

func handleGlueGetJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"Job": glueJobWire{job}})
}

// glueJobWire wraps GlueJob for response emission. The real Job shape
// has no Tags member — tags ride GetTags. The struct keeps its Tags
// JSON tag so tag state survives Store persistence; the wrapper strips
// it from GetJob/GetJobs responses.
type glueJobWire struct {
	GlueJob
}

func (j glueJobWire) MarshalJSON() ([]byte, error) {
	b, err := json.Marshal(j.GlueJob)
	if err != nil {
		return nil, err
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	delete(m, "Tags")
	return json.Marshal(m)
}

func handleGlueGetJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueJobs.List()
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, all, func(it GlueJob) string { return it.Name }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	jobs := make([]glueJobWire, 0, len(page))
	for _, job := range page {
		jobs = append(jobs, glueJobWire{job})
	}
	resp := map[string]any{"Jobs": jobs}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueDeleteJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	glueJobs.Delete(req.JobName)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobName": req.JobName})
}

func handleGlueStartJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName   string            `json:"JobName"`
		Arguments map[string]string `json:"Arguments"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	script, err := gluePythonScript(job)
	if err != nil {
		glueWriteError(w, "InvalidInputException", err.Error())
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	runID := strings.ReplaceAll(uuid.New().String(), "-", "")[:16]
	now := glueEpochNow()
	run := GlueJobRun{
		ID:           runID,
		JobName:      req.JobName,
		JobRunState:  "RUNNING",
		StartedOn:    now,
		Arguments:    req.Arguments,
		LogGroupName: glueJobsLogGroup,
	}
	glueJobRuns.Put(req.JobName+"/"+runID, run)
	go glueRunPythonJob(req.JobName, runID, job, script, req.Arguments)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobRunId": runID})
}

func gluePythonScript(job GlueJob) ([]byte, error) {
	commandName := glueString(job.Command["Name"])
	if commandName == "" {
		commandName = glueString(job.Command["name"])
	}
	if commandName != "pythonshell" {
		return nil, fmt.Errorf("only pythonshell jobs execute in the simulator")
	}
	location := glueString(job.Command["ScriptLocation"])
	if location == "" {
		location = glueString(job.Command["scriptLocation"])
	}
	if location == "" {
		return nil, fmt.Errorf("Command.ScriptLocation is required")
	}
	bucket, key, ok := glueS3Location(location)
	if !ok {
		return nil, fmt.Errorf("Command.ScriptLocation must be an s3:// URL")
	}
	obj, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		return nil, fmt.Errorf("script object not found: %s", location)
	}
	return s3ObjectData(obj)
}

// gluePythonShellImage is the interpreter a Python shell job runs on, chosen by
// the job command's PythonVersion. AWS: "The Python version being used to run a
// Python shell job. Allowed values are 2 or 3." Glue runs Python 3 shell jobs
// on Python 3.9; Python 2 shell jobs ran only on Glue versions AWS has since
// retired, so a job asking for one is refused rather than quietly run on a
// different interpreter.
func gluePythonShellImage(job GlueJob) (string, error) {
	version := glueString(job.Command["PythonVersion"])
	if version == "" {
		version = glueString(job.Command["pythonVersion"])
	}
	switch version {
	case "", "3", "3.9":
		return "mirror.gcr.io/library/python:3.9", nil
	case "2":
		return "", fmt.Errorf("Command.PythonVersion 2 is not supported for Python shell jobs")
	default:
		return "", fmt.Errorf("Command.PythonVersion %q is not a Python shell job version", version)
	}
}

func glueRunPythonJob(jobName, runID string, job GlueJob, script []byte, args map[string]string) {
	image, err := gluePythonShellImage(job)
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	workDir, err := os.MkdirTemp("", "sockerless-glue-*")
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	defer os.RemoveAll(workDir)

	scriptPath := filepath.Join(workDir, "script.py")
	if err := os.WriteFile(scriptPath, script, 0700); err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}

	registryAuth, err := ecrWorkloadRegistryAuth(image)
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	platform, err := workload.LocalImagePlatform(context.Background(), image, registryAuth)
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	network, err := awsPlatformNetwork(context.Background())
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, err.Error())
		return
	}
	holder := roleHolder("glue", jobName+"/"+runID)
	defer revokeRoleCredentials(holder)
	// The job runs in its interpreter's container, taking the script and the
	// job's arguments the way Glue hands them to a Python shell job.
	backend := placement.ContainerBackend{}
	credentials := grantContainerCredentials(backend, network, holder, job.Role, "GlueJobRunnerSession")
	handle, err := credentials.place(context.Background(), backend, sim.ContainerConfig{
		Image:        image,
		Architecture: platform,
		Command:      []string{"python3"},
		Args:         append([]string{glueJobScriptDir + "/script.py"}, glueArgs(args)...),
		WorkingDir:   glueJobScriptDir,
		Binds:        []string{filepath.Clean(workDir) + ":" + glueJobScriptDir + ":z"},
		Env: workloadhost.MergeEnv(credentials.Env, map[string]string{
			"AWS_DEFAULT_REGION": awsRegion(),
			"AWS_REGION":         awsRegion(),
		}),
		ExtraHosts: workloadhost.ExtraHosts(),
		Network:    network.Name,
		DNS:        network.DNS(),
		Labels:     map[string]string{"sockerless-glue-job-run": runID},
		Sandbox:    SandboxFargate,
	}, glueJobRunLogSink(runID))
	if err != nil {
		glueCompleteRun(jobName, runID, "FAILED", 0, fmt.Sprintf("start job environment %s: %v", image, err))
		return
	}
	result := handle.Wait()
	status := "SUCCEEDED"
	reason := ""
	if result.Error != nil {
		status = "FAILED"
		reason = result.Error.Error()
	} else if result.ExitCode != 0 {
		status = "FAILED"
		reason = fmt.Sprintf("Python shell script exited with status %d", result.ExitCode)
	}
	executionTime := int(result.StoppedAt.Sub(result.StartedAt).Seconds())
	if executionTime < 1 {
		executionTime = 1
	}
	glueCompleteRun(jobName, runID, status, executionTime, reason)
}

func glueCompleteRun(jobName, runID, status string, executionTime int, reason string) {
	glueMu.Lock()
	defer glueMu.Unlock()

	key := jobName + "/" + runID
	run, ok := glueJobRuns.Get(key)
	if !ok {
		return
	}
	run.JobRunState = status
	run.CompletedOn = glueEpochNow()
	run.ExecutionTime = executionTime
	run.ErrorMessage = reason
	glueJobRuns.Put(key, run)
}

func handleGlueGetJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
		RunId   string `json:"RunId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	run, ok := glueJobRuns.Get(req.JobName + "/" + req.RunId)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job run not found: "+req.RunId)
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobRun": run})
}

func handleGlueGetJobRuns(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName    string `json:"JobName"`
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	if _, ok := glueJobs.Get(req.JobName); !ok {
		glueWriteError(w, "EntityNotFoundException", "Job with name: "+req.JobName+" not found")
		return
	}

	all := glueJobRuns.List()
	var runs []GlueJobRun
	for _, run := range all {
		if run.JobName == req.JobName {
			runs = append(runs, run)
		}
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, runs, func(it GlueJobRun) string { return it.ID }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	resp := map[string]any{"JobRuns": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueUpdateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName   string `json:"JobName"`
		JobUpdate struct {
			Description      string            `json:"Description"`
			Role             string            `json:"Role"`
			Command          map[string]any    `json:"Command"`
			DefaultArguments map[string]string `json:"DefaultArguments"`
			GlueVersion      string            `json:"GlueVersion"`
			MaxCapacity      *float64          `json:"MaxCapacity"`
			WorkerType       string            `json:"WorkerType"`
			NumberOfWorkers  *int              `json:"NumberOfWorkers"`
		} `json:"JobUpdate"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	job, ok := glueJobs.Get(req.JobName)
	if !ok {
		glueWriteError(w, "EntityNotFoundException", "Job not found: "+req.JobName)
		return
	}
	// JobUpdate replaces the configuration; tags are preserved (they ride GetTags).
	upd := req.JobUpdate
	if !glueCanAssumeRole(w, upd.Role, "job/"+req.JobName) {
		return
	}
	job.Description = upd.Description
	job.Role = upd.Role
	job.Command = upd.Command
	job.DefaultArguments = upd.DefaultArguments
	job.GlueVersion = upd.GlueVersion
	job.MaxCapacity = upd.MaxCapacity
	job.WorkerType = upd.WorkerType
	job.NumberOfWorkers = upd.NumberOfWorkers
	job.LastModifiedOn = glueEpochNow()
	glueJobs.Put(req.JobName, job)
	glueWriteJSON(w, http.StatusOK, map[string]any{"JobName": req.JobName})
}

func handleGlueListJobs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NextToken  string `json:"NextToken"`
		MaxResults *int   `json:"MaxResults"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	all := glueJobs.List()
	names := make([]string, 0, len(all))
	for _, job := range all {
		names = append(names, job.Name)
	}
	maxR := 0
	if req.MaxResults != nil {
		maxR = *req.MaxResults
	}
	page, nextTok, pageOK := awsKeyPage(w, glueBadToken, names, func(it string) string { return it }, req.NextToken, maxR, 25)
	if !pageOK {
		return
	}
	resp := map[string]any{"JobNames": page}
	if nextTok != "" {
		resp["NextToken"] = nextTok
	}
	glueWriteJSON(w, http.StatusOK, resp)
}

func handleGlueBatchStopJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName   string   `json:"JobName"`
		JobRunIds []string `json:"JobRunIds"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}

	glueMu.Lock()
	defer glueMu.Unlock()

	successes := make([]map[string]any, 0, len(req.JobRunIds))
	errs := make([]map[string]any, 0)
	for _, id := range req.JobRunIds {
		key := req.JobName + "/" + id
		run, ok := glueJobRuns.Get(key)
		if !ok {
			errs = append(errs, map[string]any{
				"JobName":  req.JobName,
				"JobRunId": id,
				"ErrorDetail": map[string]any{
					"ErrorCode":    "EntityNotFoundException",
					"ErrorMessage": "Job run not found: " + id,
				},
			})
			continue
		}
		if run.JobRunState == "RUNNING" || run.JobRunState == "STARTING" {
			run.JobRunState = "STOPPING"
			glueJobRuns.Put(key, run)
		}
		successes = append(successes, map[string]any{
			"JobName":  req.JobName,
			"JobRunId": id,
		})
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{
		"SuccessfulSubmissions": successes,
		"Errors":                errs,
	})
}
