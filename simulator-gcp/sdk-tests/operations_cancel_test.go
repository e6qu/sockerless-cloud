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
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// google.longrunning.Operations.CancelOperation across the services whose
// Discovery documents declare it, each through its own official Go client.
// The answers differ, as the services' do:
//
//   - API Gateway, Artifact Registry, Cloud Build, Eventarc, Cloud Logging,
//     Memorystore for Redis, Service Usage, Cloud Spanner and Firestore spell
//     the standard method and answer google.protobuf.Empty. A client checks
//     GetOperation to learn "whether the cancellation succeeded or whether the
//     operation completed despite cancellation", so a completed operation keeps
//     its recorded result.
//   - Cloud SQL Admin's sql.operations.cancel is the service's own method and
//     refuses an operation that is not in progress.
//   - Cancelling a Cloud Build build operation stops the running steps.
//
// A name never minted is NOT_FOUND everywhere. The routes:
//
//   POST /v1/projects/{project}/locations/{location}/operations/{opAction}
//   POST /v2/projects/{project}/locations/{location}/operations/{opAction}
//   POST /v1/operations/{opAction...}
//   POST /v1/projects/{project}/builds/{idAction}
//   POST /v1/projects/{project}/locations/{location}/builds/{idAction}
//
// The first two are the colon-verb route on the projects/locations operation
// collection every service under it shares, which serves both wait and cancel.

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

// TestSDK_APIGateway_OperationsCancel: creating an API mints the operation,
// and cancelling it answers Empty while the recorded result survives.
func TestSDK_APIGateway_OperationsCancel(t *testing.T) {
	t.Parallel()
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

func TestSDK_ArtifactRegistry_OperationsCancel(t *testing.T) {
	t.Parallel()
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

func TestSDK_MemorystoreRedis_OperationsCancel(t *testing.T) {
	t.Parallel()
	svc := redisOpsService(t)
	parent := fmt.Sprintf("projects/%s/locations/us-central1", cancelProject)

	op, err := svc.Projects.Locations.Instances.Create(parent, &redis.Instance{
		Tier:         "BASIC",
		MemorySizeGb: 1,
	}).InstanceId("cancel-probe-redis").Do()
	require.NoError(t, err)
	require.NotEmpty(t, op.Name)
	deleteRedisOnCleanup(t, svc, parent+"/instances/cancel-probe-redis")

	_, err = svc.Projects.Locations.Operations.Cancel(op.Name).Do()
	require.NoError(t, err)

	after, err := svc.Projects.Locations.Operations.Get(op.Name).Do()
	require.NoError(t, err)
	assert.True(t, after.Done)

	_, err = svc.Projects.Locations.Operations.Cancel(parent + "/operations/never-minted").Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "404")
}

func TestSDK_Eventarc_OperationsCancel(t *testing.T) {
	t.Parallel()
	svc, err := eventarcapi.NewService(ctx, option.WithEndpoint(baseURL), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	parent := fmt.Sprintf("projects/%s/locations/us-central1", cancelProject)
	eventarcRunService(t, cancelProject, "us-central1", "cancel-probe-svc")

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

// TestSDK_ServiceUsage_OperationsCancel: Service Usage names its operations
// operations/{id}, so the enable's operation is gettable and cancellable by
// the name the enable returned.
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

// TestSDK_CloudLogging_OperationsCancel cancels on each scope through the
// client's scope-specific method. The folder, organization and billing-account
// scopes have their own mount; the project scope shares its URI with the Cloud
// Run operation collection's route. Every path must resolve a real Cloud
// Logging operation and refuse a name never minted.
func TestSDK_CloudLogging_OperationsCancel(t *testing.T) {
	t.Parallel()
	svc := loggingOpsService(t)
	cancelFolder := createTestFolder(t, "logging-cancel-folder")

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
			loc:  cancelFolder + "/locations/us-central1",
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
			loc:  "organizations/123456789012/locations/us-central1",
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

// TestSDK_CloudSQL_OperationsCancel: Cloud SQL's own cancel refuses an
// operation that is not in progress rather than answering Empty.
func TestSDK_CloudSQL_OperationsCancel(t *testing.T) {
	t.Parallel()
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

// TestSDK_CloudBuild_CancelStopsARunningBuild: the simulator runs build steps
// as real `docker` processes, so cancelling must abort a live build. The step's
// Dockerfile sleeps for an hour; the cancel lands once the step runs, and the
// operation must complete CANCELLED with no image. The operation completes only
// once the steps stop, so a cancel that only relabels the record blocks the
// wait until the deadline.
func TestSDK_CloudBuild_CancelStopsARunningBuild(t *testing.T) {
	t.Parallel()
	svc := cloudbuildService(t)

	const image = "sockerless-cloudbuild-cancel-probe:gcp-sdk"
	_ = exec.Command("docker", "rmi", "-f", image).Run()
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	bucket := "cloudbuild-cancel-src"
	object := "source.tar.gz"
	pullImageWithRetry(t, cbBaseImage)
	uploadBuildSource(t, bucket, object, "FROM "+cbBaseImage+"\nRUN sleep 3600\n")

	build := &cloudbuild.Build{
		Source: &cloudbuild.Source{
			StorageSource: &cloudbuild.StorageSource{Bucket: bucket, Object: object},
		},
		Steps: []*cloudbuild.BuildStep{{
			Name: "gcr.io/cloud-builders/docker",
			Args: []string{"build", "-t", image, "."},
		}},
	}

	// CreateBuild answers at once with the operation, whose
	// BuildOperationMetadata names the build.
	op, err := svc.Projects.Builds.Create(cancelProject, build).Do()
	require.NoError(t, err)
	require.False(t, op.Done, "the operation comes back before the build runs")
	id := buildIDFromOperationName(t, op.Name)

	// Cloud Build signals no step start, so read the step's status until it runs.
	require.Eventually(t, func() bool {
		build, err := svc.Projects.Builds.Get(cancelProject, id).Do()
		require.NoError(t, err)
		return len(build.Steps) > 0 && build.Steps[0].Status == "WORKING"
	}, 120*time.Second, 50*time.Millisecond, "the build's first step never started running")
	require.Equal(t, "WORKING", buildStatus(t, svc, id), "the build is still running when it is cancelled")

	cancelled, err := svc.Projects.Builds.Cancel(cancelProject, id, &cloudbuild.CancelBuildRequest{}).Do()
	require.NoError(t, err)
	require.Equal(t, "CANCELLED", cancelled.Status)

	_, err = waitBuild(t, startedBuild{Operation: op.Name, BuildID: id})
	require.Error(t, err, "a cancelled build's operation carries an error")
	assert.Equal(t, codes.Canceled, status.Code(err),
		"a cancelled operation reports google.rpc.Code.CANCELLED: %v", err)

	got, err := svc.Projects.Builds.Get(cancelProject, id).Do()
	require.NoError(t, err)
	assert.Equal(t, "CANCELLED", got.Status, "the cancel is the build's recorded verdict")

	// Cloud Build reports the interrupted step as CANCELLED, with its start time
	// and an end time from when it stopped.
	require.Len(t, got.Steps, 1)
	assert.Equal(t, "CANCELLED", got.Steps[0].Status,
		"the interrupted step reports the cancel, not the failure its killed process returned")
	require.NotNil(t, got.Steps[0].Timing, "an interrupted step kept its timing")
	assert.NotEmpty(t, got.Steps[0].Timing.StartTime, "the step records when it started")
	assert.NotEmpty(t, got.Steps[0].Timing.EndTime, "the step records when it stopped")

	// A cancelled build must not have produced its image. Report the image's own
	// record and the step state: they tell a completed build from an image that
	// arrived another way.
	if inspect, err := exec.Command("docker", "image", "inspect",
		"--format", "{{.Id}} created={{.Created}} size={{.Size}}", image).CombinedOutput(); err == nil {
		final, getErr := svc.Projects.Builds.Get(cancelProject, id).Do()
		var steps string
		if getErr == nil {
			for i, step := range final.Steps {
				if step == nil {
					continue
				}
				timing := "<none>"
				if step.Timing != nil {
					timing = fmt.Sprintf("%s..%s", step.Timing.StartTime, step.Timing.EndTime)
				}
				steps += fmt.Sprintf("\n  step %d %s status=%s timing=%s", i, step.Name, step.Status, timing)
			}
			steps = fmt.Sprintf("build status=%s detail=%q finish=%s%s",
				final.Status, final.StatusDetail, final.FinishTime, steps)
		} else {
			steps = fmt.Sprintf("(the build could not be read back: %v)", getErr)
		}
		t.Fatalf("a cancelled build must not have produced its image, but %s exists:\n  %s\n%s",
			image, strings.TrimSpace(string(inspect)), steps)
	}
}

// TestSDK_CloudBuild_RegionalOperationsCancel: worker-pool operations live in
// the regional collection, pollable and cancellable by their own name.
func TestSDK_CloudBuild_RegionalOperationsCancel(t *testing.T) {
	t.Parallel()
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

// buildIDFromOperationName parses `operations/build/{project}/{id}`.
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

// TestSDK_CloudBuild_OperationsCancel cancels on the top-level operations
// collection: a settled build keeps its verdict, and a build operation never
// minted is NOT_FOUND.
func TestSDK_CloudBuild_OperationsCancel(t *testing.T) {
	t.Parallel()
	svc := cloudbuildService(t)

	bucket := "cloudbuild-cancel-op-src"
	object := "source.tar.gz"
	pullImageWithRetry(t, cbBaseImage)
	uploadBuildSource(t, bucket, object, "FROM "+cbBaseImage+"\n")

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
	require.False(t, op.Done, "the operation comes back before the build runs")

	_, err = waitBuild(t, startedBuild{Operation: op.Name, BuildID: buildIDFromOperationName(t, op.Name)})
	require.NoError(t, err)
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

// uploadBuildSource uploads a gzipped tar of the Dockerfile through the Cloud
// Storage JSON API.
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
