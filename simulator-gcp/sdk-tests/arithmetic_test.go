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

// --- Cloud Run Jobs container tests ---

func TestCloudRun_JobArithmetic(t *testing.T) {
	execName := createAndRunJobWithImageAndCommand(t, "arith-crj", evalImageName, []string{"(10 + 5) * 2"}, "10s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), exec["succeededCount"])
	assert.Equal(t, float64(0), exec["failedCount"])

	// Poll until the arithmetic result is ingested into Cloud Logging.
	require.Eventually(t, func() bool {
		return strings.Contains(jobLogs(t, "arith-crj"), "Result: 30")
	}, 60*time.Second, 200*time.Millisecond)
}

func TestCloudRun_JobArithmeticInvalid(t *testing.T) {
	execName := createAndRunJobWithImageAndCommand(t, "arith-crj-fail", evalImageName, []string{"3 +"}, "10s")

	exec := waitExecutionDone(t, execName)
	assert.Equal(t, float64(1), exec["failedCount"])
	assert.Equal(t, float64(0), exec["succeededCount"])
}

func TestCloudRun_JobArithmeticLogs(t *testing.T) {
	_ = createAndRunJobWithImageAndCommand(t, "arith-crj-logs", evalImageName, []string{"10 / 3"}, "10s")

	// Poll until both the result + parsing logs are ingested.
	require.Eventually(t, func() bool {
		l := jobLogs(t, "arith-crj-logs")
		return strings.Contains(l, "3.333") && strings.Contains(l, "Parsing expression:")
	}, 60*time.Second, 200*time.Millisecond)
}

// jobLogs returns the joined Cloud Logging messages for a Cloud Run job.
func jobLogs(t *testing.T, jobName string) string {
	t.Helper()
	client := logadminClient(t)
	filter := fmt.Sprintf(`resource.type="cloud_run_job" AND resource.labels.job_name=%q`, jobName)
	it := client.Entries(ctx, logadmin.Filter(filter))
	var messages []string
	for {
		entry, err := it.Next()
		if err == iterator.Done {
			break
		}
		if err != nil {
			return strings.Join(messages, "\n")
		}
		if s, ok := entry.Payload.(string); ok {
			messages = append(messages, s)
		}
	}
	return strings.Join(messages, "\n")
}
