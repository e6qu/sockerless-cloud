package gcp_sdk_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	eventarcapi "google.golang.org/api/eventarc/v1"
	runv2 "google.golang.org/api/run/v2"

	"cloud.google.com/go/eventarc/apiv1/eventarcpb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	longrunningpb "cloud.google.com/go/longrunning/autogen/longrunningpb"
	"cloud.google.com/go/pubsub/apiv1/pubsubpb"
	"github.com/stretchr/testify/require"
	iam "google.golang.org/api/iam/v1"
	"google.golang.org/api/iterator"
	logging "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	run "google.golang.org/api/run/v1"
	vpcaccess "google.golang.org/api/vpcaccess/v1"
)

// continuationList is one list method under test: put and del create and
// delete the resource a name stands for, del nil for a resource the API never
// deletes, and page reads two items from token on.
type continuationList struct {
	put  func(t *testing.T, name string)
	del  func(t *testing.T, name string)
	page func(t *testing.T, token string) (names []string, next string)
}

// requireContinuation walks a list two items a page while it changes. order
// holds eight names in the list's own order, standing for a b c d e f h j. The
// list starts as b d f h j; once the first page has returned b and d, the test
// creates a and c ahead of the boundary and e after it, and deletes b, which it
// has read, and h, which it has not. A token that resumes after d lists e f j
// next. An index token lists d again and skips what the creations pushed past
// it.
func requireContinuation(t *testing.T, list continuationList, order [8]string) {
	t.Helper()
	a, b, c, d, e, f, h, j := order[0], order[1], order[2], order[3], order[4], order[5], order[6], order[7]
	for _, name := range []string{b, d, f, h, j} {
		list.put(t, name)
	}
	seen, next := list.page(t, "")
	require.Equal(t, []string{b, d}, seen, "the first page")
	require.NotEmpty(t, next, "a partial page carries a token")
	list.put(t, a)
	list.put(t, c)
	list.put(t, e)
	want := []string{b, d, e, f, h, j}
	if list.del != nil {
		list.del(t, b)
		list.del(t, h)
		want = []string{b, d, e, f, j}
	}
	for pages := 1; next != ""; pages++ {
		require.LessOrEqual(t, pages, 8, "the list never reached its last page: %v", seen)
		var names []string
		names, next = list.page(t, next)
		seen = append(seen, names...)
	}
	require.Equal(t, want, seen, "walking the list while it changed")
}

var continuationLetters = [8]string{"a", "b", "c", "d", "e", "f", "h", "j"}

func TestCloudKMSGRPC_ListKeyRingsResumesAfterTheLastKeyRingReturned(t *testing.T) {
	t.Parallel()
	client := newKMSGRPCClient(t)
	parent := "projects/" + uniqueName("kms-continuation") + "/locations/global"
	requireContinuation(t, continuationList{
		put: func(t *testing.T, id string) {
			_, err := client.CreateKeyRing(ctx, &kmspb.CreateKeyRingRequest{Parent: parent, KeyRingId: id, KeyRing: &kmspb.KeyRing{}})
			require.NoError(t, err)
		},
		page: func(t *testing.T, token string) ([]string, string) {
			var rings []*kmspb.KeyRing
			next, err := iterator.NewPager(client.ListKeyRings(ctx, &kmspb.ListKeyRingsRequest{Parent: parent}), 2, token).NextPage(&rings)
			require.NoError(t, err)
			var ids []string
			for _, ring := range rings {
				ids = append(ids, strings.TrimPrefix(ring.GetName(), parent+"/keyRings/"))
			}
			return ids, next
		},
	}, continuationLetters)
}

func TestPubSubGRPC_ListTopicsResumesAfterTheLastTopicReturned(t *testing.T) {
	t.Parallel()
	pub, _ := psRawClient(t)
	project := "projects/" + uniqueName("ps-continuation")
	ensureProject(t, strings.TrimPrefix(project, "projects/"))
	requireContinuation(t, continuationList{
		put: func(t *testing.T, id string) {
			_, err := pub.CreateTopic(ctx, &pubsubpb.Topic{Name: project + "/topics/" + id})
			require.NoError(t, err)
		},
		del: func(t *testing.T, id string) {
			_, err := pub.DeleteTopic(ctx, &pubsubpb.DeleteTopicRequest{Topic: project + "/topics/" + id})
			require.NoError(t, err)
		},
		page: func(t *testing.T, token string) ([]string, string) {
			resp, err := pub.ListTopics(ctx, &pubsubpb.ListTopicsRequest{Project: project, PageSize: 2, PageToken: token})
			require.NoError(t, err)
			var ids []string
			for _, topic := range resp.GetTopics() {
				ids = append(ids, strings.TrimPrefix(topic.GetName(), project+"/topics/"))
			}
			return ids, resp.GetNextPageToken()
		},
	}, continuationLetters)
}

func TestCloudRunV1_JobsListResumesAfterTheLastJobReturned(t *testing.T) {
	t.Parallel()
	svc := newRunV1(t)
	parent := "namespaces/" + uniqueName("run-v1-continuation")
	requireContinuation(t, continuationList{
		put: func(t *testing.T, id string) {
			_, err := svc.Namespaces.Jobs.Create(parent, &run.Job{
				Metadata: &run.ObjectMeta{Name: id},
				Spec: &run.JobSpec{Template: &run.ExecutionTemplateSpec{Spec: &run.ExecutionSpec{
					Template: &run.TaskTemplateSpec{Spec: &run.TaskSpec{
						Containers: []*run.Container{{Image: "mirror.gcr.io/library/alpine:latest", Args: []string{"true"}}},
					}},
				}}},
			}).Do()
			require.NoError(t, err)
		},
		del: func(t *testing.T, id string) {
			_, err := svc.Namespaces.Jobs.Delete(parent + "/jobs/" + id).Do()
			require.NoError(t, err)
		},
		page: func(t *testing.T, token string) ([]string, string) {
			call := svc.Namespaces.Jobs.List(parent).Limit(2)
			if token != "" {
				call = call.Continue(token)
			}
			resp, err := call.Do()
			require.NoError(t, err)
			var ids []string
			for _, job := range resp.Items {
				ids = append(ids, job.Metadata.Name)
			}
			return ids, knativeListContinue(resp.Metadata)
		},
	}, continuationLetters)
}

func TestIAM_CustomRolesListResumesAfterTheLastRoleReturned(t *testing.T) {
	t.Parallel()
	svc := iamService(t)
	parent := "projects/" + uniqueName("iam-continuation")
	requireContinuation(t, continuationList{
		put: func(t *testing.T, id string) {
			_, err := svc.Projects.Roles.Create(parent, &iam.CreateRoleRequest{
				RoleId: id,
				Role:   &iam.Role{Title: id, IncludedPermissions: []string{"storage.objects.get"}, Stage: "GA"},
			}).Do()
			require.NoError(t, err)
		},
		del: func(t *testing.T, id string) {
			_, err := svc.Projects.Roles.Delete(parent + "/roles/" + id).Do()
			require.NoError(t, err)
		},
		page: func(t *testing.T, token string) ([]string, string) {
			resp, err := svc.Projects.Roles.List(parent).PageSize(2).PageToken(token).Do()
			require.NoError(t, err)
			var ids []string
			for _, role := range resp.Roles {
				ids = append(ids, strings.TrimPrefix(role.Name, parent+"/roles/"))
			}
			return ids, resp.NextPageToken
		},
	}, [8]string{"roleA", "roleB", "roleC", "roleD", "roleE", "roleF", "roleH", "roleJ"})
}

// entries.list orders a log's entries by timestamp; an entry written with an
// earlier timestamp than the page boundary lands ahead of it.
func TestLogging_EntriesListResumesAfterTheLastEntryReturned(t *testing.T) {
	t.Parallel()
	svc, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	logName := "projects/" + harnessProject + "/logs/" + uniqueName("continuation")
	written := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
	requireContinuation(t, continuationList{
		put: func(t *testing.T, second string) {
			offset, err := strconv.Atoi(second)
			require.NoError(t, err)
			_, err = svc.Entries.Write(&logging.WriteLogEntriesRequest{Entries: []*logging.LogEntry{{
				LogName:     logName,
				InsertId:    second,
				Timestamp:   written.Add(time.Duration(offset) * time.Second).Format(time.RFC3339),
				TextPayload: second,
				Resource:    &logging.MonitoredResource{Type: "global"},
			}}}).Do()
			require.NoError(t, err)
		},
		page: func(t *testing.T, token string) ([]string, string) {
			resp, err := svc.Entries.List(&logging.ListLogEntriesRequest{
				ResourceNames: []string{"projects/" + harnessProject},
				Filter:        `logName="` + logName + `"`,
				PageSize:      2,
				PageToken:     token,
			}).Do()
			require.NoError(t, err)
			var ids []string
			for _, entry := range resp.Entries {
				ids = append(ids, entry.InsertId)
			}
			return ids, resp.NextPageToken
		},
	}, [8]string{"01", "02", "03", "04", "05", "06", "08", "10"})
}

// Each service serves projects.locations.operations.list from its own host, so
// a location's collection lists, per service, the operations that service
// started.
func TestOperations_LocationCollectionsListTheirOwnServicesOperations(t *testing.T) {
	t.Parallel()
	const parent = "projects/" + harnessProject + "/locations/us-central1"
	eventarcSvc := eventarcClient(t)
	busID := uniqueName("ops-scope-bus")
	createBus, err := eventarcSvc.CreateMessageBus(ctx, &eventarcpb.CreateMessageBusRequest{
		Parent: parent, MessageBusId: busID, MessageBus: &eventarcpb.MessageBus{},
	})
	require.NoError(t, err)
	_, err = createBus.Wait(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		op, err := eventarcSvc.DeleteMessageBus(ctx, &eventarcpb.DeleteMessageBusRequest{Name: parent + "/messageBuses/" + busID})
		if err == nil {
			_, _ = op.Wait(ctx)
		}
	})

	vpcSvc, err := vpcaccess.NewService(ctx, serviceHostOptions("vpcaccess.googleapis.com")...)
	require.NoError(t, err)
	connectorID := uniqueName("ops-scope")
	connectorOp, err := vpcSvc.Projects.Locations.Connectors.Create(parent, &vpcaccess.Connector{
		Network: "default", IpCidrRange: "10.8.48.0/28", MachineType: "e2-micro", MinInstances: 2, MaxInstances: 3,
	}).ConnectorId(connectorID).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = vpcSvc.Projects.Locations.Connectors.Delete(parent + "/connectors/" + connectorID).Do() })

	var eventarcOps []string
	it := eventarcSvc.ListOperations(ctx, &longrunningpb.ListOperationsRequest{Name: parent})
	for op, err := range it.All() {
		require.NoError(t, err)
		eventarcOps = append(eventarcOps, op.GetName())
	}
	require.Contains(t, eventarcOps, createBus.Name(), "Eventarc's collection lists the operation Eventarc started")
	require.NotContains(t, eventarcOps, connectorOp.Name, "Eventarc's collection lists a Serverless VPC Access operation")

	var vpcOps []string
	require.NoError(t, vpcSvc.Projects.Locations.Operations.List(parent).Pages(ctx, func(page *vpcaccess.ListOperationsResponse) error {
		for _, op := range page.Operations {
			vpcOps = append(vpcOps, op.Name)
		}
		return nil
	}))
	require.Contains(t, vpcOps, connectorOp.Name, "Serverless VPC Access's collection lists the operation it started")
	require.NotContains(t, vpcOps, createBus.Name(), "Serverless VPC Access's collection lists an Eventarc operation")

	// operations.delete reaches only the operations its own collection holds.
	eventarcREST, err := eventarcapi.NewService(ctx, serviceHostOptions(eventarcHost)...)
	require.NoError(t, err)
	_, err = eventarcREST.Projects.Locations.Operations.Delete(connectorOp.Name).Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotFound, "not found", "Eventarc deleting a Serverless VPC Access operation")
	_, err = vpcSvc.Projects.Locations.Operations.Get(connectorOp.Name).Do()
	require.NoError(t, err, "the refused delete left the operation in place")
	_, err = eventarcREST.Projects.Locations.Operations.Delete(createBus.Name()).Do()
	require.NoError(t, err)
	_, err = eventarcREST.Projects.Locations.Operations.Get(createBus.Name()).Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotFound, "not found", "reading a deleted Eventarc operation")

	runREST, err := runv2.NewService(ctx, serviceHostOptions("run.googleapis.com")...)
	require.NoError(t, err)
	jobID := uniqueName("ops-scope-job")
	jobOp, err := runREST.Projects.Locations.Jobs.Create(parent, &runv2.GoogleCloudRunV2Job{
		Template: &runv2.GoogleCloudRunV2ExecutionTemplate{Template: &runv2.GoogleCloudRunV2TaskTemplate{
			Containers: []*runv2.GoogleCloudRunV2Container{{Image: "mirror.gcr.io/library/alpine:latest"}},
		}},
	}).JobId(jobID).Do()
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = runREST.Projects.Locations.Jobs.Delete(parent + "/jobs/" + jobID).Do() })
	_, err = runREST.Projects.Locations.Operations.Delete(connectorOp.Name).Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotFound, "not found", "Cloud Run deleting a Serverless VPC Access operation")
	_, err = runREST.Projects.Locations.Operations.Delete(jobOp.Name).Do()
	require.NoError(t, err)
	_, err = runREST.Projects.Locations.Operations.Get(jobOp.Name).Do()
	requireGoogleAPIRefusal(t, err, http.StatusNotFound, "not found", "reading a deleted Cloud Run operation")
}
