package gcp_sdk_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"cloud.google.com/go/run/apiv2/runpb"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/iterator"
	"google.golang.org/protobuf/types/known/fieldmaskpb"
)

// revisionEchoTemplate is a template whose container answers every request
// with the revision Cloud Run told it it serves, carrying version so two
// templates differ.
func revisionEchoTemplate(version string) *runpb.RevisionTemplate {
	return &runpb.RevisionTemplate{Containers: []*runpb.Container{{
		Image: commandImageName,
		Args:  []string{"env-http", "8080", "K_REVISION"},
		Env:   []*runpb.EnvVar{{Name: "VERSION", Values: &runpb.EnvVar_Value{Value: version}}},
	}}}
}

func revisionID(name string) string { return name[strings.LastIndex(name, "/")+1:] }

// A service deploys a revision when its template changes and only then,
// names it after the service and its generation, sends its requests to its
// revisions by the traffic percents, and serves a tagged revision on the
// tag's own URL.
func TestSDK_CloudRunV2Services_RevisionsAndTraffic(t *testing.T) {
	t.Parallel()
	client := newServicesClient(t)
	svc := createInvokableService(t, client, "v2-svc-traffic", &runpb.Service{
		InvokerIamDisabled: true,
		Template:           revisionEchoTemplate("one"),
	})
	serviceID := revisionID(svc.Name)
	first := revisionID(svc.LatestReadyRevision)
	assert.Regexp(t, regexp.MustCompile("^"+regexp.QuoteMeta(serviceID)+`-00001-[a-z0-9]{3}$`), first)
	assert.Equal(t, svc.LatestReadyRevision, svc.LatestCreatedRevision)
	require.Len(t, svc.Traffic, 1, "a service without traffic targets sends it all to its latest revision")
	assert.Equal(t, runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST, svc.Traffic[0].Type)
	assert.EqualValues(t, 100, svc.Traffic[0].Percent)
	require.Len(t, svc.TrafficStatuses, 1)
	assert.EqualValues(t, 100, svc.TrafficStatuses[0].Percent)

	invoke := func(uri string) string {
		t.Helper()
		status, _, body := invokeService(t, "", uri, http.MethodGet, "/", "")
		require.Equal(t, http.StatusOK, status, "body=%q", body)
		return body
	}
	assert.Equal(t, first, invoke(svc.Uri), "the container learns the revision it serves from K_REVISION")

	update := func(service *runpb.Service, paths ...string) *runpb.Service {
		t.Helper()
		op, err := client.UpdateService(ctx, &runpb.UpdateServiceRequest{
			Service:    service,
			UpdateMask: &fieldmaskpb.FieldMask{Paths: paths},
		})
		require.NoError(t, err)
		updated, err := op.Wait(ctx)
		require.NoError(t, err)
		return updated
	}

	// Pinning the traffic to the revision changes no template: the service
	// moves to its next generation and deploys no revision.
	pinned := update(&runpb.Service{Name: svc.Name, Traffic: []*runpb.TrafficTarget{{
		Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: first, Percent: 100,
	}}}, "traffic")
	assert.EqualValues(t, 2, pinned.Generation)
	assert.Equal(t, svc.LatestCreatedRevision, pinned.LatestCreatedRevision, "a traffic change deploys no revision")

	// A new template deploys the next revision, which takes no traffic but
	// answers on its tag's URL.
	canary := update(&runpb.Service{
		Name:     svc.Name,
		Template: revisionEchoTemplate("two"),
		Traffic: []*runpb.TrafficTarget{
			{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: first, Percent: 100},
			{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_LATEST, Tag: "canary"},
		},
	}, "template", "traffic")
	second := revisionID(canary.LatestReadyRevision)
	assert.Regexp(t, regexp.MustCompile("^"+regexp.QuoteMeta(serviceID)+`-00003-[a-z0-9]{3}$`), second)
	var tagURI string
	for _, status := range canary.TrafficStatuses {
		if status.Tag == "canary" {
			tagURI = status.Uri
		}
	}
	require.NotEmpty(t, tagURI, "a tagged target reports its URL: %v", canary.TrafficStatuses)
	assert.True(t, strings.HasPrefix(tagURI, "https://canary---"), "the tag's URL: %s", tagURI)
	assert.Equal(t, first, invoke(canary.Uri), "the service's URL still reaches the pinned revision")
	assert.Equal(t, second, invoke(tagURI), "the tag's URL reaches the tagged revision")

	// An even split reaches both revisions.
	split := update(&runpb.Service{Name: svc.Name, Traffic: []*runpb.TrafficTarget{
		{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: first, Percent: 50},
		{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: second, Percent: 50},
	}}, "traffic")
	answered := map[string]bool{}
	for range 40 {
		answered[invoke(split.Uri)] = true
	}
	assert.Equal(t, map[string]bool{first: true, second: true}, answered, "forty requests over an even split reach both revisions")

	var listed []string
	it := newRevisionsClient(t).ListRevisions(ctx, &runpb.ListRevisionsRequest{Parent: svc.Name})
	for {
		rev, err := it.Next()
		if err == iterator.Done {
			break
		}
		require.NoError(t, err)
		listed = append(listed, revisionID(rev.Name))
	}
	assert.ElementsMatch(t, []string{first, second}, listed, "the service deployed two revisions in four writes")

	// Traffic must add up to 100 and name revisions the service has.
	_, err := client.UpdateService(ctx, &runpb.UpdateServiceRequest{
		Service: &runpb.Service{Name: svc.Name, Traffic: []*runpb.TrafficTarget{
			{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: first, Percent: 60},
		}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"traffic"}},
	})
	require.Error(t, err, "traffic adding up to 60 is refused")
	_, err = client.UpdateService(ctx, &runpb.UpdateServiceRequest{
		Service: &runpb.Service{Name: svc.Name, Traffic: []*runpb.TrafficTarget{
			{Type: runpb.TrafficTargetAllocationType_TRAFFIC_TARGET_ALLOCATION_TYPE_REVISION, Revision: serviceID + "-99999-zzz", Percent: 100},
		}},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"traffic"}},
	})
	require.Error(t, err, "traffic to a revision the service does not have is refused")
}
