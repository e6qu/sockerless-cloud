package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/csv"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
	"github.com/e6qu/sockerless-cloud/sim/bg"
)

// Amazon S3 Batch Operations: a job applies one operation to every object a
// manifest lists. The job runs as server background work after CreateJob
// answers: it reads the manifest object out of S3, runs the operation against
// each entry, writes the completion report the job asked for, and reports how
// many tasks succeeded and failed. A job that claimed to run without touching
// an object would report progress no read could confirm.

// The job statuses the service reports.
const (
	s3BatchJobNew        = "New"
	s3BatchJobPreparing  = "Preparing"
	s3BatchJobSuspended  = "Suspended"
	s3BatchJobReady      = "Ready"
	s3BatchJobActive     = "Active"
	s3BatchJobCompleting = "Completing"
	s3BatchJobComplete   = "Complete"
	s3BatchJobFailing    = "Failing"
	s3BatchJobFailed     = "Failed"
	s3BatchJobCancelling = "Cancelling"
	s3BatchJobCancelled  = "Cancelled"
)

// S3BatchJob is one batch operation job.
type S3BatchJob struct {
	AccountID            string            `json:"accountId"`
	JobID                string            `json:"jobId"`
	Description          string            `json:"description,omitempty"`
	Status               string            `json:"status"`
	Priority             int               `json:"priority"`
	RoleArn              string            `json:"roleArn"`
	CreationTime         string            `json:"creationTime"`
	TerminationDate      string            `json:"terminationDate,omitempty"`
	SuspendedDate        string            `json:"suspendedDate,omitempty"`
	ConfirmationRequired bool              `json:"confirmationRequired"`
	StatusUpdateReason   string            `json:"statusUpdateReason,omitempty"`
	Operation            s3ControlXMLNode  `json:"operation"`
	Manifest             s3ControlXMLNode  `json:"manifest"`
	Report               s3ControlXMLNode  `json:"report"`
	Tasks                []S3BatchTask     `json:"tasks,omitempty"`
	FailureReasons       []string          `json:"failureReasons,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
}

// S3BatchTask is one manifest entry and what running the operation on it
// came to. A task with no status has not run yet; a task the function asked
// to have redriven runs again once every other task has had its turn.
type S3BatchTask struct {
	TaskID        string `json:"taskId"`
	Bucket        string `json:"bucket"`
	Key           string `json:"key"`
	ManifestKey   string `json:"manifestKey"`
	VersionID     string `json:"versionId,omitempty"`
	Status        string `json:"status,omitempty"`
	Attempts      int    `json:"attempts,omitempty"`
	ErrorCode     string `json:"errorCode,omitempty"`
	HTTPStatus    int    `json:"httpStatus,omitempty"`
	ResultMessage string `json:"resultMessage,omitempty"`
}

// The outcomes a task records.
const (
	s3BatchTaskSucceeded = "succeeded"
	s3BatchTaskFailed    = "failed"
	s3BatchTaskRedrive   = "redrive"
)

// s3BatchTaskRedrives is how many times a task whose function answered
// TemporaryFailure runs again before it counts as failed. AWS documents that
// such a task is redriven before the job completes, not how often.
const s3BatchTaskRedrives = 3

func (j S3BatchJob) progress() (total, succeeded, failed int) {
	for _, task := range j.Tasks {
		switch task.Status {
		case s3BatchTaskSucceeded:
			succeeded++
		case s3BatchTaskFailed:
			failed++
		}
	}
	return len(j.Tasks), succeeded, failed
}

var (
	s3BatchJobs      sim.Store[S3BatchJob]
	s3BatchJobServer *sim.Server
)

func s3BatchJobARN(account, jobID string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:job/%s", awsRegion(), account, jobID)
}

func registerS3ControlJobs(srv *sim.Server) {
	s3BatchJobs = sim.MakeStore[S3BatchJob](srv.DB(), "s3_batch_jobs")
	s3BatchJobServer = srv

	s3ControlRegister(srv, s3ControlJobRoutes)
}

// s3ControlJobRoutes carries what each Batch Operations route is authorized
// as. Creating and listing name no job, so AWS declares no resource type for
// either; everything addressed to one job is evaluated against that job's ARN.
var s3ControlJobRoutes = []s3ControlRoute{
	{"POST /v20180820/jobs", "CreateJob", nil, handleS3CreateJob},
	{"GET /v20180820/jobs", "ListJobs", nil, handleS3ListJobs},
	{"GET /v20180820/jobs/{jobId}", "DescribeJob", s3ControlJobResource, handleS3DescribeJob},
	{"POST /v20180820/jobs/{jobId}/priority", "UpdateJobPriority", s3ControlJobResource, handleS3UpdateJobPriority},
	{"POST /v20180820/jobs/{jobId}/status", "UpdateJobStatus", s3ControlJobResource, handleS3UpdateJobStatus},
	{"PUT /v20180820/jobs/{jobId}/tagging", "PutJobTagging", s3ControlJobResource, handleS3PutJobTagging},
	{"GET /v20180820/jobs/{jobId}/tagging", "GetJobTagging", s3ControlJobResource, handleS3GetJobTagging},
	{"DELETE /v20180820/jobs/{jobId}/tagging", "DeleteJobTagging", s3ControlJobResource, handleS3DeleteJobTagging},
}

func handleS3CreateJob(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	body, ok := s3ControlReadXMLBody(w, r, "CreateJobRequest")
	if !ok {
		return
	}
	operation, hasOperation := body.Child("Operation")
	report, hasReport := body.Child("Report")
	priority, priorityErr := strconv.Atoi(body.ChildText("Priority"))
	roleArn := body.ChildText("RoleArn")
	if !hasOperation || !hasReport || priorityErr != nil || roleArn == "" ||
		body.ChildText("ClientRequestToken") == "" {
		s3ControlError(w, "InvalidRequest",
			"Operation, Report, ClientRequestToken, Priority and RoleArn are required",
			http.StatusBadRequest)
		return
	}
	if _, ok := iamRoles.Get(iamRoleNameFromArn(roleArn)); !ok {
		s3ControlError(w, "InvalidRequest", "The role "+roleArn+" does not exist", http.StatusBadRequest)
		return
	}
	manifest, hasManifest := body.Child("Manifest")
	if !hasManifest {
		s3ControlError(w, "InvalidRequest",
			"Manifest is required unless a ManifestGenerator produces one", http.StatusBadRequest)
		return
	}
	if lambda, ok := operation.Child("LambdaInvoke"); ok {
		if version := lambda.ChildText("InvocationSchemaVersion"); version != "" && version != "1.0" && version != "2.0" {
			s3ControlError(w, "InvalidRequest", "InvocationSchemaVersion must be 1.0 or 2.0", http.StatusBadRequest)
			return
		}
	}
	job := S3BatchJob{
		AccountID: account, JobID: s3BatchJobID(),
		Description:          body.ChildText("Description"),
		Status:               s3BatchJobNew,
		Priority:             priority,
		RoleArn:              roleArn,
		CreationTime:         time.Now().UTC().Format(time.RFC3339),
		ConfirmationRequired: body.ChildText("ConfirmationRequired") == "true",
		Operation:            operation, Manifest: manifest, Report: report,
		Tags: s3ControlTagsFrom(body, "Tags", "member"),
	}
	s3BatchJobs.Put(s3AccessPointKey(account, job.JobID), job)
	s3ScheduleBatchJob(account, job.JobID)
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"CreateJobResult"`
		JobID   string   `xml:"JobId"`
	}{JobID: job.JobID})
}

// s3BatchJobID is the job identifier S3 hands back, in the UUID form the
// service uses.
func s3BatchJobID() string { return sim.NewUUID() }

// s3ScheduleBatchJob hands the job to the server's background workers, which
// outlive the request that created or confirmed it and stop with the
// simulator. A job a stopping simulator interrupts keeps its status and the
// tasks it finished, and s3RecoverBatchJobs resumes it in the next process.
func s3ScheduleBatchJob(account, jobID string) {
	var workerCtx context.Context
	run, ok := bg.Handoff(func() { s3RunBatchJob(workerCtx, account, jobID) })
	if !ok {
		return
	}
	s3BatchJobServer.StartBackground("Amazon S3 Batch Operations job", func(ctx context.Context) {
		workerCtx = ctx
		run()
	})
}

// s3RecoverBatchJobs resumes every job a previous process left between
// creation and a final status. A Suspended job waits for its confirmation,
// which is not work to resume.
func s3RecoverBatchJobs() {
	for _, job := range s3BatchJobs.List() {
		switch job.Status {
		case s3BatchJobNew, s3BatchJobPreparing, s3BatchJobReady, s3BatchJobActive,
			s3BatchJobCompleting, s3BatchJobFailing, s3BatchJobCancelling:
			s3ScheduleBatchJob(job.AccountID, job.JobID)
		}
	}
}

// s3BatchJobMove moves the job from one status to another and reports whether
// it did: a cancellation can land between a worker reading the status and
// writing the next one, and the worker must not overwrite it.
func s3BatchJobMove(key, from, to string, mutate func(*S3BatchJob)) bool {
	moved := false
	s3BatchJobs.Update(key, func(j *S3BatchJob) {
		if j.Status != from {
			return
		}
		j.Status = to
		if mutate != nil {
			mutate(j)
		}
		moved = true
	})
	return moved
}

// s3RunBatchJob drives the job from whatever status it is in to the next one
// the service would report, until it reaches a status only a caller moves it
// out of, a final status, or the simulator stops.
func s3RunBatchJob(ctx context.Context, account, jobID string) {
	key := s3AccessPointKey(account, jobID)
	for ctx.Err() == nil {
		job, ok := s3BatchJobs.Get(key)
		if !ok {
			return
		}
		switch job.Status {
		case s3BatchJobNew:
			s3BatchJobMove(key, s3BatchJobNew, s3BatchJobPreparing, nil)
		case s3BatchJobPreparing:
			tasks, err := s3BatchManifestTasks(job.Manifest)
			if err != nil {
				s3BatchJobMove(key, s3BatchJobPreparing, s3BatchJobFailed, func(j *S3BatchJob) {
					j.FailureReasons = []string{err.Error()}
					j.TerminationDate = time.Now().UTC().Format(time.RFC3339)
				})
				continue
			}
			next := s3BatchJobReady
			if job.ConfirmationRequired {
				next = s3BatchJobSuspended
			}
			s3BatchJobMove(key, s3BatchJobPreparing, next, func(j *S3BatchJob) {
				j.Tasks = tasks
				if next == s3BatchJobSuspended {
					j.SuspendedDate = time.Now().UTC().Format(time.RFC3339)
				}
			})
		case s3BatchJobReady:
			s3BatchJobMove(key, s3BatchJobReady, s3BatchJobActive, nil)
		case s3BatchJobActive:
			s3RunBatchTasks(ctx, key)
		case s3BatchJobCompleting:
			s3FinishBatchJob(key, job, s3BatchJobCompleting, s3BatchJobComplete)
		case s3BatchJobFailing:
			s3FinishBatchJob(key, job, s3BatchJobFailing, s3BatchJobFailed)
		case s3BatchJobCancelling:
			s3FinishBatchJob(key, job, s3BatchJobCancelling, s3BatchJobCancelled)
		default:
			return
		}
	}
}

// s3RunBatchTasks runs the job's tasks one at a time, recording each outcome
// as it lands, so a cancellation stops the job between tasks and a restart
// resumes after the last task recorded. Tasks never run come first, then the
// ones the function asked to have redriven.
func s3RunBatchTasks(ctx context.Context, key string) {
	for ctx.Err() == nil {
		job, ok := s3BatchJobs.Get(key)
		if !ok || job.Status != s3BatchJobActive {
			return
		}
		next := -1
		for i, task := range job.Tasks {
			if task.Status == "" {
				next = i
				break
			}
			if task.Status == s3BatchTaskRedrive && next < 0 {
				next = i
			}
		}
		if next < 0 {
			_, succeeded, failed := job.progress()
			final := s3BatchJobCompleting
			if succeeded == 0 && failed > 0 {
				final = s3BatchJobFailing
			}
			s3BatchJobMove(key, s3BatchJobActive, final, nil)
			return
		}
		outcome, interrupted := s3RunBatchTask(ctx, job, job.Tasks[next])
		if interrupted {
			return
		}
		s3BatchJobs.Update(key, func(j *S3BatchJob) {
			if next >= len(j.Tasks) {
				return
			}
			task := &j.Tasks[next]
			task.Attempts++
			task.ErrorCode, task.HTTPStatus, task.ResultMessage = outcome.code, outcome.http, outcome.message
			task.Status = outcome.status
			if task.Status == s3BatchTaskRedrive && task.Attempts > s3BatchTaskRedrives {
				task.Status = s3BatchTaskFailed
			}
		})
	}
}

// s3FinishBatchJob writes the completion report the job asked for and lands
// the job in its final status. A report that cannot be written fails the job,
// with the reason.
func s3FinishBatchJob(key string, job S3BatchJob, from, final string) {
	reportErr := s3WriteBatchCompletionReport(job)
	s3BatchJobMove(key, from, final, func(j *S3BatchJob) {
		if reportErr != nil {
			j.Status = s3BatchJobFailed
			j.FailureReasons = append(j.FailureReasons, reportErr.Error())
		}
		j.TerminationDate = time.Now().UTC().Format(time.RFC3339)
	})
}

// s3BatchManifestTasks reads the manifest object out of S3. A CSV manifest
// lists one object per row, in the columns its Spec.Fields names: the bucket,
// the URL-encoded key and, when the spec names one, the version. The key
// decodes as a form value does, `+` to a space. A task keeps the key as listed
// too: the Lambda event's s3Key and the completion report carry that form.
func s3BatchManifestTasks(manifest s3ControlXMLNode) ([]S3BatchTask, error) {
	location, ok := manifest.Child("Location")
	if !ok {
		return nil, fmt.Errorf("the manifest has no Location")
	}
	objectArn := location.ChildText("ObjectArn")
	bucket, key, ok := s3BucketKeyFromARN(objectArn)
	if !ok {
		return nil, fmt.Errorf("the manifest location %q is not an S3 object ARN", objectArn)
	}
	object, ok := s3Objects.Get(s3ObjectKey(bucket, key))
	if !ok {
		return nil, fmt.Errorf("the manifest object %s does not exist", objectArn)
	}
	if etag := location.ChildText("ETag"); etag != "" &&
		strings.Trim(etag, `"`) != strings.Trim(object.ETag, `"`) {
		return nil, fmt.Errorf("the manifest object's ETag does not match the one the job was created with")
	}
	manifestData, err := s3ObjectData(object)
	if err != nil {
		return nil, fmt.Errorf("read the manifest object %s: %w", objectArn, err)
	}
	versionColumn := -1
	if spec, ok := manifest.Child("Spec"); ok {
		if fields, ok := spec.Child("Fields"); ok {
			for i, field := range fields.Children {
				if field.Text == "VersionId" {
					versionColumn = i
				}
			}
		}
	}
	reader := csv.NewReader(bytes.NewReader(manifestData))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("the manifest is not readable as CSV: %w", err)
	}
	var tasks []S3BatchTask
	for row, record := range records {
		if len(record) < 2 {
			continue
		}
		listed := strings.TrimSpace(record[1])
		key, err := url.QueryUnescape(listed)
		if err != nil {
			return nil, fmt.Errorf("the manifest's row %d key %q is not URL-encoded: %w", row+1, listed, err)
		}
		task := S3BatchTask{
			TaskID: s3ObjectLambdaID(),
			Bucket: strings.TrimSpace(record[0]), Key: key, ManifestKey: listed,
		}
		if versionColumn >= 0 && versionColumn < len(record) {
			task.VersionID = strings.TrimSpace(record[versionColumn])
		}
		tasks = append(tasks, task)
	}
	if len(tasks) == 0 {
		return nil, fmt.Errorf("the manifest lists no objects")
	}
	return tasks, nil
}

// s3BucketKeyFromARN splits an S3 object ARN into its bucket and key.
func s3BucketKeyFromARN(arn string) (string, string, bool) {
	rest, ok := strings.CutPrefix(arn, "arn:aws:s3:::")
	if !ok {
		return "", "", false
	}
	bucket, key, ok := strings.Cut(rest, "/")
	if !ok || bucket == "" || key == "" {
		return "", "", false
	}
	return bucket, key, true
}

// s3BatchTaskOutcome is what one run of a task came to, in the terms the
// completion report records it.
type s3BatchTaskOutcome struct {
	status, code, message string
	http                  int
}

func s3BatchTaskSuccess(message string) s3BatchTaskOutcome {
	return s3BatchTaskOutcome{status: s3BatchTaskSucceeded, http: http.StatusOK, message: message}
}

func s3BatchTaskFailure(code string, status int, message string) s3BatchTaskOutcome {
	return s3BatchTaskOutcome{status: s3BatchTaskFailed, code: code, http: status, message: message}
}

// s3RunBatchTask applies the job's operation to one object. interrupted
// reports a run the simulator's shutdown cut short, which records nothing.
// A LambdaInvoke task hands the function the manifest's key whether or not
// an object holds it, as the service's documented JSON-key manifests rely on.
func s3RunBatchTask(ctx context.Context, job S3BatchJob, task S3BatchTask) (s3BatchTaskOutcome, bool) {
	if hasChild(job.Operation, "LambdaInvoke") {
		return s3RunBatchLambdaInvoke(ctx, job, task)
	}
	object, ok := s3Objects.Get(s3ObjectKey(task.Bucket, task.Key))
	if !ok {
		return s3BatchTaskFailure("NoSuchKey", http.StatusNotFound,
			fmt.Sprintf("s3://%s/%s does not exist", task.Bucket, task.Key)), false
	}
	switch {
	case hasChild(job.Operation, "S3PutObjectTagging"):
		operation, _ := job.Operation.Child("S3PutObjectTagging")
		s3ObjectTags.Put(task.Bucket+"/"+task.Key, s3ControlTagsFrom(operation, "TagSet", "member"))
		return s3BatchTaskSuccess("Successful"), false
	case hasChild(job.Operation, "S3DeleteObjectTagging"):
		s3ObjectTags.Delete(task.Bucket + "/" + task.Key)
		return s3BatchTaskSuccess("Successful"), false
	case hasChild(job.Operation, "S3PutObjectCopy"):
		operation, _ := job.Operation.Child("S3PutObjectCopy")
		target := operation.ChildText("TargetResource")
		targetBucket, ok := strings.CutPrefix(target, "arn:aws:s3:::")
		if !ok || targetBucket == "" {
			return s3BatchTaskFailure("InvalidRequest", http.StatusBadRequest,
				fmt.Sprintf("the copy operation's TargetResource %q is not a bucket ARN", target)), false
		}
		targetBucket, targetPrefix, _ := strings.Cut(targetBucket, "/")
		if _, ok := s3Buckets_.Get(targetBucket); !ok {
			return s3BatchTaskFailure("NoSuchBucket", http.StatusNotFound,
				fmt.Sprintf("the target bucket %s does not exist", targetBucket)), false
		}
		targetKey := task.Key
		if targetPrefix != "" {
			targetKey = strings.TrimSuffix(targetPrefix, "/") + "/" + task.Key
		}
		object, data, err := s3OpenObjectData(object)
		if err != nil {
			return s3BatchTaskFailure("InternalError", http.StatusInternalServerError, err.Error()), false
		}
		if _, err := s3PutServiceObject(targetBucket, targetKey, data, object.ContentType, object.Metadata); err != nil {
			return s3BatchTaskFailure("InternalError", http.StatusInternalServerError, err.Error()), false
		}
		return s3BatchTaskSuccess("Successful"), false
	case hasChild(job.Operation, "S3PutObjectLegalHold"):
		if _, enabled := s3BucketObjectLock(task.Bucket); !enabled {
			return s3BatchTaskFailure("InvalidRequest", http.StatusBadRequest,
				"Bucket is missing Object Lock Configuration"), false
		}
		operation, _ := job.Operation.Child("S3PutObjectLegalHold")
		status := "OFF"
		if hold, ok := operation.Child("LegalHold"); ok {
			status = hold.ChildText("Status")
		}
		s3Objects.Update(s3ObjectKey(task.Bucket, task.Key),
			func(o *S3Object) { o.LegalHoldStatus = status })
		return s3BatchTaskSuccess("Successful"), false
	}
	return s3BatchTaskFailure("InvalidRequest", http.StatusBadRequest,
		"the job's operation is not one this job can apply"), false
}

func hasChild(node s3ControlXMLNode, name string) bool {
	_, ok := node.Child(name)
	return ok
}

// s3BatchLambdaResponse is what a Batch Operations function answers: one
// result per task, and what a task with no result counts as.
type s3BatchLambdaResponse struct {
	TreatMissingKeysAs string `json:"treatMissingKeysAs"`
	Results            []struct {
		TaskID       string `json:"taskId"`
		ResultCode   string `json:"resultCode"`
		ResultString string `json:"resultString"`
	} `json:"results"`
}

// s3BatchLambdaEvent is the task event Batch Operations sends, in the
// invocation schema the job's LambdaInvoke operation names.
func s3BatchLambdaEvent(job S3BatchJob, operation s3ControlXMLNode, task S3BatchTask) map[string]any {
	version := operation.ChildText("InvocationSchemaVersion")
	if version == "" {
		version = "1.0"
	}
	var versionID any
	if task.VersionID != "" {
		versionID = task.VersionID
	}
	if version == "1.0" {
		return map[string]any{
			"invocationSchemaVersion": version,
			"invocationId":            s3ObjectLambdaID(),
			"job":                     map[string]any{"id": job.JobID},
			"tasks": []map[string]any{{
				"taskId": task.TaskID, "s3Key": task.ManifestKey, "s3VersionId": versionID,
				"s3BucketArn": s3BucketARN(task.Bucket),
			}},
		}
	}
	userArguments := map[string]string{}
	if arguments, ok := operation.Child("UserArguments"); ok {
		for _, entry := range arguments.Children {
			userArguments[entry.ChildText("key")] = entry.ChildText("value")
		}
	}
	return map[string]any{
		"invocationSchemaVersion": version,
		"invocationId":            s3ObjectLambdaID(),
		"job":                     map[string]any{"id": job.JobID, "userArguments": userArguments},
		"tasks": []map[string]any{{
			"taskId": task.TaskID, "s3Bucket": task.Bucket, "s3Key": task.ManifestKey, "s3VersionId": versionID,
		}},
	}
}

// s3RunBatchLambdaInvoke invokes the job's function with one task and reads
// the task's outcome from the result the function returns for it.
func s3RunBatchLambdaInvoke(ctx context.Context, job S3BatchJob, task S3BatchTask) (s3BatchTaskOutcome, bool) {
	operation, _ := job.Operation.Child("LambdaInvoke")
	arn := operation.ChildText("FunctionArn")
	fn, ok := lambdaFunctions.Get(ebLambdaNameFromARN(arn))
	if !ok {
		return s3BatchTaskFailure("PermanentFailure", http.StatusBadRequest,
			fmt.Sprintf("the function %s does not exist", arn)), false
	}
	payload, err := json.Marshal(s3BatchLambdaEvent(job, operation, task))
	if err != nil {
		return s3BatchTaskFailure("PermanentFailure", http.StatusBadRequest,
			fmt.Sprintf("build the task event: %v", err)), false
	}
	out, unhandled, _ := invokeLambdaViaRuntimeAPI(ctx, fn, payload)
	if ctx.Err() != nil {
		return s3BatchTaskOutcome{}, true
	}
	if unhandled {
		return s3BatchTaskFailure("PermanentFailure", http.StatusBadRequest,
			"the function failed: "+strings.TrimSpace(string(out))), false
	}
	var response s3BatchLambdaResponse
	if err := json.Unmarshal(out, &response); err != nil {
		return s3BatchTaskFailure("PermanentFailure", http.StatusBadRequest,
			"the function's response is not a Batch Operations result: "+err.Error()), false
	}
	code, message := response.TreatMissingKeysAs, ""
	if code == "" {
		code = "PermanentFailure"
	}
	for _, result := range response.Results {
		if result.TaskID == task.TaskID {
			code, message = result.ResultCode, result.ResultString
			break
		}
	}
	switch code {
	case "Succeeded":
		return s3BatchTaskSuccess(message), false
	case "TemporaryFailure":
		return s3BatchTaskOutcome{status: s3BatchTaskRedrive, code: code,
			http: http.StatusInternalServerError, message: message}, false
	default:
		return s3BatchTaskFailure("PermanentFailure", http.StatusBadRequest, message), false
	}
}

// s3BatchReportEntry is one results file a completion report lists.
type s3BatchReportEntry struct {
	TaskExecutionStatus string `json:"TaskExecutionStatus"`
	Bucket              string `json:"Bucket"`
	MD5Checksum         string `json:"MD5Checksum"`
	Key                 string `json:"Key"`
}

// s3WriteBatchCompletionReport writes the report the job's Report asks for:
// under <prefix>/job-<id>/, one results CSV per task status the report's scope
// covers, and a manifest.json listing them. A row reads bucket, key, version,
// status, then the HTTP status code and error code in the order the service's
// published reports put them, then the result message.
func s3WriteBatchCompletionReport(job S3BatchJob) error {
	if job.Report.ChildText("Enabled") != "true" {
		return nil
	}
	bucketArn := job.Report.ChildText("Bucket")
	bucket, ok := strings.CutPrefix(bucketArn, "arn:aws:s3:::")
	if !ok || bucket == "" || strings.Contains(bucket, "/") {
		return fmt.Errorf("the report bucket %q is not a bucket ARN", bucketArn)
	}
	if _, ok := s3Buckets_.Get(bucket); !ok {
		return fmt.Errorf("the report bucket %s does not exist", bucket)
	}
	base := "job-" + job.JobID
	if prefix := strings.TrimSuffix(job.Report.ChildText("Prefix"), "/"); prefix != "" {
		base = prefix + "/" + base
	}
	statuses := []string{s3BatchTaskSucceeded, s3BatchTaskFailed}
	if job.Report.ChildText("ReportScope") == "FailedTasksOnly" {
		statuses = []string{s3BatchTaskFailed}
	}
	results := []s3BatchReportEntry{}
	for _, status := range statuses {
		var rows bytes.Buffer
		writer := csv.NewWriter(&rows)
		for _, task := range job.Tasks {
			if task.Status != status {
				continue
			}
			httpStatus := ""
			if task.HTTPStatus != 0 {
				httpStatus = strconv.Itoa(task.HTTPStatus)
			}
			if err := writer.Write([]string{task.Bucket, task.ManifestKey, task.VersionID, task.Status,
				httpStatus, task.ErrorCode, task.ResultMessage}); err != nil {
				return fmt.Errorf("write the %s results: %w", status, err)
			}
		}
		writer.Flush()
		if rows.Len() == 0 {
			continue
		}
		name := sha1.Sum([]byte(job.JobID + "/" + status))
		key := base + "/results/" + hex.EncodeToString(name[:]) + ".csv"
		if _, err := s3PutServiceObject(bucket, key, rows.Bytes(), "text/csv", nil); err != nil {
			return fmt.Errorf("write the completion report's %s results: %w", status, err)
		}
		checksum := md5.Sum(rows.Bytes())
		results = append(results, s3BatchReportEntry{
			TaskExecutionStatus: status, Bucket: bucket,
			MD5Checksum: hex.EncodeToString(checksum[:]), Key: key,
		})
	}
	document, err := json.Marshal(map[string]any{
		"Format":             job.Report.ChildText("Format"),
		"ReportCreationDate": time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		"Results":            results,
		"ReportSchema":       "Bucket, Key, VersionId, TaskStatus, ErrorCode, HTTPStatusCode, ResultMessage",
	})
	if err != nil {
		return fmt.Errorf("build the completion report manifest: %w", err)
	}
	if _, err := s3PutServiceObject(bucket, base+"/manifest.json", document, "application/json", nil); err != nil {
		return fmt.Errorf("write the completion report manifest: %w", err)
	}
	return nil
}

func handleS3DescribeJob(w http.ResponseWriter, r *http.Request) {
	account, jobID := s3ControlAccountID(r), sim.PathParam(r, "jobId")
	job, ok := s3BatchJobs.Get(s3AccessPointKey(account, jobID))
	if !ok {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	type progress struct {
		TotalNumberOfTasks     int `xml:"TotalNumberOfTasks"`
		NumberOfTasksSucceeded int `xml:"NumberOfTasksSucceeded"`
		NumberOfTasksFailed    int `xml:"NumberOfTasksFailed"`
	}
	total, succeeded, failed := job.progress()
	WriteXML(w, http.StatusOK, struct {
		XMLName              xml.Name         `xml:"DescribeJobResult"`
		JobID                string           `xml:"Job>JobId"`
		ConfirmationRequired bool             `xml:"Job>ConfirmationRequired"`
		Description          string           `xml:"Job>Description,omitempty"`
		JobArn               string           `xml:"Job>JobArn"`
		Status               string           `xml:"Job>Status"`
		Operation            s3ControlXMLNode `xml:"Job>Operation"`
		Manifest             s3ControlXMLNode `xml:"Job>Manifest"`
		Report               s3ControlXMLNode `xml:"Job>Report"`
		Priority             int              `xml:"Job>Priority"`
		Progress             progress         `xml:"Job>ProgressSummary"`
		StatusUpdateReason   string           `xml:"Job>StatusUpdateReason,omitempty"`
		FailureReasons       []string         `xml:"Job>FailureReasons>member>FailureReason,omitempty"`
		CreationTime         string           `xml:"Job>CreationTime"`
		TerminationDate      string           `xml:"Job>TerminationDate,omitempty"`
		RoleArn              string           `xml:"Job>RoleArn"`
		SuspendedDate        string           `xml:"Job>SuspendedDate,omitempty"`
	}{
		JobID: job.JobID, ConfirmationRequired: job.ConfirmationRequired, Description: job.Description,
		JobArn: s3BatchJobARN(account, job.JobID), Status: job.Status,
		Operation: job.Operation, Manifest: job.Manifest, Report: job.Report,
		Priority: job.Priority,
		Progress: progress{
			TotalNumberOfTasks: total, NumberOfTasksSucceeded: succeeded, NumberOfTasksFailed: failed,
		},
		StatusUpdateReason: job.StatusUpdateReason, FailureReasons: job.FailureReasons,
		CreationTime: job.CreationTime, TerminationDate: job.TerminationDate,
		RoleArn: job.RoleArn, SuspendedDate: job.SuspendedDate,
	})
}

func handleS3ListJobs(w http.ResponseWriter, r *http.Request) {
	account := s3ControlAccountID(r)
	wanted := map[string]bool{}
	for _, status := range r.URL.Query()["jobStatuses"] {
		for _, one := range strings.Split(status, ",") {
			if one = strings.TrimSpace(one); one != "" {
				wanted[one] = true
			}
		}
	}
	type progress struct {
		TotalNumberOfTasks     int `xml:"TotalNumberOfTasks"`
		NumberOfTasksSucceeded int `xml:"NumberOfTasksSucceeded"`
		NumberOfTasksFailed    int `xml:"NumberOfTasksFailed"`
	}
	type entry struct {
		JobID           string   `xml:"JobId"`
		Description     string   `xml:"Description,omitempty"`
		Priority        int      `xml:"Priority"`
		Status          string   `xml:"Status"`
		CreationTime    string   `xml:"CreationTime"`
		TerminationDate string   `xml:"TerminationDate,omitempty"`
		Progress        progress `xml:"ProgressSummary"`
	}
	var items []entry
	for _, job := range s3BatchJobs.List() {
		if job.AccountID != account {
			continue
		}
		if len(wanted) > 0 && !wanted[job.Status] {
			continue
		}
		total, succeeded, failed := job.progress()
		items = append(items, entry{
			JobID: job.JobID, Description: job.Description, Priority: job.Priority,
			Status: job.Status, CreationTime: job.CreationTime,
			TerminationDate: job.TerminationDate,
			Progress: progress{
				TotalNumberOfTasks: total, NumberOfTasksSucceeded: succeeded, NumberOfTasksFailed: failed,
			},
		})
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreationTime > items[j].CreationTime })
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"ListJobsResult"`
		Jobs    []entry  `xml:"Jobs>member"`
	}{Jobs: items})
}

func handleS3UpdateJobPriority(w http.ResponseWriter, r *http.Request) {
	account, jobID := s3ControlAccountID(r), sim.PathParam(r, "jobId")
	priority, err := strconv.Atoi(r.URL.Query().Get("priority"))
	if err != nil {
		s3ControlError(w, "InvalidRequest", "priority is required", http.StatusBadRequest)
		return
	}
	if !s3BatchJobs.Update(s3AccessPointKey(account, jobID),
		func(j *S3BatchJob) { j.Priority = priority }) {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName  xml.Name `xml:"UpdateJobPriorityResult"`
		JobID    string   `xml:"JobId"`
		Priority int      `xml:"Priority"`
	}{JobID: jobID, Priority: priority})
}

func handleS3UpdateJobStatus(w http.ResponseWriter, r *http.Request) {
	account, jobID := s3ControlAccountID(r), sim.PathParam(r, "jobId")
	requested := r.URL.Query().Get("requestedJobStatus")
	if requested != s3BatchJobReady && requested != s3BatchJobCancelled {
		s3ControlError(w, "InvalidRequest",
			"requestedJobStatus must be Ready or Cancelled", http.StatusBadRequest)
		return
	}
	reason := r.URL.Query().Get("statusUpdateReason")
	key := s3AccessPointKey(account, jobID)
	if _, ok := s3BatchJobs.Get(key); !ok {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	// Confirming moves a job awaiting confirmation to Ready and starts it.
	// Cancelling a job that is not running ends it at once; a running one
	// stops between tasks, which its worker does on seeing Cancelling.
	var current string
	moved := false
	s3BatchJobs.Update(key, func(j *S3BatchJob) {
		current = j.Status
		switch {
		case requested == s3BatchJobReady && j.Status == s3BatchJobSuspended:
			j.Status = s3BatchJobReady
		case requested == s3BatchJobCancelled && j.Status == s3BatchJobSuspended:
			j.Status, j.TerminationDate = s3BatchJobCancelled, time.Now().UTC().Format(time.RFC3339)
		case requested == s3BatchJobCancelled && (j.Status == s3BatchJobNew ||
			j.Status == s3BatchJobPreparing || j.Status == s3BatchJobReady || j.Status == s3BatchJobActive):
			j.Status = s3BatchJobCancelling
		default:
			return
		}
		j.StatusUpdateReason = reason
		moved = true
	})
	if !moved {
		s3ControlError(w, "JobStatusException",
			"The job is in the "+current+" state and cannot be moved to "+requested, http.StatusBadRequest)
		return
	}
	if requested == s3BatchJobReady {
		s3ScheduleBatchJob(account, jobID)
	}
	updated, _ := s3BatchJobs.Get(s3AccessPointKey(account, jobID))
	WriteXML(w, http.StatusOK, struct {
		XMLName            xml.Name `xml:"UpdateJobStatusResult"`
		JobID              string   `xml:"JobId"`
		Status             string   `xml:"Status"`
		StatusUpdateReason string   `xml:"StatusUpdateReason,omitempty"`
	}{JobID: jobID, Status: updated.Status, StatusUpdateReason: reason})
}

func handleS3PutJobTagging(w http.ResponseWriter, r *http.Request) {
	account, jobID := s3ControlAccountID(r), sim.PathParam(r, "jobId")
	body, ok := s3ControlReadXMLBody(w, r, "PutJobTaggingRequest")
	if !ok {
		return
	}
	tags := s3ControlTagsFrom(body, "Tags", "member")
	if len(tags) == 0 {
		s3ControlError(w, "InvalidRequest", "Tags is required", http.StatusBadRequest)
		return
	}
	if !s3BatchJobs.Update(s3AccessPointKey(account, jobID), func(j *S3BatchJob) { j.Tags = tags }) {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func handleS3GetJobTagging(w http.ResponseWriter, r *http.Request) {
	account, jobID := s3ControlAccountID(r), sim.PathParam(r, "jobId")
	job, ok := s3BatchJobs.Get(s3AccessPointKey(account, jobID))
	if !ok {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	s3ControlWriteTags(w, "GetJobTaggingResult", "member", job.Tags)
}

func handleS3DeleteJobTagging(w http.ResponseWriter, r *http.Request) {
	account, jobID := s3ControlAccountID(r), sim.PathParam(r, "jobId")
	if !s3BatchJobs.Update(s3AccessPointKey(account, jobID), func(j *S3BatchJob) { j.Tags = nil }) {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusOK)
}
