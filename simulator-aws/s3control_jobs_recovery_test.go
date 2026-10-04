package main

import (
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestS3BatchJobRecoveryResumesAfterTheLastRecordedTask covers a persistent
// simulator restarting under a job a previous process left Active: recovery
// resumes it after the last task that process recorded, runs only the tasks
// left, and lands the job Complete with both counted.
func TestS3BatchJobRecoveryResumesAfterTheLastRecordedTask(t *testing.T) {
	srv, _, _ := buildConformanceSimulator(t)
	call := func(method, path, body string) {
		t.Helper()
		r := httptest.NewRequest(method, "https://sim.local"+path, strings.NewReader(body))
		signSeedControlPlane(r)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, r)
		if rec.Code >= 300 {
			t.Fatalf("%s %s: %d %s", method, path, rec.Code, rec.Body.String())
		}
	}
	call(http.MethodPut, "/recovered-batch", "")
	call(http.MethodPut, "/recovered-batch/one.txt", "one")
	call(http.MethodPut, "/recovered-batch/two.txt", "two")

	var operation s3ControlXMLNode
	if err := xml.Unmarshal([]byte(`<Operation><S3PutObjectTagging><TagSet><member>`+
		`<Key>reviewed</Key><Value>yes</Value></member></TagSet></S3PutObjectTagging></Operation>`), &operation); err != nil {
		t.Fatalf("parse the operation: %v", err)
	}
	const account, jobID = "123456789012", "11111111-2222-4333-8444-555555555555"
	s3BatchJobs.Put(s3AccessPointKey(account, jobID), S3BatchJob{
		AccountID: account, JobID: jobID, Status: s3BatchJobActive, Priority: 1,
		RoleArn: "arn:aws:iam::" + account + ":role/batch", Operation: operation,
		Tasks: []S3BatchTask{
			{TaskID: "task-one", Bucket: "recovered-batch", Key: "one.txt", Status: s3BatchTaskSucceeded,
				Attempts: 1, HTTPStatus: http.StatusOK, ResultMessage: "Successful"},
			{TaskID: "task-two", Bucket: "recovered-batch", Key: "two.txt"},
		},
	})

	s3RecoverBatchJobs()

	// The job's status is the only view of its progress, as DescribeJob's is.
	var job S3BatchJob
	require.Eventually(t, func() bool {
		job, _ = s3BatchJobs.Get(s3AccessPointKey(account, jobID))
		return job.Status == s3BatchJobComplete || job.Status == s3BatchJobFailed
	}, 30*time.Second, 10*time.Millisecond, "the recovered job never settled")
	if job.Status != s3BatchJobComplete {
		t.Fatalf("status = %s (failures %v), want Complete", job.Status, job.FailureReasons)
	}
	if total, succeeded, failed := job.progress(); total != 2 || succeeded != 2 || failed != 0 {
		t.Fatalf("progress = %d total, %d succeeded, %d failed; want 2, 2, 0", total, succeeded, failed)
	}
	if job.TerminationDate == "" {
		t.Error("a complete job reports when it terminated")
	}
	if tags, _ := s3ObjectTags.Get("recovered-batch/two.txt"); tags["reviewed"] != "yes" {
		t.Errorf("two.txt tags = %v: the resumed job ran the task left", tags)
	}
	if tags, _ := s3ObjectTags.Get("recovered-batch/one.txt"); len(tags) != 0 {
		t.Errorf("one.txt tags = %v: the resumed job reran a task the previous process recorded", tags)
	}
}
