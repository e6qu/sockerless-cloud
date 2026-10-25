package gcp_cli_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type gcloudTasksQueue struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	RateLimits struct {
		MaxDispatchesPerSecond  float64 `json:"maxDispatchesPerSecond"`
		MaxBurstSize            int     `json:"maxBurstSize"`
		MaxConcurrentDispatches int     `json:"maxConcurrentDispatches"`
	} `json:"rateLimits"`
	RetryConfig struct {
		MaxAttempts      int    `json:"maxAttempts"`
		MinBackoff       string `json:"minBackoff"`
		MaxBackoff       string `json:"maxBackoff"`
		MaxDoublings     int    `json:"maxDoublings"`
		MaxRetryDuration string `json:"maxRetryDuration"`
	} `json:"retryConfig"`
	PurgeTime string `json:"purgeTime"`
}

type gcloudTask struct {
	Name          string `json:"name"`
	DispatchCount int    `json:"dispatchCount"`
	ScheduleTime  string `json:"scheduleTime"`
	HTTPRequest   struct {
		URL        string            `json:"url"`
		HTTPMethod string            `json:"httpMethod"`
		Headers    map[string]string `json:"headers"`
	} `json:"httpRequest"`
}

func describeTasksQueue(t *testing.T, id string) gcloudTasksQueue {
	t.Helper()
	var q gcloudTasksQueue
	parseJSON(t, runCLI(t, gcloudCLI("tasks", "queues", "describe", id, "--location="+location, "--format=json")), &q)
	return q
}

// TestCLI_TasksQueueLifecycle drives a queue through gcloud tasks queues:
// create with rate limits and retry parameters, describe, update, pause,
// resume, purge, list and delete.
func TestCLI_TasksQueueLifecycle(t *testing.T) {
	t.Parallel()
	id := uniquename.New("cli-tasks-queue")
	runCLI(t, gcloudCLI("tasks", "queues", "create", id, "--location="+location,
		"--max-dispatches-per-second=20", "--max-concurrent-dispatches=5",
		"--max-attempts=4", "--min-backoff=2s", "--max-backoff=60s", "--max-doublings=3", "--max-retry-duration=600s", "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("tasks", "queues", "delete", id, "--location="+location, "--quiet").Run()
	})
	q := describeTasksQueue(t, id)
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/queues/"+id, q.Name)
	assert.Equal(t, "RUNNING", q.State)
	assert.EqualValues(t, 20, q.RateLimits.MaxDispatchesPerSecond)
	assert.Equal(t, 20, q.RateLimits.MaxBurstSize)
	assert.Equal(t, 5, q.RateLimits.MaxConcurrentDispatches)
	assert.Equal(t, 4, q.RetryConfig.MaxAttempts)
	assert.Equal(t, "2s", q.RetryConfig.MinBackoff)
	assert.Equal(t, "60s", q.RetryConfig.MaxBackoff)
	assert.Equal(t, 3, q.RetryConfig.MaxDoublings)
	assert.Equal(t, "600s", q.RetryConfig.MaxRetryDuration)

	runCLI(t, gcloudCLI("tasks", "queues", "update", id, "--location="+location, "--max-attempts=9", "--quiet"))
	q = describeTasksQueue(t, id)
	assert.Equal(t, 9, q.RetryConfig.MaxAttempts)
	assert.Equal(t, 5, q.RateLimits.MaxConcurrentDispatches, "an update keeps what it does not name")

	runCLI(t, gcloudCLI("tasks", "queues", "pause", id, "--location="+location, "--quiet"))
	assert.Equal(t, "PAUSED", describeTasksQueue(t, id).State)
	runCLI(t, gcloudCLI("tasks", "create-http-task", "held", "--queue="+id, "--location="+location,
		"--url=https://hooks.example/held", "--quiet"))
	runCLI(t, gcloudCLI("tasks", "queues", "purge", id, "--location="+location, "--quiet"))
	assert.NotEmpty(t, describeTasksQueue(t, id).PurgeTime)
	var tasks []gcloudTask
	parseJSON(t, runCLI(t, gcloudCLI("tasks", "list", "--queue="+id, "--location="+location, "--format=json")), &tasks)
	assert.Empty(t, tasks, "purge deletes the queue's tasks")
	runCLI(t, gcloudCLI("tasks", "queues", "resume", id, "--location="+location, "--quiet"))
	assert.Equal(t, "RUNNING", describeTasksQueue(t, id).State)

	var listed []gcloudTasksQueue
	parseJSON(t, runCLI(t, gcloudCLI("tasks", "queues", "list", "--location="+location, "--format=json")), &listed)
	var names []string
	for _, each := range listed {
		names = append(names, each.Name)
	}
	assert.Contains(t, names, q.Name)

	runCLI(t, gcloudCLI("tasks", "queues", "delete", id, "--location="+location, "--quiet"))
	_, err := gcloudCLI("tasks", "queues", "describe", id, "--location="+location).Output()
	require.Error(t, err, "a deleted queue is not found")
}

// TestCLI_TasksCreateHTTPTaskDispatches creates HTTP tasks with gcloud tasks
// create-http-task: the target receives the task's method, headers and body
// with the X-CloudTasks headers; a task on a paused queue waits, describe and
// list show it, run forces it, and delete removes another.
func TestCLI_TasksCreateHTTPTaskDispatches(t *testing.T) {
	t.Parallel()
	type received struct {
		method, path, body, agent, queue, task, custom string
	}
	requests := make(chan received, 8)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- received{r.Method, r.URL.Path, string(body), r.Header.Get("User-Agent"),
			r.Header.Get("X-CloudTasks-QueueName"), r.Header.Get("X-CloudTasks-TaskName"), r.Header.Get("X-Release")}
	}))
	t.Cleanup(target.Close)
	await := func() received {
		t.Helper()
		select {
		case r := <-requests:
			return r
		case <-time.After(30 * time.Second):
			t.Fatal("the task's target received no request")
			return received{}
		}
	}

	queue := uniquename.New("cli-tasks-http")
	runCLI(t, gcloudCLI("tasks", "queues", "create", queue, "--location="+location, "--quiet"))
	t.Cleanup(func() {
		_ = gcloudCLI("tasks", "queues", "delete", queue, "--location="+location, "--quiet").Run()
	})
	runCLI(t, gcloudCLI("tasks", "create-http-task", "deploy-1", "--queue="+queue, "--location="+location,
		"--url="+target.URL+"/deploy", "--method=PUT", "--header=X-Release: blue", "--body-content=release", "--quiet"))
	got := await()
	assert.Equal(t, http.MethodPut, got.method)
	assert.Equal(t, "/deploy", got.path)
	assert.Equal(t, "release", got.body)
	assert.Equal(t, "blue", got.custom)
	assert.Equal(t, "Google-Cloud-Tasks", got.agent)
	assert.Equal(t, queue, got.queue)
	assert.Equal(t, "deploy-1", got.task)

	runCLI(t, gcloudCLI("tasks", "queues", "pause", queue, "--location="+location, "--quiet"))
	runCLI(t, gcloudCLI("tasks", "create-http-task", "forced", "--queue="+queue, "--location="+location,
		"--url="+target.URL+"/forced", "--quiet"))
	runCLI(t, gcloudCLI("tasks", "create-http-task", "dropped", "--queue="+queue, "--location="+location,
		"--url="+target.URL+"/dropped", "--quiet"))
	var task gcloudTask
	parseJSON(t, runCLI(t, gcloudCLI("tasks", "describe", "forced", "--queue="+queue, "--location="+location, "--format=json")), &task)
	assert.Equal(t, "projects/"+project+"/locations/"+location+"/queues/"+queue+"/tasks/forced", task.Name)
	assert.Zero(t, task.DispatchCount, "a paused queue dispatches nothing")
	assert.Equal(t, "POST", task.HTTPRequest.HTTPMethod)
	var tasks []gcloudTask
	parseJSON(t, runCLI(t, gcloudCLI("tasks", "list", "--queue="+queue, "--location="+location, "--format=json")), &tasks)
	assert.Len(t, tasks, 2)

	runCLI(t, gcloudCLI("tasks", "run", "forced", "--queue="+queue, "--location="+location, "--quiet"))
	assert.Equal(t, "/forced", await().path, "run dispatches on a paused queue")
	runCLI(t, gcloudCLI("tasks", "delete", "dropped", "--queue="+queue, "--location="+location, "--quiet"))
	_, err := gcloudCLI("tasks", "describe", "dropped", "--queue="+queue, "--location="+location).Output()
	require.Error(t, err, "a deleted task is not found")
	out, err := gcloudCLI("tasks", "create-http-task", "dropped", "--queue="+queue, "--location="+location,
		"--url="+target.URL+"/dropped", "--quiet").CombinedOutput()
	require.Error(t, err, "a deleted task's name was reused: %s", out)
}

// TestCLI_TasksCmekConfigIsDeclaredUnserved reads the location's CMEK config
// with gcloud tasks cmek-config describe: it answers the declared 501, since
// no queue here is encrypted with a customer-managed key. gcloud retries a
// 5xx answer before it gives up.
func TestCLI_TasksCmekConfigIsDeclaredUnserved(t *testing.T) {
	t.Parallel()
	out, err := gcloudCLI("tasks", "cmek-config", "describe", "--location="+location).CombinedOutput()
	require.Error(t, err, "gcloud tasks cmek-config describe succeeded: %s", out)
	assert.Contains(t, string(out), "customer-managed", "%s", out)
}
