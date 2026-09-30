package gcp_sdk_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/durationpb"
)

func TestCloudRun_JobArithmetic(t *testing.T) {
	jobID := uniqueName("arith-crj")
	execName := createAndRunJobWithImageAndCommand(t, jobID, evalImageName, []string{"(10 + 5) * 2"}, "10s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])

	// The container's own arithmetic reaches Cloud Logging.
	waitForJobLogs(t, jobID, func(logs string) bool {
		return strings.Contains(logs, "Result: 30")
	})
}

func TestCloudRun_JobArithmeticInvalid(t *testing.T) {
	execName := createAndRunJobWithImageAndCommand(t, uniqueName("arith-crj-fail"), evalImageName, []string{"3 +"}, "10s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), exec["failedCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
}

func TestCloudRun_JobArithmeticLogs(t *testing.T) {
	jobID := uniqueName("arith-crj-logs")
	_ = createAndRunJobWithImageAndCommand(t, jobID, evalImageName, []string{"10 / 3"}, "10s")

	// Both the result and the parsing line the container printed are ingested.
	waitForJobLogs(t, jobID, func(logs string) bool {
		return strings.Contains(logs, "3.333") && strings.Contains(logs, "Parsing expression:")
	})
}

// jobLogWaitTimeout bounds every wait for a Cloud Run job's log stream. A
// workload container has to start, run and have its output ingested before the
// entries a test is waiting for exist, and all three take real time on a loaded
// runner.
const jobLogWaitTimeout = 90 * time.Second

// jobLogEntry is the part of a Cloud Logging entry the Cloud Run job tests
// assert on: the monitored resource that produced it and the text it carried.
type jobLogEntry struct {
	insertID     string
	resourceType string
	jobName      string
	message      string
}

func jobLogFilter(jobName string) string {
	return fmt.Sprintf(`resource.type="cloud_run_job" AND resource.labels.job_name=%q`, jobName)
}

// readJobLogEntries returns the Cloud Logging entries a Cloud Run job has
// produced, in ingestion order. A read failure fails the test: answering with
// the entries read so far would let a caller compare an empty log stream
// against an empty log stream and read that as a match.
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
		record := jobLogEntry{insertID: entry.InsertID}
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

// waitForJobLogEntries follows the Cloud Logging entries of a Cloud Run job
// until match accepts them, and returns the accepted entries. It opens a
// TailLogEntries stream first and then lists what the job already logged, so
// an entry written between the two reads arrives on one of them; entries both
// report are counted once, by insert ID.
func waitForJobLogEntries(t *testing.T, jobName string, match func([]jobLogEntry) bool) []jobLogEntry {
	t.Helper()
	return waitForProjectJobLogEntries(t, "test-project", jobName, match)
}

// waitForProjectJobLogEntries is waitForJobLogEntries for a job in project.
func waitForProjectJobLogEntries(t *testing.T, project, jobName string, match func([]jobLogEntry) bool) []jobLogEntry {
	t.Helper()
	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, err := newLoggingV2Client(t).TailLogEntries(tailCtx)
	require.NoError(t, err)
	require.NoError(t, stream.Send(&loggingpb.TailLogEntriesRequest{
		ResourceNames: []string{"projects/" + project},
		Filter:        jobLogFilter(jobName),
		BufferWindow:  durationpb.New(0),
	}))

	seen := map[string]bool{}
	var entries []jobLogEntry
	add := func(e jobLogEntry) {
		if !seen[e.insertID] {
			seen[e.insertID] = true
			entries = append(entries, e)
		}
	}
	for _, e := range readJobLogEntries(t, logadminClientFor(t, project), jobName) {
		add(e)
	}
	for !match(entries) {
		resp, err := stream.Recv()
		require.NoError(t, err, "the Cloud Logging entries of job %q never matched within %s: %q",
			jobName, jobLogWaitTimeout, jobLogMessages(entries))
		for _, pe := range resp.GetEntries() {
			add(jobLogEntry{
				insertID:     pe.GetInsertId(),
				resourceType: pe.GetResource().GetType(),
				jobName:      pe.GetResource().GetLabels()["job_name"],
				message:      pe.GetTextPayload(),
			})
		}
	}
	return entries
}

// waitForJobLogMessage waits until a Cloud Run job's container has emitted
// exactly the given line. The line is the container's own stdout, so its
// arrival is evidence the workload really started.
func waitForJobLogMessage(t *testing.T, jobName, message string) {
	t.Helper()
	waitForJobLogEntries(t, jobName, func(entries []jobLogEntry) bool {
		return containsString(jobLogMessages(entries), message)
	})
}

// jobLogMessages returns the text payloads of entries, in order.
func jobLogMessages(entries []jobLogEntry) []string {
	messages := make([]string, 0, len(entries))
	for _, entry := range entries {
		messages = append(messages, entry.message)
	}
	return messages
}

// jobLogs returns the joined Cloud Logging messages for a Cloud Run job.
func jobLogs(t *testing.T, jobName string) string {
	t.Helper()
	return strings.Join(jobLogMessages(readJobLogEntries(t, logadminClient(t), jobName)), "\n")
}

// waitForJobLogs polls a Cloud Run job's Cloud Logging messages, joined by
// newlines, until match accepts them, and returns them.
func waitForJobLogs(t *testing.T, jobName string, match func(string) bool) string {
	t.Helper()
	entries := waitForJobLogEntries(t, jobName, func(entries []jobLogEntry) bool {
		return match(strings.Join(jobLogMessages(entries), "\n"))
	})
	return strings.Join(jobLogMessages(entries), "\n")
}
