package gcp_cli_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gcloudSchedulerJob struct {
	Name         string `json:"name"`
	Schedule     string `json:"schedule"`
	TimeZone     string `json:"timeZone"`
	State        string `json:"state"`
	PubsubTarget struct {
		TopicName  string            `json:"topicName"`
		Data       string            `json:"data"`
		Attributes map[string]string `json:"attributes"`
	} `json:"pubsubTarget"`
	HTTPTarget struct {
		URI        string `json:"uri"`
		HTTPMethod string `json:"httpMethod"`
	} `json:"httpTarget"`
	RetryConfig struct {
		RetryCount         int    `json:"retryCount"`
		MinBackoffDuration string `json:"minBackoffDuration"`
	} `json:"retryConfig"`
}

func describeSchedulerJob(t *testing.T, id string) gcloudSchedulerJob {
	t.Helper()
	var job gcloudSchedulerJob
	parseJSON(t, runCLI(t, gcloudCLI("scheduler", "jobs", "describe", id, "--location="+location, "--format=json")), &job)
	return job
}

// TestCLI_SchedulerPubsubJobLifecycle drives a Pub/Sub job through gcloud
// scheduler jobs: create, describe, update, pause, resume, run, list and
// delete; the run publishes the job's message to its topic.
func TestCLI_SchedulerPubsubJobLifecycle(t *testing.T) {
	t.Parallel()
	topic, sub, id := uniqueName("cli-sched-topic"), uniqueName("cli-sched-sub"), uniqueName("cli-sched-pubsub")
	runCLI(t, gcloudCLI("pubsub", "topics", "create", topic, "--quiet"))
	t.Cleanup(func() { _ = gcloudCLI("pubsub", "topics", "delete", topic, "--quiet").Run() })
	runCLI(t, gcloudCLI("pubsub", "subscriptions", "create", sub, "--topic="+topic, "--quiet"))
	t.Cleanup(func() { _ = gcloudCLI("pubsub", "subscriptions", "delete", sub, "--quiet").Run() })

	runCLI(t, gcloudCLI("scheduler", "jobs", "create", "pubsub", id, "--location="+location,
		"--schedule=0 9 * * 1", "--time-zone=America/New_York", "--topic="+topic,
		"--message-body=weekly", "--attributes=kind=report", "--max-retry-attempts=2", "--min-backoff=10s", "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("scheduler", "jobs", "delete", id, "--location="+location, "--quiet").Run()
	})
	job := describeSchedulerJob(t, id)
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/jobs/"+id, job.Name)
	assert.Equal(t, "ENABLED", job.State)
	assert.Equal(t, "0 9 * * 1", job.Schedule)
	assert.Equal(t, "America/New_York", job.TimeZone)
	assert.Equal(t, "projects/"+project+"/topics/"+topic, job.PubsubTarget.TopicName)
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("weekly")), job.PubsubTarget.Data)
	assert.Equal(t, "report", job.PubsubTarget.Attributes["kind"])
	assert.Equal(t, 2, job.RetryConfig.RetryCount)
	assert.Equal(t, "10s", job.RetryConfig.MinBackoffDuration)

	runCLI(t, gcloudCLI("scheduler", "jobs", "update", "pubsub", id, "--location="+location,
		"--schedule=30 8 * * *", "--quiet"))
	assert.Equal(t, "30 8 * * *", describeSchedulerJob(t, id).Schedule)

	runCLI(t, gcloudCLI("scheduler", "jobs", "pause", id, "--location="+location, "--quiet"))
	assert.Equal(t, "PAUSED", describeSchedulerJob(t, id).State)
	runCLI(t, gcloudCLI("scheduler", "jobs", "resume", id, "--location="+location, "--quiet"))
	assert.Equal(t, "ENABLED", describeSchedulerJob(t, id).State)

	runCLI(t, gcloudCLI("scheduler", "jobs", "run", id, "--location="+location, "--quiet"))
	var pulled []gcloudPulledMessage
	require.NoError(t, json.Unmarshal([]byte(runCLI(t, gcloudCLI("pubsub", "subscriptions", "pull", sub,
		"--limit=1", "--auto-ack", "--format=json"))), &pulled))
	require.Len(t, pulled, 1, "the run publishes the job's message")
	data, err := base64.StdEncoding.DecodeString(pulled[0].Message.Data)
	require.NoError(t, err)
	assert.Equal(t, "weekly", string(data))

	var listed []gcloudSchedulerJob
	parseJSON(t, runCLI(t, gcloudCLI("scheduler", "jobs", "list", "--location="+location, "--format=json")), &listed)
	var names []string
	for _, j := range listed {
		names = append(names, j.Name)
	}
	assert.Contains(t, names, job.Name)

	runCLI(t, gcloudCLI("scheduler", "jobs", "delete", id, "--location="+location, "--quiet"))
	_, err = gcloudCLI("scheduler", "jobs", "describe", id, "--location="+location).Output()
	require.Error(t, err, "a deleted job is not found")
}

// TestCLI_SchedulerCmekConfigIsDeclaredUnserved reads the location's CMEK
// config with gcloud scheduler cmek-config describe: it answers the declared
// 501, since no job here is encrypted with a customer-managed key. gcloud
// retries a 5xx answer before it gives up.
func TestCLI_SchedulerCmekConfigIsDeclaredUnserved(t *testing.T) {
	t.Parallel()
	out, err := gcloudCLI("scheduler", "cmek-config", "describe", "--location="+location).CombinedOutput()
	require.Error(t, err, "gcloud scheduler cmek-config describe succeeded: %s", out)
	assert.Contains(t, string(out), "customer-managed", "%s", out)
}

// TestCLI_SchedulerHTTPJobRuns creates an HTTP job with gcloud and runs it:
// the endpoint receives the job's method, headers and body.
func TestCLI_SchedulerHTTPJobRuns(t *testing.T) {
	t.Parallel()
	type received struct {
		method, body, agent, custom string
	}
	requests := make(chan received, 4)
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- received{r.Method, string(body), r.Header.Get("User-Agent"), r.Header.Get("X-Release")}
	}))
	t.Cleanup(endpoint.Close)

	id := uniqueName("cli-sched-http")
	runCLI(t, gcloudCLI("scheduler", "jobs", "create", "http", id, "--location="+location,
		"--schedule=*/5 * * * *", "--uri="+endpoint.URL+"/hook", "--http-method=put",
		"--headers=X-Release=blue", "--message-body=deploy", "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("scheduler", "jobs", "delete", id, "--location="+location, "--quiet").Run()
	})
	job := describeSchedulerJob(t, id)
	assert.Equal(t, endpoint.URL+"/hook", job.HTTPTarget.URI)
	assert.Equal(t, "PUT", job.HTTPTarget.HTTPMethod)

	runCLI(t, gcloudCLI("scheduler", "jobs", "run", id, "--location="+location, "--quiet"))
	select {
	case r := <-requests:
		assert.Equal(t, http.MethodPut, r.method)
		assert.Equal(t, "deploy", r.body)
		assert.Equal(t, "Google-Cloud-Scheduler", r.agent)
		assert.Equal(t, "blue", r.custom)
	case <-time.After(30 * time.Second):
		t.Fatal("the HTTP target received no request")
	}
}
