package gcp_sdk_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/api/option"
	redis "google.golang.org/api/redis/v1"
	run "google.golang.org/api/run/v2"
	storageapi "google.golang.org/api/storage/v1"
)

func runV2AdminService(t *testing.T) *run.Service {
	t.Helper()
	svc, err := run.NewService(ctx, option.WithEndpoint(baseURL+"/"), option.WithTokenSource(simTokenSource()))
	require.NoError(t, err)
	return svc
}

// Cloud Run v2's hosted build path, and Memorystore's rescheduleMaintenance:
//
//	POST /v2/projects/{project}/locations/{location}/builds:submit
//	POST /v2/projects/{project}/locations/{location}:uploadSource
//	POST /v1/projects/{project}/locations/{location}/instances/{id}:rescheduleMaintenance

func TestCloudRun_SubmitBuild(t *testing.T) {
	runSvc := runV2AdminService(t)
	parent := "projects/cr-build/locations/us-central1"
	const bucket, object = "run-sources-cr-build-us-central1", "services/source.zip"

	// Submitting a build for source that was never uploaded reports the
	// absence rather than building nothing.
	_, err := runSvc.Projects.Locations.Builds.Submit(parent, &run.GoogleCloudRunV2SubmitBuildRequest{
		ImageUri:      "us-central1-docker.pkg.dev/cr-build/repo/app:v1",
		StorageSource: &run.GoogleCloudRunV2StorageSource{Bucket: bucket, Object: object},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")

	storage := storageService(t)
	_, err = storage.Buckets.Insert("cr-build", &storageapi.Bucket{Name: bucket}).Do()
	require.NoError(t, err)
	_, err = storage.Objects.Insert(bucket, &storageapi.Object{Name: object}).
		Media(strings.NewReader("source archive")).Do()
	require.NoError(t, err)

	submitted, err := runSvc.Projects.Locations.Builds.Submit(parent, &run.GoogleCloudRunV2SubmitBuildRequest{
		ImageUri:      "us-central1-docker.pkg.dev/cr-build/repo/app:v1",
		StorageSource: &run.GoogleCloudRunV2StorageSource{Bucket: bucket, Object: object},
		DockerBuild:   &run.GoogleCloudRunV2DockerBuild{},
	}).Do()
	require.NoError(t, err)
	require.NotNil(t, submitted.BuildOperation)
	assert.True(t, submitted.BuildOperation.Done, "the build operation resolves")

	// A submit without an image has nothing to produce.
	_, err = runSvc.Projects.Locations.Builds.Submit(parent, &run.GoogleCloudRunV2SubmitBuildRequest{
		StorageSource: &run.GoogleCloudRunV2StorageSource{Bucket: bucket, Object: object},
	}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "imageUri is required")
}

func TestMemorystore_RescheduleMaintenance(t *testing.T) {
	svc := redisService(t)
	parent := "projects/ms-reschedule/locations/us-central1"

	create, err := svc.Projects.Locations.Instances.Create(parent, &redis.Instance{
		Tier: "BASIC", MemorySizeGb: 1,
	}).InstanceId("cache").Do()
	require.NoError(t, err)
	require.NotNil(t, create)
	name := parent + "/instances/cache"

	moved, err := svc.Projects.Locations.Instances.RescheduleMaintenance(name,
		&redis.RescheduleMaintenanceRequest{
			RescheduleType: "SPECIFIC_TIME",
			ScheduleTime:   "2026-12-01T02:00:00Z",
		}).Do()
	require.NoError(t, err)
	assert.True(t, moved.Done)

	instance, err := svc.Projects.Locations.Instances.Get(name).Do()
	require.NoError(t, err)
	require.NotNil(t, instance.MaintenanceSchedule, "the window is recorded on the instance")
	assert.Equal(t, "2026-12-01T02:00:00Z", instance.MaintenanceSchedule.StartTime)

	// SPECIFIC_TIME without a time has nothing to schedule.
	_, err = svc.Projects.Locations.Instances.RescheduleMaintenance(name,
		&redis.RescheduleMaintenanceRequest{RescheduleType: "SPECIFIC_TIME"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "scheduleTime is required")

	// A type the enum does not carry is refused.
	_, err = svc.Projects.Locations.Instances.RescheduleMaintenance(name,
		&redis.RescheduleMaintenanceRequest{RescheduleType: "WHENEVER"}).Do()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "IMMEDIATE, NEXT_AVAILABLE_WINDOW or SPECIFIC_TIME")
}
