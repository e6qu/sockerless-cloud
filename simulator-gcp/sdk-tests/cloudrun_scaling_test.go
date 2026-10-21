package gcp_sdk_test

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// instanceTemplate is a template whose container answers every request with
// the host of the instance that served it.
func instanceTemplate(scaling *runpb.RevisionScaling, concurrency int32) *runpb.RevisionTemplate {
	return &runpb.RevisionTemplate{
		Scaling:                       scaling,
		MaxInstanceRequestConcurrency: concurrency,
		Containers: []*runpb.Container{{
			Image: commandImageName,
			Args:  []string{"instance-http", "8080"},
		}},
	}
}

type instanceAnswer struct {
	status int
	body   string
	err    error
}

// invokeConcurrently sends n requests to the service at once, each held by
// the instance for holdMS milliseconds, and returns their answers.
func invokeConcurrently(uri string, n, holdMS int) []instanceAnswer {
	answers := make([]instanceAnswer, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			answers[i] = invokeHeld(uri, holdMS)
		}()
	}
	wg.Wait()
	return answers
}

func invokeHeld(uri string, holdMS int) instanceAnswer {
	u, err := url.Parse(uri)
	if err != nil {
		return instanceAnswer{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/?hold="+strconv.Itoa(holdMS), nil)
	if err != nil {
		return instanceAnswer{err: err}
	}
	req.Host = u.Host
	resp, err := rawClient.Do(req)
	if err != nil {
		return instanceAnswer{err: err}
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	return instanceAnswer{status: resp.StatusCode, body: string(body), err: err}
}

// A revision starts an instance for a request no running instance has room
// for, each instance taking at most maxInstanceRequestConcurrency requests at
// once, until it runs maxInstanceCount; a request beyond that waits for a
// place on one of them.
func TestSDK_CloudRunV2Services_InstancesScaleOnConcurrency(t *testing.T) {
	t.Parallel()
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-scale", &runpb.Service{
		InvokerIamDisabled: true,
		Template:           instanceTemplate(&runpb.RevisionScaling{MaxInstanceCount: 2}, 1),
	})

	hosts := map[string]bool{}
	for _, answer := range invokeConcurrently(svc.Uri, 2, 1500) {
		require.NoError(t, answer.err)
		require.Equal(t, http.StatusOK, answer.status, "body=%q", answer.body)
		hosts[answer.body] = true
	}
	assert.Len(t, hosts, 2, "two requests held at once on instances that take one each run on two instances")

	for _, answer := range invokeConcurrently(svc.Uri, 3, 1500) {
		require.NoError(t, answer.err)
		require.Equal(t, http.StatusOK, answer.status, "a request beyond the instances' room waits for a place: body=%q", answer.body)
		assert.True(t, hosts[answer.body], "the revision runs no third instance beyond maxInstanceCount: %q", answer.body)
	}
}

// A request that finds every instance the revision may run busy for longer
// than the pending queue holds it is answered 429.
func TestSDK_CloudRunV2Services_NoAvailableInstance(t *testing.T) {
	t.Parallel()
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-full", &runpb.Service{
		InvokerIamDisabled: true,
		Template:           instanceTemplate(&runpb.RevisionScaling{MaxInstanceCount: 1}, 1),
	})
	warm := invokeHeld(svc.Uri, 0)
	require.NoError(t, warm.err)
	require.Equal(t, http.StatusOK, warm.status, "body=%q", warm.body)

	// The pending queue holds a request for at least 10 seconds, so one held
	// for 12 keeps the only place past it.
	answers := invokeConcurrently(svc.Uri, 2, 12000)
	var statuses []int
	var refused string
	for _, answer := range answers {
		require.NoError(t, answer.err)
		statuses = append(statuses, answer.status)
		if answer.status == http.StatusTooManyRequests {
			refused = answer.body
		}
	}
	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusTooManyRequests}, statuses)
	assert.Contains(t, refused, "The request was aborted because there was no available instance.")
}

// A revision with minInstanceCount starts that many instances when it
// deploys, before any request reaches it, and each instance's output carries
// its instance ID.
func TestSDK_CloudRunV2Services_MinInstancesStartOnDeploy(t *testing.T) {
	t.Parallel()
	serviceID := uniqueName("v2-svc-min")
	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, early := openReadyTail(t, tailCtx, newLoggingV2Client(t), "test-project",
		`resource.type="cloud_run_revision" AND resource.labels.service_name="`+serviceID+`"`)

	client := newServicesClient(t)
	op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    "projects/test-project/locations/us-central1",
		ServiceId: serviceID,
		Service:   &runpb.Service{Template: instanceTemplate(&runpb.RevisionScaling{MinInstanceCount: 2}, 0)},
	})
	require.NoError(t, err)
	svc, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupService(t, client, svc.Name)
	revision := revisionID(svc.LatestReadyRevision)

	started := map[string]bool{}
	note := func(entry interface {
		GetTextPayload() string
		GetLabels() map[string]string
	}, revisionName string) {
		if strings.HasPrefix(entry.GetTextPayload(), "serving as ") && revisionName == revision {
			started[entry.GetLabels()["instanceId"]] = true
		}
	}
	for _, entry := range early {
		note(entry, entry.GetResource().GetLabels()["revision_name"])
	}
	for len(started) < 2 {
		resp, err := stream.Recv()
		require.NoError(t, err, "the revision's instances never both started: %v", started)
		for _, entry := range resp.GetEntries() {
			note(entry, entry.GetResource().GetLabels()["revision_name"])
		}
	}
	assert.NotContains(t, started, "", "every instance's output carries its instance ID")
	assert.Len(t, started, 2)
}
