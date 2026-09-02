package gcp_sdk_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	apigateway "google.golang.org/api/apigateway/v1"
	artifactregistry "google.golang.org/api/artifactregistry/v1"
	cloudbuild "google.golang.org/api/cloudbuild/v1"
	eventarcapi "google.golang.org/api/eventarc/v1"
	logging "google.golang.org/api/logging/v2"
	"google.golang.org/api/option"
	redis "google.golang.org/api/redis/v1"
	serviceusage "google.golang.org/api/serviceusage/v1"
	sqladmin "google.golang.org/api/sqladmin/v1"
)

// google.longrunning.Operations.CancelOperation across the Google Cloud
// services whose Discovery documents declare it, driven through each service's
// own official Go client so the request that reaches the simulator is the one a
// real client sends.
//
// The answer each service gives is the answer the service really gives, and
// they are not all the same:
//
//   - The services that spell the standard google.longrunning method — API
//     Gateway, Artifact Registry, Cloud Build, Eventarc, Cloud Logging,
//     Memorystore for Redis, Service Usage, Cloud Spanner, Firestore — answer
//     google.protobuf.Empty. Their own method description says a client checks
//     GetOperation to learn "whether the cancellation succeeded or whether the
//     operation completed despite cancellation", so cancelling a completed
//     operation is a documented outcome, not an error, and the recorded result
//     is left alone.
//   - Cloud SQL Admin's sql.operations.cancel is the service's own method and
//     refuses an operation that is not in progress.
//   - Cancelling a Cloud Build build operation cancels the build: the steps
//     that are running stop.
//
// A name no operation was ever minted under is NOT_FOUND everywhere.
//
// The routes these tests drive:
//
//	POST /v1/projects/{project}/locations/{location}/operations/{opAction}
//	POST /v2/projects/{project}/locations/{location}/operations/{opAction}
//	POST /v1/operations/{opAction...}
//	POST /v1/projects/{project}/builds/{idAction}
//	POST /v1/projects/{project}/locations/{location}/builds/{idAction}
//
// The first two are the colon-verb fan-in on the projects/locations operation
// collection — one collection shared by every service the simulator serves
// under it — which dispatches both wait and cancel.

const cancelProject = "test-project"

func artifactRegistryOpsService(t *testing.T) *artifactregistry.Service {
	t.Helper()
	svc, err := artifactregistry.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

func redisOpsService(t *testing.T) *redis.Service {
	t.Helper()
	svc, err := redis.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

func loggingOpsService(t *testing.T) *logging.Service {
	t.Helper()
	svc, err := logging.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// TestSDK_APIGateway_OperationsCancel drives
// apigateway.projects.locations.operations.cancel. Creating an API mints the
// operation; cancelling it answers Empty and the operation's recorded result
// survives, because the API was already created.
func TestSDK_APIGateway_OperationsCancel(t *testing.T) {
	svc := apigatewayService(t)
	parent := fmt.Sprintf("projects/%s/locations/global", cancelProject)

	op, err := svc.Projects.Locations.Apis.Create(parent, &apigateway.ApigatewayApi{
		DisplayName: "cancel-probe",
	}).ApiId("cancel-probe-api").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)

	before, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)

	_, err = svc.Projects.Locations.Operations.Cancel(op.Name,
		&apigateway.ApigatewayCancelOperationRequest{}).Do()
	require.NoError(t, err, "cancelling a completed operation is not an error")

	after, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, after.Done, "the operation stays done")
	assert.Equal(t, before.Error, after.Error, "a late cancel does not rewrite the recorded result")

	_, err = svc.Projects.Locations.Operations.Cancel(parent+"/operations/never-minted",
		&apigateway.ApigatewayCancelOperationRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_ArtifactRegistry_OperationsCancel drives
// artifactregistry.projects.locations.operations.cancel.
func TestSDK_ArtifactRegistry_OperationsCancel(t *testing.T) {
	svc := artifactRegistryOpsService(t)
	parent := fmt.Sprintf("projects/%s/locations/us-central1", cancelProject)

	op, err := svc.Projects.Locations.Repositories.Create(parent, &artifactregistry.Repository{
		Format: "DOCKER",
	}).RepositoryId("cancel-probe-repo").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)

	_, err = svc.Projects.Locations.Operations.Cancel(op.Name,
		&artifactregistry.CancelOperationRequest{}).Do()
	require.NoError(t, err)

	after, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, after.Done)

	_, err = svc.Projects.Locations.Operations.Cancel(parent+"/operations/never-minted",
		&artifactregistry.CancelOperationRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_MemorystoreRedis_OperationsCancel drives
// redis.projects.locations.operations.cancel.
func TestSDK_MemorystoreRedis_OperationsCancel(t *testing.T) {
	svc := redisOpsService(t)
	parent := fmt.Sprintf("projects/%s/locations/us-central1", cancelProject)

	op, err := svc.Projects.Locations.Instances.Create(parent, &redis.Instance{
		Tier:         "BASIC",
		MemorySizeGb: 1,
	}).InstanceId("cancel-probe-redis").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)

	_, err = svc.Projects.Locations.Operations.Cancel(op.Name).Do()
	require.NoError(t, err)

	after, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, after.Done)

	_, err = svc.Projects.Locations.Operations.Cancel(parent + "/operations/never-minted").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_Eventarc_OperationsCancel drives
// eventarc.projects.locations.operations.cancel.
func TestSDK_Eventarc_OperationsCancel(t *testing.T) {
	svc, err := eventarcapi.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	parent := fmt.Sprintf("projects/%s/locations/us-central1", cancelProject)

	op, err := svc.Projects.Locations.Triggers.Create(parent, &eventarcapi.Trigger{
		EventFilters: []*eventarcapi.EventFilter{{
			Attribute: "type",
			Value:     "google.cloud.pubsub.topic.v1.messagePublished",
		}},
		Destination: &eventarcapi.Destination{
			CloudRun: &eventarcapi.CloudRun{Service: "cancel-probe-svc", Region: "us-central1"},
		},
	}).TriggerId("cancel-probe-trigger").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)

	_, err = svc.Projects.Locations.Operations.Cancel(op.Name,
		&eventarcapi.GoogleLongrunningCancelOperationRequest{}).Do()
	require.NoError(t, err)

	after, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, after.Done)

	_, err = svc.Projects.Locations.Operations.Cancel(parent+"/operations/never-minted",
		&eventarcapi.GoogleLongrunningCancelOperationRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_ServiceUsage_OperationsCancel drives serviceusage.operations.cancel
// on the top-level operations collection. Service Usage names its operations
// operations/{id}, so the enable operation is addressable, gettable and
// cancellable by the name the enable call returned.
func TestSDK_ServiceUsage_OperationsCancel(t *testing.T) {
	svc := serviceUsageService(t)

	op, err := svc.Services.Enable(
		fmt.Sprintf("projects/%s/services/run.googleapis.com", cancelProject),
		&serviceusage.EnableServiceRequest{}).Do()
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(op.Name, "operations/"),
		"Service Usage names its operations operations/{id}; got %q", op.Name)

	_, err = svc.Operations.Cancel(op.Name, &serviceusage.CancelOperationRequest{}).Do()
	require.NoError(t, err)

	_, err = svc.Operations.Cancel("operations/never-minted", &serviceusage.CancelOperationRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_CloudLogging_OperationsCancel drives the cancel Cloud Logging
// declares on each of its scopes, through the scope-specific method the client
// generates for it. The folder, organization and billing-account scopes have
// their own mount; the project scope shares its URI with the Cloud Run
// operation collection and is dispatched by the fan-in that owns it. Every
// path must resolve a real Cloud Logging operation and refuse a name no
// operation was minted under.
func TestSDK_CloudLogging_OperationsCancel(t *testing.T) {
	svc := loggingOpsService(t)

	type scopeOps struct {
		name        string
		loc         string
		createAsync func(parent, bucketID string) (*logging.Operation, error)
		cancel      func(name string) error
	}
	scopes := []scopeOps{
		{
			name: "projects",
			loc:  fmt.Sprintf("projects/%s/locations/us-central1", cancelProject),
			createAsync: func(parent, id string) (*logging.Operation, error) {
				return svc.Projects.Locations.Buckets.CreateAsync(parent, &logging.LogBucket{}).BucketId(id).Do()
			},
			cancel: func(name string) error {
				_, err := svc.Projects.Locations.Operations.Cancel(name, &logging.CancelOperationRequest{}).Do()
				return err
			},
		},
		{
			name: "folders",
			loc:  "folders/123456/locations/us-central1",
			createAsync: func(parent, id string) (*logging.Operation, error) {
				return svc.Folders.Locations.Buckets.CreateAsync(parent, &logging.LogBucket{}).BucketId(id).Do()
			},
			cancel: func(name string) error {
				_, err := svc.Folders.Locations.Operations.Cancel(name, &logging.CancelOperationRequest{}).Do()
				return err
			},
		},
		{
			name: "organizations",
			loc:  "organizations/987654/locations/us-central1",
			createAsync: func(parent, id string) (*logging.Operation, error) {
				return svc.Organizations.Locations.Buckets.CreateAsync(parent, &logging.LogBucket{}).BucketId(id).Do()
			},
			cancel: func(name string) error {
				_, err := svc.Organizations.Locations.Operations.Cancel(name, &logging.CancelOperationRequest{}).Do()
				return err
			},
		},
		{
			name: "billingAccounts",
			loc:  "billingAccounts/012345-678901-ABCDEF/locations/us-central1",
			createAsync: func(parent, id string) (*logging.Operation, error) {
				return svc.BillingAccounts.Locations.Buckets.CreateAsync(parent, &logging.LogBucket{}).BucketId(id).Do()
			},
			cancel: func(name string) error {
				_, err := svc.BillingAccounts.Locations.Operations.Cancel(name, &logging.CancelOperationRequest{}).Do()
				return err
			},
		},
	}

	for i, sc := range scopes {
		t.Run(sc.name, func(t *testing.T) {
			op, err := sc.createAsync(sc.loc, fmt.Sprintf("cancel-probe-bucket-%d", i))
			require.NoError(t, err)
			require.NotEmpty(t, op.Name)

			require.NoError(t, sc.cancel(op.Name),
				"cancelling a completed operation is not an error")

			requireGoogleErrCode(t, sc.cancel(sc.loc+"/operations/never-minted"),
				http.StatusNotFound)
		})
	}
}

// TestSDK_CloudSQL_OperationsCancel drives sql.operations.cancel. Cloud SQL's
// method is its own, not the google.longrunning one, and it refuses an
// operation that is not in progress rather than answering Empty — the answer
// the service publishes for that case.
func TestSDK_CloudSQL_OperationsCancel(t *testing.T) {
	svc := sqlAdminService(t)

	insertOp, err := svc.Instances.Insert(cancelProject, &sqladmin.DatabaseInstance{
		Name:            "cancel-probe-sql",
		DatabaseVersion: "POSTGRES_15",
		Region:          "us-central1",
		Settings:        &sqladmin.Settings{Tier: "db-f1-micro"},
	}).Do()
	require.NoError(t, err)
	require.NotEmpty(t, insertOp.Name)
	t.Cleanup(func() { _, _ = svc.Instances.Delete(cancelProject, "cancel-probe-sql").Do() })

	got, err := svc.Operations.Get(cancelProject, insertOp.Name).Do()
	require.NoError(t, err)
	require.Equal(t, "DONE", got.Status, "the insert settled before the client could hold its name")

	_, err = svc.Operations.Cancel(cancelProject, insertOp.Name).Do()
	require.Error(t, err, "Cloud SQL refuses a cancel of an operation that is not in progress")
	assert.Contains(t, err.Error(), "400")
	assert.Contains(t, err.Error(), "isn't in progress")

	_, err = svc.Operations.Cancel(cancelProject, "never-minted").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// TestSDK_CloudBuild_CancelStopsARunningBuild is the cancel that stops real
// work. The simulator's Cloud Build runs a build's steps as real `docker`
// processes, so a build step is a live build on the Docker daemon and
// cancelling has to abort it.
//
// The step here is a `docker build` whose Dockerfile sleeps for an hour — 120
// times the deadline this test allows the cancelled create call to return
// within. The build is left running long enough to be unambiguously inside
// that sleep before the cancel is issued, so the cancel cannot be racing a
// step that never started; and the create call is required to come back
// CANCELLED, with its operation reporting google.rpc.Code.CANCELLED and no
// image produced. A cancel that only relabels the record leaves the create
// call blocked for the hour and the test fails on its deadline — which is what
// deleting the terminating call in cancelCloudBuild produces.
func TestSDK_CloudBuild_CancelStopsARunningBuild(t *testing.T) {
	svc := cloudbuildService(t)

	const image = "sockerless-cloudbuild-cancel-probe:gcp-sdk"
	_ = exec.Command("docker", "rmi", "-f", image).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	bucket := "cloudbuild-cancel-src"
	object := "source.tar.gz"
	// A Dockerfile whose only instruction takes an hour: the build cannot
	// finish on its own inside this test.
	uploadBuildSource(t, bucket, object, "FROM alpine:latest\nRUN sleep 3600\n")

	build := &cloudbuild.Build{
		Source: &cloudbuild.Source{
			StorageSource: &cloudbuild.StorageSource{Bucket: bucket, Object: object},
		},
		Steps: []*cloudbuild.BuildStep{{
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"build", "-t", image, "."},
		}},
	}

	type createResult struct {
		op  *cloudbuild.Operation
		err error
	}
	// The project is shared with the other builds in this file, so record which
	// builds already exist: the one this test cancels has to be the one this
	// test started.
	preexisting := buildIDs(t, svc)

	done := make(chan createResult, 1)
	go func() {
		op, err := svc.Projects.Builds.Create(cancelProject, build).Do()
		done <- createResult{op, err}
	}()

	// The build record is written before its steps run and moves to WORKING
	// when they start, so the client can see the build go live.
	id := awaitBuildStatus(t, svc, preexisting, "WORKING", 120*time.Second)
	// WORKING is recorded before the source is even fetched, so it alone does
	// not mean a step is executing. The step's own status is what says the RUN
	// is under way, and it is what makes the cancel below cancel work that is
	// genuinely in flight rather than race a step that has not begun.
	require.Eventually(t, func() bool {
		build, err := svc.Projects.Builds.Get(cancelProject, id).Do()
		require.NoError(t, err)
		return len(build.Steps) > 0 && build.Steps[0].Status == "WORKING"
	}, 120*time.Second, 50*time.Millisecond, "the build's first step never started running")
	require.Equal(t, "WORKING", buildStatus(t, svc, id), "the build is still running when it is cancelled")

	cancelled, err := svc.Projects.Builds.Cancel(cancelProject, id, &cloudbuild.CancelBuildRequest{}).Do()
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", cancelled.Status)

	select {
	case res := <-done:
		require.NoError(t, res.err)
		require.NotNil(t, res.op)
		// The operation the create returned is the operation of the build this
		// test cancelled — the two identify the same build, so the cancel above
		// cannot have hit a build some other test left running.
		require.Equal(t, id, buildIDFromOperationName(t, res.op.Name),
			"the cancelled build is the build this test started")
		require.True(t, res.op.Done)
		require.NotNil(t, res.op.Error, "a cancelled build's operation carries an error")
		assert.EqualValues(t, 1, res.op.Error.Code,
			"a cancelled operation reports google.rpc.Code.CANCELLED")
	case <-time.After(30 * time.Second):
		t.Fatal("the create call never returned: the cancel did not stop the running build step")
	}

	got, err := svc.Projects.Builds.Get(cancelProject, id).Do()
	require.NoError(t, err)
	assert.Equal(t, "CANCELLED", got.Status, "the cancel is the build's recorded verdict")

	require.Error(t, exec.Command("docker", "image", "inspect", image).Run(),
		"a cancelled build must not have produced its image")
}

// TestSDK_CloudBuild_RegionalOperationsCancel drives
// cloudbuild.projects.locations.operations.cancel. Cloud Build's worker-pool
// operations live in the regional operations collection, so the operation a
// create returns is pollable and cancellable by the name it carries.
func TestSDK_CloudBuild_RegionalOperationsCancel(t *testing.T) {
	svc := cloudbuildService(t)
	parent := fmt.Sprintf("projects/%s/locations/us-central1", cancelProject)

	op, err := svc.Projects.Locations.WorkerPools.Create(parent, &cloudbuild.WorkerPool{
		DisplayName: "cancel-probe-pool",
	}).WorkerPoolId("cancel-probe-pool").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)

	_, err = svc.Projects.Locations.Operations.Cancel(op.Name,
		&cloudbuild.CancelOperationRequest{}).Do()
	require.NoError(t, err, "cancelling a completed operation is not an error")

	after, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err, "the operation the create returned is pollable by name")
	assert.True(t, after.Done)

	_, err = svc.Projects.Locations.Operations.Cancel(parent+"/operations/never-minted",
		&cloudbuild.CancelOperationRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// buildIDs returns the ids of every build the project currently holds. Taken
// before a create, it is the set awaitBuildStatus must ignore so a build another
// test left behind can never be mistaken for this one's.
func buildIDs(t *testing.T, svc *cloudbuild.Service) map[string]bool {
	t.Helper()
	list, err := svc.Projects.Builds.List(cancelProject).Do()
	require.NoError(t, err)
	ids := make(map[string]bool, len(list.Builds))
	for _, b := range list.Builds {
		ids[b.Id] = true
	}
	return ids
}

// awaitBuildStatus waits for a build outside `ignore` to reach a status and
// returns its id. The project is shared with the other builds in this file, so
// the caller snapshots the ids that already exist and this only ever reports a
// build the caller's own create started.
func awaitBuildStatus(t *testing.T, svc *cloudbuild.Service, ignore map[string]bool, status string, within time.Duration) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		list, err := svc.Projects.Builds.List(cancelProject).Do()
		require.NoError(t, err)
		for _, b := range list.Builds {
			if ignore[b.Id] {
				continue
			}
			if b.Status == status {
				return b.Id
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("no new build reached %s", status)
	return ""
}

// buildIDFromOperationName extracts the build id from a Cloud Build build
// operation's name, which is `operations/build/{project}/{id}`.
func buildIDFromOperationName(t *testing.T, name string) string {
	t.Helper()
	parts := strings.Split(name, "/")
	require.Len(t, parts, 4, "a build operation is named operations/build/{project}/{id}, got %q", name)
	require.Equal(t, "operations", parts[0])
	require.Equal(t, "build", parts[1])
	return parts[3]
}

func buildStatus(t *testing.T, svc *cloudbuild.Service, id string) string {
	t.Helper()
	b, err := svc.Projects.Builds.Get(cancelProject, id).Do()
	require.NoError(t, err)
	return b.Status
}

// TestSDK_CloudBuild_OperationsCancel drives cloudbuild.operations.cancel on
// the top-level operations collection, which is the other way a client reaches
// the same build cancellation. A build that has already settled keeps its
// verdict; a build operation that was never minted is NOT_FOUND.
func TestSDK_CloudBuild_OperationsCancel(t *testing.T) {
	svc := cloudbuildService(t)

	bucket := "cloudbuild-cancel-op-src"
	object := "source.tar.gz"
	uploadBuildSource(t, bucket, object, "FROM alpine:latest\n")

	op, err := svc.Projects.Builds.Create(cancelProject, &cloudbuild.Build{
		Source: &cloudbuild.Source{
			StorageSource: &cloudbuild.StorageSource{Bucket: bucket, Object: object},
		},
		Steps: []*cloudbuild.BuildStep{{
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"version"},
		}},
	}).Do()
	require.NoError(t, err)
	require.True(t, op.Done)

	settled, err := svc.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	require.True(t, settled.Done)

	_, err = svc.Operations.Cancel(op.Name, &cloudbuild.CancelOperationRequest{}).Do()
	require.NoError(t, err, "cancelling a settled build operation answers Empty")

	after, err := svc.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, after.Done)

	_, err = svc.Operations.Cancel("operations/build/"+cancelProject+"/never-minted",
		&cloudbuild.CancelOperationRequest{}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

// uploadBuildSource writes the build context Cloud Build fetches: a gzipped
// tar carrying the given Dockerfile, uploaded through the Cloud Storage JSON
// API the same way a client uploads one.
func uploadBuildSource(t *testing.T, bucket, object, dockerfile string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte(dockerfile)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "Dockerfile", Mode: 0o644, Size: int64(len(body)),
	}))
	_, err := tw.Write(body)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())

	createBucket(t, cancelProject, bucket)
	uploadGCSObject(t, bucket, object, buf.Bytes())
}
