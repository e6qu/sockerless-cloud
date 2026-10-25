package gcp_sdk_test

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	cloudtasks "cloud.google.com/go/cloudtasks/apiv2"
	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudtasksapi "google.golang.org/api/cloudtasks/v2"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func tasksClient(t *testing.T) *cloudtasks.Client {
	t.Helper()
	c, err := cloudtasks.NewRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

const tasksParent = "projects/" + harnessProject + "/locations/us-central1"

type taskRequest struct {
	method string
	path   string
	header http.Header
	body   string
	at     time.Time
}

// taskTarget answers each request with the next status of statuses (the last
// one repeating) and hands every request it received to the test. While hold
// is set, each answer waits until the test sends on it.
func taskTarget(t *testing.T, hold chan struct{}, statuses ...int) (*httptest.Server, <-chan taskRequest) {
	t.Helper()
	received := make(chan taskRequest, 32)
	var n atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- taskRequest{method: r.Method, path: r.URL.RequestURI(), header: r.Header.Clone(), body: string(body), at: time.Now()}
		if hold != nil {
			<-hold
		}
		i := int(n.Add(1)) - 1
		w.WriteHeader(statuses[min(i, len(statuses)-1)])
	}))
	t.Cleanup(server.Close)
	return server, received
}

func awaitTaskRequest(t *testing.T, received <-chan taskRequest) taskRequest {
	t.Helper()
	select {
	case r := <-received:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("the task's target received no request")
		return taskRequest{}
	}
}

func createTasksQueue(t *testing.T, c *cloudtasks.Client, queue *cloudtaskspb.Queue) *cloudtaskspb.Queue {
	t.Helper()
	created, err := c.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: tasksParent, Queue: queue})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteQueue(ctx, &cloudtaskspb.DeleteQueueRequest{Name: queue.GetName()}) })
	return created
}

func httpTask(url string) *cloudtaskspb.Task {
	return &cloudtaskspb.Task{MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: url}}}
}

// awaitTaskGone reads a task until Cloud Tasks deletes it after its last
// attempt: the API offers no wait on a task.
func awaitTaskGone(t *testing.T, c *cloudtasks.Client, name string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		_, err := c.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: name})
		var apiErr *googleapi.Error
		if errors.As(err, &apiErr) && apiErr.Code == http.StatusNotFound {
			return
		}
		require.True(t, time.Now().Before(deadline), "the task %s was never deleted: %v", name, err)
		time.Sleep(200 * time.Millisecond)
	}
}

// TestCloudTasks_QueueLifecycle creates a queue, reads it with Cloud Tasks'
// defaults filled in, updates it through a field mask, pauses and resumes it,
// sets its IAM policy, lists and deletes it.
func TestCloudTasks_QueueLifecycle(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	name := tasksParent + "/queues/" + uniquename.New("sdk-queue")
	created := createTasksQueue(t, c, &cloudtaskspb.Queue{
		Name:        name,
		RateLimits:  &cloudtaskspb.RateLimits{MaxDispatchesPerSecond: 10},
		RetryConfig: &cloudtaskspb.RetryConfig{MaxAttempts: 7, MinBackoff: durationpb.New(2 * time.Second)},
	})
	assert.Equal(t, cloudtaskspb.Queue_RUNNING, created.GetState())
	assert.EqualValues(t, 10, created.GetRateLimits().GetMaxDispatchesPerSecond())
	assert.EqualValues(t, 10, created.GetRateLimits().GetMaxBurstSize())
	assert.EqualValues(t, 1000, created.GetRateLimits().GetMaxConcurrentDispatches(), "an unset field takes its default")
	assert.EqualValues(t, 7, created.GetRetryConfig().GetMaxAttempts())
	assert.Equal(t, 2*time.Second, created.GetRetryConfig().GetMinBackoff().AsDuration())
	assert.Equal(t, time.Hour, created.GetRetryConfig().GetMaxBackoff().AsDuration())
	assert.EqualValues(t, 16, created.GetRetryConfig().GetMaxDoublings())
	assert.Nil(t, created.GetRetryConfig().GetMaxRetryDuration(), "an unlimited duration is omitted")

	_, err := c.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: tasksParent, Queue: &cloudtaskspb.Queue{Name: name}})
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code)

	updated, err := c.UpdateQueue(ctx, &cloudtaskspb.UpdateQueueRequest{
		Queue: &cloudtaskspb.Queue{Name: name, RateLimits: &cloudtaskspb.RateLimits{MaxConcurrentDispatches: 3},
			RetryConfig: &cloudtaskspb.RetryConfig{MaxAttempts: 1}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"rate_limits.max_concurrent_dispatches"}},
	})
	require.NoError(t, err)
	assert.EqualValues(t, 3, updated.GetRateLimits().GetMaxConcurrentDispatches())
	assert.EqualValues(t, 10, updated.GetRateLimits().GetMaxDispatchesPerSecond(), "a field outside the mask keeps its value")
	assert.EqualValues(t, 7, updated.GetRetryConfig().GetMaxAttempts(), "a field outside the mask keeps its value")

	paused, err := c.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, cloudtaskspb.Queue_PAUSED, paused.GetState())
	resumed, err := c.ResumeQueue(ctx, &cloudtaskspb.ResumeQueueRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, cloudtaskspb.Queue_RUNNING, resumed.GetState())

	policy, err := c.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: name})
	require.NoError(t, err)
	policy.Bindings = []*iampb.Binding{{Role: "roles/cloudtasks.enqueuer", Members: []string{"serviceAccount:enqueuer@" + harnessProject + ".iam.gserviceaccount.com"}}}
	set, err := c.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: name, Policy: policy})
	require.NoError(t, err)
	require.Len(t, set.GetBindings(), 1)
	read, err := c.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: name})
	require.NoError(t, err)
	assert.Equal(t, "roles/cloudtasks.enqueuer", read.GetBindings()[0].GetRole())
	tested, err := c.TestIamPermissions(ctx, &iampb.TestIamPermissionsRequest{Resource: name, Permissions: []string{"cloudtasks.tasks.create"}})
	require.NoError(t, err)
	assert.Equal(t, []string{"cloudtasks.tasks.create"}, tested.GetPermissions(), "the account's owner holds what it asks about")

	found := false
	it := c.ListQueues(ctx, &cloudtaskspb.ListQueuesRequest{Parent: tasksParent})
	for {
		q, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		found = found || q.GetName() == name
	}
	assert.True(t, found, "ListQueues names the queue")

	got, err := c.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: name})
	require.NoError(t, err)
	assert.EqualValues(t, 3, got.GetRateLimits().GetMaxConcurrentDispatches())
	require.NoError(t, c.DeleteQueue(ctx, &cloudtaskspb.DeleteQueueRequest{Name: name}))
	_, err = c.GetQueue(ctx, &cloudtaskspb.GetQueueRequest{Name: name})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

// TestCloudTasks_HTTPTaskRetriesWithBackoff creates an HTTP task whose target
// fails twice: Cloud Tasks sends the task's method, headers, body and an OIDC
// token with the X-CloudTasks headers, retries after the queue's backoff, and
// deletes the task once the target accepts it.
func TestCloudTasks_HTTPTaskRetriesWithBackoff(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	target, received := taskTarget(t, nil, http.StatusTooManyRequests, http.StatusInternalServerError, http.StatusNoContent)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-retry")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue, RetryConfig: &cloudtaskspb.RetryConfig{
		MaxAttempts: 5, MinBackoff: durationpb.New(500 * time.Millisecond), MaxBackoff: durationpb.New(5 * time.Second), MaxDoublings: 1,
	}})
	sa := "invoker@" + harnessProject + ".iam.gserviceaccount.com"
	name := queue + "/tasks/" + uniquename.New("order")
	created, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, ResponseView: cloudtaskspb.Task_FULL, Task: &cloudtaskspb.Task{
		Name: name,
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{
			Url:                 target.URL + "/orders?id=7",
			HttpMethod:          cloudtaskspb.HttpMethod_PUT,
			Headers:             map[string]string{"Content-Type": "application/json", "X-Team": "fulfilment"},
			Body:                []byte(`{"order":7}`),
			AuthorizationHeader: &cloudtaskspb.HttpRequest_OidcToken{OidcToken: &cloudtaskspb.OidcToken{ServiceAccountEmail: sa}},
		}},
	}})
	require.NoError(t, err)
	assert.Equal(t, name, created.GetName())
	assert.Equal(t, cloudtaskspb.Task_FULL, created.GetView())
	assert.Equal(t, 10*time.Minute, created.GetDispatchDeadline().AsDuration(), "an HTTP task's default deadline is 10 minutes")
	assert.Equal(t, []byte(`{"order":7}`), created.GetHttpRequest().GetBody())

	first := awaitTaskRequest(t, received)
	assert.Equal(t, http.MethodPut, first.method)
	assert.Equal(t, "/orders?id=7", first.path)
	assert.Equal(t, `{"order":7}`, first.body)
	assert.Equal(t, "fulfilment", first.header.Get("X-Team"))
	assert.Equal(t, "Google-Cloud-Tasks", first.header.Get("User-Agent"))
	assert.Equal(t, queue[strings.LastIndex(queue, "/")+1:], first.header.Get("X-CloudTasks-QueueName"))
	assert.Equal(t, name[strings.LastIndex(name, "/")+1:], first.header.Get("X-CloudTasks-TaskName"))
	assert.Equal(t, "0", first.header.Get("X-CloudTasks-TaskRetryCount"))
	assert.NotEmpty(t, first.header.Get("X-CloudTasks-TaskETA"))
	parts := strings.Split(strings.TrimPrefix(first.header.Get("Authorization"), "Bearer "), ".")
	require.Len(t, parts, 3, "the target receives an ID token")
	claims, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var token struct {
		Aud   string `json:"aud"`
		Email string `json:"email"`
	}
	require.NoError(t, json.Unmarshal(claims, &token))
	assert.Equal(t, target.URL+"/orders?id=7", token.Aud, "the audience defaults to the task's URL")
	assert.Equal(t, sa, token.Email)

	second := awaitTaskRequest(t, received)
	assert.GreaterOrEqual(t, second.at.Sub(first.at), 500*time.Millisecond, "the first retry waits minBackoff")
	assert.Equal(t, "1", second.header.Get("X-CloudTasks-TaskRetryCount"))
	assert.Equal(t, "1", second.header.Get("X-CloudTasks-TaskExecutionCount"))
	assert.Equal(t, "429", second.header.Get("X-CloudTasks-TaskPreviousResponse"))
	third := awaitTaskRequest(t, received)
	assert.GreaterOrEqual(t, third.at.Sub(second.at), time.Second, "the second retry waits the doubled backoff")
	assert.Equal(t, "2", third.header.Get("X-CloudTasks-TaskRetryCount"))
	awaitTaskGone(t, c, name)

	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{Name: name,
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: target.URL}}}})
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code, "an executed task's name is not reused inside the deduplication window")
}

// TestCloudTasks_FailedAttemptsRecordTheirStatus fails a task with one
// attempt left: its counts and last attempt carry the answer until Cloud
// Tasks deletes it.
func TestCloudTasks_FailedAttemptsRecordTheirStatus(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	target, received := taskTarget(t, nil, http.StatusNotFound)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-failing")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue, RetryConfig: &cloudtaskspb.RetryConfig{
		MaxAttempts: 2, MinBackoff: durationpb.New(2 * time.Second), MaxBackoff: durationpb.New(2 * time.Second),
	}})
	created, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: httpTask(target.URL)})
	require.NoError(t, err)
	assert.Regexp(t, `/tasks/[0-9]+$`, created.GetName(), "an unnamed task gets a generated id")
	awaitTaskRequest(t, received)
	deadline := time.Now().Add(30 * time.Second)
	var task *cloudtaskspb.Task
	for {
		task, err = c.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: created.GetName()})
		require.NoError(t, err)
		if task.GetResponseCount() == 1 {
			break
		}
		require.True(t, time.Now().Before(deadline), "the first attempt's answer was never recorded: %v", task)
		time.Sleep(100 * time.Millisecond)
	}
	assert.EqualValues(t, 1, task.GetDispatchCount())
	assert.NotNil(t, task.GetFirstAttempt().GetDispatchTime())
	assert.EqualValues(t, 5, task.GetLastAttempt().GetResponseStatus().GetCode(), "a 404 records NOT_FOUND")
	assert.True(t, task.GetScheduleTime().AsTime().After(task.GetLastAttempt().GetDispatchTime().AsTime()), "the retry is scheduled after the attempt")
	awaitTaskRequest(t, received)
	awaitTaskGone(t, c, created.GetName())
}

// TestCloudTasks_RateLimitsSpaceDispatches resumes a queue holding four
// tasks: a burst of maxBurstSize goes out at once and the rest follow at
// maxDispatchesPerSecond, never more than maxConcurrentDispatches at a time.
func TestCloudTasks_RateLimitsSpaceDispatches(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	target, received := taskTarget(t, nil, http.StatusOK)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-rate")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue, RateLimits: &cloudtaskspb.RateLimits{MaxDispatchesPerSecond: 2}})
	_, err := c.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: queue})
	require.NoError(t, err)
	for range 4 {
		_, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: httpTask(target.URL)})
		require.NoError(t, err)
	}
	start := time.Now()
	_, err = c.ResumeQueue(ctx, &cloudtaskspb.ResumeQueueRequest{Name: queue})
	require.NoError(t, err)
	var at []time.Duration
	for range 4 {
		at = append(at, awaitTaskRequest(t, received).at.Sub(start))
	}
	assert.Less(t, at[1], 400*time.Millisecond, "a burst of two goes out at once")
	assert.GreaterOrEqual(t, at[2], 400*time.Millisecond, "the third waits for a token")
	assert.GreaterOrEqual(t, at[3], 900*time.Millisecond, "the fourth waits for another")
}

// TestCloudTasks_ConcurrencyLimitHoldsTasks keeps one dispatch in flight on a
// queue that allows one: the next task waits until the first is answered.
func TestCloudTasks_ConcurrencyLimitHoldsTasks(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	hold := make(chan struct{})
	target, received := taskTarget(t, hold, http.StatusOK)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-serial")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue, RateLimits: &cloudtaskspb.RateLimits{MaxConcurrentDispatches: 1}})
	_, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: httpTask(target.URL + "/first")})
	require.NoError(t, err)
	first := awaitTaskRequest(t, received)
	second, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: httpTask(target.URL + "/second")})
	require.NoError(t, err)
	held, err := c.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: second.GetName()})
	require.NoError(t, err)
	assert.Zero(t, held.GetDispatchCount(), "a second task waits while the first is in flight")
	hold <- struct{}{}
	next := awaitTaskRequest(t, received)
	assert.Equal(t, "/first", first.path)
	assert.Equal(t, "/second", next.path)
	hold <- struct{}{}
}

// TestCloudTasks_PauseRunAndPurge holds tasks on a paused queue, forces one
// with RunTask, purges the rest, and keeps the purged names taken.
func TestCloudTasks_PauseRunAndPurge(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	target, received := taskTarget(t, nil, http.StatusOK)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-paused")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue})
	_, err := c.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: queue})
	require.NoError(t, err)
	forced := queue + "/tasks/forced"
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{Name: forced,
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: target.URL + "/forced"}}}})
	require.NoError(t, err)
	waiting := queue + "/tasks/waiting"
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{Name: waiting,
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: target.URL + "/waiting"}}}})
	require.NoError(t, err)
	held, err := c.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: waiting})
	require.NoError(t, err)
	assert.Zero(t, held.GetDispatchCount(), "a paused queue dispatches nothing")

	run, err := c.RunTask(ctx, &cloudtaskspb.RunTaskRequest{Name: forced})
	require.NoError(t, err)
	assert.EqualValues(t, 1, run.GetDispatchCount())
	assert.Equal(t, "/forced", awaitTaskRequest(t, received).path, "RunTask dispatches on a paused queue")

	var listed []string
	it := c.ListTasks(ctx, &cloudtaskspb.ListTasksRequest{Parent: queue})
	for {
		task, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		listed = append(listed, task.GetName())
	}
	assert.Contains(t, listed, waiting)

	purged, err := c.PurgeQueue(ctx, &cloudtaskspb.PurgeQueueRequest{Name: queue})
	require.NoError(t, err)
	assert.NotNil(t, purged.GetPurgeTime())
	_, err = c.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: waiting})
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code, "purge deletes the queue's tasks")
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{Name: waiting,
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: target.URL}}}})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code, "a purged task's name stays taken")
}

// TestCloudTasks_NamedTaskDeduplication creates a named task twice, deletes
// it, and creates it again: each re-creation is refused.
func TestCloudTasks_NamedTaskDeduplication(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-dedup")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue})
	_, err := c.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: queue})
	require.NoError(t, err)
	task := &cloudtaskspb.Task{Name: queue + "/tasks/" + uniquename.New("once"),
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: "https://hooks.example/once"}}}
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: task})
	require.NoError(t, err)
	var apiErr *googleapi.Error
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: task})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code, "a task's name is taken while it exists")
	require.NoError(t, c.DeleteTask(ctx, &cloudtaskspb.DeleteTaskRequest{Name: task.GetName()}))
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: task})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code, "a deleted task's name stays taken")
}

// TestCloudTasks_ScheduledTaskWaits creates a task scheduled ahead: it is
// dispatched no earlier, with its scheduled time in X-CloudTasks-TaskETA.
func TestCloudTasks_ScheduledTaskWaits(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	target, received := taskTarget(t, nil, http.StatusOK)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-later")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue})
	at := time.Now().Add(1500 * time.Millisecond).Truncate(time.Microsecond)
	task := httpTask(target.URL)
	task.ScheduleTime = timestamppb.New(at)
	created, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: task})
	require.NoError(t, err)
	assert.True(t, created.GetScheduleTime().AsTime().Equal(at))
	got := awaitTaskRequest(t, received)
	assert.False(t, got.at.Before(at.Truncate(time.Millisecond)), "dispatched at %v, before %v", got.at, at)
	eta := got.header.Get("X-CloudTasks-TaskETA")
	assert.True(t, strings.HasPrefix(eta, strconv.FormatInt(at.Unix(), 10)+"."), "X-CloudTasks-TaskETA = %s, scheduled %v", eta, at)
}

// TestCloudTasks_QueueRoutingOverridesTasks routes a queue's tasks to its
// httpTarget: the URI components, method and headers the queue names
// replace the task's.
func TestCloudTasks_QueueRoutingOverridesTasks(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	target, received := taskTarget(t, nil, http.StatusOK)
	host, portText, _ := strings.Cut(strings.TrimPrefix(target.URL, "http://"), ":")
	port, err := strconv.ParseInt(portText, 10, 64)
	require.NoError(t, err)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-routed")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue, HttpTarget: &cloudtaskspb.HttpTarget{
		UriOverride: &cloudtaskspb.UriOverride{
			Scheme: cloudtaskspb.UriOverride_HTTP.Enum(), Host: &host, Port: &port,
			PathOverride: &cloudtaskspb.PathOverride{Path: "/routed"},
		},
		HttpMethod:      cloudtaskspb.HttpMethod_PATCH,
		HeaderOverrides: []*cloudtaskspb.HttpTarget_HeaderOverride{{Header: &cloudtaskspb.HttpTarget_Header{Key: "X-Route", Value: "queue"}}},
	}})
	created, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: httpTask("https://unreachable.example/original")})
	require.NoError(t, err)
	assert.Equal(t, "queue", created.GetHttpRequest().GetHeaders()["X-Route"], "the task carries the queue's header override")
	got := awaitTaskRequest(t, received)
	assert.Equal(t, http.MethodPatch, got.method)
	assert.Equal(t, "/routed", got.path)
	assert.Equal(t, "queue", got.header.Get("X-Route"))
}

// TestCloudTasks_BatchCreateAndDelete creates and deletes tasks in batches:
// each answers a finished operation with every request's outcome.
func TestCloudTasks_BatchCreateAndDelete(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-batch")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue})
	_, err := c.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: queue})
	require.NoError(t, err)
	named := func(id string) *cloudtaskspb.CreateTaskRequest {
		return &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{Name: queue + "/tasks/" + id,
			MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: "https://hooks.example/"}}}}
	}
	op, err := c.BatchCreateTasks(ctx, &cloudtaskspb.BatchCreateTasksRequest{Parent: queue,
		Requests: []*cloudtaskspb.CreateTaskRequest{named("one"), named("two"), named("one")}})
	require.NoError(t, err)
	resp, err := op.Wait(ctx)
	require.NoError(t, err)
	assert.Len(t, resp.GetTasks(), 2)
	meta, err := op.Metadata()
	require.NoError(t, err)
	assert.Equal(t, cloudtaskspb.BatchCreateTasksMetadata_PARTIALLY_SUCCEEDED, meta.GetState())
	require.Contains(t, meta.GetFailedRequests(), int32(2))
	assert.EqualValues(t, 6, meta.GetFailedRequests()[2].GetCode(), "the repeated name is ALREADY_EXISTS")

	del, err := c.BatchDeleteTasks(ctx, &cloudtaskspb.BatchDeleteTasksRequest{Parent: queue,
		Names: []string{queue + "/tasks/one", queue + "/tasks/two"}})
	require.NoError(t, err)
	require.NoError(t, del.Wait(ctx))
	delMeta, err := del.Metadata()
	require.NoError(t, err)
	assert.Equal(t, cloudtaskspb.BatchDeleteTasksMetadata_SUCCEEDED, delMeta.GetState())
	_, err = c.GetTask(ctx, &cloudtaskspb.GetTaskRequest{Name: queue + "/tasks/one"})
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

// TestCloudTasks_RefusesWhatItDoesNotServe sends what the simulator cannot
// serve: an App Engine task, the CMEK config pair and BufferTask each answer
// a declared 501.
func TestCloudTasks_RefusesWhatItDoesNotServe(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-refused")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue})
	var apiErr *googleapi.Error
	_, err := c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{
		MessageType: &cloudtaskspb.Task_AppEngineHttpRequest{AppEngineHttpRequest: &cloudtaskspb.AppEngineHttpRequest{RelativeUri: "/work"}}}})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotImplemented, apiErr.Code)
	_, err = c.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{Url: "https://hooks.example/", HttpMethod: cloudtaskspb.HttpMethod_GET, Body: []byte("x")}}}})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusBadRequest, apiErr.Code, "a GET task cannot carry a body")

	svc, err := cloudtasksapi.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	_, err = svc.Projects.Locations.GetCmekConfig(tasksParent + "/cmekConfig").Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotImplemented, apiErr.Code)
	_, err = svc.Projects.Locations.UpdateCmekConfig(tasksParent+"/cmekConfig", &cloudtasksapi.CmekConfig{}).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotImplemented, apiErr.Code)
	_, err = svc.Projects.Locations.Queues.Tasks.Buffer(queue, "buffered", &cloudtasksapi.BufferTaskRequest{}).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotImplemented, apiErr.Code)
}

// TestCloudTasks_BatchVerbsThroughTheDiscoveryClient sends the batch verbs
// through the Discovery-generated client: each answers the finished
// operation, with the created tasks in batchCreate's response.
func TestCloudTasks_BatchVerbsThroughTheDiscoveryClient(t *testing.T) {
	t.Parallel()
	c := tasksClient(t)
	queue := tasksParent + "/queues/" + uniquename.New("sdk-apiary-batch")
	createTasksQueue(t, c, &cloudtaskspb.Queue{Name: queue})
	_, err := c.PauseQueue(ctx, &cloudtaskspb.PauseQueueRequest{Name: queue})
	require.NoError(t, err)
	svc, err := cloudtasksapi.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	name := queue + "/tasks/apiary"
	op, err := svc.Projects.Locations.Queues.Tasks.BatchCreate(queue, &cloudtasksapi.BatchCreateTasksRequest{
		Requests: []*cloudtasksapi.CreateTaskRequest{{Parent: queue, Task: &cloudtasksapi.Task{Name: name,
			HttpRequest: &cloudtasksapi.HttpRequest{Url: "https://hooks.example/apiary"}}}},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	var created struct {
		Tasks []struct {
			Name string `json:"name"`
		} `json:"tasks"`
	}
	require.NoError(t, json.Unmarshal(op.Response, &created))
	require.Len(t, created.Tasks, 1)
	assert.Equal(t, name, created.Tasks[0].Name)

	op, err = svc.Projects.Locations.Queues.Tasks.BatchDelete(queue, &cloudtasksapi.BatchDeleteTasksRequest{Names: []string{name}}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)
	assert.Nil(t, op.Error)
	_, err = svc.Projects.Locations.Queues.Tasks.Get(name).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}
