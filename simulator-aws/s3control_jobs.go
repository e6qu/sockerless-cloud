package main

import (
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/e6qu/sockerless-cloud/sim"
)

// Amazon S3 Batch Operations: a job applies one operation to every object a
// manifest lists. The job here does the work — it reads the manifest object
// out of S3, runs the operation against each entry, and reports how many
// tasks succeeded and failed. A job that claimed to run without touching an
// object would report progress no read could confirm.

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
	ConfirmationRequired bool              `json:"confirmationRequired"`
	StatusUpdateReason   string            `json:"statusUpdateReason,omitempty"`
	Operation            s3ControlXMLNode  `json:"operation"`
	Manifest             s3ControlXMLNode  `json:"manifest"`
	Report               s3ControlXMLNode  `json:"report"`
	TotalTasks           int               `json:"totalTasks"`
	TasksSucceeded       int               `json:"tasksSucceeded"`
	TasksFailed          int               `json:"tasksFailed"`
	FailureReasons       []string          `json:"failureReasons,omitempty"`
	Tags                 map[string]string `json:"tags,omitempty"`
}

var s3BatchJobs sim.Store[S3BatchJob]

func s3BatchJobARN(account, jobID string) string {
	return fmt.Sprintf("arn:aws:s3:%s:%s:job/%s", awsRegion(), account, jobID)
}

func registerS3ControlJobs(srv *sim.Server) {
	s3BatchJobs = sim.MakeStore[S3BatchJob](srv.DB(), "s3_batch_jobs")

	srv.HandleFunc("POST /v20180820/jobs", handleS3CreateJob)
	srv.HandleFunc("GET /v20180820/jobs", handleS3ListJobs)
	srv.HandleFunc("GET /v20180820/jobs/{jobId}", handleS3DescribeJob)
	srv.HandleFunc("POST /v20180820/jobs/{jobId}/priority", handleS3UpdateJobPriority)
	srv.HandleFunc("POST /v20180820/jobs/{jobId}/status", handleS3UpdateJobStatus)
	srv.HandleFunc("PUT /v20180820/jobs/{jobId}/tagging", handleS3PutJobTagging)
	srv.HandleFunc("GET /v20180820/jobs/{jobId}/tagging", handleS3GetJobTagging)
	srv.HandleFunc("DELETE /v20180820/jobs/{jobId}/tagging", handleS3DeleteJobTagging)
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
	job := S3BatchJob{
		AccountID: account, JobID: s3BatchJobID(),
		Description:          body.ChildText("Description"),
		Priority:             priority,
		RoleArn:              roleArn,
		CreationTime:         time.Now().UTC().Format(time.RFC3339),
		ConfirmationRequired: body.ChildText("ConfirmationRequired") == "true",
		Operation:            operation, Manifest: manifest, Report: report,
		Tags: s3ControlTagsFrom(body, "Tags", "member"),
	}
	// A job awaiting confirmation does not run until the caller moves it to
	// Ready; one that needs no confirmation runs straight away.
	if job.ConfirmationRequired {
		job.Status = "Suspended"
		s3BatchJobs.Put(s3AccessPointKey(account, job.JobID), job)
	} else {
		s3BatchJobs.Put(s3AccessPointKey(account, job.JobID), job)
		s3RunBatchJob(account, job.JobID)
	}
	WriteXML(w, http.StatusOK, struct {
		XMLName xml.Name `xml:"CreateJobResult"`
		JobID   string   `xml:"JobId"`
	}{JobID: job.JobID})
}

// s3BatchJobID is the job identifier S3 hands back, in the UUID form the
// service uses.
func s3BatchJobID() string { return generateUUID() }

// s3RunBatchJob reads the job's manifest and applies its operation to every
// entry, recording what succeeded and what did not.
func s3RunBatchJob(account, jobID string) {
	job, ok := s3BatchJobs.Get(s3AccessPointKey(account, jobID))
	if !ok {
		return
	}
	entries, err := s3BatchManifestEntries(job.Manifest)
	if err != nil {
		s3BatchJobs.Update(s3AccessPointKey(account, jobID), func(j *S3BatchJob) {
			j.Status = "Failed"
			j.FailureReasons = []string{err.Error()}
			j.TerminationDate = time.Now().UTC().Format(time.RFC3339)
		})
		return
	}
	succeeded, failed := 0, 0
	var reasons []string
	for _, entry := range entries {
		if err := s3RunBatchTask(job, entry); err != nil {
			failed++
			if len(reasons) < 5 {
				reasons = append(reasons, err.Error())
			}
			continue
		}
		succeeded++
	}
	status := "Complete"
	if succeeded == 0 && failed > 0 {
		status = "Failed"
	}
	s3BatchJobs.Update(s3AccessPointKey(account, jobID), func(j *S3BatchJob) {
		j.Status = status
		j.TotalTasks, j.TasksSucceeded, j.TasksFailed = len(entries), succeeded, failed
		j.FailureReasons = reasons
		j.TerminationDate = time.Now().UTC().Format(time.RFC3339)
	})
}

// s3BatchManifestEntry is one object the job operates on.
type s3BatchManifestEntry struct {
	Bucket string
	Key    string
}

// s3BatchManifestEntries reads the manifest object out of S3. The CSV formats
// the service accepts both start with the bucket and the key, which is what
// identifies the object a task acts on.
func s3BatchManifestEntries(manifest s3ControlXMLNode) ([]s3BatchManifestEntry, error) {
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
	reader := csv.NewReader(strings.NewReader(string(object.Data)))
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("the manifest is not readable as CSV: %w", err)
	}
	var entries []s3BatchManifestEntry
	for _, record := range records {
		if len(record) < 2 {
			continue
		}
		entries = append(entries, s3BatchManifestEntry{
			Bucket: strings.TrimSpace(record[0]), Key: strings.TrimSpace(record[1]),
		})
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("the manifest lists no objects")
	}
	return entries, nil
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

// s3RunBatchTask applies the job's operation to one object.
func s3RunBatchTask(job S3BatchJob, entry s3BatchManifestEntry) error {
	object, ok := s3Objects.Get(s3ObjectKey(entry.Bucket, entry.Key))
	if !ok {
		return fmt.Errorf("s3://%s/%s does not exist", entry.Bucket, entry.Key)
	}
	switch {
	case hasChild(job.Operation, "LambdaInvoke"):
		return s3RunBatchLambdaInvoke(job, entry)
	case hasChild(job.Operation, "S3PutObjectTagging"):
		operation, _ := job.Operation.Child("S3PutObjectTagging")
		s3ObjectTags.Put(entry.Bucket+"/"+entry.Key, s3ControlTagsFrom(operation, "TagSet", "member"))
		return nil
	case hasChild(job.Operation, "S3DeleteObjectTagging"):
		s3ObjectTags.Delete(entry.Bucket + "/" + entry.Key)
		return nil
	case hasChild(job.Operation, "S3PutObjectCopy"):
		operation, _ := job.Operation.Child("S3PutObjectCopy")
		target := operation.ChildText("TargetResource")
		targetBucket, ok := strings.CutPrefix(target, "arn:aws:s3:::")
		if !ok || targetBucket == "" {
			return fmt.Errorf("the copy operation's TargetResource %q is not a bucket ARN", target)
		}
		targetBucket, targetPrefix, _ := strings.Cut(targetBucket, "/")
		if _, ok := s3Buckets_.Get(targetBucket); !ok {
			return fmt.Errorf("the target bucket %s does not exist", targetBucket)
		}
		targetKey := entry.Key
		if targetPrefix != "" {
			targetKey = strings.TrimSuffix(targetPrefix, "/") + "/" + entry.Key
		}
		_, err := s3PutServiceObject(targetBucket, targetKey, object.Data, object.ContentType, object.Metadata)
		return err
	case hasChild(job.Operation, "S3PutObjectLegalHold"):
		operation, _ := job.Operation.Child("S3PutObjectLegalHold")
		status := "OFF"
		if hold, ok := operation.Child("LegalHold"); ok {
			status = hold.ChildText("Status")
		}
		s3Objects.Update(s3ObjectKey(entry.Bucket, entry.Key),
			func(o *S3Object) { o.LegalHoldStatus = status })
		return nil
	}
	return fmt.Errorf("the job's operation is not one this job can apply")
}

func hasChild(node s3ControlXMLNode, name string) bool {
	_, ok := node.Child(name)
	return ok
}

// s3RunBatchLambdaInvoke invokes the job's function once per object, with the
// task event Batch Operations sends.
func s3RunBatchLambdaInvoke(job S3BatchJob, entry s3BatchManifestEntry) error {
	operation, _ := job.Operation.Child("LambdaInvoke")
	arn := operation.ChildText("FunctionArn")
	fn, ok := lambdaFunctions.Get(ebLambdaNameFromARN(arn))
	if !ok {
		return fmt.Errorf("the function %s does not exist", arn)
	}
	payload, err := json.Marshal(map[string]any{
		"invocationSchemaVersion": "1.0",
		"invocationId":            s3ObjectLambdaID(),
		"job":                     map[string]any{"id": job.JobID},
		"tasks": []map[string]any{{
			"taskId":      s3ObjectLambdaID(),
			"s3Key":       entry.Key,
			"s3BucketArn": s3BucketARN(entry.Bucket),
		}},
	})
	if err != nil {
		return fmt.Errorf("build the task event: %w", err)
	}
	out, handled, status := invokeLambdaViaRuntimeAPI(fn, payload)
	if !handled || status >= 300 {
		return fmt.Errorf("s3://%s/%s: the function failed: %s",
			entry.Bucket, entry.Key, strings.TrimSpace(string(out)))
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
	WriteXML(w, http.StatusOK, struct {
		XMLName            xml.Name         `xml:"DescribeJobResult"`
		JobID              string           `xml:"Job>JobId"`
		Description        string           `xml:"Job>Description,omitempty"`
		JobArn             string           `xml:"Job>JobArn"`
		Status             string           `xml:"Job>Status"`
		Operation          s3ControlXMLNode `xml:"Job>Operation"`
		Manifest           s3ControlXMLNode `xml:"Job>Manifest"`
		Report             s3ControlXMLNode `xml:"Job>Report"`
		Priority           int              `xml:"Job>Priority"`
		Progress           progress         `xml:"Job>ProgressSummary"`
		StatusUpdateReason string           `xml:"Job>StatusUpdateReason,omitempty"`
		FailureReasons     []string         `xml:"Job>FailureReasons>member>FailureReason,omitempty"`
		CreationTime       string           `xml:"Job>CreationTime"`
		TerminationDate    string           `xml:"Job>TerminationDate,omitempty"`
		RoleArn            string           `xml:"Job>RoleArn"`
	}{
		JobID: job.JobID, Description: job.Description,
		JobArn: s3BatchJobARN(account, job.JobID), Status: job.Status,
		Operation: job.Operation, Manifest: job.Manifest, Report: job.Report,
		Priority: job.Priority,
		Progress: progress{
			TotalNumberOfTasks: job.TotalTasks, NumberOfTasksSucceeded: job.TasksSucceeded,
			NumberOfTasksFailed: job.TasksFailed,
		},
		StatusUpdateReason: job.StatusUpdateReason, FailureReasons: job.FailureReasons,
		CreationTime: job.CreationTime, TerminationDate: job.TerminationDate,
		RoleArn: job.RoleArn,
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
		items = append(items, entry{
			JobID: job.JobID, Description: job.Description, Priority: job.Priority,
			Status: job.Status, CreationTime: job.CreationTime,
			TerminationDate: job.TerminationDate,
			Progress: progress{
				TotalNumberOfTasks: job.TotalTasks, NumberOfTasksSucceeded: job.TasksSucceeded,
				NumberOfTasksFailed: job.TasksFailed,
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
	if requested != "Ready" && requested != "Cancelled" {
		s3ControlError(w, "InvalidRequest",
			"requestedJobStatus must be Ready or Cancelled", http.StatusBadRequest)
		return
	}
	reason := r.URL.Query().Get("statusUpdateReason")
	job, ok := s3BatchJobs.Get(s3AccessPointKey(account, jobID))
	if !ok {
		s3ControlError(w, "NoSuchJob", "The specified job does not exist", http.StatusNotFound)
		return
	}
	// A job that already ran to completion cannot be moved: its work is done.
	if job.Status == "Complete" || job.Status == "Failed" || job.Status == "Cancelled" {
		s3ControlError(w, "JobStatusException",
			"The job is in the "+job.Status+" state and cannot be updated", http.StatusBadRequest)
		return
	}
	s3BatchJobs.Update(s3AccessPointKey(account, jobID), func(j *S3BatchJob) {
		j.StatusUpdateReason = reason
		if requested == "Cancelled" {
			j.Status, j.TerminationDate = "Cancelled", time.Now().UTC().Format(time.RFC3339)
		}
	})
	// Confirming a suspended job is what starts it, so the run happens here.
	if requested == "Ready" {
		s3RunBatchJob(account, jobID)
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
