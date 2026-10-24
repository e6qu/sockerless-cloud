package gcp_sdk_test

import (
	"context"
	"testing"

	"cloud.google.com/go/logging/apiv2/loggingpb"
	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

// Service-level scaling is divided among the revisions of the traffic split:
// a service minimum of two instances over a 50/50 split runs one instance of
// each revision, and switching the service to manual scaling at three
// instances, without touching the template, runs the third on the revision
// the traffic lists first, which takes the rounded-up share.
func TestSDK_CloudRunV2Services_ServiceScalingIsDividedAmongTheTrafficSplit(t *testing.T) {
	t.Parallel()
	serviceID := uniqueName("v2-svc-split")
	tailCtx, cancel := context.WithTimeout(ctx, jobLogWaitTimeout)
	defer cancel()
	stream, early := openReadyTail(t, tailCtx, newLoggingV2Client(t), "test-project",
		`resource.type="cloud_run_revision" AND resource.labels.service_name="`+serviceID+`" AND textPayload:"serving as "`)

	client := newServicesClient(t)
	template := func(suffix string) *runpb.RevisionTemplate {
		tmpl := instanceTemplate(nil, 0)
		tmpl.Revision = serviceID + "-" + suffix
		return tmpl
	}
	op, err := client.CreateService(ctx, &runpb.CreateServiceRequest{
		Parent:    "projects/test-project/locations/us-central1",
		ServiceId: serviceID,
		Service:   &runpb.Service{Template: template("a")},
	})
	require.NoError(t, err)
	svc, err := op.Wait(ctx)
	require.NoError(t, err)
	cleanupService(t, client, svc.Name)

	// A second revision, with the traffic split evenly and listed first.
	svc.Template = template("b")
	svc.Traffic = []*runpb.TrafficTarget{
		{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: serviceID + "-b", Percent: 50},
		{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: serviceID + "-a", Percent: 50},
	}
	update, err := client.UpdateService(ctx, &runpb.UpdateServiceRequest{Service: svc})
	require.NoError(t, err)
	svc, err = update.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, serviceID+"-b", revisionID(svc.LatestReadyRevision))

	svc.Scaling = &runpb.ServiceScaling{MinInstanceCount: 2}
	update, err = client.UpdateService(ctx, &runpb.UpdateServiceRequest{Service: svc})
	require.NoError(t, err)
	svc, err = update.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, serviceID+"-b", revisionID(svc.LatestCreatedRevision), "a scaling change deploys no revision")

	instances := map[string]map[string]bool{}
	note := func(entry *loggingpb.LogEntry) {
		revision := entry.GetResource().GetLabels()["revision_name"]
		if instances[revision] == nil {
			instances[revision] = map[string]bool{}
		}
		instances[revision][entry.GetLabels()["instanceId"]] = true
	}
	for _, entry := range early {
		note(entry)
	}
	awaitInstances := func(want map[string]int) {
		for {
			done := true
			for revision, n := range want {
				if len(instances[revision]) < n {
					done = false
				}
			}
			if done {
				return
			}
			resp, err := stream.Recv()
			require.NoError(t, err, "the instances never started: %v", instances)
			for _, entry := range resp.GetEntries() {
				note(entry)
			}
		}
	}
	awaitInstances(map[string]int{serviceID + "-a": 1, serviceID + "-b": 1})
	assert.Len(t, instances[serviceID+"-a"], 1)
	assert.Len(t, instances[serviceID+"-b"], 1)

	svc.Scaling = &runpb.ServiceScaling{ScalingMode: runpb.ServiceScaling_MANUAL, ManualInstanceCount: proto.Int32(3)}
	update, err = client.UpdateService(ctx, &runpb.UpdateServiceRequest{Service: svc})
	require.NoError(t, err)
	svc, err = update.Wait(ctx)
	require.NoError(t, err)
	require.Equal(t, serviceID+"-b", revisionID(svc.LatestCreatedRevision), "a scaling change deploys no revision")
	assert.Equal(t, runpb.ServiceScaling_MANUAL, svc.Scaling.ScalingMode)

	awaitInstances(map[string]int{serviceID + "-b": 2})
	assert.Len(t, instances[serviceID+"-b"], 2, "the first-listed revision takes the rounded-up share of three")
	assert.Len(t, instances[serviceID+"-a"], 1)
}
