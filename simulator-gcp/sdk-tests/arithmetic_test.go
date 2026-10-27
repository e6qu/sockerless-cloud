package gcp_sdk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/logging/logadmin"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

func TestCloudRun_JobArithmetic(t *testing.T) {
	t.Parallel()
	jobID := uniquename.New("arith-crj")
	run := createAndRunJobWithImageAndCommand(t, jobID, evalImageName, []string{"(10 + 5) * 2"}, "10s")

	exec, err := waitJobRun(t, run)
	require.NoError(t, err)
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])

	// The container's own arithmetic reaches Cloud Logging.
	waitForJobLogs(t, jobID, func(logs string) bool {
		return strings.Contains(logs, "Result: 30")
	})
}

func TestCloudRun_JobArithmeticInvalid(t *testing.T) {
	t.Parallel()
	run := createAndRunJobWithImageAndCommand(t, uniquename.New("arith-crj-fail"), evalImageName, []string{"3 +"}, "10s")

	exec, err := waitJobRun(t, run)
	require.Error(t, err, "the execution fails, and so does its RunJob operation")
	assert.Equal(t, float64(1), exec["failedCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
}

func TestCloudRun_JobArithmeticLogs(t *testing.T) {
	t.Parallel()
	jobID := uniquename.New("arith-crj-logs")
	_ = createAndRunJobWithImageAndCommand(t, jobID, evalImageName, []string{"10 / 3"}, "10s")

	// Both the result and the parsing line the container printed are ingested.
	waitForJobLogs(t, jobID, func(logs string) bool {
		return strings.Contains(logs, "3.333") && strings.Contains(logs, "Parsing expression:")
	})
}

// A workload container must start, run and have its output ingested before
// the entries a test waits for exist.
const jobLogWaitTimeout = 90 * time.Second

type jobLogEntry struct {
	insertID     string
	resourceType string
	jobName      string
	labels       map[string]string
	timestamp    time.Time
	message      string
}

// jobLogFilter selects the job's own run.googleapis.com logs: its Cloud Audit
// Logs entries name the same monitored resource.
func jobLogFilter(jobName string) string {
	return fmt.Sprintf(`logName:"run.googleapis.com" AND resource.type="cloud_run_job" AND resource.labels.job_name=%q`, jobName)
}

// readJobLogEntries fails the test on a read failure: answering with the
// entries read so far would let a caller match an empty stream against an
// empty stream.
func readJobLogEntries(t *testing.T, client *logadmin.Client, jobName string) []jobLogEntry {
	t.Helper()
	it := client.Entries(ctx, logadmin.Filter(jobLogFilter(jobName)))
	var entries []jobLogEntry
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			return entries
		}
		require.NoError(t, err, "read the Cloud Logging entries of job %q", jobName)
		record := jobLogEntry{insertID: entry.InsertID, labels: entry.Labels, timestamp: entry.Timestamp}
		if entry.Resource != nil {
			record.resourceType = entry.Resource.Type
			record.jobName = entry.Resource.Labels["job_name"]
		}
		if text, ok := entry.Payload.(string); ok {
			record.message = text
		}
		entries = append(entries, record)
	}
}

// waitForJobLogEntries opens a TailLogEntries stream, waits for it to stream,
// then lists what the job already logged, so an entry written between the two
// reads arrives on one of them; entries both report count once, by insert ID.
func waitForJobLogEntries(t *testing.T, jobName string, match func([]jobLogEntry) bool) []jobLogEntry {
	t.Helper()
	return waitForProjectJobLogEntries(t, "test-project", jobName, match)
}

func waitForProjectJobLogEntries(t *testing.T, project, jobName string, match func([]jobLogEntry) bool) []jobLogEntry {
	t.Helper()
	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, early := openReadyTail(t, tailCtx, newLoggingV2Client(t), project, jobLogFilter(jobName))

	seen := map[string]bool{}
	var entries []jobLogEntry
	add := func(e jobLogEntry) {
		if !seen[e.insertID] {
			seen[e.insertID] = true
			entries = append(entries, e)
		}
	}
	addProto := func(pe *loggingpb.LogEntry) {
		add(jobLogEntry{
			insertID:     pe.GetInsertId(),
			resourceType: pe.GetResource().GetType(),
			jobName:      pe.GetResource().GetLabels()["job_name"],
			labels:       pe.GetLabels(),
			timestamp:    pe.GetTimestamp().AsTime(),
			message:      pe.GetTextPayload(),
		})
	}
	for _, pe := range early {
		addProto(pe)
	}
	for _, e := range readJobLogEntries(t, logadminClientFor(t, project), jobName) {
		add(e)
	}
	for !match(entries) {
		resp, err := stream.Recv()
		require.NoError(t, err, "the Cloud Logging entries of job %q never matched within %s: %q",
			jobName, jobLogWaitTimeout, jobLogMessages(entries))
		for _, pe := range resp.GetEntries() {
			addProto(pe)
		}
	}
	return entries
}

// waitForJobLogMessage waits for exactly the given line of the container's own
// stdout, which proves the workload really started.
func waitForJobLogMessage(t *testing.T, jobName, message string) {
	t.Helper()
	waitForJobLogEntries(t, jobName, func(entries []jobLogEntry) bool {
		return containsString(jobLogMessages(entries), message)
	})
}

func jobLogMessages(entries []jobLogEntry) []string {
	messages := make([]string, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entry.message)
	}
	return messages
}

func jobLogs(t *testing.T, jobName string) string {
	t.Helper()
	return strings.Join(jobLogMessages(readJobLogEntries(t, logadminClient(t), jobName)), "\n")
}

func waitForJobLogs(t *testing.T, jobName string, match func(string) bool) string {
	t.Helper()
	entries := waitForJobLogEntries(t, jobName, func(entries []jobLogEntry) bool {
		return match(strings.Join(jobLogMessages(entries), "\n"))
	})
	return strings.Join(jobLogMessages(entries), "\n")
}
