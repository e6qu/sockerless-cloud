package main

import (
	"cmp"
	"net/http"
	"slices"
	"strings"

	"github.com/e6qu/sockerless-cloud/sim"
)

// A job run's LogGroupName "can be /aws-glue/jobs/", and the model names
// "/aws-glue/jobs/output" as a log group a job writes.
const (
	glueJobsLogGroup       = "/aws-glue/jobs"
	glueJobsOutputLogGroup = glueJobsLogGroup + "/output"
	glueJobsErrorLogGroup  = glueJobsLogGroup + "/error"
)

func registerGlueSystemLogs(r *AWSRouter) {
	r.Register("AWSGlue.GetSystemLogsForJobRun", handleGlueGetSystemLogsForJobRun)
	r.Register("AWSGlue.GetSystemLogsForSession", handleGlueGetSystemLogsForSession)
}

// glueJobRunLogSink writes a job run's standard output to the output group and
// its standard error to the error group, each in a stream named by the run ID.
func glueJobRunLogSink(runID string) sim.LogSink {
	cwEnsureLogStream(glueJobsOutputLogGroup, runID)
	cwEnsureLogStream(glueJobsErrorLogGroup, runID)
	return sim.FuncSink(func(line sim.LogLine) {
		group := glueJobsOutputLogGroup
		if line.Stream == "stderr" {
			group = glueJobsErrorLogGroup
		}
		cwIngestWorkloadLogLine(group, runID, line.Text, line.Timestamp)
	})
}

func glueSystemLogsBucketName() string {
	return awsAccountID() + "-glue-system-logs"
}

// glueSystemLogsURL stores log under key in the bucket AWS Glue serves system
// logs from and returns a presigned URL that downloads it.
func glueSystemLogsURL(r *http.Request, key string, log []byte) (string, error) {
	bucket := s3EnsureServiceBucket(glueSystemLogsBucketName())
	if _, err := s3PutServiceObject(bucket, key, log, "text/plain", map[string]string{"aws-service": "glue"}); err != nil {
		return "", err
	}
	return presignedS3URLBase(awsRequestURLBase(r), bucket, key, http.MethodGet), nil
}

// glueJobRunSystemLog returns every line the run's process wrote to either log stream, in write order.
func glueJobRunSystemLog(runID string) []byte {
	var events []CWLogEvent
	for _, group := range []string{glueJobsOutputLogGroup, glueJobsErrorLogGroup} {
		stored, _ := cwStreamEvents(cwEventsKey(group, runID))
		events = append(events, stored...)
	}
	slices.SortStableFunc(events, func(a, b CWLogEvent) int { return cmp.Compare(a.Timestamp, b.Timestamp) })
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Message)
		b.WriteByte('\n')
	}
	return []byte(b.String())
}

func handleGlueGetSystemLogsForJobRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		JobName string `json:"JobName"`
		RunId   string `json:"RunId"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.JobName == "" || req.RunId == "" {
		glueWriteError(w, "InvalidInputException", "JobName and RunId are required")
		return
	}
	if _, ok := glueJobRuns.Get(req.JobName + "/" + req.RunId); !ok {
		glueWriteError(w, "EntityNotFoundException", "Job run not found: "+req.RunId)
		return
	}
	url, err := glueSystemLogsURL(r, "jobs/"+req.JobName+"/"+req.RunId+"/system.log", glueJobRunSystemLog(req.RunId))
	if err != nil {
		glueWriteError(w, "InternalServiceException", err.Error())
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"SystemLogsUrl": url})
}

func handleGlueGetSystemLogsForSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Id string `json:"Id"`
	}
	if err := sim.ReadJSON(r, &req); err != nil {
		glueWriteError(w, "InvalidInputException", "invalid JSON")
		return
	}
	if req.Id == "" {
		glueWriteError(w, "InvalidInputException", "Id is required")
		return
	}
	if _, ok := glueSessions.Get(req.Id); !ok {
		glueWriteError(w, "EntityNotFoundException", "Session not found: "+req.Id)
		return
	}
	// The simulator runs no process for an interactive session, so nothing
	// writes to its system log.
	url, err := glueSystemLogsURL(r, "sessions/"+req.Id+"/system.log", nil)
	if err != nil {
		glueWriteError(w, "InternalServiceException", err.Error())
		return
	}
	glueWriteJSON(w, http.StatusOK, map[string]any{"SystemLogsUrl": url})
}
