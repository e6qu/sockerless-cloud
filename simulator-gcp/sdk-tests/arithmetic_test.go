package gcp_sdk_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/logging/logadmin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
)

func TestCloudRun_JobArithmetic(t *testing.T) {
	execName := createAndRunJobWithImageAndCommand(t, "arith-crj", evalImageName, []string{"(10 + 5) * 2"}, "10s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])

	// The container's own arithmetic reaches Cloud Logging.
	waitForJobLogs(t, "arith-crj", func(logs string) bool {
		return strings.Contains(logs, "Result: 30")
	})
}

func TestCloudRun_JobArithmeticInvalid(t *testing.T) {
	execName := createAndRunJobWithImageAndCommand(t, "arith-crj-fail", evalImageName, []string{"3 +"}, "10s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), exec["failedCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
}

func TestCloudRun_JobArithmeticLogs(t *testing.T) {
	_ = createAndRunJobWithImageAndCommand(t, "arith-crj-logs", evalImageName, []string{"10 / 3"}, "10s")

	// Both the result and the parsing line the container printed are ingested.
	waitForJobLogs(t, "arith-crj-logs", func(logs string) bool {
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
	resourceType string
	jobName      string
	message      string
}

// readJobLogEntries returns the Cloud Logging entries a Cloud Run job has
// produced, in ingestion order. A read failure fails the test: answering with
// the entries read so far would let a caller compare an empty log stream
// against an empty log stream and read that as a match.
func readJobLogEntries(t *testing.T, client *logadmin.Client, jobName string) []jobLogEntry {
	t.Helper()
	filter := fmt.Sprintf(`resource.type="cloud_run_job" AND resource.labels.job_name=%q`, jobName)
	it := client.Entries(ctx, logadmin.Filter(filter))
	var entries []jobLogEntry
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			return entries
		}
		require.NoError(t, err, "read the Cloud Logging entries of job %q", jobName)
		record := jobLogEntry{}
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

// waitForJobLogEntries polls the Cloud Logging entries of a Cloud Run job until
// match accepts them, and returns the accepted entries. The poll runs on the
// calling goroutine, so a failed read fails the test with the read error
// instead of being retried until the deadline expires.
func waitForJobLogEntries(t *testing.T, jobName string, match func([]jobLogEntry) bool) []jobLogEntry {
	t.Helper()
	client := logadminClient(t)
	deadline := time.Now().Add(jobLogWaitTimeout)
	for {
		entries := readJobLogEntries(t, client, jobName)
		if match(entries) {
			return entries
		}
		if time.Now().After(deadline) {
			t.Fatalf("the Cloud Logging entries of job %q never matched within %s: %q",
				jobName, jobLogWaitTimeout, jobLogMessages(entries))
		}
		time.Sleep(200 * time.Millisecond)
	}
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
