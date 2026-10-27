package gcp_sdk_test

import (
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	scheduler "cloud.google.com/go/scheduler/apiv1"
	"cloud.google.com/go/scheduler/apiv1/schedulerpb"
	"github.com/e6qu/sockerless-cloud/testutil/uniquename"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	cloudscheduler "google.golang.org/api/cloudscheduler/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

func schedulerClient(t *testing.T) *scheduler.CloudSchedulerClient {
	t.Helper()
	c, err := scheduler.NewCloudSchedulerRESTClient(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() })
	return c
}

const schedulerParent = "projects/" + harnessProject + "/locations/us-central1"

type schedulerRequest struct {
	method string
	header http.Header
	body   string
}

// schedulerEndpoint answers each request with the next of statuses, repeating
// the last, and hands every request to the test.
func schedulerEndpoint(t *testing.T, statuses ...int) (*httptest.Server, <-chan schedulerRequest) {
	t.Helper()
	received := make(chan schedulerRequest, 16)
	var n atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- schedulerRequest{method: r.Method, header: r.Header.Clone(), body: string(body)}
		i := int(n.Add(1)) - 1
		w.WriteHeader(statuses[min(i, len(statuses)-1)])
	}))
	t.Cleanup(server.Close)
	return server, received
}

func awaitSchedulerRequest(t *testing.T, received <-chan schedulerRequest, within time.Duration) schedulerRequest {
	t.Helper()
	select {
	case r := <-received:
		return r
	case <-time.After(within):
		t.Fatal("the job's endpoint received no request")
		return schedulerRequest{}
	}
}

// awaitJobStatus reads the job until Cloud Scheduler records the outcome of an
// attempt that started at or after since: the API offers no wait on a job's
// status. Compare at lastAttemptTime's full precision, since an earlier attempt
// can start within the same millisecond.
func awaitJobStatus(t *testing.T, c *scheduler.CloudSchedulerClient, name string, since time.Time) *schedulerpb.Job {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		job, err := c.GetJob(ctx, &schedulerpb.GetJobRequest{Name: name})
		require.NoError(t, err)
		if job.GetStatus() != nil && job.GetLastAttemptTime() != nil && !job.GetLastAttemptTime().AsTime().Before(since) {
			return job
		}
		require.True(t, time.Now().Before(deadline), "the job never recorded an attempt's outcome: %v", job)
		time.Sleep(200 * time.Millisecond)
	}
}

func TestCloudScheduler_HTTPJobLifecycle(t *testing.T) {
	t.Parallel()
	c := schedulerClient(t)
	endpoint, received := schedulerEndpoint(t, http.StatusOK)
	name := schedulerParent + "/jobs/" + uniquename.New("sdk-http-job")

	created, err := c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: &schedulerpb.Job{
		Name:        name,
		Description: "nightly report",
		Schedule:    "0 3 * * *",
		TimeZone:    "Europe/Berlin",
		Target: &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{
			Uri:        endpoint.URL + "/report",
			HttpMethod: schedulerpb.HttpMethod_PUT,
			Headers:    map[string]string{"Content-Type": "application/json", "X-Team": "data"},
			Body:       []byte(`{"kind":"nightly"}`),
		}},
		RetryConfig: &schedulerpb.RetryConfig{RetryCount: 1},
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}) })
	assert.Equal(t, name, created.GetName())
	assert.Equal(t, schedulerpb.Job_ENABLED, created.GetState())
	assert.Equal(t, 180*time.Second, created.GetAttemptDeadline().AsDuration(), "an HTTP target's default deadline is 3 minutes")
	assert.EqualValues(t, 1, created.GetRetryConfig().GetRetryCount())
	assert.Equal(t, 5*time.Second, created.GetRetryConfig().GetMinBackoffDuration().AsDuration(), "an unset RetryConfig field takes its default")
	assert.Equal(t, time.Hour, created.GetRetryConfig().GetMaxBackoffDuration().AsDuration())
	assert.EqualValues(t, 5, created.GetRetryConfig().GetMaxDoublings())
	require.NotNil(t, created.GetScheduleTime())
	berlin, err := time.LoadLocation("Europe/Berlin")
	require.NoError(t, err)
	next := created.GetScheduleTime().AsTime().In(berlin)
	assert.Equal(t, 3, next.Hour(), "the schedule reads in the job's time zone")
	assert.Equal(t, 0, next.Minute())

	_, err = c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: created})
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusConflict, apiErr.Code)

	before := time.Now()
	ran, err := c.RunJob(ctx, &schedulerpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, name, ran.GetName())
	request := awaitSchedulerRequest(t, received, 30*time.Second)
	assert.Equal(t, http.MethodPut, request.method)
	assert.Equal(t, `{"kind":"nightly"}`, request.body)
	assert.Equal(t, "application/json", request.header.Get("Content-Type"))
	assert.Equal(t, "data", request.header.Get("X-Team"))
	assert.Equal(t, "Google-Cloud-Scheduler", request.header.Get("User-Agent"))
	assert.Equal(t, "true", request.header.Get("X-CloudScheduler"))
	job := awaitJobStatus(t, c, name, before)
	assert.Zero(t, job.GetStatus().GetCode(), "a delivered attempt leaves an empty status")

	paused, err := c.PauseJob(ctx, &schedulerpb.PauseJobRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, schedulerpb.Job_PAUSED, paused.GetState())
	again, err := c.PauseJob(ctx, &schedulerpb.PauseJobRequest{Name: name})
	require.NoError(t, err, "pausing a paused job leaves it paused")
	assert.Equal(t, schedulerpb.Job_PAUSED, again.GetState())
	resumed, err := c.ResumeJob(ctx, &schedulerpb.ResumeJobRequest{Name: name})
	require.NoError(t, err)
	assert.Equal(t, schedulerpb.Job_ENABLED, resumed.GetState())

	updated, err := c.UpdateJob(ctx, &schedulerpb.UpdateJobRequest{
		Job:        &schedulerpb.Job{Name: name, Description: "hourly report", Schedule: "15 * * * *"},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"description", "schedule"}},
	})
	require.NoError(t, err)
	assert.Equal(t, "hourly report", updated.GetDescription())
	assert.Equal(t, "15 * * * *", updated.GetSchedule())
	assert.Equal(t, "Europe/Berlin", updated.GetTimeZone(), "a field outside the mask keeps its value")
	assert.Equal(t, endpoint.URL+"/report", updated.GetHttpTarget().GetUri())
	assert.Equal(t, 15, updated.GetScheduleTime().AsTime().Minute())

	var listed []string
	it := c.ListJobs(ctx, &schedulerpb.ListJobsRequest{Parent: schedulerParent})
	for {
		j, err := it.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		require.NoError(t, err)
		listed = append(listed, j.GetName())
	}
	assert.Contains(t, listed, name)

	require.NoError(t, c.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}))
	_, err = c.GetJob(ctx, &schedulerpb.GetJobRequest{Name: name})
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotFound, apiErr.Code)
}

// TestCloudScheduler_RetriesUnderRetryConfig fails a job's first two attempts
// and lets the third through: Cloud Scheduler retries under the job's
// RetryConfig and records each attempt's outcome in the job's status.
func TestCloudScheduler_RetriesUnderRetryConfig(t *testing.T) {
	t.Parallel()
	c := schedulerClient(t)
	endpoint, received := schedulerEndpoint(t, http.StatusServiceUnavailable, http.StatusServiceUnavailable, http.StatusOK)
	name := schedulerParent + "/jobs/" + uniquename.New("sdk-retry-job")
	_, err := c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: &schedulerpb.Job{
		Name:     name,
		Schedule: "0 0 1 1 *",
		Target: &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{
			Uri: endpoint.URL, HttpMethod: schedulerpb.HttpMethod_GET,
		}},
		RetryConfig: &schedulerpb.RetryConfig{
			RetryCount:         2,
			MinBackoffDuration: durationpb.New(time.Second),
			MaxBackoffDuration: durationpb.New(time.Second),
		},
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}) })

	_, err = c.RunJob(ctx, &schedulerpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	first := awaitSchedulerRequest(t, received, 30*time.Second)
	assert.Equal(t, http.MethodGet, first.method)
	awaitSchedulerRequest(t, received, 30*time.Second)
	retried := time.Now()
	awaitSchedulerRequest(t, received, 30*time.Second)
	job := awaitJobStatus(t, c, name, retried)
	assert.Zero(t, job.GetStatus().GetCode(), "the third attempt succeeded")
	select {
	case extra := <-received:
		t.Fatalf("a delivered execution was attempted again: %+v", extra)
	case <-time.After(1500 * time.Millisecond):
	}
}

// TestCloudScheduler_FailedAttemptRecordsItsStatus runs a job whose endpoint
// refuses it and has no retries left: the job's status carries the code of
// the endpoint's answer.
func TestCloudScheduler_FailedAttemptRecordsItsStatus(t *testing.T) {
	t.Parallel()
	c := schedulerClient(t)
	endpoint, received := schedulerEndpoint(t, http.StatusNotFound)
	name := schedulerParent + "/jobs/" + uniquename.New("sdk-refused-job")
	_, err := c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: &schedulerpb.Job{
		Name:     name,
		Schedule: "0 0 1 1 *",
		Target:   &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{Uri: endpoint.URL}},
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}) })
	before := time.Now()
	_, err = c.RunJob(ctx, &schedulerpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	request := awaitSchedulerRequest(t, received, 30*time.Second)
	assert.Equal(t, http.MethodPost, request.method, "an unspecified method is POST")
	job := awaitJobStatus(t, c, name, before)
	assert.EqualValues(t, 5, job.GetStatus().GetCode(), "a 404 answer records NOT_FOUND")
}

// TestCloudScheduler_PubsubTargetPublishes runs a job whose target is a
// Pub/Sub topic: the message reaches the topic's subscription with the job's
// data and attributes.
func TestCloudScheduler_PubsubTargetPublishes(t *testing.T) {
	t.Parallel()
	c := schedulerClient(t)
	ps := pubsubService(t)
	topic := "projects/" + harnessProject + "/topics/" + uniquename.New("sched-topic")
	sub := "projects/" + harnessProject + "/subscriptions/" + uniquename.New("sched-sub")
	_, err := ps.Projects.Topics.Create(topic, &pubsub.Topic{}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ps.Projects.Topics.Delete(topic).Do() })
	_, err = ps.Projects.Subscriptions.Create(sub, &pubsub.Subscription{Topic: topic}).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = ps.Projects.Subscriptions.Delete(sub).Do() })

	name := schedulerParent + "/jobs/" + uniquename.New("sdk-pubsub-job")
	_, err = c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: &schedulerpb.Job{
		Name:     name,
		Schedule: "*/30 * * * *",
		Target: &schedulerpb.Job_PubsubTarget{PubsubTarget: &schedulerpb.PubsubTarget{
			TopicName: topic, Data: []byte("tick"), Attributes: map[string]string{"source": "scheduler"},
		}},
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}) })

	_, err = c.RunJob(ctx, &schedulerpb.RunJobRequest{Name: name})
	require.NoError(t, err)
	pulled, err := ps.Projects.Subscriptions.Pull(sub, &pubsub.PullRequest{MaxMessages: 1}).Do()
	require.NoError(t, err)
	require.Len(t, pulled.ReceivedMessages, 1, "the job's message reaches the topic")
	data, err := base64.StdEncoding.DecodeString(pulled.ReceivedMessages[0].Message.Data)
	require.NoError(t, err)
	assert.Equal(t, "tick", string(data))
	assert.Equal(t, "scheduler", pulled.ReceivedMessages[0].Message.Attributes["source"])
}

// TestCloudScheduler_ScheduleFires waits for an every-minute job's next
// occurrence: Cloud Scheduler sends the request with the occurrence's time.
func TestCloudScheduler_ScheduleFires(t *testing.T) {
	t.Parallel()
	c := schedulerClient(t)
	endpoint, received := schedulerEndpoint(t, http.StatusOK)
	name := schedulerParent + "/jobs/" + uniquename.New("sdk-cron-job")
	created, err := c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: &schedulerpb.Job{
		Name:     name,
		Schedule: "* * * * *",
		Target:   &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{Uri: endpoint.URL}},
	}})
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.DeleteJob(ctx, &schedulerpb.DeleteJobRequest{Name: name}) })
	occurrence := created.GetScheduleTime().AsTime()
	request := awaitSchedulerRequest(t, received, time.Until(occurrence)+30*time.Second)
	scheduled, err := time.Parse(time.RFC3339, request.header.Get("X-CloudScheduler-ScheduleTime"))
	require.NoError(t, err)
	assert.True(t, scheduled.Equal(occurrence), "the request names the occurrence %v, got %v", occurrence, scheduled)
}

// TestCloudScheduler_RefusesWhatItDoesNotServe creates jobs the simulator
// cannot run: an App Engine target and an English-like schedule each answer
// a declared 501, and an invalid time zone is refused.
func TestCloudScheduler_RefusesWhatItDoesNotServe(t *testing.T) {
	t.Parallel()
	c := schedulerClient(t)
	httpTarget := &schedulerpb.Job_HttpTarget{HttpTarget: &schedulerpb.HttpTarget{Uri: "http://example.com"}}
	for _, tc := range []struct {
		job    *schedulerpb.Job
		status int
	}{
		{&schedulerpb.Job{Schedule: "* * * * *", Target: &schedulerpb.Job_AppEngineHttpTarget{
			AppEngineHttpTarget: &schedulerpb.AppEngineHttpTarget{RelativeUri: "/tick"}}}, http.StatusNotImplemented},
		{&schedulerpb.Job{Schedule: "every 5 minutes", Target: httpTarget}, http.StatusNotImplemented},
		{&schedulerpb.Job{Schedule: "* * * * *", TimeZone: "Mars/Olympus", Target: httpTarget}, http.StatusBadRequest},
		{&schedulerpb.Job{Schedule: "* * * * *", Target: httpTarget,
			RetryConfig: &schedulerpb.RetryConfig{RetryCount: 6}}, http.StatusBadRequest},
	} {
		tc.job.Name = schedulerParent + "/jobs/" + uniquename.New("sdk-refused")
		_, err := c.CreateJob(ctx, &schedulerpb.CreateJobRequest{Parent: schedulerParent, Job: tc.job})
		var apiErr *googleapi.Error
		require.ErrorAs(t, err, &apiErr, "%v", tc.job)
		assert.Equal(t, tc.status, apiErr.Code, "%v: %s", tc.job, apiErr.Message)
	}
}

// TestCloudScheduler_CmekConfigIsDeclaredUnserved reads and writes the
// location's CMEK config: both answer the declared 501.
func TestCloudScheduler_CmekConfigIsDeclaredUnserved(t *testing.T) {
	t.Parallel()
	svc, err := cloudscheduler.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	name := schedulerParent + "/cmekConfig"
	_, err = svc.Projects.Locations.GetCmekConfig(name).Do()
	var apiErr *googleapi.Error
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotImplemented, apiErr.Code)
	_, err = svc.Projects.Locations.UpdateCmekConfig(name, &cloudscheduler.CmekConfig{}).Do()
	require.ErrorAs(t, err, &apiErr)
	assert.Equal(t, http.StatusNotImplemented, apiErr.Code)
}
